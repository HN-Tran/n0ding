# n0ding: lightweight artifact caching for npm, PyPI, and OCI

[![CI](https://github.com/HN-Tran/n0ding/actions/workflows/ci.yml/badge.svg)](https://github.com/HN-Tran/n0ding/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Status](https://img.shields.io/badge/status-private%20alpha-orange)

**n0ding is an open-source, self-hosted pull-through cache that speeds up and
centralizes npm, pip/uv, and Docker/OCI downloads behind one small service.**

It is aimed at homelabs, CI runners, developer workstations, and small
technical teams that need artifact caching without operating the full feature
set of repository managers such as Sonatype Nexus Repository or JFrog
Artifactory. n0ding is deliberately read-only: it caches downloads but does not
currently provide package publishing, users, or RBAC.

```text
npm / pip / uv / Docker
           |
           v
        n0ding  ---- persistent local cache
           |
           v
 npmjs.org / PyPI / Docker Hub
```

| Ecosystem | Client path | Cached content |
|---|---|---|
| npm | `/npm/` | Package metadata and tarballs |
| PyPI | `/pypi/simple/` | Simple API pages, metadata sidecars, wheels and source archives |
| Docker / OCI | `/v2/` | Indexes, manifests, configs and blobs |

> [!WARNING]
> **v0.1 is a public preview for trusted-network evaluation.** It has not
> completed the security and long-running reliability work required for a
> production supply-chain service. Do not expose it directly to the internet.

## Why n0ding

- One Go binary with one TOML configuration file.
- One endpoint for three common package ecosystems.
- A persistent local filesystem cache for npm metadata/tarballs, OCI manifests,
  indexes, configs, blobs, and PyPI Simple API pages/distribution files.
- Compatible with standard npm, Docker/OCI, pip, and uv pull/install clients;
  no client plugin is required.
- Config-first, Docker Compose-friendly, observable through health, JSON status,
  and Prometheus-compatible metrics endpoints.
- Small enough for a homelab, explicit enough for repeatable CI and private
  team deployments.
- Deliberately narrow today: read-only package loading is the implemented
  protocol scope.

## What n0ding is not

- Not an offline mirror: OCI cache hits still require an upstream
  authorization/digest check.
- Not a private registry and not a publishing destination.
- Not an authentication, user-management, RBAC, scanning, signing, or policy
  system.
- Not a replacement for a production-grade artifact manager.
- Not a Maven, NuGet, Helm, or general artifact cache.

## Install in one minute

Requirements: Docker Engine or Docker Desktop with Compose v2.

Linux and macOS:

```sh
curl -fsSLO https://github.com/HN-Tran/n0ding/releases/latest/download/install.sh
sh install.sh
```

Windows PowerShell:

```powershell
Invoke-WebRequest https://github.com/HN-Tran/n0ding/releases/latest/download/install.ps1 -OutFile install.ps1
& .\install.ps1
```

The installer downloads checksum-verified deployment files, pulls the pinned
multi-architecture image from GHCR, binds n0ding to `127.0.0.1:8080`, and
waits for the health check. It does not change npm, pip, uv, or Docker client
settings.

Verify the service:

```sh
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
```

See the [deployment guide](docs/deployment.md) for version pinning, shared
deployment behind TLS, upgrades, logs, backups, and uninstalling.

## Build from source

From the repository checkout:

```sh
docker compose config --quiet
docker compose up --build -d
docker compose ps
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
```

PowerShell uses the same Docker commands and `curl.exe`:

```powershell
docker compose config --quiet
docker compose up --build -d
docker compose ps
curl.exe http://localhost:8080/healthz
curl.exe http://localhost:8080/api/v1/status
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
the local machine. For repeated private-alpha self-use, work through the
[private self-use checklist](docs/private-self-use.md).

After startup, point one or more standard clients at n0ding:

```sh
npm config set registry http://localhost:8080/npm/
python -m pip config set global.index-url http://localhost:8080/pypi/simple/
docker pull localhost:8080/library/alpine:3.20
```

The Docker command requires the local insecure-registry exception described
below. npm and pip/uv work immediately over loopback HTTP. On repeated requests,
responses expose `X-N0ding-Cache: HIT`, and `/api/v1/status` reports cache hits,
misses, stored objects, bytes, errors, and client cancellations.

<a id="client-setup"></a>
## Client setup

Choose only the clients you want to route through n0ding.

### npm

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

### OCI / Docker

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

### PyPI / pip and uv

Point pip or uv at the Simple API endpoint:

```sh
python -m pip install --index-url http://localhost:8080/pypi/simple/ requests
uv pip install --index-url http://localhost:8080/pypi/simple/ requests
```

The adapter rewrites Simple API HTML and JSON distribution links through
`/pypi/files/` only when the file origin is explicitly allowed by config.
Public PyPI needs `https://files.pythonhosted.org` in
`allowed_file_origins`, which is included in the example Compose config. When
a Simple API link includes a SHA-256 fragment, n0ding verifies it before
committing the distribution file to cache.

## Configuration

The container uses [`config/n0ding.toml`](config/n0ding.toml). A commented,
copyable starting point is
[`config/n0ding.example.toml`](config/n0ding.example.toml). Validate a config
without starting the listener:

```sh
n0ding -config /etc/n0ding/n0ding.toml -check-config
```

The complete field and retention semantics are documented in the
[configuration reference](docs/configuration.md). A private-upstream
compatibility run must follow the
[disposable manual drill](docs/private-upstream-drill.md); do not put provider
credentials into a committed config.

## Operational endpoints

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | Process health |
| `GET /api/v1/status` | Version and repository/cache counters |
| `GET /api/v1/repositories/npm/setup` | npm setup snippet |
| `GET /api/v1/repositories/oci/setup` | Docker pull snippet |
| `GET /api/v1/repositories/pypi/setup` | pip and uv setup snippet |
| `GET /metrics` | Prometheus-compatible counters |

## Known limitations

- n0ding is not an offline mirror.
- There is no private publish support.
- There is no n0ding client authentication, user management, or RBAC; only the
  existing upstream credential handling is present.
- Real private-upstream workflows and credential revocation are not yet
  validated.
- PyPI support is read-only and caches only allowed file origins; publishing is
  not implemented.
- Podman has not yet been tested.
- Range requests are proxied, but partial responses are not cached.
- Retention is based on maximum object age, not LRU or a strict byte quota;
  private-alpha use requires filesystem capacity monitoring.
- Only one n0ding process may write to a cache directory.
- Only local filesystem storage is supported.

See [troubleshooting](docs/troubleshooting.md) and the
[real-client compatibility evidence](docs/compatibility.md) for operational
details. The [retention decision](docs/retention-policy.md) records the
disk-full failure mode and operator guardrails.

## Development

n0ding uses the Go standard library plus `golang.org/x/net/html` for PyPI
Simple API HTML rewriting.

```sh
go test ./...
go vet ./...
go build -trimpath -o dist/n0ding ./cmd/n0ding
go run ./cmd/n0ding -config config/n0ding.local.toml
```

If Go is not installed locally, use the disposable Docker toolchain targets:

```sh
make docker-test
make docker-check
make docker-shell
```

These targets use `golang:1.25`, mount the checkout at `/src`, and keep Go
build/module/temp caches under `.tmp/`, which is ignored by Git.

The [architecture](docs/architecture.md), [baseline
scorecard](docs/spike-scorecard.md), [threat model](docs/threat-model.md), and
[v0.1-private roadmap](docs/release-checklist.md) describe the current
boundaries and evidence. The [PyPI design](docs/pypi-design.md) records the
adapter decisions and remaining real-client evidence.

## Security and license

Read [SECURITY.md](SECURITY.md) before deployment or disclosure. n0ding is
licensed under [Apache-2.0](LICENSE).
