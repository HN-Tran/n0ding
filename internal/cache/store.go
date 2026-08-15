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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HN-Tran/n0ding/internal/httppolicy"
	"github.com/HN-Tran/n0ding/internal/storage"
)

type Store struct {
	root        string
	now         func() time.Time
	mu          sync.RWMutex
	bytes       atomic.Int64
	objects     atomic.Int64
	controller  *storage.Controller
	freeBytes   func(string) (int64, error)
	activeTemps map[string]struct{}
}

func (s *Store) SetController(controller *storage.Controller) {
	s.mu.Lock()
	s.controller = controller
	s.freeBytes = storage.FreeBytes
	s.mu.Unlock()
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

// Candidate is an immutable snapshot of a complete cache entry considered for
// pressure collection. Paths stay internal to the store; callers only use the
// ordering fields and pass the value back to RemoveCandidate.
type Candidate struct {
	metadataPath string
	bodyPath     string
	Bytes        int64
	LastUsed     time.Time
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	store := &Store{root: root, now: time.Now, activeTemps: make(map[string]struct{})}
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
	// Body mtime is a cheap, crash-safe access hint for pressure GC. Failure to
	// update it must never turn a valid cache hit into a client error.
	now := s.now()
	_ = os.Chtimes(bodyPath, now, now)
	return Entry{Metadata: metadata, BodyPath: bodyPath, Body: body}, true, nil
}

// Candidates returns complete entries ordered from least to most recently
// used. It never exposes temporary, malformed, or incomplete generations.
func (s *Store) Candidates() ([]Candidate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var candidates []Candidate
	err := filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
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
		lastUsed := info.ModTime()
		if lastUsed.IsZero() {
			lastUsed = metadata.StoredAt
		}
		candidates = append(candidates, Candidate{metadataPath: path, bodyPath: bodyPath, Bytes: info.Size(), LastUsed: lastUsed})
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].LastUsed.Before(candidates[j].LastUsed) })
	return candidates, err
}

// RemoveCandidate removes an entry only if metadata still references the
// exact generation observed by Candidates. Concurrent replacements are kept.
func (s *Store) RemoveCandidate(candidate Candidate) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	legacyBodyPath, valid := bodyPathForMetadata(candidate.metadataPath)
	if !valid {
		return false, nil
	}
	metadata, err := readMetadata(candidate.metadataPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	bodyPath, err := bodyPathForEntry(candidate.metadataPath, legacyBodyPath, metadata)
	if err != nil || bodyPath != candidate.bodyPath || metadata.ContentBytes != candidate.Bytes {
		return false, nil
	}
	if err := os.Remove(bodyPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	s.bytes.Add(-candidate.Bytes)
	s.objects.Add(-1)
	if err := os.Remove(candidate.metadataPath); err != nil {
		return true, err
	}
	return true, nil
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
	var reservation *storage.Reservation
	expectedBytes, sizeKnown := contentLength(metadata.Header)
	if s.controller != nil {
		freeBytes, err := s.freeBytes(s.root)
		if err != nil {
			return fmt.Errorf("read filesystem capacity: %w", err)
		}
		if !sizeKnown {
			s.controller.RecordBypass(0)
			_, err := io.Copy(downstream, source)
			return err
		}
		reservation = s.controller.Reserve(expectedBytes, freeBytes)
		if reservation == nil {
			_, err := io.Copy(downstream, source)
			return err
		}
		defer reservation.Release()
	}

	bodyPath, metadataPath := s.paths(key)
	directory := filepath.Dir(bodyPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create cache shard: %w", err)
	}

	s.mu.Lock()
	bodyTemp, err := os.CreateTemp(directory, ".body-*")
	if err == nil {
		s.activeTemps[bodyTemp.Name()] = struct{}{}
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("create cache body: %w", err)
	}
	bodyTempPath := bodyTemp.Name()
	defer func() {
		s.mu.Lock()
		delete(s.activeTemps, bodyTempPath)
		s.mu.Unlock()
	}()
	generationPath := bodyPath + "." + strings.TrimPrefix(filepath.Base(bodyTempPath), ".body-")
	keepBody := false
	defer func() {
		_ = bodyTemp.Close()
		if !keepBody {
			_ = os.Remove(bodyTempPath)
		}
	}()

	sink := &boundedCacheWriter{cache: bodyTemp, downstream: downstream, limit: expectedBytes, enabled: reservation != nil}
	if s.controller == nil {
		sink.enabled = true
		sink.limit = -1
	}
	written, err := io.Copy(sink, source)
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
	if sink.overflow {
		s.controller.RecordBypass(written)
		return nil
	}

	metadata.StoredAt = s.now().UTC()
	metadata.ContentBytes = written
	metadata.BodyFile = filepath.Base(generationPath)
	metadata.Header = httppolicy.CacheMetadataHeaders(metadata.Header)
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode cache metadata: %w", err)
	}
	s.mu.Lock()
	metadataTemp, err := os.CreateTemp(directory, ".metadata-*")
	if err == nil {
		s.activeTemps[metadataTemp.Name()] = struct{}{}
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("create cache metadata: %w", err)
	}
	metadataTempPath := metadataTemp.Name()
	defer func() {
		s.mu.Lock()
		delete(s.activeTemps, metadataTempPath)
		s.mu.Unlock()
	}()
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
	// Seed the pressure-GC access hint from the same clock used for StoredAt.
	// Besides keeping fresh entries ordered correctly, this makes stores with
	// an injected clock deterministic instead of depending on filesystem time.
	if err := os.Chtimes(generationPath, metadata.StoredAt, metadata.StoredAt); err != nil {
		_ = os.Remove(generationPath)
		return fmt.Errorf("set cache body access time: %w", err)
	}
	if err := replace(metadataTempPath, metadataPath); err != nil {
		_ = os.Remove(generationPath)
		return fmt.Errorf("commit cache metadata: %w", err)
	}
	keepMetadata = true
	if reservation != nil && !reservation.Commit(written, previousBytes) {
		_ = os.Remove(generationPath)
		return fmt.Errorf("cache body exceeded reserved capacity")
	}
	s.bytes.Add(written - previousBytes)
	if !previousValid {
		s.objects.Add(1)
	}
	if previousBodyPath != "" && previousBodyPath != generationPath {
		_ = os.Remove(previousBodyPath)
	}
	return nil
}

func contentLength(header http.Header) (int64, bool) {
	raw := header.Get("Content-Length")
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0
}

type boundedCacheWriter struct {
	cache      io.Writer
	downstream io.Writer
	limit      int64
	written    int64
	enabled    bool
	overflow   bool
}

func (w *boundedCacheWriter) Write(body []byte) (int, error) {
	written, err := w.downstream.Write(body)
	if written > 0 && w.enabled && !w.overflow {
		if w.limit >= 0 && int64(written) > w.limit-w.written {
			w.overflow = true
		} else if _, cacheErr := w.cache.Write(body[:written]); cacheErr != nil {
			return written, cacheErr
		}
		w.written += int64(written)
	}
	return written, err
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
		if _, active := s.activeTemps[path]; active {
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
		result.Objects++
		result.Bytes += info.Size()
		if removeErr := os.Remove(path); removeErr != nil {
			result.Skipped++
		}
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
