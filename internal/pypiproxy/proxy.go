package pypiproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"

	"github.com/HN-Tran/n0ding/internal/cache"
	"github.com/HN-Tran/n0ding/internal/httppolicy"
	"github.com/HN-Tran/n0ding/internal/repository"
)

const maxSimpleBytes = 64 << 20

type Options struct {
	Name                 string
	Path                 string
	Upstream             string
	PublicBaseURL        string
	TTL                  time.Duration
	ForwardAuthorization bool
	AllowedFileOrigins   []string
	Store                *cache.Store
	Client               *http.Client
	Logger               *slog.Logger
}

type Proxy struct {
	name                 string
	path                 string
	filePath             string
	upstream             *url.URL
	proxySimpleBaseURL   string
	proxyFileBaseURL     string
	ttl                  time.Duration
	forwardAuthorization bool
	allowedFileOrigins   map[string]struct{}
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
	rangeRequests  atomic.Uint64
}

func New(options Options) (*Proxy, error) {
	upstream, err := url.Parse(options.Upstream)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("parse upstream URL")
	}
	if !strings.HasSuffix(options.Path, "/simple/") {
		return nil, fmt.Errorf("PyPI path must end with /simple/")
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
	filePath := strings.TrimSuffix(options.Path, "simple/") + "files/"
	allowed := map[string]struct{}{originKey(upstream): {}}
	for _, origin := range options.AllowedFileOrigins {
		parsed, parseErr := url.Parse(origin)
		if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("parse allowed file origin %q", origin)
		}
		allowed[originKey(parsed)] = struct{}{}
	}
	return &Proxy{
		name:                 options.Name,
		path:                 options.Path,
		filePath:             filePath,
		upstream:             upstream,
		proxySimpleBaseURL:   strings.TrimRight(options.PublicBaseURL, "/") + strings.TrimSuffix(options.Path, "/"),
		proxyFileBaseURL:     strings.TrimRight(options.PublicBaseURL, "/") + strings.TrimSuffix(filePath, "/"),
		ttl:                  options.TTL,
		forwardAuthorization: options.ForwardAuthorization,
		allowedFileOrigins:   allowed,
		store:                options.Store,
		client:               options.Client,
		logger:               options.Logger,
		locks:                keyedLocker{items: make(map[string]*lockRef)},
	}, nil
}

func (p *Proxy) FilePath() string {
	return p.filePath
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	p.stats.requests.Add(1)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get("Range") != "" {
		p.stats.rangeRequests.Add(1)
	}
	switch {
	case strings.HasPrefix(request.URL.Path, p.path):
		p.serveSimple(writer, request)
	case strings.HasPrefix(request.URL.Path, p.filePath):
		p.serveFile(writer, request)
	default:
		http.NotFound(writer, request)
	}
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
		Type:           "pypi",
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
	return "python -m pip install --index-url " + p.proxySimpleBaseURL + "/ PACKAGE\n" +
		"uv pip install --index-url " + p.proxySimpleBaseURL + "/ PACKAGE\n"
}

func (p *Proxy) serveSimple(writer http.ResponseWriter, request *http.Request) {
	target, err := p.simpleTargetURL(request.URL)
	if err != nil {
		p.fail(writer, request, http.StatusBadRequest, "invalid upstream URL", err)
		return
	}
	cacheable := request.Header.Get("Range") == "" &&
		!httppolicy.RequestBypassesCache(request.Header) &&
		(!p.forwardAuthorization || request.Header.Get("Authorization") == "")
	key := p.cacheKey("simple", target, request.Header.Get("Accept"), "")
	p.serveCached(writer, request, target, key, cacheable, p.serveSimpleMiss)
}

func (p *Proxy) serveFile(writer http.ResponseWriter, request *http.Request) {
	target, expectedSHA256, err := p.fileTargetURL(request.URL)
	if err != nil {
		p.fail(writer, request, http.StatusBadRequest, "invalid PyPI file URL", err)
		return
	}
	cacheable := request.Header.Get("Range") == "" &&
		!httppolicy.RequestBypassesCache(request.Header) &&
		(!p.forwardAuthorization || request.Header.Get("Authorization") == "")
	key := p.cacheKey("file", target, request.Header.Get("Accept"), expectedSHA256)
	p.serveCached(writer, request, target, key, cacheable, func(
		writer http.ResponseWriter,
		request *http.Request,
		target *url.URL,
		key string,
		cacheable bool,
	) {
		p.serveFileMiss(writer, request, target, key, expectedSHA256, cacheable)
	})
}

