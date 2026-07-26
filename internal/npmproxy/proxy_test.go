package npmproxy

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
)

func TestProxyRewritesAndCachesNPMMetadata(t *testing.T) {
	var requests atomic.Int64
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"name":"tiny","dist":{"tarball":%q}}`, upstreamURL+"/tiny/-/tiny-1.0.0.tgz")
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "npm",
		Path:          "/npm/",
		Upstream:      upstream.URL,
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		Store:         store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/npm/tiny", nil)
		request.Header.Set("Accept", "application/json")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d", attempt, response.Code)
		}
		if !strings.Contains(response.Body.String(), "http://packages.test/npm/tiny/-/tiny-1.0.0.tgz") {
			t.Fatalf("attempt %d: body = %s", attempt, response.Body.String())
		}
		wantCache := "MISS"
		if attempt == 1 {
			wantCache = "HIT"
		}
		if got := response.Header().Get("X-N0ding-Cache"); got != wantCache {
			t.Fatalf("attempt %d: cache = %q, want %q", attempt, got, wantCache)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
}

func TestProxyCachesTarball(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("package bytes"))
	}))
	defer upstream.Close()

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "npm",
		Path:          "/npm/",
		Upstream:      upstream.URL,
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		Store:         store,
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/npm/tiny/-/tiny-1.0.0.tgz", nil)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "package bytes" {
			t.Fatalf("attempt %d: status=%d body=%q", attempt, response.Code, response.Body.String())
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
}

func TestConcurrentCacheMissIsCoalesced(t *testing.T) {
	const clients = 8
	var requests atomic.Int64
	var startedOnce sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	body := []byte("shared package bytes")
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write(body)
	}))
	defer upstream.Close()

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "npm",
		Path:          "/npm/",
		Upstream:      upstream.URL,
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		Store:         store,
	})
	if err != nil {
		t.Fatal(err)
	}

	begin := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, clients)
	var wait sync.WaitGroup
	for client := 0; client < clients; client++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-begin
			request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/npm/tiny/-/tiny-1.0.0.tgz", nil)
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, request)
			responses <- response
		}()
	}
	close(begin)
	<-started
	close(release)
	wait.Wait()
	close(responses)

	for response := range responses {
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), body) {
			t.Fatalf("response: status=%d body=%q", response.Code, response.Body.String())
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
	snapshot := proxy.Snapshot()
	if snapshot.CacheMisses != 1 || snapshot.CacheHits != clients-1 || snapshot.CacheObjects != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestProxyDoesNotLeakAuthorizationByDefault(t *testing.T) {
	var authorization string
	var cookie string
	var npmOTP string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		cookie = request.Header.Get("Cookie")
		npmOTP = request.Header.Get("Npm-Otp")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "npm",
		Path:          "/npm/",
		Upstream:      upstream.URL,
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		Store:         store,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/npm/tiny", nil)
	request.Header.Set("Authorization", "Bearer should-not-leak")
	request.Header.Set("Cookie", "session=should-not-leak")
	request.Header.Set("Npm-Otp", "123456")
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	if authorization != "" {
		t.Fatalf("authorization leaked upstream: %q", authorization)
	}
	if cookie != "" {
		t.Fatalf("cookie leaked upstream: %q", cookie)
	}
	if npmOTP != "" {
		t.Fatalf("npm OTP leaked upstream: %q", npmOTP)
	}
}

func TestProxyDoesNotCachePrivateOrCredentialBearingResponse(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Cache-Control", "private, max-age=300")
		writer.Header().Set("Set-Cookie", "session=upstream-secret; HttpOnly")
		_, _ = writer.Write([]byte("private package bytes"))
	}))
	defer upstream.Close()

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:          "npm",
		Path:          "/npm/",
		Upstream:      upstream.URL,
		PublicBaseURL: "http://packages.test",
		TTL:           time.Hour,
		Store:         store,
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/npm/private/-/private-1.0.0.tgz", nil)
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "private package bytes" {
			t.Fatalf("attempt %d: status=%d body=%q", attempt+1, response.Code, response.Body.String())
		}
		if response.Header().Get("X-N0ding-Cache") != "MISS" {
			t.Fatalf("attempt %d cache result = %q", attempt+1, response.Header().Get("X-N0ding-Cache"))
		}
		if response.Header().Get("Set-Cookie") == "" {
			t.Fatalf("attempt %d did not preserve Set-Cookie for current client", attempt+1)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
	if objects := proxy.Snapshot().CacheObjects; objects != 0 {
		t.Fatalf("cache objects = %d", objects)
	}
}

func TestProxyForwardsAuthorizationWithoutCachingWhenEnabled(t *testing.T) {
	var requests atomic.Int64
	var authorization string
	var cookie string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		authorization = request.Header.Get("Authorization")
		cookie = request.Header.Get("Cookie")
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("authorized package bytes"))
	}))
	defer upstream.Close()

	store, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Options{
		Name:                 "npm",
		Path:                 "/npm/",
		Upstream:             upstream.URL,
		PublicBaseURL:        "http://packages.test",
		TTL:                  time.Hour,
		ForwardAuthorization: true,
		Store:                store,
	})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "http://n0ding.test/npm/private/-/private-1.0.0.tgz", nil)
		request.Header.Set("Authorization", "Bearer private-token")
		request.Header.Set("Cookie", "session=must-not-leak")
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("X-N0ding-Cache") != "MISS" {
			t.Fatalf(
				"attempt %d: status=%d cache=%q",
				attempt+1,
				response.Code,
				response.Header().Get("X-N0ding-Cache"),
			)
		}
	}
	if authorization != "Bearer private-token" {
		t.Fatalf("Authorization = %q", authorization)
	}
	if cookie != "" {
		t.Fatalf("Cookie forwarded as %q", cookie)
	}
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d", requests.Load())
	}
	if objects := proxy.Snapshot().CacheObjects; objects != 0 {
		t.Fatalf("cache objects = %d", objects)
	}
}
