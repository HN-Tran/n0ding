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
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	npmTokenA      = "Bearer n0ding-backup-npm-token-a-canary"
	npmTokenB      = "Bearer n0ding-backup-npm-token-b-canary"
	ociTokenA      = "Bearer n0ding-backup-oci-token-a-canary"
	ociTokenB      = "Bearer n0ding-backup-oci-token-b-canary"
	responseCanary = "n0ding-backup-response-canary"
)

var (
	tarball      = buildTarball()
	tarballSRI   = integrity(tarball)
	manifestBody = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`)
	manifestHash = digest(manifestBody)
)

func main() {
	printIntegrity := flag.Bool("print-integrity", false, "print the deterministic npm tarball integrity")
	flag.Parse()
	if *printIntegrity {
		fmt.Println(tarballSRI)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		writeBytes(writer, request, http.StatusOK, "application/json", []byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/npm/n0ding-restore-fixture", npmMetadata)
	mux.HandleFunc("/npm/n0ding-restore-fixture/-/n0ding-restore-fixture-1.0.0.tgz", npmTarball)
	mux.HandleFunc("/npm/private-package", npmPrivate)
	mux.HandleFunc("/v2/private/restore/manifests/latest", ociManifest)

	server := &http.Server{
		Addr:              ":9090",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	log.Print("backup/restore fixture listening on :9090")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func npmMetadata(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	body, err := json.Marshal(map[string]any{
		"name": "n0ding-restore-fixture",
		"dist-tags": map[string]string{
			"latest": "1.0.0",
		},
		"versions": map[string]any{
			"1.0.0": map[string]any{
				"name":    "n0ding-restore-fixture",
				"version": "1.0.0",
				"dist": map[string]string{
					"tarball":   "http://fixture:9090/npm/n0ding-restore-fixture/-/n0ding-restore-fixture-1.0.0.tgz",
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
		body = "private npm body for identity A"
	case npmTokenB:
		body = "private npm body for identity B"
	default:
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
	switch request.Header.Get("Authorization") {
	case ociTokenA, ociTokenB:
	default:
		http.Error(writer, "denied", http.StatusForbidden)
		return
	}
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writer.Header().Set("Docker-Content-Digest", manifestHash)
	writeBytes(
		writer,
		request,
		http.StatusOK,
		"application/vnd.oci.image.manifest.v1+json",
		manifestBody,
	)
}

func readMethod(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", "GET, HEAD")
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	return false
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
			body: `{"name":"n0ding-restore-fixture","version":"1.0.0","main":"index.js"}` + "\n",
		},
		{
			name: "package/index.js",
			body: `module.exports = "n0ding restore fixture";` + "\n",
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

func integrity(body []byte) string {
	sum := sha512.Sum512(body)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
