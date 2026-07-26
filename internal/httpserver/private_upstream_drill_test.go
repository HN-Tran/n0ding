package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HN-Tran/n0ding/internal/config"
	"github.com/HN-Tran/n0ding/internal/repository"
)

const (
	npmTokenA           = "Bearer n0ding-drill-npm-token-a-canary"
	npmTokenB           = "Bearer n0ding-drill-npm-token-b-canary"
	npmDeniedToken      = "Bearer n0ding-drill-npm-denied-canary"
	ociTokenA           = "Bearer n0ding-drill-oci-token-a-canary"
	ociTokenB           = "Bearer n0ding-drill-oci-token-b-canary"
	ociDeniedToken      = "Bearer n0ding-drill-oci-denied-canary"
	npmUserCanary       = "n0ding-drill-npm-user-canary"
	npmPasswordCanary   = "n0ding-drill-npm-password-canary"
	ociUserCanary       = "n0ding-drill-oci-user-canary"
	ociPasswordCanary   = "n0ding-drill-oci-password-canary"
	npmQueryCanary      = "n0ding-drill-npm-query-canary"
	ociQueryCanary      = "n0ding-drill-oci-query-canary"
	errorQueryCanary    = "n0ding-drill-error-query-canary"
	npmResponseCanary   = "n0ding-drill-npm-response-canary"
	npmPrivatePath      = "/npm/@private%2fdrill"
	npmRedirectPath     = "/npm/@private%2fredirect"
	npmErrorPath        = "/npm/@private%2fproxy-error"
	ociSamePath         = "/v2/private/same/manifests/latest"
	ociDistinctPath     = "/v2/private/distinct/manifests/latest"
	ociMissingPath      = "/v2/private/missing/manifests/latest"
	ociDeniedPath       = "/v2/private/denied/manifests/latest"
	ociRedirectBasePath = "/v2/private/redirect/blobs/"
)

var privateDrillCanaries = []string{
	strings.TrimPrefix(npmTokenA, "Bearer "),
	strings.TrimPrefix(npmTokenB, "Bearer "),
	strings.TrimPrefix(npmDeniedToken, "Bearer "),
	strings.TrimPrefix(ociTokenA, "Bearer "),
	strings.TrimPrefix(ociTokenB, "Bearer "),
	strings.TrimPrefix(ociDeniedToken, "Bearer "),
	npmUserCanary,
	npmPasswordCanary,
	ociUserCanary,
	ociPasswordCanary,
	npmQueryCanary,
	ociQueryCanary,
	errorQueryCanary,
	npmResponseCanary,
}

