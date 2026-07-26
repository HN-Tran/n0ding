package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	clientNPMTokenA      = "n0ding-soak-npm-token-a-canary"
	clientNPMTokenB      = "n0ding-soak-npm-token-b-canary"
	clientNPMDenied      = "n0ding-soak-npm-denied-canary"
	clientOCITokenA      = "n0ding-soak-oci-token-a-canary"
	clientOCITokenB      = "n0ding-soak-oci-token-b-canary"
	clientOCIDenied      = "n0ding-soak-oci-denied-canary"
	clientQueryCanary    = "n0ding-soak-query-canary"
	clientResponseCanary = "n0ding-soak-response-canary"
)

type clientCatalog struct {
	ManifestPath   string `json:"manifest_path"`
	ManifestDigest string `json:"manifest_digest"`
	BlobPath       string `json:"blob_path"`
	BlobDigest     string `json:"blob_digest"`
	SlowBlobPath   string `json:"slow_blob_path"`
	SlowBlobDigest string `json:"slow_blob_digest"`
}

type clientStats struct {
	NPMMetadataGET  uint64 `json:"npm_metadata_get"`
	NPMTarballGET   uint64 `json:"npm_tarball_get"`
	NPMPrivateA     uint64 `json:"npm_private_a"`
	NPMPrivateB     uint64 `json:"npm_private_b"`
	NPMDenied       uint64 `json:"npm_denied"`
	OCIManifestGET  uint64 `json:"oci_manifest_get"`
	OCIManifestHEAD uint64 `json:"oci_manifest_head"`
	OCIBlobGET      uint64 `json:"oci_blob_get"`
	OCIBlobHEAD     uint64 `json:"oci_blob_head"`
	SlowBlobGET     uint64 `json:"slow_blob_get"`
	SlowBlobHEAD    uint64 `json:"slow_blob_head"`
	SlowActive      int64  `json:"slow_active"`
	SlowAborted     uint64 `json:"slow_aborted"`
}

type requestResult struct {
	status             int
	cache              string
	bodyHash           string
	digest             string
	authenticationInfo string
}

type burstSummary struct {
	Requests   int    `json:"requests"`
	Hits       int    `json:"hits"`
	Misses     int    `json:"misses"`
	BodySHA256 string `json:"body_sha256"`
}

type workloadResult struct {
	Status            string            `json:"status"`
	Cycle             string            `json:"cycle"`
	Expectation       string            `json:"expectation"`
	Workers           int               `json:"workers"`
	NPMMetadata       burstSummary      `json:"npm_metadata"`
	NPMTarball        burstSummary      `json:"npm_tarball"`
	OCIManifest       burstSummary      `json:"oci_manifest"`
	OCIBlob           burstSummary      `json:"oci_blob"`
	UpstreamGETDeltas map[string]uint64 `json:"upstream_get_deltas"`
	IdentitySafety    string            `json:"identity_safety"`
	DeniedStatuses    map[string]int    `json:"denied_statuses"`
}

type abortResult struct {
	Status     string `json:"status"`
	Cycle      string `json:"cycle"`
	BytesRead  int    `json:"bytes_read"`
	Cache      string `json:"cache"`
	HTTPStatus int    `json:"http_status"`
}

