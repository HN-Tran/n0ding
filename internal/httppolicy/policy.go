package httppolicy

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ClientWithSafeRedirects returns a shallow client copy that retains
// credentials only across redirects to the exact same origin. Redirects to a
// different scheme, host, or effective port remain allowed for registry/CDN
// compatibility, but continue without client credentials.
func ClientWithSafeRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	secured := *client
	previousCheck := client.CheckRedirect
	secured.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		sanitizeRedirectRequest(request, via)
		if previousCheck != nil {
			if err := previousCheck(request, via); err != nil {
				return err
			}
			// A caller-supplied redirect hook cannot weaken the shared policy.
			sanitizeRedirectRequest(request, via)
			return nil
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &secured
}

func sanitizeRedirectRequest(request *http.Request, via []*http.Request) {
	hasUserinfo := request.URL != nil && request.URL.User != nil
	if hasUserinfo {
		request.URL.User = nil
	}
	stripBlockedRequestHeaders(request.Header)
	if hasUserinfo || len(via) == 0 || !sameOrigin(via[len(via)-1].URL, request.URL) {
		stripAuthorizationHeader(request.Header)
	}
}

// CopyHeaders replaces target values with a copy of source values.
func CopyHeaders(target, source http.Header) {
	for name, values := range source {
		target.Del(name)
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

// ForwardRequestHeaders copies end-to-end client headers to an upstream
// request. Credential-bearing headers other than an explicitly allowed
// Authorization header are never forwarded.
func ForwardRequestHeaders(target, source http.Header, forwardAuthorization bool) {
	connectionHeaders := namedConnectionHeaders(source)
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if isHopByHop(canonical) || connectionHeaders[canonical] || blockedRequestHeader(canonical) {
			continue
		}
		if canonical == "Authorization" && !forwardAuthorization {
			continue
		}
		for _, value := range values {
			target.Add(canonical, value)
		}
	}
}

// ResponseHeaders removes hop-by-hop fields while retaining end-to-end fields
// for the current downstream response.
func ResponseHeaders(source http.Header) http.Header {
	connectionHeaders := namedConnectionHeaders(source)
	target := make(http.Header)
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if isHopByHop(canonical) || connectionHeaders[canonical] {
			continue
		}
		for _, value := range values {
			target.Add(canonical, value)
		}
	}
	return target
}

// CacheMetadataHeaders is a defense-in-depth filter for headers written to
// persistent cache metadata. It does not decide whether the body is safe to
// cache; callers must also use ResponseAllowsStorage.
func CacheMetadataHeaders(source http.Header) http.Header {
	target := ResponseHeaders(source)
	for _, name := range sensitiveResponseHeaders {
		target.Del(name)
	}
	target.Del("Age")
	target.Del("X-N0ding-Cache")
	return target
}

// RequestBypassesCache returns true when a client explicitly requests that a
// shared cache not reuse or store a response.
func RequestBypassesCache(header http.Header) bool {
	for _, directive := range cacheDirectives(header.Values("Cache-Control")) {
		switch directive.name {
		case "no-cache", "no-store":
			return true
		case "max-age":
			if strings.Trim(directive.value, `"`) == "0" {
				return true
			}
		}
	}
	return containsToken(header.Values("Pragma"), "no-cache")
}

// ResponseAllowsStorage enforces the response-side subset of shared-cache
// semantics that n0ding understands. Unknown Vary dimensions are rejected
// instead of being collapsed into the current cache key.
func ResponseAllowsStorage(requestHeader, responseHeader http.Header, allowedVary ...string) bool {
	if hasCacheDirective(requestHeader, "no-store") {
		return false
	}
	if hasCacheDirective(responseHeader, "no-cache", "no-store", "private") {
		return false
	}
	if containsToken(responseHeader.Values("Pragma"), "no-cache") {
		return false
	}
	explicitlyPublic := hasCacheDirective(responseHeader, "public")
	for _, name := range sensitiveResponseHeaders {
		if _, present := responseHeader[http.CanonicalHeaderKey(name)]; present {
			if explicitlyPublic && isCookieHeader(name) {
				continue
			}
			return false
		}
	}

	allowed := make(map[string]struct{}, len(allowedVary))
	for _, name := range allowedVary {
		allowed[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for _, value := range responseHeader.Values("Vary") {
		for _, name := range strings.Split(value, ",") {
			canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
			if canonical == "" {
				continue
			}
			if canonical == "*" {
				return false
			}
			if _, ok := allowed[canonical]; !ok {
				return false
			}
		}
	}
	return true
}

// PublicUpstreamURL removes URL components that commonly contain credentials
// before an upstream is exposed through status APIs or logs.
func PublicUpstreamURL(upstream *url.URL) string {
	if upstream == nil {
		return ""
	}
	public := *upstream
	public.User = nil
	public.RawQuery = ""
	public.ForceQuery = false
	public.Fragment = ""
	return public.String()
}

// SafeError removes the request URL carried by net/url errors before they are
// logged. The underlying transport error remains available for diagnosis.
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		if urlError.Err == nil {
			return urlError.Op
		}
		return urlError.Op + ": " + SafeError(urlError.Err)
	}
	return err.Error()
}

type cacheDirective struct {
	name  string
	value string
}

func cacheDirectives(values []string) []cacheDirective {
	var directives []cacheDirective
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name, directiveValue, _ := strings.Cut(strings.TrimSpace(part), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			directives = append(directives, cacheDirective{
				name:  name,
				value: strings.TrimSpace(directiveValue),
			})
		}
	}
	return directives
}

func hasCacheDirective(header http.Header, names ...string) bool {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[strings.ToLower(name)] = struct{}{}
	}
	for _, directive := range cacheDirectives(header.Values("Cache-Control")) {
		if _, ok := wanted[directive.name]; ok {
			return true
		}
	}
	return false
}

func containsToken(values []string, token string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func namedConnectionHeaders(header http.Header) map[string]bool {
	named := make(map[string]bool)
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
			if canonical != "" {
				named[canonical] = true
			}
		}
	}
	return named
}

func blockedRequestHeader(name string) bool {
	switch name {
	case "Cookie",
		"Cookie2",
		"Forwarded",
		"Host",
		"Npm-Otp",
		"Proxy-Authorization",
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Port",
		"X-Forwarded-Proto",
		"X-Npm-Otp",
		"X-Registry-Auth",
		"X-Registry-Config":
		return true
	default:
		return false
	}
}

func stripBlockedRequestHeaders(header http.Header) {
	for name := range header {
		canonical := http.CanonicalHeaderKey(name)
		if blockedRequestHeader(canonical) {
			delete(header, name)
		}
	}
}

func stripAuthorizationHeader(header http.Header) {
	for name := range header {
		if http.CanonicalHeaderKey(name) == "Authorization" {
			delete(header, name)
		}
	}
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
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

func isCookieHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Cookie", "Set-Cookie", "Set-Cookie2":
		return true
	default:
		return false
	}
}

func isHopByHop(name string) bool {
	switch name {
	case "Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade":
		return true
	default:
		return false
	}
}

var sensitiveResponseHeaders = []string{
	"Authentication-Info",
	"Authorization",
	"Cookie",
	"Proxy-Authentication-Info",
	"Proxy-Authorization",
	"Set-Cookie",
	"Set-Cookie2",
	"WWW-Authenticate",
}
