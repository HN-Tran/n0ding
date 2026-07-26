package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	npmTokenA      = "Bearer n0ding-soak-npm-token-a-canary"
	npmTokenB      = "Bearer n0ding-soak-npm-token-b-canary"
	ociTokenA      = "Bearer n0ding-soak-oci-token-a-canary"
	ociTokenB      = "Bearer n0ding-soak-oci-token-b-canary"
	responseCanary = "n0ding-soak-response-canary"
)

var (
	tarball        = buildTarball()
	tarballSRI     = integrity(tarball)
	normalBlob     = repeatedBody("n0ding-soak-normal-blob\n", 256<<10)
	normalBlobHash = digest(normalBlob)
	slowBlob       = repeatedBody("n0ding-soak-slow-blob\n", 32<<20)
	slowBlobHash   = digest(slowBlob)
	manifestBody   = buildManifest()
	manifestHash   = digest(manifestBody)
	counters       fixtureCounters
)

type fixtureCounters struct {
	npmMetadataGET  atomic.Uint64
	npmTarballGET   atomic.Uint64
	npmPrivateA     atomic.Uint64
	npmPrivateB     atomic.Uint64
	npmDenied       atomic.Uint64
	ociManifestGET  atomic.Uint64
	ociManifestHEAD atomic.Uint64
	ociBlobGET      atomic.Uint64
	ociBlobHEAD     atomic.Uint64
	slowBlobGET     atomic.Uint64
	slowBlobHEAD    atomic.Uint64
	slowActive      atomic.Int64
	slowAborted     atomic.Uint64
}