func main() {
	mode := flag.String("mode", "workload", "workload, abort, or hold")
	n0dingURL := flag.String("n0ding-url", "http://n0ding:8080", "n0ding base URL")
	fixtureURL := flag.String("fixture-url", "http://localhost:9090", "fixture base URL")
	cycle := flag.String("cycle", "0", "safe cycle identifier")
	workers := flag.Int("workers", 6, "concurrent clients per cache key")
	expectation := flag.String("expect", "cold", "cold or warm")
	flag.Parse()

	if *workers < 2 {
		exitError(errors.New("workers must be at least 2"))
	}
	if *expectation != "cold" && *expectation != "warm" {
		exitError(errors.New("expect must be cold or warm"))
	}
	if _, err := url.ParseRequestURI(*n0dingURL); err != nil {
		exitError(errors.New("n0ding URL is invalid"))
	}
	if _, err := url.ParseRequestURI(*fixtureURL); err != nil {
		exitError(errors.New("fixture URL is invalid"))
	}

	client := &http.Client{Timeout: 45 * time.Second}
	switch *mode {
	case "workload":
		result, err := runWorkload(client, *n0dingURL, *fixtureURL, *cycle, *workers, *expectation)
		if err != nil {
			exitError(err)
		}
		writeResult(result)
	case "abort":
		result, err := runAbort(client, *n0dingURL, *fixtureURL, *cycle)
		if err != nil {
			exitError(err)
		}
		writeResult(result)
	case "hold":
		if err := runHold(*n0dingURL, *fixtureURL, *cycle); err != nil {
			exitError(err)
		}
	default:
		exitError(errors.New("unsupported client mode"))
	}
}

func runWorkload(
	client *http.Client,
	n0dingURL string,
	fixtureURL string,
	cycle string,
	workers int,
	expectation string,
) (workloadResult, error) {
	var catalog clientCatalog
	if err := getJSON(client, fixtureURL+"/control/catalog", &catalog); err != nil {
		return workloadResult{}, errors.New("read fixture catalog failed")
	}
	var before clientStats
	if err := getJSON(client, fixtureURL+"/control/stats", &before); err != nil {
		return workloadResult{}, errors.New("read initial fixture stats failed")
	}

	npmMetadata, err := runBurst(
		client,
		n0dingURL+"/npm/n0ding-soak-fixture?cycle="+url.QueryEscape(cycle),
		workers,
		nil,
		"",
	)
	if err != nil {
		return workloadResult{}, fmt.Errorf("npm metadata burst: %w", err)
	}
	npmTarball, err := runBurst(
		client,
		n0dingURL+"/npm/n0ding-soak-fixture/-/n0ding-soak-fixture-1.0.0.tgz?cycle="+url.QueryEscape(cycle),
		workers,
		nil,
		"",
	)
	if err != nil {
		return workloadResult{}, fmt.Errorf("npm tarball burst: %w", err)
	}
	ociManifest, err := runBurst(
		client,
		n0dingURL+catalog.ManifestPath+"?cycle="+url.QueryEscape(cycle),
		workers,
		func(index int) string {
			if index%2 == 0 {
				return clientOCITokenA
			}
			return clientOCITokenB
		},
		"application/vnd.oci.image.manifest.v1+json",
	)
	if err != nil {
		return workloadResult{}, fmt.Errorf("OCI manifest burst: %w", err)
	}
	ociBlob, err := runBurst(
		client,
		n0dingURL+catalog.BlobPath+"?cycle="+url.QueryEscape(cycle),
		workers,
		func(index int) string {
			if index%2 == 0 {
				return clientOCITokenA
			}
			return clientOCITokenB
		},
		"application/octet-stream",
	)
	if err != nil {
		return workloadResult{}, fmt.Errorf("OCI blob burst: %w", err)
	}

	if err := validateBurst(npmMetadata, workers, expectation, ""); err != nil {
		return workloadResult{}, fmt.Errorf("npm metadata validation: %w", err)
	}
	if err := validateBurst(npmTarball, workers, expectation, ""); err != nil {
		return workloadResult{}, fmt.Errorf("npm tarball validation: %w", err)
	}
	if err := validateBurst(ociManifest, workers, expectation, catalog.ManifestDigest); err != nil {
		return workloadResult{}, fmt.Errorf("OCI manifest validation: %w", err)
	}
	if err := validateBurst(ociBlob, workers, expectation, catalog.BlobDigest); err != nil {
		return workloadResult{}, fmt.Errorf("OCI blob validation: %w", err)
	}

	var afterBurst clientStats
	if err := getJSON(client, fixtureURL+"/control/stats", &afterBurst); err != nil {
		return workloadResult{}, errors.New("read final fixture stats failed")
	}
	wantGETs := uint64(1)
	if expectation == "warm" {
		wantGETs = 0
	}
	deltas := map[string]uint64{
		"npm_metadata": afterBurst.NPMMetadataGET - before.NPMMetadataGET,
		"npm_tarball":  afterBurst.NPMTarballGET - before.NPMTarballGET,
		"oci_manifest": afterBurst.OCIManifestGET - before.OCIManifestGET,
		"oci_blob":     afterBurst.OCIBlobGET - before.OCIBlobGET,
	}
	for name, delta := range deltas {
		if delta != wantGETs {
			return workloadResult{}, fmt.Errorf("%s upstream GET delta = %d, want %d", name, delta, wantGETs)
		}
	}

	npmA, err := doRequest(
		client,
		n0dingURL+"/npm/private-package?access_token="+url.QueryEscape(clientQueryCanary),
		clientNPMTokenA,
		"",
	)
	if err != nil {
		return workloadResult{}, errors.New("npm identity A request failed")
	}
	npmB, err := doRequest(client, n0dingURL+"/npm/private-package", clientNPMTokenB, "")
	if err != nil {
		return workloadResult{}, errors.New("npm identity B request failed")
	}
	npmDenied, err := doRequest(client, n0dingURL+"/npm/private-package", clientNPMDenied, "")
	if err != nil {
		return workloadResult{}, errors.New("npm denied request failed")
	}
	ociDenied, err := doRequest(
		client,
		n0dingURL+catalog.ManifestPath+"?cycle="+url.QueryEscape(cycle),
		clientOCIDenied,
		"application/vnd.oci.image.manifest.v1+json",
	)
	if err != nil {
		return workloadResult{}, errors.New("OCI denied request failed")
	}
	if npmA.status != http.StatusOK || npmB.status != http.StatusOK ||
		npmA.cache != "MISS" || npmB.cache != "MISS" ||
		npmA.bodyHash == npmB.bodyHash {
		return workloadResult{}, errors.New("npm identity isolation validation failed")
	}
	if npmA.authenticationInfo != `nextnonce="`+clientResponseCanary+`"` {
		return workloadResult{}, errors.New("npm transient authentication response validation failed")
	}
	if npmDenied.status != http.StatusForbidden || npmDenied.cache != "MISS" {
		return workloadResult{}, errors.New("npm denied identity validation failed")
	}
	if ociDenied.status != http.StatusForbidden || ociDenied.cache != "MISS" {
		return workloadResult{}, errors.New("OCI denied identity validation failed")
	}

	return workloadResult{
		Status:            "pass",
		Cycle:             cycle,
		Expectation:       expectation,
		Workers:           workers,
		NPMMetadata:       summarize(npmMetadata),
		NPMTarball:        summarize(npmTarball),
		OCIManifest:       summarize(ociManifest),
		OCIBlob:           summarize(ociBlob),
		UpstreamGETDeltas: deltas,
		IdentitySafety:    "pass",
		DeniedStatuses: map[string]int{
			"npm": npmDenied.status,
			"oci": ociDenied.status,
		},
	}, nil
}

