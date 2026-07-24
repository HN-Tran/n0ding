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
	if len(cfg.Repositories) != 1 {
		t.Fatalf("repositories = %d", len(cfg.Repositories))
	}
	repo := cfg.Repositories[0]
	if repo.Name != "npm" || repo.TTL != 12*time.Hour {
		t.Fatalf("repository = %#v", repo)
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
