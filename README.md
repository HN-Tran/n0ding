# n0ding

> n0ding is a small, self-hosted, read-only pull-through cache for npm and OCI
> artifacts, built for homelabs and small technical teams.

> [!WARNING]
> **v0.1.0 is a preview release.** It is intended for evaluation on trusted
> networks. It has not completed the security, recovery, or long-running
> reliability work required for a production supply-chain service.

## What n0ding is

- One Go binary with one TOML configuration file.
- A persistent local filesystem cache for npm metadata/tarballs and OCI
  manifests, indexes, configs, and blobs.
- Compatible with standard npm and Docker/OCI pull clients; no client plugin is
  required.
- Config-first, Docker Compose-friendly, observable through health, JSON status,
  and Prometheus-compatible metrics endpoints.
- Deliberately narrow: read-only npm and OCI are the complete v0.1.0 protocol
  scope.

## What n0ding is not

- Not an offline mirror: OCI cache hits still require an upstream
  authorization/digest check.
- Not a private registry and not a publishing destination.
- Not an authentication, user-management, RBAC, scanning, signing, or policy
  system.
- Not a replacement for a production-grade artifact manager.
- Not a supported PyPI, Maven, NuGet, Helm, or general artifact cache.

## Quickstart with Docker Compose

Requirements: Docker Engine or Docker Desktop with Compose.

From the repository checkout:

```sh
docker compose config --quiet
docker compose up --build -d
docker compose ps
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
```

The service listens on `http://localhost:8080` and stores its cache in the
named `n0ding-data` volume. A normal `docker compose down` preserves that
volume. Do not add `--volumes` unless you intentionally want to delete the
cache.

For a hostname or TLS reverse proxy, set `N0DING_PUBLIC_URL` before starting
Compose. It must be the exact URL clients use:

```sh
N0DING_PUBLIC_URL=https://packages.example.com docker compose up --build -d
```

PowerShell:

```powershell
$env:N0DING_PUBLIC_URL = "https://packages.example.com"
docker compose up --build -d
```

See the [operations guide](docs/operations.md) before exposing n0ding outside
the local machine.

## npm client setup

For a project-local setup, create `.npmrc` next to `package.json`:

```ini
registry=http://localhost:8080/npm/
```

Or set the current user's npm registry:

```sh
npm config set registry http://localhost:8080/npm/
```

Then use npm normally:

```sh
npm view @types/node version
npm ci
```

To verify n0ding rather than npm's own client cache, use two empty cache
directories:

```sh
npm view @types/node version --registry http://localhost:8080/npm/ \
  --cache .tmp/npm-cache-1 --prefer-online
npm view @types/node version --registry http://localhost:8080/npm/ \
  --cache .tmp/npm-cache-2 --prefer-online
```

n0ding rewrites npm tarball URLs to its configured `public_base_url`, so that
value must be client-reachable. Responses expose `X-N0ding-Cache: MISS` or
`HIT`.

## OCI / Docker client setup

Docker expects registries to use trusted TLS. For local evaluation only, add
`localhost:8080` to the Docker daemon's `insecure-registries` list and restart
the daemon. On Docker Desktop this setting is under **Settings → Docker
Engine**:

```json
{
  "insecure-registries": ["localhost:8080"]
}
```

Pull a Docker Hub image through n0ding:

```sh
docker pull localhost:8080/library/alpine:3.20
```

Official Docker Hub images retain the `library/` namespace. For shared
deployments, use a trusted TLS reverse proxy instead of an insecure-registry
exception. Detailed Caddy, nginx, and private-CA guidance is in
[docs/operations.md](docs/operations.md).

## Configuration

The container uses [`config/n0ding.toml`](config/n0ding.toml). A commented,
copyable starting point is
[`config/n0ding.example.toml`](config/n0ding.example.toml). Validate a config
without starting the listener:

```sh
n0ding -config /etc/n0ding/n0ding.toml -check-config
```

The complete field and retention semantics are documented in the
[configuration reference](docs/configuration.md).

## Operational endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Process health |
| `GET /api/v1/status` | Version and repository/cache counters |
| `GET /api/v1/repositories/npm/setup` | npm setup snippet |
| `GET /api/v1/repositories/oci/setup` | Docker pull snippet |
| `GET /metrics` | Prometheus-compatible counters |

## Known limitations

- n0ding is not an offline mirror.
- There is no private publish support.
- There is no n0ding client authentication, user management, or RBAC; only the
  existing upstream credential handling is present.
- Podman has not yet been tested.
- Range requests are proxied, but partial responses are not cached.
- Retention is based on maximum object age, not LRU or a strict byte quota.
- Only one n0ding process may write to a cache directory.
- Only local filesystem storage is supported.

See [troubleshooting](docs/troubleshooting.md) and the
[real-client compatibility evidence](docs/compatibility.md) for operational
details.

## Development

n0ding uses the Go standard library only.

```sh
go test ./...
go vet ./...
go build -trimpath -o dist/n0ding ./cmd/n0ding
go run ./cmd/n0ding -config config/n0ding.local.toml
```

The [architecture](docs/architecture.md), [MVP readiness
scorecard](docs/spike-scorecard.md), and [release
checklist](docs/release-checklist.md) describe the current boundaries and
evidence.

## Security and license

Read [SECURITY.md](SECURITY.md) before deployment or disclosure. n0ding is
licensed under [Apache-2.0](LICENSE).
