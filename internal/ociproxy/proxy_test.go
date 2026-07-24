package ociproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
)

func TestRegistryAuthChallengeIsPreserved(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		writer.Header().Set(
			"WWW-Authenticate",
			`Bearer realm="https://auth.example.test/token",service="registry.example.test"`,
		)
		http.Error(writer, "authentication required", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/v2/", nil)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("WWW-Authenticate"); got != `Bearer realm="https://auth.example.test/token",service="registry.example.test"` {
		t.Fatalf("challenge = %q", got)
	}
	if got := response.Header().Get("Docker-Distribution-Api-Version"); got != "registry/2.0" {
		t.Fatalf("API version = %q", got)
	}
}

func TestManifestIsDigestVerifiedAndPersistsAcrossProxyRestart(t *testing.T) {
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	digest := digestOf(body)
	var getRequests atomic.Int64
	var headRequests atomic.Int64
	var authorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead {
			headRequests.Add(1)
		} else {
			getRequests.Add(1)
		}
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		writer.Header().Set("Docker-Content-Digest", digest)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(body)
		}
	}))
	defer upstream.Close()

	cacheDirectory := t.TempDir()
	proxy := newTestProxy(t, upstream.URL, cacheDirectory)
	first := performRequest(proxy, http.MethodGet, "/v2/library/tiny/manifests/latest", "Bearer public-pull-token")
	if first.Code != http.StatusOK || first.Body.String() != string(body) {
		t.Fatalf("first response: status=%d body=%q", first.Code, first.Body.String())
	}
	if first.Header().Get("X-N0ding-Cache") != "MISS" {
		t.Fatalf("first cache result = %q", first.Header().Get("X-N0ding-Cache"))
	}
	if authorization != "Bearer public-pull-token" {
		t.Fatalf("authorization = %q", authorization)
	}

	restarted := newTestProxy(t, upstream.URL, cacheDirectory)
	second := performRequest(restarted, http.MethodGet, "/v2/library/tiny/manifests/latest", "Bearer another-token")
	if second.Code != http.StatusOK || second.Body.String() != string(body) {
		t.Fatalf("second response: status=%d body=%q", second.Code, second.Body.String())
	}
	if second.Header().Get("X-N0ding-Cache") != "HIT" {
		t.Fatalf("second cache result = %q", second.Header().Get("X-N0ding-Cache"))
	}
	if second.Header().Get("Docker-Content-Digest") != digest {
		t.Fatalf("digest = %q", second.Header().Get("Docker-Content-Digest"))
	}
	if getRequests.Load() != 1 || headRequests.Load() != 1 {
		t.Fatalf("upstream requests: GET=%d HEAD=%d", getRequests.Load(), headRequests.Load())
	}
}

func TestBlobIsDigestVerifiedAndCached(t *testing.T) {
	body := []byte("compressed OCI layer bytes")
	digest := digestOf(body)
	var getRequests atomic.Int64
	var headRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead {
			headRequests.Add(1)
		} else {
			getRequests.Add(1)
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Docker-Content-Digest", digest)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(body)
		}
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	path := "/v2/library/tiny/blobs/" + digest
	first := performRequest(proxy, http.MethodGet, path, "Bearer token")
	second := performRequest(proxy, http.MethodGet, path, "Bearer token")

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d", first.Code, second.Code)
	}
	if first.Header().Get("X-N0ding-Cache") != "MISS" || second.Header().Get("X-N0ding-Cache") != "HIT" {
		t.Fatalf("cache results = %q, %q", first.Header().Get("X-N0ding-Cache"), second.Header().Get("X-N0ding-Cache"))
	}
	if second.Body.String() != string(body) || second.Header().Get("Docker-Content-Digest") != digest {
		t.Fatalf("cached blob: digest=%q body=%q", second.Header().Get("Docker-Content-Digest"), second.Body.String())
	}
	if getRequests.Load() != 1 || headRequests.Load() != 1 {
		t.Fatalf("upstream requests: GET=%d HEAD=%d", getRequests.Load(), headRequests.Load())
	}
}

func TestConcurrentBlobCacheMissIsCoalesced(t *testing.T) {
	const clients = 8
	body := []byte("shared compressed OCI layer bytes")
	digest := digestOf(body)
	var getRequests atomic.Int64
	var headRequests atomic.Int64
	var startedOnce sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Docker-Content-Digest", digest)
		if request.Method == http.MethodHead {
			headRequests.Add(1)
			return
		}
		getRequests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		_, _ = writer.Write(body)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	path := "/v2/library/tiny/blobs/" + digest
	begin := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, clients)
	var wait sync.WaitGroup
	for client := 0; client < clients; client++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-begin
			responses <- performRequest(proxy, http.MethodGet, path, "")
		}()
	}
	close(begin)
	<-started
	close(release)
	wait.Wait()
	close(responses)

	for response := range responses {
		if response.Code != http.StatusOK || response.Body.String() != string(body) {
			t.Fatalf("response: status=%d body=%q", response.Code, response.Body.String())
		}
	}
	if getRequests.Load() != 1 {
		t.Fatalf("upstream GET requests = %d", getRequests.Load())
	}
	if headRequests.Load() != clients-1 {
		t.Fatalf("upstream HEAD requests = %d", headRequests.Load())
	}
	snapshot := proxy.Snapshot()
	if snapshot.CacheMisses != 1 || snapshot.CacheHits != clients-1 || snapshot.CacheObjects != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestBlobWithWrongDigestIsNotCached(t *testing.T) {
	body := []byte("wrong bytes")
	requestedDigest := digestOf([]byte("expected bytes"))
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(body)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	path := "/v2/library/tiny/blobs/" + requestedDigest
	first := performRequest(proxy, http.MethodGet, path, "Bearer token")
	second := performRequest(proxy, http.MethodGet, path, "Bearer token")

	if first.Header().Get("X-N0ding-Cache") != "MISS" || second.Header().Get("X-N0ding-Cache") != "MISS" {
		t.Fatalf("cache results = %q, %q", first.Header().Get("X-N0ding-Cache"), second.Header().Get("X-N0ding-Cache"))
	}
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
	if proxy.Snapshot().Errors != 2 {
		t.Fatalf("errors = %d", proxy.Snapshot().Errors)
	}
}

