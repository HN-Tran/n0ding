# n0ding

> A homelab-native package hub.

n0ding is a lightweight, read-only pull-through cache for package registries.
It is config-first, Docker-friendly, and deliberately smaller than an
enterprise artifact manager.

The narrow MVP supports **npm and OCI read-through caching**:

- standard npm registry requests under `/npm/`
- persistent metadata and tarball caching
- metadata rewriting so tarball downloads also pass through n0ding
- standard OCI Distribution pull requests under `/v2/`
- local manifest, image-index, config, and layer caching
- SHA-256 verification before OCI objects are committed to the cache
- upstream authorization checks before authenticated OCI cache hits
- cache hit, object, and storage statistics
- startup cleanup for stale temporary files
- periodic age-based retention of complete cache objects
- in-process coalescing of concurrent requests for the same object
- Prometheus-compatible metrics
- generated npm and Docker setup snippets

This is not a private package registry or a production supply-chain security
platform. Client authentication, private publishing, PyPI, RBAC, and S3 storage
are deliberately outside the MVP.

## Quickstart

Requirements: Docker with Compose. For local-only evaluation:

```sh
docker compose up --build -d
docker compose ps
curl http://localhost:8080/healthz
```

Point npm at n0ding:

```sh
npm config set registry http://localhost:8080/npm/
npm view @types/node version
```

Run the request twice with separate npm client caches to distinguish the
n0ding cache from npm's local cache:

```sh
npm view @types/node version --registry http://localhost:8080/npm/ \
  --cache .tmp/npm-cache-1 --prefer-online
npm view @types/node version --registry http://localhost:8080/npm/ \
  --cache .tmp/npm-cache-2 --prefer-online
```

Responses expose `X-N0ding-Cache: MISS` or `HIT`. Runtime state is available at:

- `GET /api/v1/status`
- `GET /api/v1/repositories/npm/setup`
- `GET /metrics`
- `GET /healthz`

For a Docker Engine configured to allow the local HTTP registry, pull through
the OCI adapter with:

```sh
docker pull localhost:8080/library/alpine:3.20
```

Docker treats non-TLS registries as insecure. Keep that exception local to a
development daemon and use TLS for every shared deployment. See the
[operations guide](docs/operations.md) for reverse-proxy, backup, and restore
guidance. Reproducible real-client results are in the
[compatibility matrix](docs/compatibility.md).

To stop the service without deleting cached data:

```sh
docker compose down
```

Do not add `--volumes` unless you intentionally want to delete the cache.

## Local development

The service uses only the Go standard library.

```sh
go test ./...
go run ./cmd/n0ding -config config/n0ding.local.toml
```

Configuration uses a deliberately small TOML subset. Environment variables in
string values are expanded. See the
[configuration reference](docs/configuration.md) and
[`config/n0ding.toml`](config/n0ding.toml).

## Security posture

- Client `Authorization` headers are not forwarded upstream by default.
- Authenticated upstream responses are not shared through the cache when
  authorization forwarding is enabled.
- Cache writes are atomic and incomplete downloads are discarded.
- Age-based GC deletes only complete body/metadata pairs and never temporary
  writes.
- Only `GET` and `HEAD` are accepted by both adapters.

This does not make the spike production-ready. See
[`SECURITY.md`](SECURITY.md) before exposing it outside a trusted network.

## MVP scope

The [MVP readiness scorecard](docs/spike-scorecard.md) records the release gate.
The current scope remains read-only npm plus OCI. PyPI, publishing, user
management, RBAC, and additional registry ecosystems must wait.

Apache-2.0 licensed.
