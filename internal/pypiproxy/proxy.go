package pypiproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"

	"github.com/HN-Tran/n0ding/internal/cache"
	"github.com/HN-Tran/n0ding/internal/httppolicy"
	"github.com/HN-Tran/n0ding/internal/repository"
	storagecontroller "github.com/HN-Tran/n0ding/internal/storage"
)

const (
	maxSimpleBytes = 64 << 20
	maxUploadBytes = 512 << 20
)

type Options struct {
	Name                 string
	Path                 string
	Upstream             string
	PublicBaseURL        string
	TTL                  time.Duration
	ForwardAuthorization bool
	PublishToken         string
	AllowedFileOrigins   []string
	Store                *cache.Store
	LocalPath            string
	Client               *http.Client
	Logger               *slog.Logger
}

type Proxy struct {
	name                 string
	path                 string
	filePath             string
	uploadPath           string
	packagePath          string
	upstream             *url.URL
	proxySimpleBaseURL   string
	proxyFileBaseURL     string
	proxyPackageBaseURL  string
	ttl                  time.Duration
	forwardAuthorization bool
	publishToken         string
	allowedFileOrigins   map[string]struct{}
	store                *cache.Store
	localPath            string
	client               *http.Client
	logger               *slog.Logger
	stats                counters
	locks                keyedLocker
	storage              *storagecontroller.Controller
	freeBytes            func(string) (int64, error)
}

type counters struct {
	requests       atomic.Uint64
	hits           atomic.Uint64
	misses         atomic.Uint64
	errors         atomic.Uint64
	clientCanceled atomic.Uint64
	rangeRequests  atomic.Uint64
}

type localPackage struct {
	Filename       string    `json:"filename"`
	Hash           string    `json:"sha256"`
	Size           int64     `json:"size"`
	RequiresPython string    `json:"requires_python,omitempty"`
	UploadedAt     time.Time `json:"uploaded_at"`
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
	uploadPath := strings.TrimSuffix(options.Path, "simple/") + "legacy/"
	packagePath := strings.TrimSuffix(options.Path, "simple/") + "packages/"
	if options.PublishToken != "" && options.LocalPath == "" {
		return nil, fmt.Errorf("local package path is required when publishing is enabled")
	}
	if options.LocalPath != "" {
		if err := os.MkdirAll(options.LocalPath, 0o750); err != nil {
			return nil, fmt.Errorf("create local package directory: %w", err)
		}
	}
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
		uploadPath:           uploadPath,
		packagePath:          packagePath,
		upstream:             upstream,
		proxySimpleBaseURL:   strings.TrimRight(options.PublicBaseURL, "/") + strings.TrimSuffix(options.Path, "/"),
		proxyFileBaseURL:     strings.TrimRight(options.PublicBaseURL, "/") + strings.TrimSuffix(filePath, "/"),
		proxyPackageBaseURL:  strings.TrimRight(options.PublicBaseURL, "/") + strings.TrimSuffix(packagePath, "/"),
		ttl:                  options.TTL,
		forwardAuthorization: options.ForwardAuthorization,
		publishToken:         options.PublishToken,
		allowedFileOrigins:   allowed,
		store:                options.Store,
		localPath:            options.LocalPath,
		client:               options.Client,
		logger:               options.Logger,
		locks:                keyedLocker{items: make(map[string]*lockRef)},
	}, nil
}

func (p *Proxy) FilePath() string {
	return p.filePath
}

func (p *Proxy) UploadPath() string { return p.uploadPath }

func (p *Proxy) PackagePath() string { return p.packagePath }

func (p *Proxy) SetStorageController(controller *storagecontroller.Controller) {
	p.storage = controller
	p.freeBytes = storagecontroller.FreeBytes
}

