package httppolicy

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestForwardRequestHeadersEnforcesCredentialBoundary(t *testing.T) {
	source := http.Header{
		"Accept":           {"application/json"},
		"Authorization":    {"Bearer allowed-only-explicitly"},
		"Connection":       {"X-Connection-Secret"},
		"Cookie":           {"session=secret"},
		"Host":             {"attacker.example.test"},
		"Npm-Otp":          {"123456"},
		"Proxy-Connection": {"keep-alive"},
		"X-Connection-Secret": {
			"secret",
		},
		"X-Forwarded-For": {"192.0.2.10"},
		"X-Registry-Auth": {"registry-secret"},
	}

	withoutAuthorization := make(http.Header)
	ForwardRequestHeaders(withoutAuthorization, source, false)
	if got := withoutAuthorization.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	for _, name := range []string{
		"Authorization",
		"Connection",
		"Cookie",
		"Host",
		"Npm-Otp",
		"Proxy-Connection",
		"X-Connection-Secret",
		"X-Forwarded-For",
		"X-Registry-Auth",
	} {
		if value := withoutAuthorization.Get(name); value != "" {
			t.Fatalf("%s forwarded as %q", name, value)
		}
	}

	withAuthorization := make(http.Header)
	ForwardRequestHeaders(withAuthorization, source, true)
	if got := withAuthorization.Get("Authorization"); got != "Bearer allowed-only-explicitly" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := withAuthorization.Get("Cookie"); got != "" {
		t.Fatalf("Cookie forwarded as %q", got)
	}
}

func TestResponseAllowsStorageRejectsPrivateAndUnkeyedResponses(t *testing.T) {
	tests := []struct {
		name     string
		request  http.Header
		response http.Header
	}{
		{
			name:     "request no-store",
			request:  http.Header{"Cache-Control": {"no-store"}},
			response: http.Header{},
		},
		{
			name:     "response private",
			request:  http.Header{},
			response: http.Header{"Cache-Control": {"max-age=60, private"}},
		},
		{
			name:     "response no-cache",
			request:  http.Header{},
			response: http.Header{"Cache-Control": {"no-cache"}},
		},
		{
			name:     "set cookie",
			request:  http.Header{},
			response: http.Header{"Set-Cookie": {"session=secret"}},
		},
		{
			name:     "authentication info",
			request:  http.Header{},
			response: http.Header{"Authentication-Info": {"nextnonce=secret"}},
		},
		{
			name:     "vary authorization",
			request:  http.Header{},
			response: http.Header{"Vary": {"Accept, Authorization"}},
		},
		{
			name:     "vary wildcard",
			request:  http.Header{},
			response: http.Header{"Vary": {"*"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if ResponseAllowsStorage(test.request, test.response, "Accept", "Accept-Encoding") {
				t.Fatal("response unexpectedly allowed in shared cache")
			}
		})
	}

	if !ResponseAllowsStorage(
		http.Header{},
		http.Header{"Vary": {"Accept, Accept-Encoding"}},
		"Accept",
		"Accept-Encoding",
	) {
		t.Fatal("covered Vary dimensions were rejected")
	}
	if !ResponseAllowsStorage(
		http.Header{},
		http.Header{
			"Cache-Control": {"public, max-age=300"},
			"Set-Cookie":    {"edge-session=secret"},
			"Vary":          {"Accept, Accept-Encoding"},
		},
		"Accept",
		"Accept-Encoding",
	) {
		t.Fatal("explicitly public response with scrubbed cookie was rejected")
	}
}

func TestRequestBypassesCache(t *testing.T) {
	for _, header := range []http.Header{
		{"Cache-Control": {"no-cache"}},
		{"Cache-Control": {"no-store"}},
		{"Cache-Control": {"max-age=0"}},
		{"Pragma": {"no-cache"}},
	} {
		if !RequestBypassesCache(header) {
			t.Fatalf("header did not bypass cache: %#v", header)
		}
	}
	if RequestBypassesCache(http.Header{"Cache-Control": {"max-age=60"}}) {
		t.Fatal("ordinary max-age bypassed cache")
	}
}

func TestCacheMetadataHeadersStripsSecretsAndConnectionFields(t *testing.T) {
	source := http.Header{
		"Authentication-Info": {"nextnonce=secret"},
		"Connection":          {"X-Upstream-Secret"},
		"Content-Type":        {"application/octet-stream"},
		"Set-Cookie":          {"session=secret"},
		"WWW-Authenticate":    {`Bearer realm="https://auth.example.test"`},
		"X-N0ding-Cache":      {"MISS"},
		"X-Upstream-Secret":   {"secret"},
	}
	filtered := CacheMetadataHeaders(source)
	if got := filtered.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	for _, name := range []string{
		"Authentication-Info",
		"Connection",
		"Set-Cookie",
		"WWW-Authenticate",
		"X-N0ding-Cache",
		"X-Upstream-Secret",
	} {
		if value := filtered.Get(name); value != "" {
			t.Fatalf("%s persisted as %q", name, value)
		}
	}
}

func TestPublicUpstreamURLRemovesCredentialComponents(t *testing.T) {
	upstream, err := url.Parse("https://user:password@packages.example.test/base?token=secret#fragment")
	if err != nil {
		t.Fatal(err)
	}
	if got := PublicUpstreamURL(upstream); got != "https://packages.example.test/base" {
		t.Fatalf("public URL = %q", got)
	}
}

func TestSafeErrorRemovesRequestURL(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://user:password@packages.example.test/base?token=secret",
		Err: errors.New("connection refused"),
	}
	got := SafeError(err)
	if got != "Get: connection refused" {
		t.Fatalf("safe error = %q", got)
	}
	for _, secret := range []string{"user", "password", "token", "secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("safe error leaked %q: %q", secret, got)
		}
	}
}
