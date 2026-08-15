package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
	"github.com/HN-Tran/n0ding/internal/config"
	"github.com/HN-Tran/n0ding/internal/maintenance"
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

func TestOperatorEndpointReportsBoundedStorageAndGC(t *testing.T) {
	server, err := New(config.Config{
		Server: config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{
			Path:          t.TempDir(),
			MaxAge:        time.Hour,
			GCInterval:    time.Hour,
			MaxBytes:      1_000,
			HighWatermark: 0.9,
			LowWatermark:  0.75,
			MinFreeBytes:  1,
			StaleTempAge:  time.Hour,
		},
		Repositories: []config.Repository{{
			Name: "npm", Type: "npm", Path: "/npm/",
			Upstream: "https://registry.npmjs.org", TTL: time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/operator", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response operatorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "ok" || !response.Storage.Bounded || response.Storage.MaxBytes != 1_000 {
		t.Fatalf("response = %#v", response)
	}
	if response.GC.State != "idle" || response.GC.Last == nil || response.GC.Last.Trigger != maintenance.TriggerStartup {
		t.Fatalf("GC snapshot = %#v", response.GC)
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRecorder := httptest.NewRecorder()
	server.ServeHTTP(metricsRecorder, metricsRequest)
	for _, expected := range []string{
		"# TYPE n0ding_storage_committed_bytes gauge",
		"n0ding_storage_max_bytes 1000",
		"n0ding_storage_pressure 0",
		"n0ding_gc_running 0",
		"n0ding_gc_last_errors 0",
	} {
		if !strings.Contains(metricsRecorder.Body.String(), expected) {
			t.Fatalf("metrics missing %q: %s", expected, metricsRecorder.Body.String())
		}
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/operator", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func TestPressureCollectionRemovesLRUUntilLowWatermark(t *testing.T) {
	server, err := New(config.Config{
		Server: config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{
			Path: t.TempDir(), MaxAge: time.Hour, GCInterval: time.Hour,
			MaxBytes: 10, HighWatermark: 0.9, LowWatermark: 0.5,
			StaleTempAge: time.Hour,
		},
		Repositories: []config.Repository{{
			Name: "npm", Type: "npm", Path: "/npm/",
			Upstream: "https://registry.npmjs.org", TTL: time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	store := server.stores[0].store
	header := http.Header{"Content-Length": []string{"5"}}
	if err := store.PutBytes("old", cache.Metadata{Status: http.StatusOK, Header: header.Clone()}, []byte("older")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := store.PutBytes("new", cache.Metadata{Status: http.StatusOK, Header: header.Clone()}, []byte("newer")); err != nil {
		t.Fatal(err)
	}
	if !server.storage.Snapshot().Pressure {
		t.Fatal("expected storage pressure")
	}
	result, err := server.collectPressure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedObjects != 1 || result.RemovedBytes != 5 {
		t.Fatalf("result = %#v", result)
	}
	if _, found, err := store.Lookup("old", time.Hour); err != nil || found {
		t.Fatalf("old entry found=%v err=%v", found, err)
	}
	if entry, found, err := store.Lookup("new", time.Hour); err != nil || !found {
		t.Fatalf("new entry found=%v err=%v", found, err)
	} else {
		_ = entry.Close()
	}
	if snapshot := server.storage.Snapshot(); snapshot.CommittedBytes != 5 || snapshot.Pressure {
		t.Fatalf("storage = %#v", snapshot)
	}
}

func TestStorageBudgetSurvivesConcurrentAdmissionAndRestart(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		Server: config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{
			Path: root, MaxBytes: 10, HighWatermark: 0.8, LowWatermark: 0.5,
			StaleTempAge: time.Hour,
		},
		Repositories: []config.Repository{
			{Name: "npm", Type: "npm", Path: "/npm/", Upstream: "https://registry.npmjs.org", TTL: time.Hour},
			{Name: "oci", Type: "oci", Path: "/v2/", Upstream: "https://registry-1.docker.io", TTL: time.Hour},
		},
	}
	server, err := New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"Content-Length": []string{"6"}}
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index, store := range server.stores {
		wait.Add(1)
		go func(index int, store *cache.Store) {
			defer wait.Done()
			<-start
			var downstream strings.Builder
			if err := store.PutStream(
				fmt.Sprintf("concurrent-%d", index),
				cache.Metadata{Status: http.StatusOK, Header: header.Clone()},
				strings.NewReader("123456"), &downstream,
			); err != nil {
				t.Errorf("concurrent write %d: %v", index, err)
			}
			if downstream.String() != "123456" {
				t.Errorf("downstream %d = %q", index, downstream.String())
			}
		}(index, store.store)
	}
	close(start)
	wait.Wait()

	snapshot := server.storage.Snapshot()
	if snapshot.CommittedBytes != 6 || snapshot.BypassObjects != 1 || snapshot.BypassBytes != 6 {
		t.Fatalf("concurrent admission snapshot = %#v", snapshot)
	}
	if err := server.stores[0].store.PutBytes(
		"newest", cache.Metadata{Status: http.StatusOK, Header: http.Header{"Content-Length": []string{"3"}}}, []byte("new"),
	); err != nil {
		t.Fatal(err)
	}
	oldest := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	newest := oldest.Add(time.Hour)
	for index, managed := range server.stores {
		entry, found, lookupErr := managed.store.Lookup(fmt.Sprintf("concurrent-%d", index), time.Hour)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if found {
			if err := os.Chtimes(entry.BodyPath, oldest, oldest); err != nil {
				_ = entry.Close()
				t.Fatal(err)
			}
			_ = entry.Close()
		}
	}
	newestEntry, found, err := server.stores[0].store.Lookup("newest", time.Hour)
	if err != nil || !found {
		t.Fatalf("newest entry before restart found=%v err=%v", found, err)
	}
	if err := os.Chtimes(newestEntry.BodyPath, newest, newest); err != nil {
		_ = newestEntry.Close()
		t.Fatal(err)
	}
	_ = newestEntry.Close()
	if !server.storage.Snapshot().Pressure {
		t.Fatal("expected pressure after admitted write")
	}

	// Reconstruct the server from disk to prove that complete-object usage is
	// reconciled before the next admission or pressure collection.
	restarted, err := New(cfg, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := restarted.storage.Snapshot(); snapshot.CommittedBytes != 9 || !snapshot.Pressure {
		t.Fatalf("restart snapshot = %#v", snapshot)
	}
	result, err := restarted.collectPressure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedObjects != 1 || result.RemovedBytes != 6 {
		t.Fatalf("pressure result = %#v", result)
	}
	if snapshot := restarted.storage.Snapshot(); snapshot.CommittedBytes != 3 || snapshot.Pressure {
		t.Fatalf("post-pressure snapshot = %#v", snapshot)
	}
	entry, found, err := restarted.stores[0].store.Lookup("newest", time.Hour)
	if err != nil || !found {
		t.Fatalf("newest entry found=%v err=%v", found, err)
	}
	_ = entry.Close()
}

func TestOperatorGCRequiresSeparateBearerToken(t *testing.T) {
	token := strings.Repeat("a", 32)
	tokenFile := filepath.Join(t.TempDir(), "operator-token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := New(config.Config{
		Server:   config.Server{PublicBaseURL: "http://packages.test"},
		Storage:  config.Storage{Path: t.TempDir(), MaxAge: time.Hour, GCInterval: time.Hour, StaleTempAge: time.Hour},
		Operator: config.Operator{TokenFile: tokenFile},
		Repositories: []config.Repository{{
			Name: "npm", Type: "npm", Path: "/npm/",
			Upstream: "https://registry.npmjs.org", TTL: time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for name, authorization := range map[string]string{
		"missing":               "",
		"without bearer scheme": token,
		"wrong":                 "Bearer " + strings.Repeat("b", 32),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/operator/gc", nil)
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/operator/gc", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"trigger": "operator"`) {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestOperatorGCIsHiddenWhenTokenIsNotConfigured(t *testing.T) {
	server, err := New(config.Config{
		Server:  config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{Path: t.TempDir(), MaxAge: time.Hour, GCInterval: time.Hour, StaleTempAge: time.Hour},
		Repositories: []config.Repository{{
			Name: "npm", Type: "npm", Path: "/npm/",
			Upstream: "https://registry.npmjs.org", TTL: time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/operator/gc", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestDashboardUsesOperatorAPIAndAccessibleStatus(t *testing.T) {
	server, err := New(config.Config{
		Server:  config.Server{PublicBaseURL: "http://packages.test"},
		Storage: config.Storage{Path: t.TempDir()},
		Repositories: []config.Repository{{
			Name: "npm", Type: "npm", Path: "/npm/",
			Upstream: "https://registry.npmjs.org", TTL: time.Hour,
		}},
	}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, expected := range []string{
		`aria-live="polite"`,
		`role="progressbar"`,
		`fetch('/api/v1/operator'`,
		`aria-label="Repository status"`,
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("dashboard missing %q", expected)
		}
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

	bytes, objects, err := store.Reconcile()
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