func (p *Proxy) PrivateSize() (int64, error) {
	var total int64
	err := filepath.WalkDir(p.localPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if info.Size() > 0 && total > (1<<63-1)-info.Size() {
				return errors.New("private PyPI storage size overflow")
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	p.stats.requests.Add(1)
	if request.URL.Path == p.uploadPath {
		p.serveUpload(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, p.packagePath) {
		p.serveLocalFile(writer, request)
		return
	}
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
	if project := p.simpleProject(request.URL.Path); project != "" && p.localPath != "" {
		packages, err := p.localPackages(project)
		if err != nil {
			p.fail(writer, request, http.StatusInternalServerError, "read private PyPI package index", err)
			return
		}
		if len(packages) > 0 {
			p.serveLocalProject(writer, request, project, packages)
			return
		}
	}
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
			p.recordStreamError(request, "cache PyPI body failed", err)
		}
		return
	}
	if _, err := io.Copy(writer, response.Body); err != nil {
		p.logger.Debug("proxy PyPI stream failed", "repository", p.name, "error", err)
	}
}

func (p *Proxy) recordStreamError(request *http.Request, message string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(request.Context().Err(), context.Canceled) {
		p.stats.clientCanceled.Add(1)
		p.logger.Debug("client canceled stream", "repository", p.name, "method", request.Method, "path", request.URL.Path)
		return
	}
	p.stats.errors.Add(1)
	p.logger.Warn(message, "repository", p.name, "error", httppolicy.SafeError(err))
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

func (p *Proxy) serveUpload(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !p.authorizedUpload(request.Header.Get("Authorization")) {
		writer.Header().Set("WWW-Authenticate", `Basic realm="n0ding-pypi"`)
		http.Error(writer, "upload authorization required", http.StatusUnauthorized)
		return
	}
	var reservation *storagecontroller.Reservation
	if p.storage != nil {
		freeBytes, err := p.freeBytes(p.localPath)
		if err != nil {
			p.fail(writer, request, http.StatusInsufficientStorage, "could not verify PyPI storage capacity", err)
			return
		}
		reservation = p.storage.Reserve(request.ContentLength, freeBytes)
		if reservation == nil {
			http.Error(writer, "insufficient storage for PyPI upload", http.StatusInsufficientStorage)
			return
		}
		defer reservation.Release()
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxUploadBytes)
	if err := request.ParseMultipartForm(32 << 20); err != nil {
		p.fail(writer, request, http.StatusBadRequest, "invalid PyPI upload", err)
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	project := normalizeProjectName(request.FormValue("name"))
	if project == "" {
		p.fail(writer, request, http.StatusBadRequest, "PyPI upload is missing package name", errors.New("missing name"))
		return
	}
	file, header, err := request.FormFile("content")
	if err != nil {
		p.fail(writer, request, http.StatusBadRequest, "PyPI upload is missing distribution content", err)
		return
	}
	defer file.Close()
	pkg, err := p.storeLocalPackage(project, header.Filename, request.FormValue("requires_python"), file)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, os.ErrExist) {
			status = http.StatusConflict
		}
		p.fail(writer, request, status, "could not store PyPI package", err)
		return
	}
	if reservation != nil && !reservation.Commit(pkg.Size, 0) {
		_ = os.Remove(filepath.Join(p.localProjectPath(project), pkg.Filename))
		_ = os.Remove(filepath.Join(p.localProjectPath(project), pkg.Filename+".json"))
		p.fail(writer, request, http.StatusInsufficientStorage, "could not commit PyPI storage reservation", errors.New("storage reservation commit failed"))
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(writer, "stored %s sha256:%s\n", pkg.Filename, pkg.Hash)
}

func (p *Proxy) authorizedUpload(header string) bool {
	if p.publishToken == "" || header == "" {
		return false
	}
	scheme, credentials, ok := strings.Cut(header, " ")
	if !ok {
		return false
	}
	var provided string
	switch strings.ToLower(scheme) {
	case "bearer":
		provided = credentials
	case "basic":
		decoded, err := base64.StdEncoding.DecodeString(credentials)
		if err != nil {
			return false
		}
		_, provided, ok = strings.Cut(string(decoded), ":")
		if !ok {
			return false
		}
	default:
		return false
	}
	return len(provided) == len(p.publishToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(p.publishToken)) == 1
}

func (p *Proxy) simpleProject(requestPath string) string {
	relative := strings.Trim(strings.TrimPrefix(requestPath, p.path), "/")
	if relative == "" || strings.Contains(relative, "/") {
		return ""
	}
	decoded, err := url.PathUnescape(relative)
	if err != nil {
		return ""
	}
	return normalizeProjectName(decoded)
}

func (p *Proxy) serveLocalProject(writer http.ResponseWriter, request *http.Request, project string, packages []localPackage) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-N0ding-Cache", "LOCAL")
	if strings.Contains(request.Header.Get("Accept"), "application/vnd.pypi.simple.v1+json") {
		files := make([]map[string]any, 0, len(packages))
		for _, pkg := range packages {
			file := map[string]any{
				"filename": pkg.Filename,
				"url":      p.localPackageURL(project, pkg),
				"hashes":   map[string]string{"sha256": pkg.Hash},
			}
			if pkg.RequiresPython != "" {
				file["requires-python"] = pkg.RequiresPython
			}
			files = append(files, file)
		}
		writer.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		if request.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"meta":  map[string]string{"api-version": "1.0"},
			"name":  project,
			"files": files,
		})
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+html; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	var body strings.Builder
	body.WriteString("<!doctype html><html><body>\n")
	for _, pkg := range packages {
		_, _ = fmt.Fprintf(&body, `<a href="%s"`, htmlEscape(p.localPackageURL(project, pkg)))
		if pkg.RequiresPython != "" {
			_, _ = fmt.Fprintf(&body, ` data-requires-python="%s"`, htmlEscape(pkg.RequiresPython))
		}
		_, _ = fmt.Fprintf(&body, ">%s</a>\n", htmlEscape(pkg.Filename))
	}
	body.WriteString("</body></html>\n")
	_, _ = io.WriteString(writer, body.String())
}