// TestPrivateUpstreamDrill exercises the real n0ding HTTP server against
// deterministic local private npm and OCI fixtures. It deliberately uses
// conspicuous fake canaries and scans every persistent/operator-facing output
// produced by the drill. It is not evidence for any external registry product.
func TestPrivateUpstreamDrill(t *testing.T) {
	npmIdentities := newDrillIdentities(map[string]string{
		npmTokenA: "A",
		npmTokenB: "B",
	})
	ociIdentities := newDrillIdentities(map[string]string{
		ociTokenA: "A",
		ociTokenB: "B",
	})

	var npmCDNAuthorization atomic.Value
	npmCDN := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		npmCDNAuthorization.Store(request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/octet-stream")
		_, _ = writer.Write([]byte("npm redirected package bytes"))
	}))
	defer npmCDN.Close()

	var failingAuthorization atomic.Value
	failingTarget := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		failingAuthorization.Store(request.Header.Get("Authorization"))
		if request.URL.Query().Get("access_token") != errorQueryCanary {
			t.Error("failing redirect target did not receive its query canary")
		}
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("failing fixture does not support connection hijacking")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack failing fixture connection: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer failingTarget.Close()

	npmUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, allowed := npmIdentities.authorize(request.Header.Get("Authorization"))
		if !allowed {
			http.Error(writer, "denied", http.StatusForbidden)
			return
		}
		switch request.URL.EscapedPath() {
		case "/@private%2fdrill":
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("Authentication-Info", `nextnonce="`+npmResponseCanary+`"`)
			_, _ = writer.Write([]byte("npm private package for identity " + identity))
		case "/@private%2fredirect":
			http.Redirect(
				writer,
				request,
				npmCDN.URL+"/package.tgz?download=1",
				http.StatusTemporaryRedirect,
			)
		case "/@private%2fproxy-error":
			if request.URL.Query().Get("access_token") != npmQueryCanary {
				t.Error("npm upstream did not receive its query canary")
			}
			http.Redirect(
				writer,
				request,
				failingTarget.URL+"/drop?access_token="+errorQueryCanary,
				http.StatusTemporaryRedirect,
			)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer npmUpstream.Close()

	sameManifest := []byte(`{"schemaVersion":2,"visibility":"same"}`)
	distinctManifestA := []byte(`{"schemaVersion":2,"visibility":"identity-a"}`)
	distinctManifestB := []byte(`{"schemaVersion":2,"visibility":"identity-b"}`)
	missingManifestA := []byte(`{"schemaVersion":2,"missing-head":"identity-a"}`)
	missingManifestB := []byte(`{"schemaVersion":2,"missing-head":"identity-b"}`)
	redirectedBlob := []byte("redirected OCI blob bytes")
	redirectedBlobDigest := drillDigest(redirectedBlob)
	ociCalls := newDrillCalls()

	var ociCDNAuthorization atomic.Value
	ociCDN := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ociCDNAuthorization.Store(request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/octet-stream")
		writer.Header().Set("Docker-Content-Digest", redirectedBlobDigest)
		if request.Method != http.MethodHead {
			_, _ = writer.Write(redirectedBlob)
		}
	}))
	defer ociCDN.Close()

	ociUpstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		ociCalls.record(request.Method, request.URL.Path, authorization)
		if request.URL.Path == ociSamePath &&
			request.URL.Query().Get("access_token") != ociQueryCanary {
			t.Error("OCI upstream did not receive its query canary")
		}
		identity, allowed := ociIdentities.authorize(authorization)
		if !allowed {
			http.Error(writer, "denied", http.StatusForbidden)
			return
		}

		var body []byte
		switch request.URL.Path {
		case ociSamePath:
			body = sameManifest
		case ociDistinctPath:
			if identity == "A" {
				body = distinctManifestA
			} else {
				body = distinctManifestB
			}
		case ociMissingPath:
			if identity == "A" {
				body = missingManifestA
			} else {
				body = missingManifestB
			}
		case ociRedirectBasePath + redirectedBlobDigest:
			if request.Method == http.MethodGet {
				http.Redirect(writer, request, ociCDN.URL+"/blob", http.StatusTemporaryRedirect)
				return
			}
			body = redirectedBlob
		default:
			http.NotFound(writer, request)
			return
		}

		digest := drillDigest(body)
		writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		if strings.HasPrefix(request.URL.Path, ociRedirectBasePath) {
			writer.Header().Set("Content-Type", "application/octet-stream")
		}
		if !(request.URL.Path == ociMissingPath && identity == "B" && request.Method == http.MethodHead) {
			writer.Header().Set("Docker-Content-Digest", digest)
		}
		if request.Method != http.MethodHead {
			_, _ = writer.Write(body)
		}
	}))
	defer ociUpstream.Close()

	cacheRoot := filepath.Join(t.TempDir(), "cache")
	server, logs := newPrivateDrillServer(
		t,
		cacheRoot,
		drillURLWithUserinfo(npmUpstream.URL, npmUserCanary, npmPasswordCanary),
		drillURLWithUserinfo(ociUpstream.URL, ociUserCanary, ociPasswordCanary),
	)
	serverClosed := false
	defer func() {
		if !serverClosed {
			server.Close()
		}
	}()
	client := server.Client()
	var proxyErrors [][]byte

	npmForA := drillGet(t, client, server.URL+npmPrivatePath, npmTokenA, "")
	assertDrillResponse(t, npmForA, http.StatusOK, "MISS", "npm private package for identity A")
	if got := npmForA.header.Get("Authentication-Info"); got != `nextnonce="`+npmResponseCanary+`"` {
		t.Fatal("npm fixture did not return its transient authentication-response canary")
	}
	assertDrillResponse(t, drillGet(t, client, server.URL+npmPrivatePath, npmTokenB, ""), http.StatusOK, "MISS", "npm private package for identity B")
	assertDrillResponse(t, drillGet(t, client, server.URL+npmPrivatePath, npmTokenA, ""), http.StatusOK, "MISS", "npm private package for identity A")
	assertDrillResponse(t, drillGet(t, client, server.URL+npmPrivatePath, npmDeniedToken, ""), http.StatusForbidden, "MISS", "denied\n")
	npmIdentities.revoke(npmTokenA)
	assertDrillResponse(t, drillGet(t, client, server.URL+npmPrivatePath, npmTokenA, ""), http.StatusForbidden, "MISS", "denied\n")
	assertDrillResponse(t, drillGet(t, client, server.URL+npmPrivatePath, npmTokenB, ""), http.StatusOK, "MISS", "npm private package for identity B")
	assertDrillResponse(t, drillGet(t, client, server.URL+npmRedirectPath, npmTokenB, ""), http.StatusOK, "MISS", "npm redirected package bytes")
	if got, _ := npmCDNAuthorization.Load().(string); got != "" {
		t.Fatal("npm cross-origin redirect retained Authorization")
	}
	npmFailure := drillGet(t, client, server.URL+npmErrorPath+"?access_token="+npmQueryCanary, npmTokenB, "")
	assertDrillResponse(t, npmFailure, http.StatusBadGateway, "", "upstream request failed\n")
	proxyErrors = append(proxyErrors, npmFailure.body)
	if got, _ := failingAuthorization.Load().(string); got != "" {
		t.Fatal("npm failing cross-origin redirect retained Authorization")
	}

	ociAccept := "application/vnd.oci.image.manifest.v1+json"
	sameQueryPath := ociSamePath + "?access_token=" + ociQueryCanary
	assertDrillResponse(t, drillGet(t, client, server.URL+sameQueryPath, ociTokenA, ociAccept), http.StatusOK, "MISS", string(sameManifest))
	sameForB := drillGet(t, client, server.URL+sameQueryPath, ociTokenB, ociAccept)
	assertDrillResponse(t, sameForB, http.StatusOK, "HIT", string(sameManifest))
	if got := sameForB.header.Get("Authentication-Info"); got != "" {
		t.Fatal("OCI cache hit replayed authentication metadata")
	}

	assertDrillResponse(t, drillGet(t, client, server.URL+ociDistinctPath, ociTokenA, ociAccept), http.StatusOK, "MISS", string(distinctManifestA))
	assertDrillResponse(t, drillGet(t, client, server.URL+ociDistinctPath, ociTokenB, ociAccept), http.StatusOK, "MISS", string(distinctManifestB))
	assertDrillResponse(t, drillGet(t, client, server.URL+ociMissingPath, ociTokenA, ociAccept), http.StatusOK, "MISS", string(missingManifestA))
	assertDrillResponse(t, drillGet(t, client, server.URL+ociMissingPath, ociTokenB, ociAccept), http.StatusOK, "MISS", string(missingManifestB))

	redirectPath := ociRedirectBasePath + redirectedBlobDigest
	assertDrillResponse(t, drillGet(t, client, server.URL+redirectPath, ociTokenA, "application/octet-stream"), http.StatusOK, "MISS", string(redirectedBlob))
	redirectForB := drillGet(t, client, server.URL+redirectPath, ociTokenB, "application/octet-stream")
	assertDrillResponse(t, redirectForB, http.StatusOK, "HIT", string(redirectedBlob))
	if got, _ := ociCDNAuthorization.Load().(string); got != "" {
		t.Fatal("OCI cross-origin redirect retained Authorization")
	}
	if got := redirectForB.header.Get("Authentication-Info"); got != "" {
		t.Fatal("OCI blob cache hit replayed authentication metadata")
	}

	assertDrillResponse(t, drillGet(t, client, server.URL+ociDeniedPath, ociDeniedToken, ociAccept), http.StatusForbidden, "MISS", "denied\n")
	ociIdentities.revoke(ociTokenB)
	assertDrillResponse(t, drillGet(t, client, server.URL+sameQueryPath, ociTokenB, ociAccept), http.StatusForbidden, "MISS", "denied\n")
	assertDrillResponse(t, drillGet(t, client, server.URL+sameQueryPath, ociTokenA, ociAccept), http.StatusOK, "HIT", string(sameManifest))

	if got := ociCalls.count(http.MethodHead, ociSamePath, ociTokenB); got != 3 {
		t.Fatalf("identity B same-digest/revocation HEAD count = %d, want 3", got)
	}
	if got := ociCalls.count(http.MethodGet, ociSamePath, ociTokenB); got != 1 {
		t.Fatalf("identity B revoked fallback GET count = %d, want 1", got)
	}
	if got := ociCalls.count(http.MethodHead, ociDistinctPath, ociTokenB); got != 2 {
		t.Fatalf("changed-digest HEAD count = %d, want 2", got)
	}
	if got := ociCalls.count(http.MethodGet, ociDistinctPath, ociTokenB); got != 1 {
		t.Fatalf("changed-digest GET count = %d, want 1", got)
	}
	if got := ociCalls.count(http.MethodHead, ociMissingPath, ociTokenB); got != 2 {
		t.Fatalf("missing-digest HEAD count = %d, want 2", got)
	}
	if got := ociCalls.count(http.MethodGet, ociMissingPath, ociTokenB); got != 1 {
		t.Fatalf("missing-digest GET count = %d, want 1", got)
	}
	if got := ociCalls.count(http.MethodHead, redirectPath, ociTokenB); got != 1 {
		t.Fatalf("redirected-blob authorization HEAD count = %d, want 1", got)
	}
	if got := ociCalls.count(http.MethodGet, redirectPath, ociTokenB); got != 0 {
		t.Fatalf("redirected-blob identity B GET count = %d, want 0", got)
	}

	status := drillGet(t, client, server.URL+"/api/v1/status", "", "")
	if status.status != http.StatusOK {
		t.Fatalf("status endpoint = %d", status.status)
	}
	metrics := drillGet(t, client, server.URL+"/metrics", "", "")
	if metrics.status != http.StatusOK {
		t.Fatalf("metrics endpoint = %d", metrics.status)
	}
	assertDrillSnapshots(t, status.body, map[string]repository.Snapshot{
		"npm": {CacheHits: 0, CacheMisses: 8, CacheObjects: 0},
		"oci": {CacheHits: 3, CacheMisses: 8, CacheObjects: 4},
	})

	server.Close()
	serverClosed = true
	assertDrillTreeExcludesCanaries(t, cacheRoot, privateDrillCanaries)
	assertDrillBytesExcludeCanaries(t, "status", status.body, privateDrillCanaries)
	assertDrillBytesExcludeCanaries(t, "metrics", metrics.body, privateDrillCanaries)
	assertDrillBytesExcludeCanaries(t, "logs", logs.Bytes(), privateDrillCanaries)
	for _, body := range proxyErrors {
		assertDrillBytesExcludeCanaries(t, "proxy error", body, privateDrillCanaries)
	}

	// This copy is fixture-level security coverage for stopped cache artifacts.
	// It does not replace the separate Docker Compose backup/restore drill.
	backupRoot := filepath.Join(t.TempDir(), "backup-artifact")
	copyDrillTree(t, cacheRoot, backupRoot)
	assertDrillTreeExcludesCanaries(t, backupRoot, privateDrillCanaries)
	restoreRoot := filepath.Join(t.TempDir(), "restored-cache")
	copyDrillTree(t, backupRoot, restoreRoot)
	assertDrillTreeExcludesCanaries(t, restoreRoot, privateDrillCanaries)

	restoredServer, restoredLogs := newPrivateDrillServer(
		t,
		restoreRoot,
		drillURLWithUserinfo(npmUpstream.URL, npmUserCanary, npmPasswordCanary),
		drillURLWithUserinfo(ociUpstream.URL, ociUserCanary, ociPasswordCanary),
	)
	defer restoredServer.Close()
	restoredClient := restoredServer.Client()
	assertDrillResponse(t, drillGet(t, restoredClient, restoredServer.URL+sameQueryPath, ociTokenA, ociAccept), http.StatusOK, "HIT", string(sameManifest))
	assertDrillResponse(t, drillGet(t, restoredClient, restoredServer.URL+sameQueryPath, ociTokenB, ociAccept), http.StatusForbidden, "MISS", "denied\n")
	restoredStatus := drillGet(t, restoredClient, restoredServer.URL+"/api/v1/status", "", "")
	restoredMetrics := drillGet(t, restoredClient, restoredServer.URL+"/metrics", "", "")
	assertDrillTreeExcludesCanaries(t, restoreRoot, privateDrillCanaries)
	assertDrillBytesExcludeCanaries(t, "restored status", restoredStatus.body, privateDrillCanaries)
	assertDrillBytesExcludeCanaries(t, "restored metrics", restoredMetrics.body, privateDrillCanaries)
	assertDrillBytesExcludeCanaries(t, "restored logs", restoredLogs.Bytes(), privateDrillCanaries)
}

