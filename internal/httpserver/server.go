package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	mux           *http.ServeMux
	handler       http.Handler
	version       string
	publicURL     string
	started       time.Time
	repositories  []repository.Handler
	byName        map[string]repository.Handler
	stores        []managedStore
	maxAge        time.Duration
	gcInterval    time.Duration
	logger        *slog.Logger
	storage       *storagecontroller.Controller
	storagePath   string
	gc            *maintenance.Coordinator
	operatorToken string
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

type operatorResponse struct {
	GeneratedAt   time.Time             `json:"generated_at"`
	Version       string                `json:"version"`
	Status        string                `json:"status"`
	UptimeSeconds int64                 `json:"uptime_seconds"`
	Storage       operatorStorage       `json:"storage"`
	GC            maintenance.Snapshot  `json:"gc"`
	Repositories  []repository.Snapshot `json:"repositories"`
}

type operatorStorage struct {
	storagecontroller.Snapshot
	FilesystemFreeBytes int64 `json:"filesystem_free_bytes"`
	Bounded             bool  `json:"bounded"`
}

func New(cfg config.Config, version string, logger *slog.Logger) (*Server, error) {
	server := &Server{
		mux:         http.NewServeMux(),
		version:     version,
		publicURL:   strings.TrimRight(cfg.Server.PublicBaseURL, "/"),
		started:     time.Now(),
		byName:      make(map[string]repository.Handler),
		maxAge:      cfg.Storage.MaxAge,
		gcInterval:  cfg.Storage.GCInterval,
		logger:      logger,
		storagePath: cfg.Storage.Path,
	}
	if cfg.Operator.TokenFile != "" {
		tokenBytes, err := os.ReadFile(cfg.Operator.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read operator token file: %w", err)
		}
		server.operatorToken = strings.TrimSpace(string(tokenBytes))
		if len(server.operatorToken) < 32 || len(server.operatorToken) > 4096 {
			return nil, fmt.Errorf("operator token must contain between 32 and 4096 non-whitespace bytes")
		}
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
	server.mux.HandleFunc("/api/v1/operator", server.operatorStatus)
	server.mux.HandleFunc("/api/v1/operator/gc", server.operatorGC)
	server.mux.HandleFunc("/api/v1/repositories/", server.repositoryAPI)
	server.mux.HandleFunc("/metrics", server.metrics)
	server.mux.HandleFunc("/", server.dashboard)
	server.handler = securityHeaders(server.mux)
	return server, nil
}

func (s *Server) operatorGC(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/v1/operator/gc" || s.operatorToken == "" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="n0ding-operator"`)
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	provided := strings.TrimPrefix(authorization, "Bearer ")
	if len(provided) != len(s.operatorToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.operatorToken)) != 1 {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="n0ding-operator"`)
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	run, accepted := s.gc.Run(request.Context(), maintenance.TriggerOperator)
	if !accepted {
		writer.WriteHeader(http.StatusConflict)
	} else if run.Error != "" {
		writer.WriteHeader(http.StatusInternalServerError)
	}
	writeJSON(writer, run)
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) RunMaintenance(ctx context.Context) {
	if s.gcInterval <= 0 {
		return
	}
	scheduleTicker := time.NewTicker(s.gcInterval)
	defer scheduleTicker.Stop()
	pressureTicker := time.NewTicker(30 * time.Second)
	defer pressureTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pressureTicker.C:
			if s.storage == nil || !s.storage.Snapshot().Pressure {
				continue
			}
			run, accepted := s.gc.Run(ctx, maintenance.TriggerPressure)
			if !accepted {
				s.logger.Debug("pressure cache GC coalesced", "active_run_id", run.ID, "trigger", run.Trigger)
			} else if run.Error != "" {
				s.logger.Warn("pressure cache GC failed", "run_id", run.ID, "error", run.Error)
			}
		case <-scheduleTicker.C:
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
	if s.storage != nil && s.storage.Snapshot().Pressure {
		return s.collectPressure(ctx)
	}
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
		if s.storage != nil {
			s.storage.Remove(storeResult.Bytes)
		}
	}
	return result, nil
}

type pressureCandidate struct {
	store     *cache.Store
	candidate cache.Candidate
}

func (s *Server) collectPressure(ctx context.Context) (maintenance.Result, error) {
	var result maintenance.Result
	state := s.storage.Snapshot()
	toRemove := state.CommittedBytes - state.LowBytes
	if toRemove <= 0 {
		return result, nil
	}
	var candidates []pressureCandidate
	for _, managed := range s.stores {
		storeCandidates, err := managed.store.Candidates()
		if err != nil {
			return result, fmt.Errorf("repository %q: list pressure candidates: %w", managed.name, err)
		}
		for _, candidate := range storeCandidates {
			candidates = append(candidates, pressureCandidate{store: managed.store, candidate: candidate})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].candidate.LastUsed.Before(candidates[j].candidate.LastUsed)
	})
	for _, item := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if result.RemovedBytes >= toRemove {
			break
		}
		removed, err := item.store.RemoveCandidate(item.candidate)
		if err != nil {
			result.SkippedObjects++
			continue
		}
		if removed {
			result.RemovedObjects++
			result.RemovedBytes += item.candidate.Bytes
			s.storage.Remove(item.candidate.Bytes)
		}
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

func (s *Server) operatorStatus(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/v1/operator" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repositories := make([]repository.Snapshot, 0, len(s.repositories))
	for _, configured := range s.repositories {
		repositories = append(repositories, configured.Snapshot())
	}
	storageState := operatorStorage{}
	status := "degraded"
	if s.storage != nil {
		storageState.Snapshot = s.storage.Snapshot()
		storageState.Bounded = true
		status = "ok"
	}
	if freeBytes, err := storagecontroller.FreeBytes(s.storagePath); err == nil {
		storageState.FilesystemFreeBytes = freeBytes
	} else {
		status = "degraded"
		s.logger.Warn("read operator filesystem capacity", "error", err)
	}
	writeJSON(writer, operatorResponse{
		GeneratedAt:   time.Now().UTC(),
		Version:       s.version,
		Status:        status,
		UptimeSeconds: int64(time.Since(s.started).Seconds()),
		Storage:       storageState,
		GC:            s.gc.Snapshot(),
		Repositories:  repositories,
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
	fmt.Fprintln(writer, "# HELP n0ding_repository_requests_total Repository requests received.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_requests_total counter")
	fmt.Fprintln(writer, "# HELP n0ding_repository_cache_hits_total Repository requests served from cache.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_cache_hits_total counter")
	fmt.Fprintln(writer, "# HELP n0ding_repository_cache_misses_total Repository cache misses.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_cache_misses_total counter")
	fmt.Fprintln(writer, "# HELP n0ding_repository_errors_total Repository request errors.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_errors_total counter")
	fmt.Fprintln(writer, "# HELP n0ding_repository_client_canceled_total Repository requests canceled by clients.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_client_canceled_total counter")
	fmt.Fprintln(writer, "# HELP n0ding_repository_range_requests_total Repository byte-range requests.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_range_requests_total counter")
	fmt.Fprintln(writer, "# HELP n0ding_repository_storage_bytes Complete cached body bytes by repository.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_storage_bytes gauge")
	fmt.Fprintln(writer, "# HELP n0ding_repository_cache_objects Complete cached objects by repository.")
	fmt.Fprintln(writer, "# TYPE n0ding_repository_cache_objects gauge")
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
	if s.storage != nil {
		storage := s.storage.Snapshot()
		fmt.Fprintln(writer, "# HELP n0ding_storage_committed_bytes Bytes committed to complete cache objects.")
		fmt.Fprintln(writer, "# TYPE n0ding_storage_committed_bytes gauge")
		fmt.Fprintf(writer, "n0ding_storage_committed_bytes %d\n", storage.CommittedBytes)
		fmt.Fprintln(writer, "# HELP n0ding_storage_reserved_bytes Bytes reserved by in-flight cache writes.")
		fmt.Fprintln(writer, "# TYPE n0ding_storage_reserved_bytes gauge")
		fmt.Fprintf(writer, "n0ding_storage_reserved_bytes %d\n", storage.ReservedBytes)
		fmt.Fprintln(writer, "# HELP n0ding_storage_max_bytes Configured global cache budget.")
		fmt.Fprintln(writer, "# TYPE n0ding_storage_max_bytes gauge")
		fmt.Fprintf(writer, "n0ding_storage_max_bytes %d\n", storage.MaxBytes)
		fmt.Fprintln(writer, "# HELP n0ding_storage_pressure Whether usage is at or above the high watermark.")
		fmt.Fprintln(writer, "# TYPE n0ding_storage_pressure gauge")
		fmt.Fprintf(writer, "n0ding_storage_pressure %d\n", boolMetric(storage.Pressure))
		fmt.Fprintln(writer, "# HELP n0ding_storage_bypass_objects_total Objects proxied without cache admission.")
		fmt.Fprintln(writer, "# TYPE n0ding_storage_bypass_objects_total counter")
		fmt.Fprintf(writer, "n0ding_storage_bypass_objects_total %d\n", storage.BypassObjects)
		fmt.Fprintln(writer, "# HELP n0ding_storage_bypass_bytes_total Known bytes proxied without cache admission.")
		fmt.Fprintln(writer, "# TYPE n0ding_storage_bypass_bytes_total counter")
		fmt.Fprintf(writer, "n0ding_storage_bypass_bytes_total %d\n", storage.BypassBytes)
	}
	gc := s.gc.Snapshot()
	fmt.Fprintln(writer, "# HELP n0ding_gc_running Whether a garbage collection run is active.")
	fmt.Fprintln(writer, "# TYPE n0ding_gc_running gauge")
	fmt.Fprintf(writer, "n0ding_gc_running %d\n", boolMetric(gc.State == "running"))
	if gc.Last != nil {
		fmt.Fprintln(writer, "# HELP n0ding_gc_last_removed_bytes Bytes removed by the last garbage collection run.")
		fmt.Fprintln(writer, "# TYPE n0ding_gc_last_removed_bytes gauge")
		fmt.Fprintf(writer, "n0ding_gc_last_removed_bytes %d\n", gc.Last.Result.RemovedBytes)
		fmt.Fprintln(writer, "# HELP n0ding_gc_last_errors Errors recorded by the last garbage collection run.")
		fmt.Fprintln(writer, "# TYPE n0ding_gc_last_errors gauge")
		fmt.Fprintf(writer, "n0ding_gc_last_errors %d\n", gc.Last.Result.Errors)
		if gc.Last.FinishedAt != nil {
			fmt.Fprintln(writer, "# HELP n0ding_gc_last_finished_timestamp_seconds Unix timestamp when the last garbage collection run finished.")
			fmt.Fprintln(writer, "# TYPE n0ding_gc_last_finished_timestamp_seconds gauge")
			fmt.Fprintf(writer, "n0ding_gc_last_finished_timestamp_seconds %d\n", gc.Last.FinishedAt.Unix())
		}
	}
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
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
  <title>n0ding operator</title>
  <style>
    :root { color-scheme: dark; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
    * { box-sizing: border-box; }
    body { margin: 0; background: #090c0a; color: #e8eee9; }
    main { width: min(1120px, calc(100% - 32px)); margin: 48px auto; }
    header { display: flex; justify-content: space-between; align-items: center; gap: 24px; }
    h1 { font-size: clamp(2.2rem, 8vw, 4.5rem); margin: 0; letter-spacing: -.07em; }
    h2 { margin: 40px 0 14px; font-size: 1rem; color: #aebbb2; }
    h3 { margin: 0 0 18px; font-size: 1rem; }
    .accent, a { color: #8cf0ae; }
    .muted { color: #8b988f; }
    .status { border: 1px solid #385243; border-radius: 999px; padding: 8px 12px; font-weight: 700; }
    .status.degraded { border-color: #80642e; color: #ffd37a; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 16px; }
    article { border: 1px solid #29332c; background: #101411; padding: 20px; border-radius: 12px; min-width: 0; }
    dl { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 10px 16px; margin: 0; }
    dt { color: #8b988f; }
    dd { margin: 0; text-align: right; overflow-wrap: anywhere; }
    .meter { height: 10px; background: #252d28; border-radius: 999px; overflow: hidden; margin: 18px 0 10px; }
    .meter > span { display: block; height: 100%; width: 0; background: #65dc8e; transition: width .2s ease; }
    .meter.pressure > span { background: #ffbd59; }
    .repo-head { display: flex; justify-content: space-between; gap: 12px; align-items: center; }
    .repo-head span { overflow-wrap: anywhere; }
    .repo-link { display: inline-block; min-height: 44px; padding-top: 16px; }
    .empty { grid-column: 1 / -1; }
    #notice { min-height: 24px; margin-top: 18px; }
    a:focus-visible { outline: 3px solid #8cf0ae; outline-offset: 3px; }
    @media (max-width: 560px) {
      main { margin: 28px auto; }
      header { align-items: flex-start; flex-direction: column; gap: 12px; }
      .grid { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <div class="muted">artifact cache operator</div>
        <h1>n<span class="accent">0</span>ding</h1>
      </div>
      <div><span id="service-status" class="status">loading</span> <span class="muted">v{{.Version}}</span></div>
    </header>
    <div id="notice" class="muted" role="status" aria-live="polite">Loading operator state…</div>

    <h2>Overview</h2>
    <section class="grid" aria-label="Storage and garbage collection overview">
      <article>
        <h3>Storage</h3>
        <dl>
          <dt>Committed</dt><dd id="committed">—</dd>
          <dt>In flight</dt><dd id="reserved">—</dd>
          <dt>Budget</dt><dd id="budget">—</dd>
          <dt>Filesystem free</dt><dd id="filesystem-free">—</dd>
          <dt>Bypassed</dt><dd id="bypassed">—</dd>
        </dl>
        <div id="capacity-meter" class="meter" role="progressbar" aria-label="Cache budget used" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0"><span></span></div>
        <div id="capacity-label" class="muted">Quota not loaded</div>
      </article>
      <article>
        <h3>Garbage collection</h3>
        <dl>
          <dt>State</dt><dd id="gc-state">—</dd>
          <dt>Last trigger</dt><dd id="gc-trigger">—</dd>
          <dt>Last finished</dt><dd id="gc-finished">—</dd>
          <dt>Removed</dt><dd id="gc-removed">—</dd>
          <dt>Errors</dt><dd id="gc-errors">—</dd>
        </dl>
      </article>
    </section>

    <h2>Repositories</h2>
    <section id="repositories" class="grid" aria-label="Repository status">
      <article class="empty">Loading repository status…</article>
    </section>
  </main>
  <script>
    const escapeHTML = value => String(value).replace(/[&<>"']/g, character => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[character]));
    const size = bytes => {
      if (!bytes) return '0 B';
      const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
      const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
      return (bytes / Math.pow(1024, index)).toFixed(index ? 1 : 0) + ' ' + units[index];
    };
    const text = (id, value) => { document.getElementById(id).textContent = value; };
    let refreshing = false;
    async function refresh() {
      if (refreshing) return;
      refreshing = true;
      try {
        const response = await fetch('/api/v1/operator', {headers: {'Accept': 'application/json'}});
        if (!response.ok) throw new Error('HTTP ' + response.status);
        const data = await response.json();
        const storage = data.storage;
        const serviceStatus = document.getElementById('service-status');
        serviceStatus.textContent = data.status;
        serviceStatus.className = 'status' + (data.status === 'ok' ? '' : ' degraded');
        text('committed', size(storage.committed_bytes));
        text('reserved', size(storage.reserved_bytes));
        text('budget', storage.bounded ? size(storage.max_bytes) : 'unbounded');
        text('filesystem-free', size(storage.filesystem_free_bytes));
        text('bypassed', storage.bypass_objects + ' objects / ' + size(storage.bypass_bytes));
        const percent = storage.max_bytes ? Math.min(100, ((storage.committed_bytes + storage.reserved_bytes) / storage.max_bytes) * 100) : 0;
        const meter = document.getElementById('capacity-meter');
        meter.className = 'meter' + (storage.pressure ? ' pressure' : '');
        meter.setAttribute('aria-valuenow', percent.toFixed(1));
        meter.querySelector('span').style.width = percent + '%';
        text('capacity-label', storage.bounded ? percent.toFixed(1) + '% used' : 'No storage budget configured');
        text('gc-state', data.gc.state);
        const last = data.gc.last;
        text('gc-trigger', last ? last.trigger : 'none');
        text('gc-finished', last && last.finished_at ? new Date(last.finished_at).toLocaleString() : 'never');
        text('gc-removed', last ? last.result.removed_objects + ' objects / ' + size(last.result.removed_bytes) : '—');
        text('gc-errors', last ? last.result.errors : '0');
        document.getElementById('repositories').innerHTML = data.repositories.map(repo =>
          '<article><div class="repo-head"><h3>' + escapeHTML(repo.name) + '</h3><span class="muted">' + escapeHTML(repo.type) + '</span></div>' +
          '<dl><dt>Requests</dt><dd>' + repo.requests + '</dd><dt>Hits / misses</dt><dd>' + repo.cache_hits + ' / ' + repo.cache_misses + '</dd>' +
          '<dt>Hit ratio</dt><dd>' + (repo.hit_ratio * 100).toFixed(1) + '%</dd><dt>Errors</dt><dd>' + repo.errors + '</dd>' +
          '<dt>Objects</dt><dd>' + repo.cache_objects + '</dd><dt>Storage</dt><dd>' + size(repo.storage_bytes) + '</dd></dl>' +
          '<a class="repo-link" href="/api/v1/repositories/' + encodeURIComponent(repo.name) + '/setup">Client setup</a></article>'
        ).join('');
        text('notice', 'Updated ' + new Date(data.generated_at).toLocaleTimeString());
      } catch (error) {
        text('notice', 'Operator state unavailable: ' + error.message + '. Showing last known values.');
      } finally {
        refreshing = false;
      }
    }
    refresh();
    setInterval(refresh, 5000);
  </script>
</body>
</html>`))
