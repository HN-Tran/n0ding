package pypiproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
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
	"github.com/HN-Tran/n0ding/internal/storage"
)

func TestClientCancellationIsNotCountedAsRepositoryError(t *testing.T) {
	proxy := &Proxy{name: "pypi", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/pypi/simple/idna/", nil)

	proxy.fail(httptest.NewRecorder(), request, 499, "upstream request failed", context.Canceled)

	if canceled, failures := proxy.stats.clientCanceled.Load(), proxy.stats.errors.Load(); canceled != 1 || failures != 0 {
		t.Fatalf("client_canceled=%d errors=%d", canceled, failures)
	}
}

type cancelingStreamWriter struct {
	header http.Header
	cancel context.CancelFunc
	err    error
}

func (w *cancelingStreamWriter) Header() http.Header { return w.header }
func (w *cancelingStreamWriter) WriteHeader(int)     {}
func (w *cancelingStreamWriter) Write([]byte) (int, error) {
	if w.cancel != nil {
		w.cancel()
	}
	return 0, w.err
}

func TestStreamResponseClassifiesCancellationAndCleansCache(t *testing.T) {
	root := t.TempDir()
	store, err := cache.New(root)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &Proxy{name: "pypi", store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/pypi/files/x.whl", nil).WithContext(ctx)
	writer := &cancelingStreamWriter{header: make(http.Header), cancel: cancel, err: context.Canceled}
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("wheel bytes"))}
	proxy.streamResponse(writer, request, response, make(http.Header), make(http.Header), "key", true, "", nil)
	if proxy.stats.clientCanceled.Load() != 1 || proxy.stats.errors.Load() != 0 {
		t.Fatalf("canceled=%d errors=%d", proxy.stats.clientCanceled.Load(), proxy.stats.errors.Load())
	}
	if bytes, objects, err := store.Size(); err != nil || bytes != 0 || objects != 0 {
		t.Fatalf("cache=%d/%d err=%v", bytes, objects, err)
	}
	assertNoStreamCacheFiles(t, root)
}

func assertNoStreamCacheFiles(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("unexpected cache file %s", path)
		}
		return nil
	})
}

