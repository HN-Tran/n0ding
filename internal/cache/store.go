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
	"time"
)

type Store struct {
	root string
	now  func() time.Time
	mu   sync.RWMutex
}

type Metadata struct {
	Status        int         `json:"status"`
	Header        http.Header `json:"header"`
	StoredAt      time.Time   `json:"stored_at"`
	ContentBytes  int64       `json:"content_bytes"`
	ContentDigest string      `json:"content_digest,omitempty"`
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
	return &Store{root: root, now: time.Now}, nil
}

func (s *Store) Lookup(key string, ttl time.Duration) (Entry, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bodyPath, metadataPath := s.paths(key)
	data, err := os.ReadFile(metadataPath)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("read cache metadata: %w", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Entry{}, false, fmt.Errorf("decode cache metadata: %w", err)
	}
	if s.now().Sub(metadata.StoredAt) >= ttl {
		return Entry{}, false, nil
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
	if err := replace(bodyTempPath, bodyPath); err != nil {
		return fmt.Errorf("commit cache body: %w", err)
	}
	keepBody = true
	if err := replace(metadataTempPath, metadataPath); err != nil {
		return fmt.Errorf("commit cache metadata: %w", err)
	}
	keepMetadata = true
	return nil
}

func (s *Store) Size() (bytes int64, objects int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		bodyPath, valid := bodyPathForMetadata(path)
		if !valid {
			return nil
		}
		metadata, metadataErr := readMetadata(path)
		if metadataErr != nil {
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
		return 0, 0, nil
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
		bodyPath, valid := bodyPathForMetadata(path)
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
	data, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, err
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, err
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
