package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HN-Tran/n0ding/internal/httppolicy"
)

type Store struct {
	root    string
	now     func() time.Time
	mu      sync.RWMutex
	bytes   atomic.Int64
	objects atomic.Int64
}

const maxMetadataBytes = 1 << 20

type Metadata struct {
	Status        int         `json:"status"`
	Header        http.Header `json:"header"`
	StoredAt      time.Time   `json:"stored_at"`
	ContentBytes  int64       `json:"content_bytes"`
	ContentDigest string      `json:"content_digest,omitempty"`
	BodyFile      string      `json:"body_file,omitempty"`
}

type Entry struct {
	Metadata Metadata
	BodyPath string
	Body     *os.File
}

func (e Entry) Close() error {
	if e.Body == nil {
		return nil
	}
	return e.Body.Close()
}

type TempCleanupResult struct {
	Files int64
	Bytes int64
}

type GCResult struct {
	Objects int64
	Bytes   int64
	Skipped int64
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	store := &Store{root: root, now: time.Now}
	if _, _, err := store.Reconcile(); err != nil {
		return nil, fmt.Errorf("scan cache directory: %w", err)
	}
	return store, nil
}

func (s *Store) Lookup(key string, ttl time.Duration) (Entry, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	legacyBodyPath, metadataPath := s.paths(key)
	metadata, err := readMetadata(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("read cache metadata: %w", err)
	}

	if s.now().Sub(metadata.StoredAt) >= ttl {
		return Entry{}, false, nil
	}
	bodyPath, err := bodyPathForEntry(metadataPath, legacyBodyPath, metadata)
	if err != nil {
		return Entry{}, false, fmt.Errorf("resolve cache body: %w", err)
	}
	info, err := os.Stat(bodyPath)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("stat cache body: %w", err)
	}
	if info.Size() != metadata.ContentBytes {
		return Entry{}, false, fmt.Errorf("cache body size mismatch: got %d, want %d", info.Size(), metadata.ContentBytes)
	}
	body, err := os.Open(bodyPath)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("open cache body: %w", err)
	}
	return Entry{Metadata: metadata, BodyPath: bodyPath, Body: body}, true, nil
}

func (s *Store) PutBytes(key string, metadata Metadata, body []byte) error {
	return s.PutStream(key, metadata, bytesReader(body), io.Discard)
}

func (s *Store) PutStream(key string, metadata Metadata, source io.Reader, downstream io.Writer) error {
	return s.PutStreamVerified(key, metadata, source, downstream, nil)
}

