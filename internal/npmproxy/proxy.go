package npmproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HN-Tran/n0ding/internal/cache"
	"github.com/HN-Tran/n0ding/internal/httppolicy"
	"github.com/HN-Tran/n0ding/internal/repository"
)

const maxMetadataBytes = 64 << 20

type Options struct {
	Name                 string
	Path                 string
	Upstream             string
	PublicBaseURL        string
	TTL                  time.Duration
	ForwardAuthorization bool
	Store                *cache.Store
	Client               *http.Client
	Logger               *slog.Logger
}

type Proxy struct {
	name                 string
	path                 string
	upstream             *url.URL
	proxyBaseURL         string
	ttl                  time.Duration
	forwardAuthorization bool
	store                *cache.Store
	client               *http.Client
	logger               *slog.Logger
	stats                counters
	locks                keyedLocker
}

type counters struct {
	requests       atomic.Uint64
	hits           atomic.Uint64
	misses         atomic.Uint64
	errors         atomic.Uint64
	clientCanceled atomic.Uint64
}

func New(options Options) (*Proxy, error) {
	upstream, err := url.Parse(options.Upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	if options.Store == nil {
		return nil, fmt.Errorf("cache store is required")
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	options.Client = httppolicy.ClientWithSafeRedirects(options.Client)
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Proxy{
		name:                 options.Name,
		path:                 options.Path,
		upstream:             upstream,
		proxyBaseURL:         strings.TrimRight(options.PublicBaseURL, "/") + strings.TrimSuffix(options.Path, "/"),
		ttl:                  options.TTL,
		forwardAuthorization: options.ForwardAuthorization,
		store:                options.Store,
		client:               options.Client,
		logger:               options.Logger,
		locks:                keyedLocker{items: make(map[string]*lockRef)},
	}, nil
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	p.stats.requests.Add(1)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(request.URL.Path, p.path) {
		http.NotFound(writer, request)
		return
	}

	target, err := p.targetURL(request.URL)
	if err != nil {
		p.fail(writer, request, http.StatusBadRequest, "invalid upstream URL", err)
		return
	}
	cacheable := request.Header.Get("Range") == "" &&
		!httppolicy.RequestBypassesCache(request.Header) &&
		(!p.forwardAuthorization || request.Header.Get("Authorization") == "")
	key := p.cacheKey(target, request.Header.Get("Accept"))

	if cacheable {
		if entry, found, lookupErr := p.store.Lookup(key, p.ttl); lookupErr != nil {
			p.logger.Warn("cache lookup failed", "repository", p.name, "error", lookupErr)
		} else if found {
			p.stats.hits.Add(1)
			p.serveHit(writer, request, entry)
			return
		}
	}

	unlock := p.locks.lock(key)
	defer unlock()
	if cacheable {
		if entry, found, lookupErr := p.store.Lookup(key, p.ttl); lookupErr == nil && found {
			p.stats.hits.Add(1)
			p.serveHit(writer, request, entry)
			return
		}
	}

	p.stats.misses.Add(1)
	p.serveMiss(writer, request, target, key, cacheable)
}

func (p *Proxy) Snapshot() repository.Snapshot {
	requests := p.stats.requests.Load()
	hits := p.stats.hits.Load()
	misses := p.stats.misses.Load()
	var ratio float64
	if hits+misses > 0 {
		ratio = float64(hits) / float64(hits+misses)
	}
	storageBytes, objects, err := p.store.Size()
	if err != nil {
		p.logger.Warn("cache size scan failed", "repository", p.name, "error", err)
	}
	return repository.Snapshot{
		Name:           p.name,
		Type:           "npm",
		Path:           p.path,
		Upstream:       httppolicy.PublicUpstreamURL(p.upstream),
		Requests:       requests,
		CacheHits:      hits,
		CacheMisses:    misses,
		Errors:         p.stats.errors.Load(),
		ClientCanceled: p.stats.clientCanceled.Load(),
		HitRatio:       ratio,
		StorageBytes:   storageBytes,
		CacheObjects:   objects,
	}
}

func (p *Proxy) SetupSnippet() string {
	return "npm config set registry " + p.proxyBaseURL + "/\n"
}

func (p *Proxy) targetURL(requestURL *url.URL) (*url.URL, error) {
	escapedPath := requestURL.EscapedPath()
	relativePath := strings.TrimPrefix(escapedPath, p.path)
	target, err := url.Parse(strings.TrimRight(p.upstream.String(), "/") + "/" + relativePath)
	if err != nil {
		return nil, err
	}
	target.RawQuery = requestURL.RawQuery
	return target, nil
}

func (p *Proxy) cacheKey(target *url.URL, accept string) string {
	return p.name + "\n" + target.String() + "\naccept:" + accept
}

func (p *Proxy) serveHit(writer http.ResponseWriter, request *http.Request, entry cache.Entry) {
	defer entry.Close()
	httppolicy.CopyHeaders(writer.Header(), entry.Metadata.Header)
	writer.Header().Set("X-N0ding-Cache", "HIT")
	writer.Header().Set("Age", strconv.FormatInt(int64(time.Since(entry.Metadata.StoredAt).Seconds()), 10))
	writer.Header().Set("Content-Length", strconv.FormatInt(entry.Metadata.ContentBytes, 10))
	writer.WriteHeader(entry.Metadata.Status)
	if request.Method == http.MethodHead {
		return
	}

	if _, err := io.Copy(writer, entry.Body); err != nil {
		p.logger.Debug("send cached body failed", "repository", p.name, "error", err)
	}
}

func (p *Proxy) serveMiss(
	writer http.ResponseWriter,
	request *http.Request,
	target *url.URL,
	key string,
	cacheable bool,
) {
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), nil)
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "could not create upstream request", err)
		return
	}
	httppolicy.ForwardRequestHeaders(upstreamRequest.Header, request.Header, p.forwardAuthorization)
	upstreamRequest.Header.Set("Accept-Encoding", "identity")

	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		status := http.StatusBadGateway
		if request.Context().Err() != nil {
			status = 499
		}
		p.fail(writer, request, status, "upstream request failed", err)
		return
	}
	defer response.Body.Close()

	headers := httppolicy.ResponseHeaders(response.Header)
	headers.Del("Content-Encoding")
	headers.Set("X-N0ding-Cache", "MISS")
	cacheable = cacheable &&
		request.Method == http.MethodGet &&
		response.StatusCode == http.StatusOK &&
		httppolicy.ResponseAllowsStorage(
			request.Header,
			response.Header,
			"Accept",
			"Accept-Encoding",
		)
	cacheHeaders := httppolicy.CacheMetadataHeaders(headers)

	if isJSON(response.Header.Get("Content-Type")) {
		p.serveMetadata(writer, request, response, headers, cacheHeaders, key, cacheable)
		return
	}

	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)
	if request.Method == http.MethodHead {
		return
	}
	if cacheable {
		metadata := cache.Metadata{Status: response.StatusCode, Header: cacheHeaders}
		if err := p.store.PutStream(key, metadata, response.Body, writer); err != nil {
			p.logger.Warn(
				"cache stream failed",
				"repository", p.name,
				"upstream", httppolicy.PublicUpstreamURL(target),
				"error", err,
			)
		}
		return
	}
	if _, err := io.Copy(writer, response.Body); err != nil {
		p.logger.Debug("proxy stream failed", "repository", p.name, "error", err)
	}
}

