package ociproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
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

const (
	maxManifestBytes = 16 << 20
	immutableTTL     = 10 * 365 * 24 * time.Hour
)

type Options struct {
	Name          string
	Path          string
	Upstream      string
	PublicBaseURL string
	TTL           time.Duration
	Store         *cache.Store
	Client        *http.Client
	Logger        *slog.Logger
}

type Proxy struct {
	name     string
	path     string
	upstream *url.URL
	registry string
	ttl      time.Duration
	store    *cache.Store
	client   *http.Client
	logger   *slog.Logger
	stats    counters
	locks    keyedLocker
}

type counters struct {
	requests       atomic.Uint64
	hits           atomic.Uint64
	misses         atomic.Uint64
	errors         atomic.Uint64
	clientCanceled atomic.Uint64
	rangeRequests  atomic.Uint64
}

func New(options Options) (*Proxy, error) {
	upstream, err := url.Parse(options.Upstream)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("parse upstream URL")
	}
	publicURL, err := url.Parse(options.PublicBaseURL)
	if err != nil || publicURL.Host == "" {
		return nil, fmt.Errorf("parse public base URL")
	}
	if options.Path != "/v2/" {
		return nil, fmt.Errorf("OCI path must be /v2/")
	}
	if options.Store == nil {
		return nil, fmt.Errorf("cache store is required")
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 15 * time.Minute}
	}
	options.Client = httppolicy.ClientWithSafeRedirects(options.Client)
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Proxy{
		name:     options.Name,
		path:     options.Path,
		upstream: upstream,
		registry: publicURL.Host,
		ttl:      options.TTL,
		store:    options.Store,
		client:   options.Client,
		logger:   options.Logger,
		locks:    keyedLocker{items: make(map[string]*lockRef)},
	}, nil
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	p.stats.requests.Add(1)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "OCI pull-through spike is read-only", http.StatusMethodNotAllowed)
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
	kind, requestedDigest, cacheResource := classify(request.URL.Path)
	rangeRequest := request.Header.Get("Range") != ""
	if rangeRequest {
		p.stats.rangeRequests.Add(1)
	}
	cacheableRequest := cacheResource &&
		!rangeRequest &&
		!httppolicy.RequestBypassesCache(request.Header)
	key := p.cacheKey(target, request.Header.Get("Accept"))

	if cacheableRequest {
		if entry, found := p.lookup(key, p.cacheTTL(kind, requestedDigest)); found {
			if p.authorizeCacheHit(request, target, entry.Metadata.ContentDigest) {
				p.stats.hits.Add(1)
				p.serveHit(writer, request, entry)
				return
			}
			_ = entry.Close()
		}

		unlock := p.locks.lock(key)
		defer unlock()
		if entry, found := p.lookup(key, p.cacheTTL(kind, requestedDigest)); found {
			if p.authorizeCacheHit(request, target, entry.Metadata.ContentDigest) {
				p.stats.hits.Add(1)
				p.serveHit(writer, request, entry)
				return
			}
			_ = entry.Close()
		}
		p.stats.misses.Add(1)
	}

	p.serveMiss(writer, request, target, key, kind, requestedDigest, cacheableRequest)
}

func (p *Proxy) Snapshot() repository.Snapshot {
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
		Type:           "oci",
		Path:           p.path,
		Upstream:       httppolicy.PublicUpstreamURL(p.upstream),
		Requests:       p.stats.requests.Load(),
		CacheHits:      hits,
		CacheMisses:    misses,
		Errors:         p.stats.errors.Load(),
		ClientCanceled: p.stats.clientCanceled.Load(),
		RangeRequests:  p.stats.rangeRequests.Load(),
		HitRatio:       ratio,
		StorageBytes:   storageBytes,
		CacheObjects:   objects,
	}
}

func (p *Proxy) SetupSnippet() string {
	return "docker pull " + p.registry + "/library/alpine:3.20\n"
}

func (p *Proxy) lookup(key string, ttl time.Duration) (cache.Entry, bool) {
	entry, found, err := p.store.Lookup(key, ttl)
	if err != nil {
		p.logger.Warn("cache lookup failed", "repository", p.name, "error", err)
		return cache.Entry{}, false
	}
	return entry, found
}

