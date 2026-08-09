package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	Path          string
	MaxAge        time.Duration
	GCInterval    time.Duration
	StaleTempAge  time.Duration
	MaxBytes      int64
	HighWatermark float64
	LowWatermark  float64
	MinFreeBytes  int64
}

type Repository struct {
	Name                 string
	Type                 string
	Path                 string
	Upstream             string
	TTL                  time.Duration
	ForwardAuthorization bool
	AllowedFileOrigins   []string
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
			Path:          "./data",
			MaxAge:        30 * 24 * time.Hour,
			GCInterval:    time.Hour,
			StaleTempAge:  time.Hour,
			HighWatermark: 0.90,
			LowWatermark:  0.75,
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
			case "max_bytes":
				cfg.Storage.MaxBytes, err = strconv.ParseInt(value, 10, 64)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid max_bytes: %w", lineNumber, err)
				}
			case "high_watermark":
				cfg.Storage.HighWatermark, err = strconv.ParseFloat(value, 64)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid high_watermark: %w", lineNumber, err)
				}
			case "low_watermark":
				cfg.Storage.LowWatermark, err = strconv.ParseFloat(value, 64)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid low_watermark: %w", lineNumber, err)
				}
			case "min_free_bytes":
				cfg.Storage.MinFreeBytes, err = strconv.ParseInt(value, 10, 64)
				if err != nil {
					return Config{}, fmt.Errorf("line %d: invalid min_free_bytes: %w", lineNumber, err)
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
			case "allowed_file_origins":
				current.AllowedFileOrigins = splitCSV(value)
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
	if c.Storage.MaxBytes < 0 {
		return errors.New("storage.max_bytes must not be negative")
	}
	if c.Storage.MinFreeBytes < 0 {
		return errors.New("storage.min_free_bytes must not be negative")
	}
	if math.IsNaN(c.Storage.LowWatermark) || math.IsInf(c.Storage.LowWatermark, 0) ||
		c.Storage.LowWatermark <= 0 || c.Storage.LowWatermark >= 1 {
		return errors.New("storage.low_watermark must be greater than 0 and less than 1")
	}
	if math.IsNaN(c.Storage.HighWatermark) || math.IsInf(c.Storage.HighWatermark, 0) ||
		c.Storage.HighWatermark <= c.Storage.LowWatermark || c.Storage.HighWatermark >= 1 {
		return errors.New("storage.high_watermark must be greater than low_watermark and less than 1")
	}
	if len(c.Repositories) == 0 {
		return errors.New("at least one repository is required")
	}

	paths := make(map[string]string)
	names := make(map[string]struct{})
	for index := range c.Repositories {
		repo := &c.Repositories[index]
		if !validRepositoryName(repo.Name) {
			return fmt.Errorf("repository name %q must use 1-63 lowercase letters, digits, dots, underscores, or hyphens and may not contain path segments", repo.Name)
		}
		if _, exists := names[repo.Name]; exists {
			return fmt.Errorf("duplicate repository name %q", repo.Name)
		}
		names[repo.Name] = struct{}{}
		if repo.Type != "npm" && repo.Type != "oci" && repo.Type != "pypi" {
			return fmt.Errorf("repository %q: unsupported type %q", repo.Name, repo.Type)
		}
		if repo.Type == "oci" && repo.Path != "/v2/" {
			return fmt.Errorf("repository %q: OCI path must be /v2/", repo.Name)
		}
		if repo.Type == "pypi" && !strings.HasSuffix(repo.Path, "/simple/") {
			return fmt.Errorf("repository %q: PyPI path must end with /simple/", repo.Name)
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
		for _, origin := range repo.AllowedFileOrigins {
			parsed, parseErr := url.Parse(origin)
			if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("repository %q: allowed_file_origins must contain absolute HTTP(S) origins", repo.Name)
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return fmt.Errorf("repository %q: allowed_file_origins must use http or https", repo.Name)
			}
			if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
				(parsed.Path != "" && parsed.Path != "/") {
				return fmt.Errorf("repository %q: allowed_file_origins must not contain path, userinfo, query, or fragment", repo.Name)
			}
		}
	}
	return nil
}

func validRepositoryName(name string) bool {
	if len(name) == 0 || len(name) > 63 || name == "." || name == ".." {
		return false
	}
	for index, character := range name {
		if character > unicode.MaxASCII || !(unicode.IsLower(character) || unicode.IsDigit(character) ||
			(index > 0 && (character == '.' || character == '_' || character == '-'))) {
			return false
		}
	}
	return true
}

func splitCSV(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
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