func runAbort(
	client *http.Client,
	n0dingURL string,
	fixtureURL string,
	cycle string,
) (abortResult, error) {
	var catalog clientCatalog
	if err := getJSON(client, fixtureURL+"/control/catalog", &catalog); err != nil {
		return abortResult{}, errors.New("read fixture catalog failed")
	}
	request, err := http.NewRequest(
		http.MethodGet,
		n0dingURL+catalog.SlowBlobPath+"?abort="+url.QueryEscape(cycle),
		nil,
	)
	if err != nil {
		return abortResult{}, errors.New("create abort request failed")
	}
	request.Header.Set("Authorization", "Bearer "+clientOCITokenA)
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return abortResult{}, errors.New("start abort request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return abortResult{}, errors.New("abort request returned unexpected status")
	}
	buffer := make([]byte, 64<<10)
	read, err := io.ReadFull(response.Body, buffer)
	if err != nil {
		return abortResult{}, errors.New("abort request did not stream enough bytes")
	}
	if err := response.Body.Close(); err != nil {
		return abortResult{}, errors.New("close abort response failed")
	}
	return abortResult{
		Status:     "pass",
		Cycle:      cycle,
		BytesRead:  read,
		Cache:      response.Header.Get("X-N0ding-Cache"),
		HTTPStatus: response.StatusCode,
	}, nil
}

