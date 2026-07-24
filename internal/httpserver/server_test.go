package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"version": "test"`) {
		t.Fatalf("status: code=%d body=%s", statusResponse.Code, statusResponse.Body.String())
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
