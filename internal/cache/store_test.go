package cache

import (
	"bytes"
	"net/http"
	"os"
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