func (p *Proxy) serveCached(
	writer http.ResponseWriter,
	request *http.Request,
	target *url.URL,
	key string,
	cacheable bool,
	serveMiss func(http.ResponseWriter, *http.Request, *url.URL, string, bool),
) {
	if cacheable {
		if entry, found := p.lookup(key, p.ttl); found {
			p.stats.hits.Add(1)
			p.serveHit(writer, request, entry)
			return
		}
	}

	unlock := p.locks.lock(key)
	defer unlock()
	if cacheable {
		if entry, found := p.lookup(key, p.ttl); found {
			p.stats.hits.Add(1)
			p.serveHit(writer, request, entry)
			return
		}
	}

	p.stats.misses.Add(1)
	serveMiss(writer, request, target, key, cacheable)
}

func (p *Proxy) lookup(key string, ttl time.Duration) (cache.Entry, bool) {
	entry, found, err := p.store.Lookup(key, ttl)
	if err != nil {
		p.logger.Warn("cache lookup failed", "repository", p.name, "error", err)
		return cache.Entry{}, false
	}
	return entry, found
}

func (p *Proxy) simpleTargetURL(requestURL *url.URL) (*url.URL, error) {
	relativePath := strings.TrimPrefix(requestURL.EscapedPath(), p.path)
	target, err := url.Parse(strings.TrimRight(p.upstream.String(), "/") + "/" + relativePath)
	if err != nil {
		return nil, err
	}
	target.RawQuery = requestURL.RawQuery
	return target, nil
}

func (p *Proxy) fileTargetURL(requestURL *url.URL) (*url.URL, string, error) {
	query := requestURL.Query()
	raw := query.Get("url")
	if raw == "" {
		return nil, "", errors.New("missing url")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, "", errors.New("file url must be absolute")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, "", errors.New("file url must use http or https")
	}
	if target.User != nil || target.Fragment != "" {
		return nil, "", errors.New("file url must not contain userinfo or fragment")
	}
	if !p.fileOriginAllowed(target) {
		return nil, "", errors.New("file origin is not allowed")
	}
	metadataRequest := strings.HasSuffix(path.Base(requestURL.Path), ".metadata") &&
		!strings.HasSuffix(path.Base(target.Path), ".metadata")
	if metadataRequest {
		// PEP 658/714 clients form the metadata sidecar URL by appending
		// `.metadata` to the distribution link. The original upstream URL is
		// carried in our query string, so mirror that suffix onto the target.
		target.Path += ".metadata"
		if target.RawPath != "" {
			target.RawPath += ".metadata"
		}
	}
	expectedSHA256 := strings.ToLower(strings.TrimSpace(query.Get("sha256")))
	if metadataRequest {
		// A hash fragment on the distribution link covers the wheel/sdist, not
		// its metadata sidecar. The client validates the separate metadata hash.
		expectedSHA256 = ""
	}
	if expectedSHA256 != "" {
		if len(expectedSHA256) != sha256.Size*2 {
			return nil, "", errors.New("sha256 must be 64 lowercase hex characters")
		}
		if _, err := hex.DecodeString(expectedSHA256); err != nil {
			return nil, "", errors.New("sha256 must be hexadecimal")
		}
	}
	return target, expectedSHA256, nil
}

func (p *Proxy) serveSimpleMiss(
	writer http.ResponseWriter,
	request *http.Request,
	target *url.URL,
	key string,
	cacheable bool,
) {
	response, err := p.doUpstream(request, target)
	if err != nil {
		p.proxyFailure(writer, request, err)
		return
	}
	defer response.Body.Close()

	headers := httppolicy.ResponseHeaders(response.Header)
	headers.Del("Content-Encoding")
	headers.Set("X-N0ding-Cache", "MISS")
	if location := rewrittenLocation(headers.Get("Location"), target, p.upstream, p.proxySimpleBaseURL); location != "" {
		headers.Set("Location", location)
	}
	cacheable = cacheable &&
		request.Method == http.MethodGet &&
		response.StatusCode == http.StatusOK &&
		httppolicy.ResponseAllowsStorage(request.Header, response.Header, "Accept", "Accept-Encoding")
	cacheHeaders := httppolicy.CacheMetadataHeaders(headers)

	if !isHTML(response.Header.Get("Content-Type")) && !isJSON(response.Header.Get("Content-Type")) {
		p.streamResponse(writer, request, response, headers, cacheHeaders, key, cacheable, nil)
		return
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxSimpleBytes+1))
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "could not read upstream PyPI metadata", err)
		return
	}
	if len(body) > maxSimpleBytes {
		p.logger.Warn("PyPI metadata exceeds rewrite limit", "repository", p.name, "limit_bytes", maxSimpleBytes)
		headers.Del("Content-Length")
		httppolicy.CopyHeaders(writer.Header(), headers)
		writer.WriteHeader(response.StatusCode)
		if request.Method != http.MethodHead {
			_, _ = io.Copy(writer, io.MultiReader(bytes.NewReader(body), response.Body))
		}
		return
	}

	rewritten, err := p.rewriteSimple(body, response.Header.Get("Content-Type"), target)
	if err != nil {
		p.fail(writer, request, http.StatusBadGateway, "could not rewrite PyPI metadata", err)
		return
	}
	headers.Set("Content-Length", strconv.Itoa(len(rewritten)))
	cacheHeaders.Set("Content-Length", strconv.Itoa(len(rewritten)))
	if cacheable {
		metadata := cache.Metadata{Status: response.StatusCode, Header: cacheHeaders}
		if err := p.store.PutBytes(key, metadata, rewritten); err != nil {
			p.logger.Warn("cache PyPI metadata failed", "repository", p.name, "error", err)
		}
	}
	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(rewritten)
	}
}

