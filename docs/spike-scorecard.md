# Spike scorecard

Date opened: 2026-07-24  
Decision date: 2026-07-24

## Go criteria

| Criterion | Target | Current evidence | Result |
|---|---|---|---|
| Standard client proxy is stable | npm or OCI | npm 11.16 scoped/lockfile installs pass; Docker Engine 29.6.2 pulled three images before and after restart with matching digests | Pass |
| Local setup time | Under 10 minutes | One `docker compose up --build -d` command, but no formal clean-host timing | Unknown |
| Two ecosystems | One configuration | npm at `/npm/` and OCI at `/v2/` use one binary, config, storage abstraction, and status API | Pass for spike |
| Cache/storage visibility | Visible without filesystem access | Dashboard, JSON status, Prometheus metrics | Pass for spike |
| Private publish feasibility | One ecosystem | Deliberately not analyzed in this pull-only spike | Unknown / deferred |
| Lighter than Nexus | Qualitative operator test | Two ecosystems remain one process, one config, one volume, no database | Pass for spike; revisit after auth/retention |

## Kill signals

- Standard npm clients require undocumented workarounds.
- Metadata rewriting breaks lockfiles, integrity checks, or scoped packages.
- Cache correctness requires package-specific exceptions.
- The OCI experiment forces a second incompatible operating model.
- Private publishing cannot fit behind the same simple configuration and auth
  boundary.
- Setup or recovery becomes more complex than the tools n0ding is meant to
  replace.

## Week 1 work

- [x] Repository structure
- [x] Go runtime decision
- [x] Persistent HTTP cache
- [x] npm metadata and tarball proxy behavior
- [x] TOML configuration
- [x] Minimal status UI and metrics
- [x] Docker Compose quickstart
- [x] Real npm CLI end-to-end test
- [x] Scoped package and lockfile tests

## Week 2 gate

- [ ] Measure clean setup time
- [x] Stabilize tested npm scoped-package, lockfile, and integrity paths
- [x] Analyze OCI `/v2/` pull/auth flow
- [x] Implement and test OCI manifest/blob pull-through caching
- [x] Verify a second pull with a clean Docker daemon after n0ding restart
- [ ] Add lazy retention cleanup or document the required storage redesign
- [x] Test interrupted/range behavior with Docker
- [ ] Test Podman (client unavailable on the test host)
- [ ] Assess private npm publish path (deferred; no publish work in this spike)
- [x] Record go, cut, or kill decision

## Compatibility-hardening gate

| Check | Evidence | Result |
|---|---|---|
| npm scoped metadata | `npm view @types/node@22.10.0` through n0ding returned `22.10.0` | Pass |
| npm lockfile install | Two clean-client `npm ci` runs installed 3 packages; lockfile SHA-256 stayed unchanged | Pass |
| npm tarball integrity | npm 11.16.0 accepted all three lockfile SHA-512 integrity values | Pass |
| npm persisted cache | First run 0 hits/5 misses; second run after n0ding restart 5 hits/0 misses | Pass |
| OCI image breadth | `alpine:3.20`, `nginx:1.27-alpine`, and multi-arch `busybox:1.36` pulled | Pass |
| OCI restart persistence | Both n0ding and Docker daemon were replaced; 30 objects and 27,820,024 bytes persisted | Pass |
| OCI digest integrity | All three repo digests matched before/after restart; normal pulls had zero errors | Pass |
| OCI object coverage | 3 indexes, 6 manifests, 6 referenced configs, and 15 referenced layers were present | Pass |
| Normal Docker Range use | Six normal pulls recorded 0 Range requests | Pass: behavior determined |
| Interrupted Docker resume | Controlled short read retried successfully; retry recorded 0 Range requests | Pass: behavior determined |
| Explicit Range handling | Automated upstream test verifies forwarding `Range`, `206`, and `Content-Range` without partial caching | Pass |
| Podman pull | `podman version` was not available | Unknown |
| Crash-temp cleanup | Forced kill left 7 `.body-*` files | Fail / known limitation |

## Final verification

```powershell
go test ./...
go vet ./...
go build -trimpath -o dist\n0ding.exe ./cmd/n0ding
.\dist\n0ding.exe -config config\n0ding.local.toml -check-config
docker build --tag n0ding:compat-spike .
docker compose config --quiet
```

| Check | Result |
|---|---|
| All Go tests | Pass |
| `go vet ./...` | Pass |
| Windows binary build | Pass, 13,170,176 bytes |
| Config validation | Pass |
| Container build, including tests | Pass |
| Compose config validation | Pass |
| New dependencies | None |
| UI/auth/publish/RBAC/new ecosystem changes | None |

## OCI gate evidence

Exact core commands:

```powershell
docker exec n0ding-oci-spike-dind docker pull --platform linux/amd64 `
  n0ding:8080/library/alpine:3.20
docker exec n0ding-oci-spike-dind docker image inspect `
  n0ding:8080/library/alpine:3.20 --format '{{index .RepoDigests 0}}'
docker rm --force n0ding-oci-spike-dind
docker restart n0ding-oci-spike-server
docker run --detach --privileged --name n0ding-oci-spike-dind `
  --network n0ding-oci-spike docker:29-dind `
  --insecure-registry=n0ding:8080
docker exec n0ding-oci-spike-dind docker pull --platform linux/amd64 `
  n0ding:8080/library/alpine:3.20
```

Measured hardening results:

| Measurement | First clean client | Second clean client after both restarts |
|---|---:|---:|
| Images with matching repo digest | 3/3 | 3/3 |
| Cumulative cache hits | 0 | 30 |
| Cumulative cache misses | 33 | 3 |
| Normal-pull digest/cache errors | 0 | 0 |
| Range requests | 0 | 0 |
| Stored objects | 30 | 30 |
| Stored bytes | 27,820,024 | 27,820,024 |

The full reproducible setup, complete digest, and cleanup context are in the
[compatibility matrix](compatibility.md#oci-pull-through-spike).

## Current decision

**Go: n0ding is ready to become a narrowly scoped MVP project.** The
compatibility-hardening spike passed for real npm and Docker clients without a
database, sidecar, second configuration format, or protocol-specific storage
service.

The smallest next MVP is a read-only npm plus OCI pull-through cache:

- one binary and one TOML file
- local filesystem storage
- standard npm and Docker clients
- existing JSON status and metrics
- retention, crash-temp scavenging, TLS deployment guidance, and an explicit
  compatibility matrix

Do not add PyPI, private publish, UI, auth, or RBAC in that scope. Before PyPI
or private publish, the project still needs retention/garbage collection,
startup cleanup for orphaned temporary files, private-upstream auth analysis,
TLS and supply-chain threat documentation, concurrency/cancellation semantics,
and protocol-specific compatibility tests. Podman remains unknown.