type drillIdentities struct {
	mu     sync.RWMutex
	active map[string]string
}

func newDrillIdentities(active map[string]string) *drillIdentities {
	return &drillIdentities{active: active}
}

func (identities *drillIdentities) authorize(authorization string) (string, bool) {
	identities.mu.RLock()
	defer identities.mu.RUnlock()
	identity, ok := identities.active[authorization]
	return identity, ok
}

func (identities *drillIdentities) revoke(authorization string) {
	identities.mu.Lock()
	defer identities.mu.Unlock()
	delete(identities.active, authorization)
}

type drillCalls struct {
	mu     sync.Mutex
	counts map[string]int
}

func newDrillCalls() *drillCalls {
	return &drillCalls{counts: make(map[string]int)}
}

func (calls *drillCalls) record(method, path, authorization string) {
	calls.mu.Lock()
	defer calls.mu.Unlock()
	calls.counts[method+"\n"+path+"\n"+authorization]++
}

func (calls *drillCalls) count(method, path, authorization string) int {
	calls.mu.Lock()
	defer calls.mu.Unlock()
	return calls.counts[method+"\n"+path+"\n"+authorization]
}

type drillHTTPResponse struct {
	status int
	header http.Header
	body   []byte
}

func newPrivateDrillServer(
	t *testing.T,
	cacheRoot string,
	npmUpstream string,
	ociUpstream string,
) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler, err := New(config.Config{
		Server: config.Server{
			PublicBaseURL: "http://n0ding.private-drill.invalid",
		},
		Storage: config.Storage{
			Path: cacheRoot,
		},
		Repositories: []config.Repository{
			{
				Name:                 "npm",
				Type:                 "npm",
				Path:                 "/npm/",
				Upstream:             npmUpstream,
				TTL:                  time.Hour,
				ForwardAuthorization: true,
			},
			{
				Name:     "oci",
				Type:     "oci",
				Path:     "/v2/",
				Upstream: ociUpstream,
				TTL:      time.Hour,
			},
		},
	}, "private-drill-test", logger)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(handler), &logs
}

