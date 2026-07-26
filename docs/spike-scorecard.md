# npm + OCI baseline scorecard

Assessment date: 2026-07-25
Target: narrow, read-only npm + OCI pull-through cache

This is retained as historical technical evidence. The public-preview decision
was superseded on 2026-07-26 by the
[v0.1-private roadmap](release-checklist.md).

## Scope guard

| Capability | MVP decision | Result |
|---|---|---|
| npm proxy/cache | Included | Pass |
| OCI pull-through cache | Included | Pass |
| Local filesystem storage | Included | Pass |
| Age-based retention and stale-temp cleanup | Included | Pass |
| JSON status, health, and metrics | Existing operational surface retained | Pass |
| UI work | No new work | Pass |
| Client auth, users, or RBAC | Excluded | Pass |
| Private publish | Excluded | Pass |
| PyPI or other ecosystems | Excluded | Pass |
| Database or new Go dependencies | Excluded | Pass |

## Readiness gates

| Gate | Evidence | Result |
|---|---|---|
| One binary / one config | Go binary, one TOML file, one `/data` volume | Pass |
| Startup temp cleanup | Old `.body-*` and `.metadata-*` files removed; recent files retained | Pass |
| Retention implementation | Configurable `max_age`; startup and periodic GC | Pass |
| Safe GC deletion | Only valid, size-matched `.json`/`.body` pairs are removed | Pass |
| Active writes protected | Runtime GC ignores every temp file; final commits and GC are store-synchronized | Pass |
| npm same-key concurrency | 8 simultaneous requests, 1 upstream body fetch, 1 complete object | Pass |
| OCI same-key concurrency | 8 simultaneous requests, 1 upstream body fetch, 7 upstream-validated cache hits | Pass |
| Duplicate upstream policy | OCI per-client `HEAD`, different `Accept` keys, and cross-process fetches documented | Pass |
| Compose quickstart | Build, start, health, stop, and volume-preservation guidance | Pass |
| Config reference | Every supported key, default, and TTL/retention distinction documented | Pass |
| TLS guidance | Caddy/nginx examples, public URL rule, private-CA and insecure-registry boundary | Pass |
| Backup/restore | Quiesced backup and restore-to-new-volume workflow documented | Pass |
| Known limitations | Offline, auth, publish, Podman, Range, quota, and single-process limits documented | Pass |
| Podman compatibility | Client unavailable on the test host | Unknown, non-blocking |
| Live restore drill | Procedure documented but not executed in this hardening task | Unknown, pre-stable follow-up |

## Automated and build validation

```powershell
go test -count=1 ./...
go vet ./...
go build -trimpath -ldflags="-s -w -X main.version=v0.1-private" `
  -o dist\n0ding.exe ./cmd/n0ding
.\dist\n0ding.exe -config config\n0ding.local.toml -check-config
.\dist\n0ding.exe -config config\n0ding.example.toml -check-config
docker build --build-arg VERSION=v0.1-private --tag n0ding:v0.1-private-check .
docker compose config --quiet
```

| Check | Result |
|---|---|
| All Go tests | Pass |
| `go vet ./...` | Pass |
| Windows private-hardening binary | Pass, 9,151,488 bytes |
| Container, local, and example config validation | Pass |
| Container build, including tests | Pass |
| Compose config validation | Pass |
| Isolated Compose start, health, and status | Pass, 2 repositories |
| Third-party Go dependencies | None |

Private-hardening validation used Go 1.24.13, Docker Engine 29.5.3, and Docker
Compose 5.1.4 through 2026-07-26. The container config was validated with
`N0DING_PUBLIC_URL=http://localhost:8080`, matching the Compose default.

## Real npm revalidation

Client: Node.js 24.18.0, npm 11.16.0.

The committed `testdata/npm-compat` lockfile was installed twice through
n0ding. Each run used a separate project directory and empty npm client cache;
n0ding was restarted between runs.

| Measurement | First run | Second run after restart |
|---|---:|---:|
| `npm view @types/node@22.10.0` | `22.10.0` | `22.10.0` |
| Packages installed by `npm ci` | 3 | 3 |
| Cache hits | 0 | 5 |
| Cache misses | 5 | 0 |
| Errors | 0 | 0 |
| Complete objects | 5 | 5 |
| Storage bytes | 13,956,353 | 13,956,353 |

Result: scoped metadata, lockfile resolution, SHA-512 tarball integrity, local
cache persistence, and startup maintenance coexist correctly.

## Real OCI revalidation

Client: Docker Engine 29.6.2 in two separate `docker:29-dind` containers.
Platform: `linux/amd64`.

| Image | Repo digest |
|---|---|
| `alpine:3.20` | `sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc` |
| `nginx:1.27-alpine` | `sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10` |
| `busybox:1.36` | `sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662` |

All images were pulled through an empty n0ding cache. Both n0ding and the
Docker daemon were then replaced while the n0ding volume was retained, and all
three pulls were repeated.

| Measurement | First clean daemon | Second clean daemon after n0ding restart |
|---|---:|---:|
| Matching repo digests | 3/3 | 3/3 |
| Cumulative hits | 0 | 30 |
| Cumulative misses | 39 | 9 |
| Errors | 0 | 0 |
| Range requests | 0 | 0 |
| Complete objects | 30 | 30 |
| Storage bytes | 27,820,024 | 27,820,024 |

The miss counter now includes cacheable unauthenticated and `HEAD` request
variants. Object and byte counts show that the second run did not add body
objects.

## Accepted MVP limitations

- n0ding is not an offline mirror; OCI hits require an upstream
  authorization/digest `HEAD`.
- There is no private publish support.
- There is no n0ding client auth, user management, or RBAC beyond forwarding
  the upstream auth required for OCI pulls.
- Podman is untested.
- Range requests are proxied but partial responses are not cached.
- Retention is maximum age from commit, not LRU or a strict maximum size.
- A cache directory supports one n0ding process; there is no distributed lock.
- Restore instructions have not yet completed a separate disaster-recovery
  drill.

## Baseline verdict

**Approved as the npm + OCI baseline for private hardening.**

The read-only npm + OCI core now has real-client compatibility, persisted cache
reuse, digest/integrity validation, startup cleanup, bounded age-based
retention, same-key request coalescing, and sufficient operating
documentation. Release packaging now includes an explicit preview README,
changelog, example configuration, architecture and troubleshooting documents,
Apache-2.0 licensing, contribution and security policies, and CI for Go and
container gates. No required technical MVP gate failed.

This is not a public release or a stable production supply-chain service. The
private roadmap now adds cache/credential safety, real private upstreams,
recovery and soak evidence, client/TLS compatibility, a threat model, and a
gated PyPI adapter. Publishing, UI work, users, and RBAC remain outside the
current scope.