func TestStreamResponseClassifiesNonCancellationError(t *testing.T) {
	store, _ := cache.New(t.TempDir())
	proxy := &Proxy{name: "pypi", store: store, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/pypi/files/x.whl", nil)
	writer := &cancelingStreamWriter{header: make(http.Header), err: errors.New("broken downstream")}
	proxy.streamResponse(writer, request, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("wheel"))}, make(http.Header), make(http.Header), "key", true, "", nil)
	if proxy.stats.clientCanceled.Load() != 0 || proxy.stats.errors.Load() != 1 {
		t.Fatalf("canceled=%d errors=%d", proxy.stats.clientCanceled.Load(), proxy.stats.errors.Load())
	}
}

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
	requestURL := strings.Split(fileURL, "#")[0]
	parsed, err := url.Parse(requestURL)
	if err != nil {
		t.Fatal(err)
	}
	target, expectedSHA256, err := proxy.fileTargetURL(parsed)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := proxy.store.Lookup(proxy.cacheKey("file", target, "", expectedSHA256), time.Hour)
	if err != nil || !found {
		t.Fatalf("verified file cache entry found=%v err=%v", found, err)
	}
	defer entry.Close()
	if want := "sha256:" + sha; entry.Metadata.ContentDigest != want {
		t.Fatalf("content digest = %q, want %q", entry.Metadata.ContentDigest, want)
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

func TestProxyServesPEP658MetadataSidecar(t *testing.T) {
	metadataBody := []byte("Metadata-Version: 2.1\nName: tiny\nVersion: 1.0.0\n")
	var requestedPath string
	files := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestedPath = request.URL.Path
		_, _ = writer.Write(metadataBody)
	}))
	defer files.Close()

	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	proxy, err := newTestProxy(t, upstream.URL, files.URL, false)
	if err != nil {
		t.Fatal(err)
	}

	distributionURL := "http://n0ding.test/pypi/files/tiny-1.0.0-py3-none-any.whl?url=" +
		urlQueryEscape(files.URL+"/packages/tiny-1.0.0-py3-none-any.whl") +
		"&sha256=" + strings.Repeat("0", 64)
	metadataURL := strings.Replace(distributionURL, ".whl?", ".whl.metadata?", 1)
	request := httptest.NewRequest(http.MethodGet, metadataURL, nil)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), metadataBody) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if requestedPath != "/packages/tiny-1.0.0-py3-none-any.whl.metadata" {
		t.Fatalf("metadata upstream path = %q", requestedPath)
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

func TestPrivateUploadIsAuthenticatedImmutableAndInstallable(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "pypi",
		Path:          "/pypi/simple/",
		Upstream:      upstream.URL + "/simple/",
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		PublishToken:  strings.Repeat("p", 32),
		LocalPath:     t.TempDir(),
		Store:         store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	upload := func(token string, content []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("name", "Private_Demo")
		_ = form.WriteField("requires_python", ">=3.11")
		part, createErr := form.CreateFormFile("content", "private_demo-1.0.0-py3-none-any.whl")
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, _ = part.Write(content)
		_ = form.Close()
		request := httptest.NewRequest(http.MethodPost, "http://packages.test/pypi/legacy/", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		if token != "" {
			request.SetBasicAuth("__token__", token)
		}
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		return response
	}

	if response := upload("wrong", []byte("wheel-one")); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized upload status = %d", response.Code)
	}
	if response := upload(strings.Repeat("p", 32), []byte("wheel-one")); response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", response.Code, response.Body.String())
	}
	if response := upload(strings.Repeat("p", 32), []byte("wheel-two")); response.Code != http.StatusConflict {
		t.Fatalf("overwrite status = %d, want 409", response.Code)
	}

	indexRequest := httptest.NewRequest(http.MethodGet, "http://packages.test/pypi/simple/private-demo/", nil)
	indexRequest.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	indexResponse := httptest.NewRecorder()
	proxy.ServeHTTP(indexResponse, indexRequest)
	if indexResponse.Code != http.StatusOK || !strings.Contains(indexResponse.Body.String(), "private_demo-1.0.0-py3-none-any.whl") {
		t.Fatalf("private index status=%d body=%s", indexResponse.Code, indexResponse.Body.String())
	}
	var index struct {
		Files []struct {
			URL string `json:"url"`
		} `json:"files"`
	}
	if err := json.Unmarshal(indexResponse.Body.Bytes(), &index); err != nil || len(index.Files) != 1 {
		t.Fatalf("decode private index: files=%d err=%v", len(index.Files), err)
	}
	fileRequest := httptest.NewRequest(http.MethodGet, strings.Split(index.Files[0].URL, "#")[0], nil)
	fileResponse := httptest.NewRecorder()
	proxy.ServeHTTP(fileResponse, fileRequest)
	if fileResponse.Code != http.StatusOK || fileResponse.Body.String() != "wheel-one" {
		t.Fatalf("private file status=%d body=%q", fileResponse.Code, fileResponse.Body.String())
	}
}

func TestPrivateUploadHonorsStorageQuota(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "pypi",
		Path:          "/pypi/simple/",
		Upstream:      upstream.URL + "/simple/",
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		PublishToken:  strings.Repeat("p", 32),
		LocalPath:     t.TempDir(),
		Store:         store,
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy.SetStorageController(storage.NewController(1, 0.9, 0.75, 0, 0))
	proxy.freeBytes = func(string) (int64, error) { return 1 << 30, nil }

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("name", "quota-demo")
	part, err := form.CreateFormFile("content", "quota_demo-1.0.0-py3-none-any.whl")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("larger than quota"))
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "http://packages.test/pypi/legacy/", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.SetBasicAuth("__token__", strings.Repeat("p", 32))
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf("quota upload status = %d, want 507", response.Code)
	}
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
