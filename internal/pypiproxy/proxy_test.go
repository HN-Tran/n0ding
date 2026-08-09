package pypiproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
)

func TestProxyRewritesAndCachesSimpleHTMLAndHashedFile(t *testing.T) {
	fileBody := []byte("wheel bytes")
	hash := sha256.Sum256(fileBody)
	sha := hex.EncodeToString(hash[:])
	var simpleRequests atomic.Int64
	var fileRequests atomic.Int64

	files := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fileRequests.Add(1)
		if request.URL.Path != "/packages/tiny-1.0.0-py3-none-any.whl" {
			t.Fatalf("file path = %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(fileBody)
	}))
	defer files.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		simpleRequests.Add(1)
		if request.URL.Path != "/simple/tiny/" {
			t.Errorf("simple path = %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+html")
		fmt.Fprintf(
			writer,
			`<!doctype html><a href=%q data-requires-python=">=3.11" data-yanked="bad release">tiny</a>`,
			files.URL+"/packages/tiny-1.0.0-py3-none-any.whl#sha256="+sha,
		)
	}))
	defer upstream.Close()

	proxy, err := newTestProxy(t, upstream.URL, files.URL, false)
	if err != nil {
		t.Fatal(err)
	}

	var fileURL string
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/pypi/simple/tiny/", nil)
		request.Header.Set("Accept", "text/html")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d", attempt+1, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, "data-requires-python=\"&gt;=3.11\"") ||
			!strings.Contains(body, `data-yanked="bad release"`) {
			t.Fatalf("attempt %d: metadata attributes were not preserved: %s", attempt+1, body)
		}
		if !strings.Contains(body, "http://packages.test/pypi/files/tiny-1.0.0-py3-none-any.whl?") ||
			!strings.Contains(body, "#sha256="+sha) {
			t.Fatalf("attempt %d: file URL was not rewritten with hash: %s", attempt+1, body)
		}
		wantCache := "MISS"
		if attempt == 1 {
			wantCache = "HIT"
		}
		if got := response.Header().Get("X-N0ding-Cache"); got != wantCache {
			t.Fatalf("attempt %d: cache = %q, want %q", attempt+1, got, wantCache)
		}
		if fileURL == "" {
			fileURL = extractHref(t, body)
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		requestURL := strings.Split(fileURL, "#")[0]
		request := httptest.NewRequest(http.MethodGet, requestURL, nil)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), fileBody) {
			t.Fatalf("file attempt %d: url=%s status=%d body=%q", attempt+1, fileURL, response.Code, response.Body.String())
		}
		wantCache := "MISS"
		if attempt == 1 {
			wantCache = "HIT"
		}
		if got := response.Header().Get("X-N0ding-Cache"); got != wantCache {
			t.Fatalf("file attempt %d: cache = %q, want %q", attempt+1, got, wantCache)
		}
	}

	if simpleRequests.Load() != 1 {
		t.Fatalf("simple upstream requests = %d", simpleRequests.Load())
	}
	if fileRequests.Load() != 1 {
		t.Fatalf("file upstream requests = %d", fileRequests.Load())
	}
	if objects := proxy.Snapshot().CacheObjects; objects != 2 {
		t.Fatalf("cache objects = %d", objects)
	}
}

func TestProxyRewritesSimpleJSON(t *testing.T) {
	var fileURL string
	files := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("sdist bytes"))
	}))
	defer files.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		fmt.Fprintf(writer, `{"meta":{"api-version":"1.0"},"files":[{"filename":"tiny.tar.gz","url":%q}]}`, fileURL)
	}))
	defer upstream.Close()
	fileURL = files.URL + "/packages/tiny.tar.gz"

	proxy, err := newTestProxy(t, upstream.URL, files.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/pypi/simple/tiny/", nil)
	request.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "http://packages.test/pypi/files/tiny.tar.gz?") {
		t.Fatalf("JSON URL was not rewritten: %s", response.Body.String())
	}
}

func TestProxyRejectsUnallowedFileOrigin(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	proxy, err := newTestProxy(t, upstream.URL, "", false)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"http://n0ding.test/pypi/files/?url="+urlQueryEscape("https://other.example/packages/pkg.whl"),
		nil,
	)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestProxyDoesNotCacheHashMismatch(t *testing.T) {
	var fileRequests atomic.Int64
	files := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fileRequests.Add(1)
		_, _ = writer.Write([]byte("wrong bytes"))
	}))
	defer files.Close()

	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	proxy, err := newTestProxy(t, upstream.URL, files.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	fileURL := "http://n0ding.test/pypi/files/?url=" + urlQueryEscape(files.URL+"/packages/pkg.whl") +
		"&sha256=" + strings.Repeat("0", 64)

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, fileURL, nil)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "wrong bytes" {
			t.Fatalf("attempt %d: status=%d body=%q", attempt+1, response.Code, response.Body.String())
		}
		if got := response.Header().Get("X-N0ding-Cache"); got != "MISS" {
			t.Fatalf("attempt %d: cache = %q", attempt+1, got)
		}
	}
	if fileRequests.Load() != 2 {
		t.Fatalf("file upstream requests = %d", fileRequests.Load())
	}
	if objects := proxy.Snapshot().CacheObjects; objects != 0 {
		t.Fatalf("cache objects = %d", objects)
	}
}