func (p *Proxy) authorizeCacheHit(request *http.Request, target *url.URL, cachedDigest string) bool {
	authorizationRequest, err := http.NewRequestWithContext(request.Context(), http.MethodHead, target.String(), nil)
	if err != nil {
		p.logger.Warn("create OCI authorization request failed", "repository", p.name, "error", err)
		return false
	}
	httppolicy.ForwardRequestHeaders(authorizationRequest.Header, request.Header, true)
	authorizationRequest.Header.Set("Accept-Encoding", "identity")

	response, err := p.client.Do(authorizationRequest)
	if err != nil {
		p.logger.Warn(
			"authorize OCI cache hit failed",
			"repository", p.name,
			"error", httppolicy.SafeError(err),
		)
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}
	upstreamDigest := response.Header.Get("Docker-Content-Digest")
	return upstreamDigest != "" &&
		cachedDigest != "" &&
		strings.EqualFold(upstreamDigest, cachedDigest)
}

func (p *Proxy) targetURL(requestURL *url.URL) (*url.URL, error) {
	target, err := url.Parse(strings.TrimRight(p.upstream.String(), "/") + requestURL.EscapedPath())
	if err != nil {
		return nil, err
	}
	target.RawQuery = requestURL.RawQuery
	return target, nil
}

func (p *Proxy) cacheKey(target *url.URL, accept string) string {
	return p.name + "\n" + target.String() + "\naccept:" + accept
}

func (p *Proxy) cacheTTL(kind, requestedDigest string) time.Duration {
	if kind == "blob" || requestedDigest != "" {
		return immutableTTL
	}
	return p.ttl
}

func (p *Proxy) serveHit(writer http.ResponseWriter, request *http.Request, entry cache.Entry) {
	defer entry.Close()
	httppolicy.CopyHeaders(writer.Header(), entry.Metadata.Header)
	writer.Header().Set("X-N0ding-Cache", "HIT")
	writer.Header().Set("Age", strconv.FormatInt(int64(time.Since(entry.Metadata.StoredAt).Seconds()), 10))
	writer.Header().Set("Content-Length", strconv.FormatInt(entry.Metadata.ContentBytes, 10))
	if entry.Metadata.ContentDigest != "" {
		writer.Header().Set("Docker-Content-Digest", entry.Metadata.ContentDigest)
	}
	writer.WriteHeader(entry.Metadata.Status)
	if request.Method == http.MethodHead {
		return
	}

	var source io.Reader = entry.Body
	var verifier hash.Hash
	if entry.Metadata.ContentDigest != "" {
		verifier = sha256.New()
		source = io.TeeReader(entry.Body, verifier)
	}
	if _, err := io.Copy(writer, source); err != nil {
		p.logger.Debug("send cached OCI body failed", "repository", p.name, "error", err)
		return
	}
	if verifier != nil {
		if err := verifySHA256(verifier, entry.Metadata.ContentDigest); err != nil {
			p.stats.errors.Add(1)
			p.logger.Error("cached OCI digest mismatch", "repository", p.name, "error", err)
		}
	}
}

func (p *Proxy) serveMiss(
	writer http.ResponseWriter,
	request *http.Request,
	target *url.URL,
	key string,
	kind string,
	requestedDigest string,
	cacheableRequest bool,
) {
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), nil)
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "could not create upstream request", err)
		return
	}
	httppolicy.ForwardRequestHeaders(upstreamRequest.Header, request.Header, true)
	upstreamRequest.Header.Set("Accept-Encoding", "identity")

	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "upstream request failed", err)
		return
	}
	defer response.Body.Close()

	headers := httppolicy.ResponseHeaders(response.Header)
	headers.Set("X-N0ding-Cache", "MISS")
	cacheable := cacheableRequest &&
		request.Method == http.MethodGet &&
		response.StatusCode == http.StatusOK &&
		httppolicy.ResponseAllowsStorage(
			request.Header,
			response.Header,
			"Accept",
			"Accept-Encoding",
		)
	if !cacheable {
		p.passThrough(writer, request, response, headers)
		return
	}

	expectedDigest := requestedDigest
	if expectedDigest == "" {
		expectedDigest = response.Header.Get("Docker-Content-Digest")
	}
	if _, valid := decodeSHA256(expectedDigest); !valid {
		p.logger.Warn("OCI response has no verifiable sha256 digest", "repository", p.name, "path", request.URL.Path)
		p.passThrough(writer, request, response, headers)
		return
	}
	headers.Set("Docker-Content-Digest", expectedDigest)
	cacheHeaders := httppolicy.CacheMetadataHeaders(headers)

	switch kind {
	case "manifest":
		p.cacheManifest(writer, request, response, headers, cacheHeaders, key, expectedDigest)
	case "blob":
		p.cacheBlob(writer, response, headers, cacheHeaders, key, expectedDigest)
	default:
		p.passThrough(writer, request, response, headers)
	}
}