func (p *Proxy) serveFileMiss(
	writer http.ResponseWriter,
	request *http.Request,
	target *url.URL,
	key string,
	expectedSHA256 string,
	cacheable bool,
) {
	response, err := p.doUpstream(request, target)
	if err != nil {
		p.proxyFailure(writer, request, err)
		return
	}
	defer response.Body.Close()

	headers := httppolicy.ResponseHeaders(response.Header)
	headers.Del("Content-Encoding")
	headers.Set("X-N0ding-Cache", "MISS")
	cacheable = cacheable &&
		request.Method == http.MethodGet &&
		response.StatusCode == http.StatusOK &&
		httppolicy.ResponseAllowsStorage(request.Header, response.Header, "Accept-Encoding")
	cacheHeaders := httppolicy.CacheMetadataHeaders(headers)

	var verifier func(hash.Hash) func(int64) error
	if expectedSHA256 != "" {
		verifier = func(hasher hash.Hash) func(int64) error {
			return func(int64) error {
				if got := hex.EncodeToString(hasher.Sum(nil)); got != expectedSHA256 {
					return fmt.Errorf("sha256 mismatch: got %s, want %s", got, expectedSHA256)
				}
				return nil
			}
		}
	}
	p.streamResponse(writer, request, response, headers, cacheHeaders, key, cacheable, verifier)
}

func (p *Proxy) streamResponse(
	writer http.ResponseWriter,
	request *http.Request,
	response *http.Response,
	headers http.Header,
	cacheHeaders http.Header,
	key string,
	cacheable bool,
	verifierFactory func(hash.Hash) func(int64) error,
) {
	httppolicy.CopyHeaders(writer.Header(), headers)
	writer.WriteHeader(response.StatusCode)
	if request.Method == http.MethodHead {
		return
	}
	if cacheable {
		source := response.Body
		var verifier func(int64) error
		if verifierFactory != nil {
			hasher := sha256.New()
			source = io.NopCloser(io.TeeReader(response.Body, hasher))
			verifier = verifierFactory(hasher)
		}
		metadata := cache.Metadata{Status: response.StatusCode, Header: cacheHeaders}
		if err := p.store.PutStreamVerified(key, metadata, source, writer, verifier); err != nil {
			p.logger.Warn("cache PyPI body failed", "repository", p.name, "error", err)
		}
		return
	}
	if _, err := io.Copy(writer, response.Body); err != nil {
		p.logger.Debug("proxy PyPI stream failed", "repository", p.name, "error", err)
	}
}

func (p *Proxy) doUpstream(request *http.Request, target *url.URL) (*http.Response, error) {
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	httppolicy.ForwardRequestHeaders(upstreamRequest.Header, request.Header, p.forwardAuthorization)
	upstreamRequest.Header.Set("Accept-Encoding", "identity")
	return p.client.Do(upstreamRequest)
}

func (p *Proxy) rewriteSimple(body []byte, contentType string, pageURL *url.URL) ([]byte, error) {
	switch {
	case isJSON(contentType):
		var value any
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		p.rewriteJSONValue(value, pageURL)
		return json.Marshal(value)
	case isHTML(contentType):
		document, err := html.Parse(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		p.rewriteHTMLNode(document, pageURL)
		var output bytes.Buffer
		if err := html.Render(&output, document); err != nil {
			return nil, err
		}
		return output.Bytes(), nil
	default:
		return body, nil
	}
}

func (p *Proxy) rewriteJSONValue(value any, pageURL *url.URL) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if strings.EqualFold(key, "url") {
				if raw, ok := nested.(string); ok {
					typed[key] = p.rewriteHref(raw, pageURL)
					continue
				}
			}
			p.rewriteJSONValue(nested, pageURL)
		}
	case []any:
		for _, nested := range typed {
			p.rewriteJSONValue(nested, pageURL)
		}
	}
}

