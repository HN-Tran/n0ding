package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	t.Setenv("N0DING_DATA", "./test-data")
	cfg, err := Parse(strings.NewReader(`
[server]
listen = ":9090"
public_base_url = "https://packages.example.test"
log_level = "debug"

[storage]
path = "${N0DING_DATA}"
max_age = "168h"
gc_interval = "30m"
stale_temp_age = "2h"

[repository.npm]
type = "npm"
path = "/npm/"
upstream = "https://registry.npmjs.org"
ttl = "12h"
forward_authorization = false
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Fatalf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Storage.Path != "./test-data" {
		t.Fatalf("storage path = %q", cfg.Storage.Path)
	}
	if cfg.Storage.MaxAge != 168*time.Hour ||
		cfg.Storage.GCInterval != 30*time.Minute ||
		cfg.Storage.StaleTempAge != 2*time.Hour {
		t.Fatalf("storage policy = %#v", cfg.Storage)
	}
	if len(cfg.Repositories) != 1 {
		t.Fatalf("repositories = %d", len(cfg.Repositories))
	}
	repo := cfg.Repositories[0]
	if repo.Name != "npm" || repo.TTL != 12*time.Hour {
		t.Fatalf("repository = %#v", repo)
	}
}

func TestParseStorageDefaults(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
[repository.npm]
upstream = "https://registry.npmjs.org"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.MaxAge != 30*24*time.Hour {
		t.Fatalf("max age = %s", cfg.Storage.MaxAge)
	}
	if cfg.Storage.GCInterval != time.Hour {
		t.Fatalf("GC interval = %s", cfg.Storage.GCInterval)
	}
	if cfg.Storage.StaleTempAge != time.Hour {
		t.Fatalf("stale temp age = %s", cfg.Storage.StaleTempAge)
	}
}

func TestParseRejectsInvalidStoragePolicy(t *testing.T) {
	_, err := Parse(strings.NewReader(`
[storage]
max_age = "0s"

[repository.npm]
upstream = "https://registry.npmjs.org"
`))
	if err == nil || !strings.Contains(err.Error(), "storage.max_age must be positive") {
		t.Fatalf("expected max age error, got %v", err)
	}
}

func TestParseRejectsUnsupportedRepository(t *testing.T) {
	_, err := Parse(strings.NewReader(`
[repository.maven]
upstream = "https://repo1.maven.org/maven2"
`))
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("expected unsupported type error, got %v", err)
	}
}

func TestParseOCIRepository(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`
[repository.oci]
type = "oci"
path = "/v2/"
upstream = "https://registry-1.docker.io"
ttl = "1h"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0].Type != "oci" {
		t.Fatalf("repositories = %#v", cfg.Repositories)
	}
}

func TestParseRejectsOCIPathPrefix(t *testing.T) {
	_, err := Parse(strings.NewReader(`
[repository.oci]
type = "oci"
path = "/oci/v2/"
upstream = "https://registry-1.docker.io"
`))
	if err == nil || !strings.Contains(err.Error(), "OCI path must be /v2/") {
		t.Fatalf("expected OCI path error, got %v", err)
	}
}

func TestLoadResolvesStorageRelativeToConfig(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "n0ding.toml")
	err := os.WriteFile(configPath, []byte(`
[storage]
path = "../cache"

[repository.npm]
upstream = "https://registry.npmjs.org"
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(directory, "../cache"))
	if cfg.Storage.Path != want {
		t.Fatalf("storage path = %q, want %q", cfg.Storage.Path, want)
	}
}