func TestAuthorizedPyPIRequestsBypassPersistentCache(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.Header.Get("Authorization") {
		case "Bearer identity-a-canary":
			_, _ = writer.Write([]byte("private package for A"))
		case "Bearer identity-b-canary":
			_, _ = writer.Write([]byte("private package for B"))
		default:
			http.Error(writer, "denied", http.StatusForbidden)
		}
	}))
	defer upstream.Close()

	cacheDirectory := t.TempDir()
	store, err := cache.New(cacheDirectory)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:                 "pypi",
		Path:                 "/pypi/simple/",
		Upstream:             upstream.URL + "/simple",
		PublicBaseURL:        "http://packages.test",
		TTL:                  time.Hour,
		ForwardAuthorization: true,
		AllowedFileOrigins:   []string{upstream.URL},
		Store:                store,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		authorization string
		body          string
	}{
		{"Bearer identity-a-canary", "private package for A"},
		{"Bearer identity-b-canary", "private package for B"},
		{"Bearer identity-a-canary", "private package for A"},
	}
	for attempt, test := range tests {
		request := httptest.NewRequest(
			http.MethodGet,
			"http://n0ding.test/pypi/files/?url="+urlQueryEscape(upstream.URL+"/packages/private.whl?query-canary=1"),
			nil,
		)
		request.Header.Set("Authorization", test.authorization)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK ||
			response.Body.String() != test.body ||
			response.Header().Get("X-N0ding-Cache") != "MISS" {
			t.Fatalf(
				"attempt %d: status=%d cache=%q body=%q",
				attempt+1,
				response.Code,
				response.Header().Get("X-N0ding-Cache"),
				response.Body.String(),
			)
		}
	}
	if requests.Load() != int64(len(tests)) {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
	if objects := proxy.Snapshot().CacheObjects; objects != 0 {
		t.Fatalf("cache objects = %d", objects)
	}
	assertDirectoryExcludesCanaries(t, cacheDirectory, "identity-a-canary", "identity-b-canary", "query-canary")
}

func TestProxyFailureLogAndErrorRedactCredentialCanaries(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:                 "pypi",
		Path:                 "/pypi/simple/",
		Upstream:             "https://userinfo-canary:password-canary@pypi.example.test/simple",
		PublicBaseURL:        "http://packages.test",
		TTL:                  time.Hour,
		ForwardAuthorization: true,
		Store:                store,
		Client:               client,
		Logger:               logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"http://n0ding.test/pypi/simple/private/?access_token=query-canary",
		nil,
	)
	request.Header.Set("Authorization", "Bearer authorization-canary")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", response.Code)
	}

	output := logs.String() + response.Body.String()
	for _, canary := range []string{
		"userinfo-canary",
		"password-canary",
		"query-canary",
		"authorization-canary",
	} {
		if strings.Contains(output, canary) {
			t.Fatalf("log or client error leaked %q: %s", canary, output)
		}
	}
}

func newTestProxy(t *testing.T, upstreamURL, fileOrigin string, forwardAuthorization bool) (*Proxy, error) {
	t.Helper()
	store, err := cache.New(t.TempDir())
	if err != nil {
		return nil, err
	}
	var origins []string
	if fileOrigin != "" {
		origins = append(origins, fileOrigin)
	}
	return New(Options{
		Name:                 "pypi",
		Path:                 "/pypi/simple/",
		Upstream:             upstreamURL + "/simple",
		PublicBaseURL:        "http://packages.test",
		TTL:                  time.Hour,
		ForwardAuthorization: forwardAuthorization,
		AllowedFileOrigins:   origins,
		Store:                store,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func extractHref(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `href="`)
	if start == -1 {
		t.Fatalf("href not found: %s", body)
	}
	start += len(`href="`)
	end := strings.Index(body[start:], `"`)
	if end == -1 {
		t.Fatalf("href not terminated: %s", body)
	}
	return strings.ReplaceAll(body[start:start+end], "&amp;", "&")
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func assertDirectoryExcludesCanaries(t *testing.T, root string, canaries ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, canary := range canaries {
			if bytes.Contains(content, []byte(canary)) {
				t.Errorf("cache file %q contains credential canary %q", path, canary)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