func (p *Proxy) rewriteHTMLNode(node *html.Node, pageURL *url.URL) {
	if node.Type == html.ElementNode && node.Data == "a" {
		for index := range node.Attr {
			if strings.EqualFold(node.Attr[index].Key, "href") {
				node.Attr[index].Val = p.rewriteHref(node.Attr[index].Val, pageURL)
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		p.rewriteHTMLNode(child, pageURL)
	}
}

func (p *Proxy) rewriteHref(raw string, pageURL *url.URL) string {
	link, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || link.Scheme == "mailto" || link.Scheme == "data" {
		return raw
	}
	resolved := pageURL.ResolveReference(link)
	fragment := resolved.Fragment
	resolved.Fragment = ""
	if p.isSimpleURL(resolved) {
		return p.proxySimpleURL(resolved, fragment)
	}
	if p.fileOriginAllowed(resolved) {
		return p.proxyFileURL(resolved, fragment)
	}
	return raw
}

func (p *Proxy) isSimpleURL(target *url.URL) bool {
	return sameOrigin(target, p.upstream) &&
		strings.HasPrefix(ensureTrailingSlash(target.EscapedPath()), ensureTrailingSlash(p.upstream.EscapedPath()))
}

func (p *Proxy) proxySimpleURL(target *url.URL, fragment string) string {
	upstreamPath := ensureTrailingSlash(p.upstream.EscapedPath())
	relative := strings.TrimPrefix(target.EscapedPath(), upstreamPath)
	proxy := p.proxySimpleBaseURL + "/"
	if relative != "" {
		proxy += relative
	}
	if target.RawQuery != "" {
		proxy += "?" + target.RawQuery
	}
	if fragment != "" {
		proxy += "#" + fragment
	}
	return proxy
}

func (p *Proxy) proxyFileURL(target *url.URL, fragment string) string {
	sha := sha256FromFragment(fragment)
	targetCopy := *target
	targetCopy.Fragment = ""
	values := url.Values{"url": {targetCopy.String()}}
	if sha != "" {
		values.Set("sha256", sha)
	}
	// Package clients derive candidate versions and wheel compatibility from
	// the URL path, even when the Simple JSON response also carries a filename
	// field. Keep the upstream basename visible instead of exposing every file
	// as the indistinguishable `/files/` endpoint.
	filename := path.Base(target.Path)
	if filename == "." || filename == "/" || filename == "" {
		filename = "artifact"
	}
	proxy := p.proxyFileBaseURL + "/" + url.PathEscape(filename) + "?" + values.Encode()
	if fragment != "" {
		proxy += "#" + fragment
	}
	return proxy
}

func (p *Proxy) fileOriginAllowed(target *url.URL) bool {
	_, ok := p.allowedFileOrigins[originKey(target)]
	return ok
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
		p.logger.Debug("send cached PyPI body failed", "repository", p.name, "error", err)
	}
}

func (p *Proxy) cacheKey(kind string, target *url.URL, accept string, sha256 string) string {
	return p.name + "\n" + kind + "\n" + target.String() + "\naccept:" + accept + "\nsha256:" + sha256
}

func (p *Proxy) proxyFailure(writer http.ResponseWriter, request *http.Request, err error) {
	status := http.StatusBadGateway
	if request.Context().Err() != nil {
		status = 499
	}
	p.fail(writer, request, status, "upstream request failed", err)
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

func rewrittenLocation(raw string, pageURL, upstream *url.URL, proxySimpleBaseURL string) string {
	if raw == "" {
		return ""
	}
	location, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	resolved := pageURL.ResolveReference(location)
	if !sameOrigin(resolved, upstream) {
		return raw
	}
	upstreamPath := ensureTrailingSlash(upstream.EscapedPath())
	if !strings.HasPrefix(ensureTrailingSlash(resolved.EscapedPath()), upstreamPath) {
		return raw
	}
	relative := strings.TrimPrefix(resolved.EscapedPath(), upstreamPath)
	proxy := proxySimpleBaseURL + "/"
	if relative != "" {
		proxy += relative
	}
	if resolved.RawQuery != "" {
		proxy += "?" + resolved.RawQuery
	}
	if resolved.Fragment != "" {
		proxy += "#" + resolved.Fragment
	}
	return proxy
}

func isHTML(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.Contains(strings.ToLower(contentType), "html")
	}
	return mediaType == "text/html" || mediaType == "application/vnd.pypi.simple.v1+html"
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.Contains(strings.ToLower(contentType), "json")
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func sha256FromFragment(fragment string) string {
	values, err := url.ParseQuery(fragment)
	if err != nil {
		return ""
	}
	sha := strings.ToLower(strings.TrimSpace(values.Get("sha256")))
	if len(sha) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return ""
	}
	return sha
}

func ensureTrailingSlash(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasSuffix(path, "/") {
		return path + "/"
	}
	return path
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
}

func originKey(value *url.URL) string {
	if value == nil {
		return ""
	}
	return strings.ToLower(value.Scheme) + "://" + strings.ToLower(value.Hostname()) + ":" + effectivePort(value)
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
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
