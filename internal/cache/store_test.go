package cache

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	orphanMetadataBody, orphanMetadataPath := store.paths("orphan-metadata")
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