func (p *Proxy) serveMetadata(
	writer http.ResponseWriter,
	request *http.Request,
	response *http.Response,
	headers http.Header,
	cacheHeaders http.Header,
	key string,
	cacheable bool,
) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMetadataBytes+1))
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "could not read upstream metadata", err)
		return
	}
	if len(body) <= maxMetadataBytes {
		body = bytes.ReplaceAll(body, []byte(strings.TrimRight(p.upstream.String(), "/")), []byte(p.proxyBaseURL))
		headers.Set("Content-Length", strconv.Itoa(len(body)))
		cacheHeaders.Set("Content-Length", strconv.Itoa(len(body)))
		if cacheable {
			metadata := cache.Metadata{Status: response.StatusCode, Header: cacheHeaders}
			if err := p.store.PutBytes(key, metadata, body); err != nil {
				p.logger.Warn("cache metadata failed", "repository", p.name, "error", err)
			}
		}
		httppolicy.CopyHeaders(writer.Header(), headers)
		writer.WriteHeader(response.StatusCode)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(body)
		}
		return
	}

	p.logger.Warn("metadata exceeds rewrite limit", "repository", p.name, "limit_bytes", maxMetadataBytes)
	headers.Del("Content-Length")
	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)
	if request.Method != http.MethodHead {
		_, _ = io.Copy(writer, io.MultiReader(bytes.NewReader(body), response.Body))
	}
}

func (p *Proxy) fail(writer http.ResponseWriter, request *http.Request, status int, message string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(request.Context().Err(), context.Canceled) {
		p.stats.clientCanceled.Add(1)
		p.logger.Debug(
			"client canceled request",
			"repository", p.name,
			"method", request.Method,
			"path", request.URL.Path,
		)
		return
	}
	p.stats.errors.Add(1)
	p.logger.Error(
		message,
		"repository", p.name,
		"method", request.Method,
		"path", request.URL.Path,
		"error", httppolicy.SafeError(err),
	)
	http.Error(writer, message, status)
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.Contains(strings.ToLower(contentType), "json")
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

type keyedLocker struct {
	mu    sync.Mutex
	items map[string]*lockRef
}

type lockRef struct {
	mu   sync.Mutex
	refs int
}

func (locker *keyedLocker) lock(key string) func() {
	locker.mu.Lock()
	item := locker.items[key]
	if item == nil {
		item = &lockRef{}
		locker.items[key] = item
	}
	item.refs++
	locker.mu.Unlock()

	item.mu.Lock()
	return func() {
		item.mu.Unlock()
		locker.mu.Lock()
		item.refs--
		if item.refs == 0 {
			delete(locker.items, key)
		}
		locker.mu.Unlock()
	}
}
