package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
	"github.com/HN-Tran/n0ding/internal/config"
)

func TestStatusAndSetupEndpoints(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"name":"tiny"}`))
	}))
	defer upstream.Close()

	handler, err := New(config.Config{
		Server:  config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{Path: filepath.Join(t.TempDir(), "data")},
		Repositories: []config.Repository{{
			Name:     "npm",
			Type:     "npm",
			Path:     "/npm/",
			Upstream: upstream.URL,
			TTL:      time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK ||
		!strings.Contains(statusResponse.Body.String(), `"version": "test"`) ||
		!strings.Contains(statusResponse.Body.String(), `"client_canceled": 0`) {
		t.Fatalf("status: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}

	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, metricsRequest)
	if metricsResponse.Code != http.StatusOK ||
		!strings.Contains(metricsResponse.Body.String(), `n0ding_repository_client_canceled_total{repository="npm",type="npm"} 0`) {
		t.Fatalf("metrics: code=%d body=%s", metricsResponse.Code, metricsResponse.Body.String())
	}

	setupRequest := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/npm/setup", nil)
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	if got := setupResponse.Body.String(); got != "npm config set registry http://packages.test/npm/\n" {
		t.Fatalf("setup snippet = %q", got)
	}
}

func TestOCIRepositoryWiring(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	handler, err := New(config.Config{
		Server:  config.Server{PublicBaseURL: "http://registry.test:8080"},
		Storage: config.Storage{Path: filepath.Join(t.TempDir(), "data")},
		Repositories: []config.Repository{{
			Name:     "oci",
			Type:     "oci",
			Path:     "/v2/",
			Upstream: upstream.URL,
			TTL:      time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	pingRequest := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	pingResponse := httptest.NewRecorder()
	handler.ServeHTTP(pingResponse, pingRequest)
	if pingResponse.Code != http.StatusOK {
		t.Fatalf("ping status = %d", pingResponse.Code)
	}

	setupRequest := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/oci/setup", nil)
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	if got := setupResponse.Body.String(); got != "docker pull registry.test:8080/library/alpine:3.20\n" {
		t.Fatalf("setup snippet = %q", got)
	}
}

func TestPyPIRepositoryWiring(t *testing.T) {
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/simple/tiny/" {
			t.Errorf("path = %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+html")
		_, _ = writer.Write([]byte(`<a href="` + upstreamURL + `/packages/tiny-1.0.0.tar.gz">tiny</a>`))
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	handler, err := New(config.Config{
		Server:  config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{Path: filepath.Join(t.TempDir(), "data")},
		Repositories: []config.Repository{{
			Name:     "pypi",
			Type:     "pypi",
			Path:     "/pypi/simple/",
			Upstream: upstream.URL + "/simple",
			TTL:      time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/pypi/simple/tiny/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "http://packages.test/pypi/files/tiny-1.0.0.tar.gz?") {
		t.Fatalf("PyPI response: status=%d body=%s", response.Code, response.Body.String())
	}

	setupRequest := httptest.NewRequest(http.MethodGet, "/api/v1/repositories/pypi/setup", nil)
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	if got := setupResponse.Body.String(); !strings.Contains(got, "python -m pip install --index-url http://packages.test/pypi/simple/ PACKAGE") {
		t.Fatalf("setup snippet = %q", got)
	}
}

func TestStatusRedactsUpstreamCredentialComponents(t *testing.T) {
	handler, err := New(config.Config{
		Server:  config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{Path: filepath.Join(t.TempDir(), "data")},
		Repositories: []config.Repository{{
			Name:     "npm",
			Type:     "npm",
			Path:     "/npm/",
			Upstream: "https://user:password@packages.example.test/base?token=secret#fragment",
			TTL:      time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, body)
	}
	if !strings.Contains(body, `"upstream": "https://packages.example.test/base"`) {
		t.Fatalf("sanitized upstream missing: %s", body)
	}
	for _, secret := range []string{"user", "password", "token", "secret", "fragment"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
}

func TestNewRunsStartupCacheMaintenance(t *testing.T) {
	root := t.TempDir()
	repositoryRoot := filepath.Join(root, "npm")
	store, err := cache.New(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBytes("expired", cache.Metadata{Status: http.StatusOK}, []byte("expired")); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(repositoryRoot, "ab")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	staleTemp := filepath.Join(shard, ".body-stale")
	if err := os.WriteFile(staleTemp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(staleTemp, old, old); err != nil {
		t.Fatal(err)
	}

	_, err = New(config.Config{
		Server: config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{
			Path:         root,
			MaxAge:       time.Nanosecond,
			GCInterval:   time.Hour,
			StaleTempAge: time.Hour,
		},
		Repositories: []config.Repository{{
			Name:     "npm",
			Type:     "npm",
			Path:     "/npm/",
			Upstream: "https://registry.npmjs.org",
			TTL:      time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	bytes, objects, err := store.Size()
	if err != nil {
		t.Fatal(err)
	}
	if bytes != 0 || objects != 0 {
		t.Fatalf("cache size after startup GC = (%d, %d)", bytes, objects)
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Fatalf("stale temp still exists: %v", err)
	}
}

func TestRunMaintenanceDeletesExpiredCompleteObject(t *testing.T) {
	server, err := New(config.Config{
		Server: config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{
			Path:         t.TempDir(),
			MaxAge:       20 * time.Millisecond,
			GCInterval:   5 * time.Millisecond,
			StaleTempAge: time.Hour,
		},
		Repositories: []config.Repository{{
			Name:     "npm",
			Type:     "npm",
			Path:     "/npm/",
			Upstream: "https://registry.npmjs.org",
			TTL:      time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store := server.stores[0].store
	if err := store.PutBytes("periodic", cache.Metadata{Status: http.StatusOK}, []byte("cached")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.RunMaintenance(ctx)

	deadline := time.Now().Add(time.Second)
	for {
		bytes, objects, sizeErr := store.Size()
		if sizeErr != nil {
			t.Fatal(sizeErr)
		}
		if bytes == 0 && objects == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("periodic GC did not delete object: size=(%d, %d)", bytes, objects)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