func drillGet(
	t *testing.T,
	client *http.Client,
	target string,
	authorization string,
	accept string,
) drillHTTPResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return drillHTTPResponse{
		status: response.StatusCode,
		header: response.Header.Clone(),
		body:   body,
	}
}

func assertDrillResponse(
	t *testing.T,
	response drillHTTPResponse,
	status int,
	cacheResult string,
	body string,
) {
	t.Helper()
	if response.status != status ||
		response.header.Get("X-N0ding-Cache") != cacheResult ||
		string(response.body) != body {
		t.Fatalf(
			"response: status=%d cache=%q body=%q; want status=%d cache=%q body=%q",
			response.status,
			response.header.Get("X-N0ding-Cache"),
			string(response.body),
			status,
			cacheResult,
			body,
		)
	}
}

func assertDrillSnapshots(
	t *testing.T,
	body []byte,
	expected map[string]repository.Snapshot,
) {
	t.Helper()
	var status struct {
		Repositories []repository.Snapshot `json:"repositories"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range status.Repositories {
		want, ok := expected[snapshot.Name]
		if !ok {
			continue
		}
		if snapshot.CacheHits != want.CacheHits ||
			snapshot.CacheMisses != want.CacheMisses ||
			snapshot.CacheObjects != want.CacheObjects {
			t.Fatalf(
				"repository %s snapshot: hits=%d misses=%d objects=%d; want %d/%d/%d",
				snapshot.Name,
				snapshot.CacheHits,
				snapshot.CacheMisses,
				snapshot.CacheObjects,
				want.CacheHits,
				want.CacheMisses,
				want.CacheObjects,
			)
		}
		delete(expected, snapshot.Name)
	}
	if len(expected) != 0 {
		t.Fatalf("missing repository snapshots: %v", expected)
	}
}

func drillURLWithUserinfo(rawURL, username, password string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

func drillDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func assertDrillTreeExcludesCanaries(t *testing.T, root string, canaries []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertDrillBytesExcludeCanaries(t, path, content, canaries)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertDrillBytesExcludeCanaries(
	t *testing.T,
	label string,
	content []byte,
	canaries []string,
) {
	t.Helper()
	for _, canary := range canaries {
		if bytes.Contains(content, []byte(canary)) {
			t.Errorf("%s contains credential canary", label)
		}
	}
}

func copyDrillTree(t *testing.T, sourceRoot, targetRoot string) {
	t.Helper()
	err := filepath.WalkDir(sourceRoot, func(source string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, source)
		if err != nil {
			return err
		}
		target := filepath.Join(targetRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