func (p *Proxy) cacheManifest(
	writer http.ResponseWriter,
	request *http.Request,
	response *http.Response,
	headers http.Header,
	cacheHeaders http.Header,
	key string,
	expectedDigest string,
) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxManifestBytes+1))
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "could not read OCI manifest", err)
		return
	}
	if len(body) > maxManifestBytes {
		p.logger.Warn("OCI manifest exceeds cache limit", "repository", p.name, "limit_bytes", maxManifestBytes)
		headers.Del("Content-Length")
		httppolicy.CopyHeaders(writer.Header(), headers)
		writer.WriteHeader(response.StatusCode)
		_, _ = io.Copy(writer, io.MultiReader(bytes.NewReader(body), response.Body))
		return
	}
	sum := sha256.Sum256(body)
	if err := verifySum(sum[:], expectedDigest); err != nil {
		p.fail(writer, request, http.StatusBadGateway, "upstream OCI manifest digest mismatch", err)
		return
	}

	headers.Set("Content-Length", strconv.Itoa(len(body)))
	cacheHeaders.Set("Content-Length", strconv.Itoa(len(body)))
	metadata := cache.Metadata{
		Status:        response.StatusCode,
		Header:        cacheHeaders,
		ContentDigest: expectedDigest,
	}
	if err := p.store.PutBytes(key, metadata, body); err != nil {
		p.logger.Warn("cache OCI manifest failed", "repository", p.name, "error", err)
	}
	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(body)
}

func (p *Proxy) cacheBlob(
	writer http.ResponseWriter,
	response *http.Response,
	headers http.Header,
	cacheHeaders http.Header,
	key string,
	expectedDigest string,
) {
	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)

	hasher := sha256.New()
	source := io.TeeReader(response.Body, hasher)
	metadata := cache.Metadata{
		Status:        response.StatusCode,
		Header:        cacheHeaders,
		ContentDigest: expectedDigest,
	}
	err := p.store.PutStreamVerified(
		key,
		metadata,
		source,
		writer,
		func(int64) error { return verifySHA256(hasher, expectedDigest) },
	)
	if err != nil {
		p.stats.errors.Add(1)
		p.logger.Error("cache OCI blob failed", "repository", p.name, "error", err)
	}
}

func (p *Proxy) passThrough(
	writer http.ResponseWriter,
	request *http.Request,
	response *http.Response,
	headers http.Header,
) {
	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)
	if request.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(writer, response.Body); err != nil {
		p.logger.Debug("proxy OCI response failed", "repository", p.name, "error", err)
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

func classify(path string) (kind string, requestedDigest string, cacheable bool) {
	if index := strings.Index(path, "/manifests/"); index >= 0 {
		reference := path[index+len("/manifests/"):]
		if _, valid := decodeSHA256(reference); valid {
			return "manifest", reference, true
		}
		return "manifest", "", reference != ""
	}
	if index := strings.Index(path, "/blobs/"); index >= 0 {
		digest := path[index+len("/blobs/"):]
		if _, valid := decodeSHA256(digest); valid {
			return "blob", digest, true
		}
	}
	return "", "", false
}

func verifySHA256(hasher hash.Hash, expected string) error {
	return verifySum(hasher.Sum(nil), expected)
}

func verifySum(actual []byte, expected string) error {
	expectedBytes, valid := decodeSHA256(expected)
	if !valid {
		return fmt.Errorf("invalid expected digest %q", expected)
	}
	if !equalBytes(actual, expectedBytes) {
		return fmt.Errorf("sha256:%s does not match %s", hex.EncodeToString(actual), expected)
	}
	return nil
}

func decodeSHA256(value string) ([]byte, bool) {
	algorithm, encoded, ok := strings.Cut(strings.ToLower(value), ":")
	if !ok || algorithm != "sha256" || len(encoded) != sha256.Size*2 {
		return nil, false
	}
	decoded, err := hex.DecodeString(encoded)
	return decoded, err == nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
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