func TestBlobRangeRequestIsForwardedAndNotCached(t *testing.T) {
	body := []byte("compressed OCI layer bytes")
	digest := digestOf(body)
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if got := request.Header.Get("Range"); got != "bytes=11-" {
			t.Errorf("Range = %q", got)
		}
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Content-Range", "bytes 11-25/26")
		writer.Header().Set("Docker-Content-Digest", digest)
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = writer.Write(body[11:])
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	path := "/v2/library/tiny/blobs/" + digest
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "http://n0ding.test"+path, nil)
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Range", "bytes=11-")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)

		if response.Code != http.StatusPartialContent {
			t.Fatalf("attempt %d status = %d", attempt+1, response.Code)
		}
		if response.Header().Get("Content-Range") != "bytes 11-25/26" {
			t.Fatalf("attempt %d Content-Range = %q", attempt+1, response.Header().Get("Content-Range"))
		}
		if response.Header().Get("X-N0ding-Cache") != "MISS" {
			t.Fatalf("attempt %d cache result = %q", attempt+1, response.Header().Get("X-N0ding-Cache"))
		}
		if response.Body.String() != string(body[11:]) {
			t.Fatalf("attempt %d body = %q", attempt+1, response.Body.String())
		}
	}

	snapshot := proxy.Snapshot()
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
	if snapshot.RangeRequests != 2 {
		t.Fatalf("range requests = %d", snapshot.RangeRequests)
	}
	if snapshot.CacheObjects != 0 {
		t.Fatalf("cache objects = %d", snapshot.CacheObjects)
	}
}

func TestChangedTagDigestRefreshesCachedManifest(t *testing.T) {
	firstBody := []byte(`{"schemaVersion":2,"generation":1}`)
	secondBody := []byte(`{"schemaVersion":2,"generation":2}`)
	var changed atomic.Bool
	var getRequests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := firstBody
		if changed.Load() {
			body = secondBody
		}
		writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		writer.Header().Set("Docker-Content-Digest", digestOf(body))
		if request.Method != http.MethodHead {
			getRequests.Add(1)
			_, _ = writer.Write(body)
		}
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	first := performRequest(proxy, http.MethodGet, "/v2/library/tiny/manifests/latest", "Bearer token")
	if first.Body.String() != string(firstBody) {
		t.Fatalf("first body = %q", first.Body.String())
	}

	changed.Store(true)
	second := performRequest(proxy, http.MethodGet, "/v2/library/tiny/manifests/latest", "Bearer token")
	if second.Body.String() != string(secondBody) {
		t.Fatalf("second body = %q", second.Body.String())
	}
	if second.Header().Get("X-N0ding-Cache") != "MISS" {
		t.Fatalf("second cache result = %q", second.Header().Get("X-N0ding-Cache"))
	}
	if getRequests.Load() != 2 {
		t.Fatalf("GET requests = %d", getRequests.Load())
	}
}

func TestDeniedTokenCannotUseCachedObject(t *testing.T) {
	body := []byte(`{"schemaVersion":2}`)
	digest := digestOf(body)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer allowed" {
			http.Error(writer, "denied", http.StatusForbidden)
			return
		}
		writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		writer.Header().Set("Docker-Content-Digest", digest)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(body)
		}
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, upstream.URL, t.TempDir())
	first := performRequest(proxy, http.MethodGet, "/v2/library/tiny/manifests/latest", "Bearer allowed")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}

	denied := performRequest(proxy, http.MethodGet, "/v2/library/tiny/manifests/latest", "Bearer denied")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d", denied.Code)
	}
	if denied.Header().Get("X-N0ding-Cache") == "HIT" {
		t.Fatal("denied request was served from cache")
	}
}

func newTestProxy(t *testing.T, upstream, cacheDirectory string) *Proxy {
	t.Helper()
	store, err := cache.New(cacheDirectory)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "oci",
		Path:          "/v2/",
		Upstream:      upstream,
		PublicBaseURL: "http://n0ding.test:8080",
		TTL:           time.Hour,
		Store:         store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return proxy
}

func performRequest(proxy *Proxy, method, path, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://n0ding.test"+path, nil)
	request.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	return response
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
