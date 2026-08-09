package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
	"github.com/HN-Tran/n0ding/internal/config"
	"github.com/HN-Tran/n0ding/internal/maintenance"
	"github.com/HN-Tran/n0ding/internal/npmproxy"
	"github.com/HN-Tran/n0ding/internal/ociproxy"
	"github.com/HN-Tran/n0ding/internal/pypiproxy"
	"github.com/HN-Tran/n0ding/internal/repository"
	storagecontroller "github.com/HN-Tran/n0ding/internal/storage"
)

type Server struct {
	mux          *http.ServeMux
	handler      http.Handler
	version      string
	publicURL    string
	started      time.Time
	repositories []repository.Handler
	byName       map[string]repository.Handler
	stores       []managedStore
	maxAge       time.Duration
	gcInterval   time.Duration
	logger       *slog.Logger
	storage      *storagecontroller.Controller
	gc           *maintenance.Coordinator
}

type managedStore struct {
	name  string
	store *cache.Store
}

type statusResponse struct {
	Version      string                `json:"version"`
	Status       string                `json:"status"`
	Uptime       string                `json:"uptime"`
	Repositories []repository.Snapshot `json:"repositories"`
}

func New(cfg config.Config, version string, logger *slog.Logger) (*Server, error) {
	server := &Server{
		mux:        http.NewServeMux(),
		version:    version,
		publicURL:  strings.TrimRight(cfg.Server.PublicBaseURL, "/"),
		started:    time.Now(),
		byName:     make(map[string]repository.Handler),
		maxAge:     cfg.Storage.MaxAge,
		gcInterval: cfg.Storage.GCInterval,
		logger:     logger,
	}

	for _, configuredRepository := range cfg.Repositories {
		store, err := cache.New(filepath.Join(cfg.Storage.Path, configuredRepository.Name))
		if err != nil {
			return nil, fmt.Errorf("repository %q: %w", configuredRepository.Name, err)
		}
		if cfg.Storage.StaleTempAge > 0 {
			result, cleanupErr := store.CleanupStaleTemps(cfg.Storage.StaleTempAge)
			if cleanupErr != nil {
				return nil, fmt.Errorf("repository %q: clean stale temporary files: %w", configuredRepository.Name, cleanupErr)
			}
			if result.Files > 0 {
				logger.Info(
					"stale cache temporary files removed",
					"repository", configuredRepository.Name,
					"files", result.Files,
					"bytes", result.Bytes,
				)
			}
		}
		server.stores = append(server.stores, managedStore{name: configuredRepository.Name, store: store})
		var proxy repository.Handler
		switch configuredRepository.Type {
		case "npm":
			proxy, err = npmproxy.New(npmproxy.Options{
				Name:                 configuredRepository.Name,
				Path:                 configuredRepository.Path,
				Upstream:             configuredRepository.Upstream,
				PublicBaseURL:        cfg.Server.PublicBaseURL,
				TTL:                  configuredRepository.TTL,
				ForwardAuthorization: configuredRepository.ForwardAuthorization,
				Store:                store,
				Logger:               logger,
			})
		case "oci":
			proxy, err = ociproxy.New(ociproxy.Options{
				Name:          configuredRepository.Name,
				Path:          configuredRepository.Path,
				Upstream:      configuredRepository.Upstream,
				PublicBaseURL: cfg.Server.PublicBaseURL,
				TTL:           configuredRepository.TTL,
				Store:         store,
				Logger:        logger,
			})
		case "pypi":
			proxy, err = pypiproxy.New(pypiproxy.Options{
				Name:                 configuredRepository.Name,
				Path:                 configuredRepository.Path,
				Upstream:             configuredRepository.Upstream,
				PublicBaseURL:        cfg.Server.PublicBaseURL,
				TTL:                  configuredRepository.TTL,
				ForwardAuthorization: configuredRepository.ForwardAuthorization,
				AllowedFileOrigins:   configuredRepository.AllowedFileOrigins,
				Store:                store,
				Logger:               logger,
			})
		default:
			err = fmt.Errorf("unsupported repository type %q", configuredRepository.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("repository %q: %w", configuredRepository.Name, err)
		}
		server.repositories = append(server.repositories, proxy)
		server.byName[configuredRepository.Name] = proxy
		server.mux.Handle(configuredRepository.Path, proxy)
		if pypi, ok := proxy.(*pypiproxy.Proxy); ok {
			server.mux.Handle(pypi.FilePath(), proxy)
		}
	}
	var committedBytes int64
	for _, managed := range server.stores {
		bytes, _, sizeErr := managed.store.Size()
		if sizeErr != nil {
			return nil, fmt.Errorf("repository %q: read cache usage: %w", managed.name, sizeErr)
		}
		if bytes > math.MaxInt64-committedBytes {
			committedBytes = math.MaxInt64
		} else {
			committedBytes += bytes
		}
	}
	if cfg.Storage.MaxBytes > 0 || cfg.Storage.MinFreeBytes > 0 {
		server.storage = storagecontroller.NewController(
			cfg.Storage.MaxBytes,
			cfg.Storage.HighWatermark,
			cfg.Storage.LowWatermark,
			cfg.Storage.MinFreeBytes,
			committedBytes,
		)
		for _, managed := range server.stores {
			managed.store.SetController(server.storage)
		}
	}
	server.gc = maintenance.New(server.collect)
	if server.maxAge > 0 {
		if run, _ := server.gc.Run(context.Background(), maintenance.TriggerStartup); run.Error != "" {
			return nil, fmt.Errorf("startup cache GC: %s", run.Error)
		}
	}

	server.mux.HandleFunc("/healthz", server.health)
	server.mux.HandleFunc("/api/v1/status", server.status)
	server.mux.HandleFunc("/api/v1/repositories/", server.repositoryAPI)
	server.mux.HandleFunc("/metrics", server.metrics)
	server.mux.HandleFunc("/", server.dashboard)
	server.handler = securityHeaders(server.mux)
	return server, nil
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) RunMaintenance(ctx context.Context) {
	if s.maxAge <= 0 || s.gcInterval <= 0 {
		return
	}
	ticker := time.NewTicker(s.gcInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, accepted := s.gc.Run(ctx, maintenance.TriggerSchedule)
			if !accepted {
				s.logger.Debug("scheduled cache GC coalesced", "active_run_id", run.ID, "trigger", run.Trigger)
			} else if run.Error != "" {
				s.logger.Warn("scheduled cache GC failed", "run_id", run.ID, "error", run.Error)
			}
		}
	}
}

