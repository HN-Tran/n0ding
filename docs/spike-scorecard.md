# Spike scorecard

Date opened: 2026-07-24  
Decision date: TBD

## Go criteria

| Criterion | Target | Current evidence | Result |
|---|---|---|---|
| Standard client proxy is stable | npm or OCI | npm 11.16 passes; Docker Engine 29.6.2 pulled `alpine:3.20` twice through n0ding with matching digest | Pass for npm and OCI spikes |
| Local setup time | Under 10 minutes | One `docker compose up --build -d` command | Pending measurement |
| Two ecosystems | One configuration | npm at `/npm/` and OCI at `/v2/` use one binary, config, storage abstraction, and status API | Pass for spike |
| Cache/storage visibility | Visible without filesystem access | Dashboard, JSON status, Prometheus metrics | Pass for spike |
| Private publish feasibility | One ecosystem | Deliberately not analyzed in this pull-only spike | Deferred |
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
- [ ] Scoped package and lockfile tests

## Week 2 gate

- [ ] Measure clean setup time
- [ ] Stabilize npm edge cases found by real-client tests
- [x] Analyze OCI `/v2/` pull/auth flow
- [x] Implement and test OCI manifest/blob pull-through caching
- [x] Verify a second pull with a clean Docker daemon after n0ding restart
- [ ] Add lazy retention cleanup or document the required storage redesign
- [ ] Test Podman and interrupted/range downloads
- [ ] Assess private npm publish path (deferred; no publish work in this spike)
- [ ] Record go, cut, or kill decision

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

Measured final results:

| Measurement | First clean client | Second clean client after n0ding restart |
|---|---:|---:|
| Image digest | `sha256:d9e853…b6bc` | `sha256:d9e853…b6bc` |
| Cache hits | 0 | 8 |
| Cache misses | 9 | 1 |
| Digest/cache errors | 0 | 0 |
| Stored objects | 8 | 8 |
| Stored bytes | 3,721,009 | 3,721,009 |

The full reproducible setup, complete digest, and cleanup context are in the
[compatibility matrix](compatibility.md#oci-pull-through-spike).

## Current decision

**Provisional go for the pull-only core.** Option B remains viable across npm
and OCI without adding a database, sidecar, or second service. Do not expand
into UI or publishing yet. The next gate is operational hardening: tag
mutation races, retention, interrupted downloads, Range behavior, Podman
compatibility, and private-registry authorization.
