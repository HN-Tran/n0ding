# n0ding

> A homelab-native package hub.

n0ding is an experimental, lightweight proxy cache for package registries. It is
config-first, Docker-friendly, and deliberately smaller than an enterprise
artifact manager.

The current spike supports **npm read-through caching**:

- standard npm registry requests under `/npm/`
- persistent metadata and tarball caching
- metadata rewriting so tarball downloads also pass through n0ding
- cache hit, object, and storage statistics
- Prometheus-compatible metrics
- generated npm setup snippets

This is a proof of concept, not production-ready supply-chain infrastructure.
Authentication, private publishing, retention cleanup, OCI, PyPI, and S3 storage
are not implemented yet.

## Quickstart

Requirements: Docker with Compose.

```sh
docker compose up --build -d
```

Open <http://localhost:8080>, then point npm at n0ding:

```sh
npm config set registry http://localhost:8080/npm/
npm view lodash version
```

Run the same registry request twice to see a cache miss followed by a hit:

```sh
curl -i http://localhost:8080/npm/is-number
curl -i http://localhost:8080/npm/is-number
```

Responses expose `X-N0ding-Cache: MISS` or `HIT`. Runtime state is available at:

- `GET /api/v1/status`
- `GET /api/v1/repositories/npm/setup`
- `GET /metrics`
- `GET /healthz`

To stop the service without deleting cached data:

```sh
docker compose down
```

## Local development

The service uses only the Go standard library.

```sh
go test ./...
go run ./cmd/n0ding -config config/n0ding.local.toml
```

Configuration uses a deliberately small TOML subset. Environment variables in
string values are expanded. See [`config/n0ding.toml`](config/n0ding.toml).

## Security posture

- Client `Authorization` headers are not forwarded upstream by default.
- Authenticated upstream responses are not shared through the cache when
  authorization forwarding is enabled.
- Cache writes are atomic and incomplete downloads are discarded.
- Only `GET` and `HEAD` are accepted by the npm proxy.

This does not make the spike production-ready. See
[`SECURITY.md`](SECURITY.md) before exposing it outside a trusted network.

## Project direction

The [decision paper](docs/decision-paper.de.md) defines the scope, go/kill
criteria, and one-to-two-week spike plan. The implementation follows the
conservative path: prove npm first, then evaluate OCI before widening scope.

Apache-2.0 licensed.