func (s *Server) collect(ctx context.Context) (maintenance.Result, error) {
	var result maintenance.Result
	for _, managed := range s.stores {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		storeResult, err := managed.store.GC(s.maxAge)
		if err != nil {
			return result, fmt.Errorf("repository %q: %w", managed.name, err)
		}
		logGC(s.logger, managed.name, "coordinated", storeResult)
		result.RemovedObjects += storeResult.Objects
		result.RemovedBytes += storeResult.Bytes
		result.SkippedObjects += storeResult.Skipped
	}
	return result, nil
}

func logGC(logger *slog.Logger, repositoryName, trigger string, result cache.GCResult) {
	if result.Objects == 0 && result.Skipped == 0 {
		return
	}
	logger.Info(
		"cache GC completed",
		"repository", repositoryName,
		"trigger", trigger,
		"objects", result.Objects,
		"bytes", result.Bytes,
		"skipped", result.Skipped,
	)
}

func (s *Server) health(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/healthz" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/v1/status" {
		http.NotFound(writer, request)
		return
	}
	snapshots := make([]repository.Snapshot, 0, len(s.repositories))
	for _, repository := range s.repositories {
		snapshots = append(snapshots, repository.Snapshot())
	}
	writeJSON(writer, statusResponse{
		Version:      s.version,
		Status:       "ok",
		Uptime:       time.Since(s.started).Round(time.Second).String(),
		Repositories: snapshots,
	})
}