func (s *Store) PutStreamVerified(
	key string,
	metadata Metadata,
	source io.Reader,
	downstream io.Writer,
	verify func(written int64) error,
) error {
	bodyPath, metadataPath := s.paths(key)
	directory := filepath.Dir(bodyPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create cache shard: %w", err)
	}

	bodyTemp, err := os.CreateTemp(directory, ".body-*")
	if err != nil {
		return fmt.Errorf("create cache body: %w", err)
	}
	bodyTempPath := bodyTemp.Name()
	generationPath := bodyPath + "." + strings.TrimPrefix(filepath.Base(bodyTempPath), ".body-")
	keepBody := false
	defer func() {
		_ = bodyTemp.Close()
		if !keepBody {
			_ = os.Remove(bodyTempPath)
		}
	}()

	written, err := io.Copy(io.MultiWriter(bodyTemp, downstream), source)
	if err != nil {
		return fmt.Errorf("stream cache body: %w", err)
	}
	if err := bodyTemp.Sync(); err != nil {
		return fmt.Errorf("sync cache body: %w", err)
	}
	if err := bodyTemp.Close(); err != nil {
		return fmt.Errorf("close cache body: %w", err)
	}
	if verify != nil {
		if err := verify(written); err != nil {
			return fmt.Errorf("verify cache body: %w", err)
		}
	}

	metadata.StoredAt = s.now().UTC()
	metadata.ContentBytes = written
	metadata.BodyFile = filepath.Base(generationPath)
	metadata.Header = httppolicy.CacheMetadataHeaders(metadata.Header)
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode cache metadata: %w", err)
	}
	metadataTemp, err := os.CreateTemp(directory, ".metadata-*")
	if err != nil {
		return fmt.Errorf("create cache metadata: %w", err)
	}
	metadataTempPath := metadataTemp.Name()
	keepMetadata := false
	defer func() {
		_ = metadataTemp.Close()
		if !keepMetadata {
			_ = os.Remove(metadataTempPath)
		}
	}()
	if _, err := metadataTemp.Write(metadataBytes); err != nil {
		return fmt.Errorf("write cache metadata: %w", err)
	}
	if err := metadataTemp.Sync(); err != nil {
		return fmt.Errorf("sync cache metadata: %w", err)
	}
	if err := metadataTemp.Close(); err != nil {
		return fmt.Errorf("close cache metadata: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var previousBodyPath string
	var previousBytes int64
	var previousValid bool
	if previous, previousErr := readMetadata(metadataPath); previousErr == nil {
		previousBodyPath, _ = bodyPathForEntry(metadataPath, bodyPath, previous)
		if previousBodyPath != "" {
			if info, statErr := os.Stat(previousBodyPath); statErr == nil && info.Mode().IsRegular() && info.Size() == previous.ContentBytes {
				previousBytes = info.Size()
				previousValid = true
			}
		}
	}
	if err := os.Rename(bodyTempPath, generationPath); err != nil {
		return fmt.Errorf("commit cache body: %w", err)
	}
	keepBody = true
	if err := replace(metadataTempPath, metadataPath); err != nil {
		_ = os.Remove(generationPath)
		return fmt.Errorf("commit cache metadata: %w", err)
	}
	keepMetadata = true
	s.bytes.Add(written - previousBytes)
	if !previousValid {
		s.objects.Add(1)
	}
	if previousBodyPath != "" && previousBodyPath != generationPath {
		_ = os.Remove(previousBodyPath)
	}
	return nil
}

func (s *Store) Size() (bytes int64, objects int64, err error) {
	return s.bytes.Load(), s.objects.Load(), nil
}

// Reconcile rebuilds the O(1) usage counters from complete cache entries.
// It is intended for startup and explicit repair, not request-time status.
func (s *Store) Reconcile() (bytes int64, objects int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		legacyBodyPath, valid := bodyPathForMetadata(path)
		if !valid {
			return nil
		}
		metadata, metadataErr := readMetadata(path)
		if metadataErr != nil {
			return nil
		}
		bodyPath, bodyErr := bodyPathForEntry(path, legacyBodyPath, metadata)
		if bodyErr != nil {
			return nil
		}
		info, infoErr := os.Stat(bodyPath)
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() != metadata.ContentBytes {
			return nil
		}
		bytes += info.Size()
		objects++
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err == nil {
		s.bytes.Store(bytes)
		s.objects.Store(objects)
	}
	return bytes, objects, err
}

func (s *Store) CleanupStaleTemps(maxAge time.Duration) (result TempCleanupResult, err error) {
	if maxAge <= 0 {
		return result, fmt.Errorf("stale temp age must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-maxAge)
	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !isTempName(entry.Name()) {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return removeErr
		}
		result.Files++
		result.Bytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	return result, err
}

func (s *Store) GC(maxAge time.Duration) (result GCResult, err error) {
	if maxAge <= 0 {
		return result, fmt.Errorf("max age must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.now().Add(-maxAge)
	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		legacyBodyPath, valid := bodyPathForMetadata(path)
		if !valid {
			return nil
		}
		metadata, metadataErr := readMetadata(path)
		if metadataErr != nil {
			result.Skipped++
			return nil
		}
		if metadata.StoredAt.IsZero() || metadata.StoredAt.After(cutoff) {
			return nil
		}
		bodyPath, bodyErr := bodyPathForEntry(path, legacyBodyPath, metadata)
		if bodyErr != nil {
			result.Skipped++
			return nil
		}
		info, infoErr := os.Stat(bodyPath)
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() != metadata.ContentBytes {
			result.Skipped++
			return nil
		}

		// Removing the body first is safe for open readers on Unix and fails
		// without changing metadata on Windows. New lookups are blocked by mu.
		if removeErr := os.Remove(bodyPath); removeErr != nil {
			result.Skipped++
			return nil
		}
		s.bytes.Add(-info.Size())
		s.objects.Add(-1)
		if removeErr := os.Remove(path); removeErr != nil {
			return removeErr
		}
		result.Objects++
		result.Bytes += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	return result, err
}

func (s *Store) paths(key string) (body string, metadata string) {
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	base := filepath.Join(s.root, hash[:2], hash)
	return base + ".body", base + ".json"
}

func readMetadata(path string) (Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMetadataBytes+1))
	if err != nil {
		return Metadata{}, err
	}
	if len(data) > maxMetadataBytes {
		return Metadata{}, fmt.Errorf("cache metadata exceeds %d bytes", maxMetadataBytes)
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode cache metadata: %w", err)
	}
	return metadata, nil
}

func bodyPathForMetadata(metadataPath string) (string, bool) {
	if filepath.Ext(metadataPath) != ".json" {
		return "", false
	}
	base := strings.TrimSuffix(metadataPath, ".json")
	hash := filepath.Base(base)
	if len(hash) != sha256.Size*2 || filepath.Base(filepath.Dir(base)) != hash[:2] {
		return "", false
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", false
	}
	return base + ".body", true
}

func bodyPathForEntry(metadataPath, legacyBodyPath string, metadata Metadata) (string, error) {
	if metadata.BodyFile == "" {
		return legacyBodyPath, nil
	}
	if filepath.Base(metadata.BodyFile) != metadata.BodyFile ||
		!strings.HasPrefix(metadata.BodyFile, strings.TrimSuffix(filepath.Base(metadataPath), ".json")+".body.") {
		return "", fmt.Errorf("invalid cache body file %q", metadata.BodyFile)
	}
	return filepath.Join(filepath.Dir(metadataPath), metadata.BodyFile), nil
}

func isTempName(name string) bool {
	return strings.HasPrefix(name, ".body-") || strings.HasPrefix(name, ".metadata-")
}

func replace(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		if removeErr := os.Remove(destination); removeErr != nil {
			return removeErr
		}
		return os.Rename(source, destination)
	}
	return os.Rename(source, destination)
}

type byteReader struct {
	data   []byte
	offset int
}

func bytesReader(data []byte) *byteReader {
	return &byteReader{data: data}
}

func (r *byteReader) Read(target []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	count := copy(target, r.data[r.offset:])
	r.offset += count
	return count, nil
}