func runHold(n0dingURL string, fixtureURL string, cycle string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	var catalog clientCatalog
	if err := getJSON(client, fixtureURL+"/control/catalog", &catalog); err != nil {
		return errors.New("read fixture catalog failed")
	}
	request, err := http.NewRequest(
		http.MethodGet,
		n0dingURL+catalog.SlowBlobPath+"?restart="+url.QueryEscape(cycle),
		nil,
	)
	if err != nil {
		return errors.New("create restart request failed")
	}
	request.Header.Set("Authorization", "Bearer "+clientOCITokenA)
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("restart request transport stopped")
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	if err != nil {
		return errors.New("restart request interrupted")
	}
	return errors.New("restart request unexpectedly completed")
}

func runBurst(
	client *http.Client,
	target string,
	workers int,
	token func(index int) string,
	accept string,
) ([]requestResult, error) {
	results := make([]requestResult, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer wait.Done()
			<-start
			var bearer string
			if token != nil {
				bearer = token(index)
			}
			results[index], errs[index] = doRequest(client, target, bearer, accept)
		}(index)
	}
	close(start)
	wait.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func validateBurst(results []requestResult, workers int, expectation string, digest string) error {
	hits := 0
	misses := 0
	var bodyHash string
	for _, result := range results {
		if result.status != http.StatusOK {
			return errors.New("concurrent request returned unexpected status")
		}
		switch result.cache {
		case "HIT":
			hits++
		case "MISS":
			misses++
		default:
			return errors.New("concurrent request returned unexpected cache result")
		}
		if bodyHash == "" {
			bodyHash = result.bodyHash
		} else if result.bodyHash != bodyHash {
			return errors.New("concurrent clients received different bodies")
		}
		if digest != "" && (result.digest != digest || "sha256:"+result.bodyHash != digest) {
			return errors.New("OCI response digest validation failed")
		}
	}
	if expectation == "cold" && (misses != 1 || hits != workers-1) {
		return fmt.Errorf("cold burst hits/misses = %d/%d, want %d/1", hits, misses, workers-1)
	}
	if expectation == "warm" && (misses != 0 || hits != workers) {
		return fmt.Errorf("warm burst hits/misses = %d/%d, want %d/0", hits, misses, workers)
	}
	return nil
}

func summarize(results []requestResult) burstSummary {
	summary := burstSummary{Requests: len(results)}
	for _, result := range results {
		switch result.cache {
		case "HIT":
			summary.Hits++
		case "MISS":
			summary.Misses++
		}
		if summary.BodySHA256 == "" {
			summary.BodySHA256 = result.bodyHash
		}
	}
	return summary
}

func doRequest(
	client *http.Client,
	target string,
	bearer string,
	accept string,
) (requestResult, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return requestResult{}, errors.New("create client request failed")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := client.Do(request)
	if err != nil {
		return requestResult{}, errors.New("client request transport failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return requestResult{}, errors.New("read client response failed")
	}
	sum := sha256.Sum256(body)
	return requestResult{
		status:             response.StatusCode,
		cache:              response.Header.Get("X-N0ding-Cache"),
		bodyHash:           hex.EncodeToString(sum[:]),
		digest:             response.Header.Get("Docker-Content-Digest"),
		authenticationInfo: response.Header.Get("Authentication-Info"),
	}, nil
}

func getJSON(client *http.Client, target string, destination any) error {
	response, err := client.Get(target)
	if err != nil {
		return errors.New("JSON request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("JSON endpoint returned unexpected status")
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(destination); err != nil {
		return errors.New("decode JSON response failed")
	}
	return nil
}

func writeResult(result any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		exitError(errors.New("encode client result failed"))
	}
}

func exitError(err error) {
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	fmt.Fprintln(os.Stderr, "soak client:", message)
	os.Exit(1)
}
