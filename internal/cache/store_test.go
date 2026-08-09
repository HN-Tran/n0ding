package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPutAndLookup(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	body := []byte("cached package")
	err = store.PutBytes("npm:package", Metadata{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/octet-stream"}},
	}, body)
	if err != nil {
		t.Fatal(err)
	}

	entry, found, err := store.Lookup("npm:package", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected cache hit")
	}
	defer entry.Close()
	got, err := os.ReadFile(entry.BodyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q", got)
	}
	if entry.Metadata.ContentBytes != int64(len(body)) {
		t.Fatalf("content bytes = %d", entry.Metadata.ContentBytes)
	}

	now = now.Add(2 * time.Hour)
	if _, found, err := store.Lookup("npm:package", time.Hour); err != nil || found {
		t.Fatalf("expired lookup: found=%v err=%v", found, err)
	}
}

func TestLookupRejectsOversizedMetadata(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bodyPath, metadataPath := store.paths("oversized")
	if err := os.MkdirAll(filepath.Dir(bodyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bodyPath, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, bytes.Repeat([]byte("x"), maxMetadataBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lookup("oversized", time.Hour); err == nil || found {
		t.Fatalf("expected oversized metadata error, found=%v err=%v", found, err)
	}
}

func TestReplacementPublishesNewGenerationAtomically(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("same-key", Metadata{Status: http.StatusOK}, []byte("first")); err != nil {
		t.Fatal(err)
	}
	oldEntry, found, err := store.Lookup("same-key", time.Hour)
	if err != nil || !found {
		t.Fatalf("old lookup found=%v err=%v", found, err)
	}
	defer oldEntry.Close()
	oldPath := oldEntry.BodyPath

	if err := store.PutBytes("same-key", Metadata{Status: http.StatusOK}, []byte("other")); err != nil {
		t.Fatal(err)
	}
	newEntry, found, err := store.Lookup("same-key", time.Hour)
	if err != nil || !found {
		t.Fatalf("new lookup found=%v err=%v", found, err)
	}
	defer newEntry.Close()
	if newEntry.BodyPath == oldPath {
		t.Fatal("replacement reused mutable body path")
	}
	newBody, err := io.ReadAll(newEntry.Body)
	if err != nil || string(newBody) != "other" {
		t.Fatalf("new body = %q, err=%v", newBody, err)
	}
	oldBody, err := io.ReadAll(oldEntry.Body)
	if err != nil || string(oldBody) != "first" {
		t.Fatalf("open old reader changed: body=%q err=%v", oldBody, err)
	}
}

func TestPutStripsSensitiveMetadataHeaders(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.PutBytes("sensitive", Metadata{
		Status: http.StatusOK,
		Header: http.Header{
			"Content-Type":        {"application/octet-stream"},
			"Set-Cookie":          {"session=secret"},
			"Authentication-Info": {"nextnonce=secret"},
		},
	}, []byte("cached"))
	if err != nil {
		t.Fatal(err)
	}

	entry, found, err := store.Lookup("sensitive", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected cache hit")
	}
	defer entry.Close()
	if got := entry.Metadata.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := entry.Metadata.Header.Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie persisted as %q", got)
	}
	if got := entry.Metadata.Header.Get("Authentication-Info"); got != "" {
		t.Fatalf("Authentication-Info persisted as %q", got)
	}
}

func TestCleanupStaleTempsRemovesOnlyOldTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	shard := filepath.Join(root, "ab")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	oldBody := filepath.Join(shard, ".body-old")
	oldMetadata := filepath.Join(shard, ".metadata-old")
	recentBody := filepath.Join(shard, ".body-recent")
	unrelated := filepath.Join(shard, "notes.txt")
	for path, body := range map[string]string{
		oldBody:     "old body",
		oldMetadata: "old metadata",
		recentBody:  "active write",
		unrelated:   "operator file",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := now.Add(-2 * time.Hour)
	if err := os.Chtimes(oldBody, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldMetadata, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentBody, now, now); err != nil {
		t.Fatal(err)
	}

	result, err := store.CleanupStaleTemps(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 2 || result.Bytes != int64(len("old body")+len("old metadata")) {
		t.Fatalf("cleanup result = %#v", result)
	}
	for _, path := range []string{oldBody, oldMetadata} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale temp %q still exists: %v", path, err)
		}
	}
	for _, path := range []string{recentBody, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained file %q: %v", path, err)
		}
	}
}

