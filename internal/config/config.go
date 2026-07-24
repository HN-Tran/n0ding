package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server       Server
	Storage      Storage
	Repositories []Repository
}

type Server struct {
	Listen        string
	PublicBaseURL string
	Log           string
}

func (s Server) LogLevel() slog.Level {
	switch strings.ToLower(s.Log) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type Storage struct {
	Path         string
	MaxAge       time.Duration
	GCInterval   time.Duration
	StaleTempAge time.Duration
}

type Repository struct {
	Name                 string
	Type                 string
	Path                 string
	Upstream             string
	TTL                  time.Duration
	ForwardAuthorization bool
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	cfg, err := Parse(file)
	if err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if !filepath.IsAbs(cfg.Storage.Path) {
		cfg.Storage.Path = filepath.Clean(filepath.Join(filepath.Dir(path), cfg.Storage.Path))
	}
	return cfg, nil
}

func Parse(reader io.Reader) (Config, error) {
	cfg := Config{
		Server: Server{
			Listen:        ":8080",
			PublicBaseURL: "http://localhost:8080",
			Log:           "info",
		},
		Storage: Storage{
			Path:         "./data",
			MaxAge:       30 * 24 * time.Hour,
			GCInterval:   time.Hour,
			StaleTempAge: time.Hour,
		},
	}

	var section string
	var current *Repository
	scanner := bufio.NewScanner(reader)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(stripComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			current = nil
			if strings.HasPrefix(section, "repository.") {
				name := strings.TrimSpace(strings.TrimPrefix(section, "repository."))
				if name == "" {
					return Config{}, fmt.Errorf("line %d: repository name is empty", lineNumber)
				}
				cfg.Repositories = append(cfg.Repositories, Repository{
					Name: name,
					Type: name,
					Path: "/" + name + "/",
					TTL:  24 * time.Hour,
				})
				current = &cfg.Repositories[len(cfg.Repositories)-1]
			}
			continue
		}

		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		key = strings.TrimSpace(key)
		value, err := parseValue(strings.TrimSpace(rawValue))
		if err != nil {
			return Config{}, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		value = os.ExpandEnv(value)

		switch {
		case section == "server":
			switch key {
			case "listen":
				cfg.Server.Listen = value
			case "public_base_url":
				cfg.Server.PublicBaseURL = value
			case "log_level":
				cfg.Server.Log = value
			default:
				return Config{}, fmt.Errorf("line %d: unknown server key %q", lineNumber, key)
			}
		case section == "storage":
			switch key {
			case "path":
				cfg.Storage.Path = value
			case "max_age":
				cfg.Storage.MaxAge, err = time.ParseDuration(value)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid max_age: %w", lineNumber, err)
				}
			case "gc_interval":
				cfg.Storage.GCInterval, err = time.ParseDuration(value)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid gc_interval: %w", lineNumber, err)
				}
			case "stale_temp_age":
				cfg.Storage.StaleTempAge, err = time.ParseDuration(value)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid stale_temp_age: %w", lineNumber, err)
				}
			default:
				return Config{}, fmt.Errorf("line %d: unknown storage key %q", lineNumber, key)
			}
		case current != nil:
			switch key {
			case "type":
				current.Type = value
			case "path":
				current.Path = value
			case "upstream":
				current.Upstream = value
			case "ttl":
				current.TTL, err = time.ParseDuration(value)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid ttl: %w", lineNumber, err)
				}
			case "forward_authorization":
				current.ForwardAuthorization, err = strconv.ParseBool(value)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid boolean: %w", lineNumber, err)
				}
			default:
				return Config{}, fmt.Errorf("line %d: unknown repository key %q", lineNumber, key)
			}
		default:
			return Config{}, fmt.Errorf("line %d: key outside a supported section", lineNumber)
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.Listen) == "" {
		return errors.New("server.listen must not be empty")
	}
	publicURL, err := url.Parse(c.Server.PublicBaseURL)
	if err != nil || publicURL.Scheme == "" || publicURL.Host == "" {
		return errors.New("server.public_base_url must be an absolute HTTP(S) URL")
	}
	if publicURL.Scheme != "http" && publicURL.Scheme != "https" {
		return errors.New("server.public_base_url must use http or https")
	}
	if strings.TrimSpace(c.Storage.Path) == "" {
		return errors.New("storage.path must not be empty")
	}
	if c.Storage.MaxAge <= 0 {
		return errors.New("storage.max_age must be positive")
	}
	if c.Storage.GCInterval <= 0 {
		return errors.New("storage.gc_interval must be positive")
	}
	if c.Storage.StaleTempAge <= 0 {
		return errors.New("storage.stale_temp_age must be positive")
	}
	if len(c.Repositories) == 0 {
		return errors.New("at least one repository is required")
	}

	paths := make(map[string]string)
	names := make(map[string]struct{})
	for index := range c.Repositories {
		repo := &c.Repositories[index]
		if _, exists := names[repo.Name]; exists {
			return fmt.Errorf("duplicate repository name %q", repo.Name)
		}
		names[repo.Name] = struct{}{}
		if repo.Type != "npm" && repo.Type != "oci" {
			return fmt.Errorf("repository %q: unsupported type %q", repo.Name, repo.Type)
		}
		if repo.Type == "oci" && repo.Path != "/v2/" {
			return fmt.Errorf("repository %q: OCI path must be /v2/", repo.Name)
		}
		if !strings.HasPrefix(repo.Path, "/") || !strings.HasSuffix(repo.Path, "/") {
			return fmt.Errorf("repository %q: path must start and end with /", repo.Name)
		}
		if previous, exists := paths[repo.Path]; exists {
			return fmt.Errorf("repositories %q and %q use the same path", previous, repo.Name)
		}
		paths[repo.Path] = repo.Name
		upstream, err := url.Parse(repo.Upstream)
		if err != nil || upstream.Scheme == "" || upstream.Host == "" {
			return fmt.Errorf("repository %q: upstream must be an absolute URL", repo.Name)
		}
		if upstream.Scheme != "http" && upstream.Scheme != "https" {
			return fmt.Errorf("repository %q: upstream must use http or https", repo.Name)
		}
		if repo.TTL <= 0 {
			return fmt.Errorf("repository %q: ttl must be positive", repo.Name)
		}
	}
	return nil
}

func parseValue(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("value must not be empty")
	}
	if strings.HasPrefix(raw, `"`) {
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value: %w", err)
		}
		return value, nil
	}
	return raw, nil
}

func stripComment(line string) string {
	quoted := false
	escaped := false
	for index, character := range line {
		switch {
		case escaped:
			escaped = false
		case character == '\\' && quoted:
			escaped = true
		case character == '"':
			quoted = !quoted
		case character == '#' && !quoted:
			return line[:index]
		}
	}
	return line
}