func (p *Proxy) localPackageURL(project string, pkg localPackage) string {
	return p.proxyPackageBaseURL + "/" + url.PathEscape(project) + "/" + url.PathEscape(pkg.Filename) + "#sha256=" + pkg.Hash
}

func (p *Proxy) serveLocalFile(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative := strings.TrimPrefix(request.URL.Path, p.packagePath)
	escapedProject, escapedFilename, ok := strings.Cut(relative, "/")
	if !ok || strings.Contains(escapedFilename, "/") {
		http.NotFound(writer, request)
		return
	}
	project, projectErr := url.PathUnescape(escapedProject)
	filename, filenameErr := url.PathUnescape(escapedFilename)
	if projectErr != nil || filenameErr != nil || normalizeProjectName(project) != project || !safeDistributionFilename(filename) {
		http.NotFound(writer, request)
		return
	}
	file, err := os.Open(filepath.Join(p.localProjectPath(project), filename))
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(writer, request)
		return
	}
	p.stats.hits.Add(1)
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("X-N0ding-Cache", "LOCAL")
	http.ServeContent(writer, request, filename, info.ModTime(), file)
}

func (p *Proxy) storeLocalPackage(project, filename, requiresPython string, source io.Reader) (localPackage, error) {
	if project == "" || normalizeProjectName(project) != project || !safeDistributionFilename(filename) {
		return localPackage{}, errors.New("unsafe package name or distribution filename")
	}
	unlock := p.locks.lock("private\n" + project + "\n" + filename)
	defer unlock()
	directory := p.localProjectPath(project)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return localPackage{}, err
	}
	target := filepath.Join(directory, filename)
	if _, err := os.Stat(target); err == nil {
		return localPackage{}, fmt.Errorf("%w: distribution already exists", os.ErrExist)
	} else if !os.IsNotExist(err) {
		return localPackage{}, err
	}
	temp, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return localPackage{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hasher), source)
	if err != nil {
		temp.Close()
		return localPackage{}, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return localPackage{}, err
	}
	if err := temp.Close(); err != nil {
		return localPackage{}, err
	}
	if err := os.Chmod(tempPath, 0o640); err != nil {
		return localPackage{}, err
	}
	// Link within the same directory so an external writer cannot win a race
	// between the existence check and commit by replacing an existing release.
	if err := os.Link(tempPath, target); err != nil {
		if os.IsExist(err) {
			return localPackage{}, fmt.Errorf("%w: distribution already exists", os.ErrExist)
		}
		return localPackage{}, err
	}
	if err := os.Remove(tempPath); err != nil {
		_ = os.Remove(target)
		return localPackage{}, err
	}
	pkg := localPackage{Filename: filename, Hash: hex.EncodeToString(hasher.Sum(nil)), Size: size, RequiresPython: requiresPython, UploadedAt: time.Now().UTC()}
	metadata, err := json.Marshal(pkg)
	if err != nil {
		return localPackage{}, err
	}
	if err := os.WriteFile(target+".json", metadata, 0o640); err != nil {
		_ = os.Remove(target)
		return localPackage{}, err
	}
	return pkg, nil
}

func (p *Proxy) localPackages(project string) ([]localPackage, error) {
	entries, err := os.ReadDir(p.localProjectPath(project))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	packages := make([]localPackage, 0)
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".json") || !safeDistributionFilename(entry.Name()) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(p.localProjectPath(project), entry.Name()+".json"))
		var pkg localPackage
		if readErr == nil {
			if err := json.Unmarshal(data, &pkg); err != nil {
				return nil, err
			}
		} else if os.IsNotExist(readErr) {
			packagePath := filepath.Join(p.localProjectPath(project), entry.Name())
			file, openErr := os.Open(packagePath)
			if openErr != nil {
				return nil, openErr
			}
			hasher := sha256.New()
			size, hashErr := io.Copy(hasher, file)
			closeErr := file.Close()
			if hashErr != nil {
				return nil, hashErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			pkg = localPackage{Filename: entry.Name(), Hash: hex.EncodeToString(hasher.Sum(nil)), Size: size}
		} else {
			return nil, readErr
		}
		packages = append(packages, pkg)
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Filename < packages[j].Filename })
	return packages, nil
}

func (p *Proxy) localProjectPath(project string) string { return filepath.Join(p.localPath, project) }

func safeDistributionFilename(filename string) bool {
	if filename == "" || strings.ContainsAny(filename, `/\\`) || strings.HasPrefix(filename, ".") {
		return false
	}
	return strings.HasSuffix(filename, ".whl") || strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".zip")
}

func normalizeProjectName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var builder strings.Builder
	separator := false
	for _, character := range name {
		if character == '-' || character == '_' || character == '.' {
			separator = builder.Len() > 0
			continue
		}
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return ""
			}
		}
		if separator {
			builder.WriteByte('-')
			separator = false
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

func htmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&#34;", "'", "&#39;", "<", "&lt;", ">", "&gt;").Replace(value)
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