func TestGCDeletesOnlyOldCompleteObjectsAndIgnoresTemps(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	if err := store.PutBytes("old", Metadata{Status: http.StatusOK}, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("orphan-metadata", Metadata{Status: http.StatusOK}, []byte("missing")); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour)
	orphanMetadataBody, orphanMetadataPath := currentBodyPath(t, store, "orphan-metadata")
	if err := os.Remove(orphanMetadataBody); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("current", Metadata{Status: http.StatusOK}, []byte("current")); err != nil {
		t.Fatal(err)
	}
	orphanBody, _ := store.paths("orphan")
	if err := os.MkdirAll(filepath.Dir(orphanBody), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanBody, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	activeTemp := filepath.Join(filepath.Dir(orphanBody), ".body-active")
	if err := os.WriteFile(activeTemp, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := now.Add(-24 * time.Hour)
	if err := os.Chtimes(activeTemp, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	result, err := store.GC(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Objects != 1 || result.Bytes != int64(len("old")) || result.Skipped != 1 {
		t.Fatalf("GC result = %#v", result)
	}
	if entry, found, lookupErr := store.Lookup("old", 24*time.Hour); lookupErr != nil || found {
		if found {
			_ = entry.Close()
		}
		t.Fatalf("old lookup: found=%v err=%v", found, lookupErr)
	}
	current, found, err := store.Lookup("current", 24*time.Hour)
	if err != nil || !found {
		t.Fatalf("current lookup: found=%v err=%v", found, err)
	}
	_ = current.Close()
	for _, path := range []string{orphanBody, orphanMetadataPath, activeTemp} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("GC touched %q: %v", path, err)
		}
	}
}

func TestSize(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("one", Metadata{Status: http.StatusOK}, []byte("123")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("two", Metadata{Status: http.StatusOK}, []byte("4567")); err != nil {
		t.Fatal(err)
	}

	bytes, objects, err := store.Size()
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 7 || objects != 2 {
		t.Fatalf("size = (%d, %d)", bytes, objects)
	}
}

func TestVerifiedStreamRejectsInvalidBody(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	verifyErr := errors.New("digest mismatch")
	err = store.PutStreamVerified(
		"invalid",
		Metadata{Status: http.StatusOK},
		bytes.NewReader([]byte("invalid")),
		io.Discard,
		func(int64) error { return verifyErr },
	)
	if !errors.Is(err, verifyErr) {
		t.Fatalf("put error = %v", err)
	}
	if _, found, lookupErr := store.Lookup("invalid", time.Hour); lookupErr != nil || found {
		t.Fatalf("lookup after failed verification: found=%v err=%v", found, lookupErr)
	}
}

func TestCanceledDownstreamLeavesNoCompleteOrTemporaryObject(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	downstreamErr := errors.New("client canceled")
	downstream := &failAfterWriter{
		remaining: 4,
		err:       downstreamErr,
	}
	err = store.PutStream(
		"canceled-download",
		Metadata{Status: http.StatusOK},
		bytes.NewReader([]byte("incomplete response body")),
		downstream,
	)
	if !errors.Is(err, downstreamErr) {
		t.Fatalf("put error = %v, want %v", err, downstreamErr)
	}
	if _, found, lookupErr := store.Lookup("canceled-download", time.Hour); lookupErr != nil || found {
		t.Fatalf("lookup after cancellation: found=%v err=%v", found, lookupErr)
	}
	bytes, objects, err := store.Size()
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 0 || objects != 0 {
		t.Fatalf("canceled stream counted as complete: bytes=%d objects=%d", bytes, objects)
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && isTempName(entry.Name()) {
			t.Fatalf("temporary file remained after cancellation: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRestoredIncompleteOrCorruptObjectsAreNeverCountedAsComplete(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(t *testing.T, bodyPath, metadataPath string)
		wantLookupErr string
	}{
		{
			name: "missing body is a safe miss",
			mutate: func(t *testing.T, bodyPath, _ string) {
				t.Helper()
				if err := os.Remove(bodyPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "truncated body is rejected",
			mutate: func(t *testing.T, bodyPath, _ string) {
				t.Helper()
				if err := os.WriteFile(bodyPath, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantLookupErr: "cache body size mismatch",
		},
		{
			name: "malformed metadata is rejected",
			mutate: func(t *testing.T, _, metadataPath string) {
				t.Helper()
				if err := os.WriteFile(metadataPath, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantLookupErr: "decode cache metadata",
		},
		{
			name: "metadata size mismatch is rejected",
			mutate: func(t *testing.T, _ string, metadataPath string) {
				t.Helper()
				data, err := os.ReadFile(metadataPath)
				if err != nil {
					t.Fatal(err)
				}
				var metadata Metadata
				if err := json.Unmarshal(data, &metadata); err != nil {
					t.Fatal(err)
				}
				metadata.ContentBytes++
				data, err = json.Marshal(metadata)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantLookupErr: "cache body size mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutBytes(
				"restored-object",
				Metadata{Status: http.StatusOK},
				[]byte("complete before backup"),
			); err != nil {
				t.Fatal(err)
			}
			bodyPath, metadataPath := currentBodyPath(t, store, "restored-object")
			test.mutate(t, bodyPath, metadataPath)

			bytes, objects, err := store.Size()
			if err != nil {
				t.Fatal(err)
			}
			if bytes != 0 || objects != 0 {
				t.Fatalf("corrupt restored object counted as complete: bytes=%d objects=%d", bytes, objects)
			}

			entry, found, lookupErr := store.Lookup("restored-object", time.Hour)
			if found {
				_ = entry.Close()
				t.Fatal("corrupt restored object was returned")
			}
			if test.wantLookupErr == "" {
				if lookupErr != nil {
					t.Fatalf("safe miss returned error: %v", lookupErr)
				}
				return
			}
			if lookupErr == nil || !strings.Contains(lookupErr.Error(), test.wantLookupErr) {
				t.Fatalf("lookup error = %v, want substring %q", lookupErr, test.wantLookupErr)
			}
		})
	}
}

func currentBodyPath(t *testing.T, store *Store, key string) (string, string) {
	t.Helper()
	legacyBodyPath, metadataPath := store.paths(key)
	metadata, err := readMetadata(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	bodyPath, err := bodyPathForEntry(metadataPath, legacyBodyPath, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return bodyPath, metadataPath
}

type failAfterWriter struct {
	remaining int
	err       error
}

func (writer *failAfterWriter) Write(body []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, writer.err
	}
	if len(body) <= writer.remaining {
		writer.remaining -= len(body)
		return len(body), nil
	}
	written := writer.remaining
	writer.remaining = 0
	return written, writer.err
}