func (s *Server) repositoryAPI(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/repositories/")
	name, action, ok := strings.Cut(path, "/")
	if !ok || action != "setup" {
		http.NotFound(writer, request)
		return
	}
	repository := s.byName[name]
	if repository == nil {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write([]byte(repository.SetupSnippet()))
}

func (s *Server) metrics(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/metrics" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, repository := range s.repositories {
		snapshot := repository.Snapshot()
		label := fmt.Sprintf(`repository=%q,type=%q`, snapshot.Name, snapshot.Type)
		fmt.Fprintf(writer, "n0ding_repository_requests_total{%s} %d\n", label, snapshot.Requests)
		fmt.Fprintf(writer, "n0ding_repository_cache_hits_total{%s} %d\n", label, snapshot.CacheHits)
		fmt.Fprintf(writer, "n0ding_repository_cache_misses_total{%s} %d\n", label, snapshot.CacheMisses)
		fmt.Fprintf(writer, "n0ding_repository_errors_total{%s} %d\n", label, snapshot.Errors)
		fmt.Fprintf(writer, "n0ding_repository_client_canceled_total{%s} %d\n", label, snapshot.ClientCanceled)
		fmt.Fprintf(writer, "n0ding_repository_range_requests_total{%s} %d\n", label, snapshot.RangeRequests)
		fmt.Fprintf(writer, "n0ding_repository_storage_bytes{%s} %d\n", label, snapshot.StorageBytes)
		fmt.Fprintf(writer, "n0ding_repository_cache_objects{%s} %d\n", label, snapshot.CacheObjects)
	}
}

func (s *Server) dashboard(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTemplate.Execute(writer, map[string]string{
		"Version":   s.version,
		"PublicURL": s.publicURL,
	})
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>n0ding</title>
  <style>
    :root { color-scheme: dark; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
    body { margin: 0; background: #0a0c0b; color: #e8eee9; }
    main { width: min(960px, calc(100% - 32px)); margin: 64px auto; }
    header { display: flex; justify-content: space-between; align-items: baseline; gap: 24px; }
    h1 { font-size: clamp(2rem, 8vw, 4.5rem); margin: 0; letter-spacing: -.08em; }
    .accent, a { color: #8cf0ae; }
    .muted { color: #8b988f; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 16px; margin-top: 48px; }
    article { border: 1px solid #29332c; background: #101411; padding: 20px; border-radius: 10px; }
    article h2 { margin: 0 0 20px; font-size: 1rem; }
    dl { display: grid; grid-template-columns: 1fr auto; gap: 10px 16px; margin: 0; }
    dt { color: #8b988f; }
    dd { margin: 0; }
    pre { overflow: auto; padding: 12px; background: #090b0a; border-radius: 6px; color: #b7f8cc; }
    .empty { grid-column: 1 / -1; }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <div class="muted">homelab-native package hub</div>
        <h1>n<span class="accent">0</span>ding</h1>
      </div>
      <div class="muted">v{{.Version}}</div>
    </header>
    <section id="repositories" class="grid">
      <article class="empty">Loading repository status…</article>
    </section>
  </main>
  <script>
    const escapeHTML = value => String(value).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    const size = bytes => {
      if (!bytes) return '0 B';
      const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
      const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
      return (bytes / Math.pow(1024, index)).toFixed(index ? 1 : 0) + ' ' + units[index];
    };
    async function refresh() {
      const response = await fetch('/api/v1/status');
      const data = await response.json();
      document.querySelector('#repositories').innerHTML = data.repositories.map(repo => ` + "`" + `
        <article>
          <h2><span class="accent">●</span> ${escapeHTML(repo.name)} <span class="muted">/${escapeHTML(repo.type)}</span></h2>
          <dl>
            <dt>Requests</dt><dd>${repo.requests}</dd>
            <dt>Cache hit ratio</dt><dd>${(repo.hit_ratio * 100).toFixed(1)}%</dd>
            <dt>Objects</dt><dd>${repo.cache_objects}</dd>
            <dt>Storage</dt><dd>${size(repo.storage_bytes)}</dd>
          </dl>
          <a href="/api/v1/repositories/${encodeURIComponent(repo.name)}/setup">setup snippet</a>
        </article>` + "`" + `).join('');
    }
    refresh().catch(error => {
      document.querySelector('#repositories').innerHTML = '<article class="empty">Status unavailable: ' + escapeHTML(error) + '</article>';
    });
    setInterval(refresh, 5000);
  </script>
</body>
</html>`))