type statsResponse struct {
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

type catalogResponse struct {
	ManifestPath   string `json:"manifest_path"`
	ManifestDigest string `json:"manifest_digest"`
	BlobPath       string `json:"blob_path"`
	BlobDigest     string `json:"blob_digest"`
	SlowBlobPath   string `json:"slow_blob_path"`
	SlowBlobDigest string `json:"slow_blob_digest"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/control/stats", stats)
	mux.HandleFunc("/control/catalog", catalog)
	mux.HandleFunc("/npm/n0ding-soak-fixture", npmMetadata)
	mux.HandleFunc("/npm/n0ding-soak-fixture/-/n0ding-soak-fixture-1.0.0.tgz", npmTarball)
	mux.HandleFunc("/npm/private-package", npmPrivate)
	mux.HandleFunc("/v2/private/soak/manifests/latest", ociManifest)
	mux.HandleFunc("/v2/private/soak/blobs/", ociBlob)

	server := &http.Server{
		Addr:              ":9090",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	log.Print("retention soak fixture listening on :9090")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func health(writer http.ResponseWriter, request *http.Request) {
	writeBytes(writer, request, http.StatusOK, "application/json", []byte(`{"status":"ok"}`))
}

func stats(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, statsResponse{
		NPMMetadataGET:  counters.npmMetadataGET.Load(),
		NPMTarballGET:   counters.npmTarballGET.Load(),
		NPMPrivateA:     counters.npmPrivateA.Load(),
		NPMPrivateB:     counters.npmPrivateB.Load(),
		NPMDenied:       counters.npmDenied.Load(),
		OCIManifestGET:  counters.ociManifestGET.Load(),
		OCIManifestHEAD: counters.ociManifestHEAD.Load(),
		OCIBlobGET:      counters.ociBlobGET.Load(),
		OCIBlobHEAD:     counters.ociBlobHEAD.Load(),
		SlowBlobGET:     counters.slowBlobGET.Load(),
		SlowBlobHEAD:    counters.slowBlobHEAD.Load(),
		SlowActive:      counters.slowActive.Load(),
		SlowAborted:     counters.slowAborted.Load(),
	})
}

func catalog(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer)
		return
	}
	writeJSON(writer, catalogResponse{
		ManifestPath:   "/v2/private/soak/manifests/latest",
		ManifestDigest: manifestHash,
		BlobPath:       "/v2/private/soak/blobs/" + normalBlobHash,
		BlobDigest:     normalBlobHash,
		SlowBlobPath:   "/v2/private/soak/blobs/" + slowBlobHash,
		SlowBlobDigest: slowBlobHash,
	})
}

func npmMetadata(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if request.Method == http.MethodGet {
		counters.npmMetadataGET.Add(1)
	}
	body, err := json.Marshal(map[string]any{
		"name": "n0ding-soak-fixture",
		"dist-tags": map[string]string{
			"latest": "1.0.0",
		},
		"versions": map[string]any{
			"1.0.0": map[string]any{
				"name":    "n0ding-soak-fixture",
				"version": "1.0.0",
				"dist": map[string]string{
					"tarball":   "http://fixture:9090/npm/n0ding-soak-fixture/-/n0ding-soak-fixture-1.0.0.tgz",
					"integrity": tarballSRI,
				},
			},
		},
	})
	if err != nil {
		http.Error(writer, "fixture metadata failure", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writeBytes(writer, request, http.StatusOK, "application/json", body)
}

func npmTarball(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if request.Method == http.MethodGet {
		counters.npmTarballGET.Add(1)
	}
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writeBytes(writer, request, http.StatusOK, "application/octet-stream", tarball)
}

func npmPrivate(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	var body string
	switch request.Header.Get("Authorization") {
	case npmTokenA:
		counters.npmPrivateA.Add(1)
		body = "private npm body for soak identity A"
	case npmTokenB:
		counters.npmPrivateB.Add(1)
		body = "private npm body for soak identity B"
	default:
		counters.npmDenied.Add(1)
		http.Error(writer, "denied", http.StatusForbidden)
		return
	}
	writer.Header().Set("Authentication-Info", `nextnonce="`+responseCanary+`"`)
	writeBytes(writer, request, http.StatusOK, "application/octet-stream", []byte(body))
}

func ociManifest(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if !authorizedOCI(request) {
		http.Error(writer, "denied", http.StatusForbidden)
		return
	}
	if request.Method == http.MethodHead {
		counters.ociManifestHEAD.Add(1)
	} else {
		counters.ociManifestGET.Add(1)
	}
	writeOCI(writer, request, "application/vnd.oci.image.manifest.v1+json", manifestHash, manifestBody)
}

func ociBlob(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if !authorizedOCI(request) {
		http.Error(writer, "denied", http.StatusForbidden)
		return
	}
	requestedDigest := strings.TrimPrefix(request.URL.Path, "/v2/private/soak/blobs/")
	switch requestedDigest {
	case normalBlobHash:
		if request.Method == http.MethodHead {
			counters.ociBlobHEAD.Add(1)
		} else {
			counters.ociBlobGET.Add(1)
		}
		writeOCI(writer, request, "application/octet-stream", normalBlobHash, normalBlob)
	case slowBlobHash:
		if request.Method == http.MethodHead {
			counters.slowBlobHEAD.Add(1)
			writeOCI(writer, request, "application/octet-stream", slowBlobHash, slowBlob)
			return
		}
		counters.slowBlobGET.Add(1)
		writeSlowBlob(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func authorizedOCI(request *http.Request) bool {
	switch request.Header.Get("Authorization") {
	case ociTokenA, ociTokenB:
		return true
	default:
		return false
	}
}

func writeOCI(
	writer http.ResponseWriter,
	request *http.Request,
	contentType string,
	contentDigest string,
	body []byte,
) {
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writer.Header().Set("Docker-Content-Digest", contentDigest)
	writeBytes(writer, request, http.StatusOK, contentType, body)
}

func writeSlowBlob(writer http.ResponseWriter, request *http.Request) {
	counters.slowActive.Add(1)
	defer counters.slowActive.Add(-1)

	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Length", fmt.Sprint(len(slowBlob)))
	writer.Header().Set("Docker-Content-Digest", slowBlobHash)
	writer.WriteHeader(http.StatusOK)

	flusher, _ := writer.(http.Flusher)
	const chunkSize = 32 << 10
	for offset := 0; offset < len(slowBlob); offset += chunkSize {
		end := offset + chunkSize
		if end > len(slowBlob) {
			end = len(slowBlob)
		}
		if _, err := writer.Write(slowBlob[offset:end]); err != nil {
			counters.slowAborted.Add(1)
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func readMethod(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	methodNotAllowed(writer)
	return false
}

func methodNotAllowed(writer http.ResponseWriter) {
	writer.Header().Set("Allow", "GET, HEAD")
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}

func writeBytes(
	writer http.ResponseWriter,
	request *http.Request,
	status int,
	contentType string,
	body []byte,
) {
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
	writer.WriteHeader(status)
	if request.Method != http.MethodHead {
		_, _ = writer.Write(body)
	}
}

func writeJSON(writer http.ResponseWriter, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "fixture JSON failure", http.StatusInternalServerError)
		return
	}
	writeBytes(writer, &http.Request{Method: http.MethodGet}, http.StatusOK, "application/json", body)
}

func buildManifest() []byte {
	body, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    digest([]byte("{}")),
			"size":      2,
		},
		"layers": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.layer.v1.tar",
				"digest":    normalBlobHash,
				"size":      len(normalBlob),
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func buildTarball() []byte {
	var compressed bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		panic(err)
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	files := []struct {
		name string
		body string
	}{
		{
			name: "package/package.json",
			body: `{"name":"n0ding-soak-fixture","version":"1.0.0","main":"index.js"}` + "\n",
		},
		{
			name: "package/index.js",
			body: `module.exports = "n0ding soak fixture";` + "\n",
		},
	}
	for _, file := range files {
		header := &tar.Header{
			Name:    file.name,
			Mode:    0o644,
			Size:    int64(len(file.body)),
			ModTime: time.Unix(0, 0).UTC(),
			Format:  tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			panic(err)
		}
		if _, err := io.WriteString(tarWriter, file.body); err != nil {
			panic(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		panic(err)
	}
	if err := gzipWriter.Close(); err != nil {
		panic(err)
	}
	return compressed.Bytes()
}

func repeatedBody(pattern string, size int) []byte {
	result := make([]byte, size)
	source := []byte(pattern)
	for offset := 0; offset < len(result); {
		offset += copy(result[offset:], source)
	}
	return result
}

func integrity(body []byte) string {
	sum := sha512.Sum512(body)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
