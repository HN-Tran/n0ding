# Spike scorecard

Date opened: 2026-07-24  
Decision date: TBD

## Go criteria

| Criterion | Target | Current evidence | Result |
|---|---|---|---|
| Standard client proxy is stable | npm or OCI | npm 11.16 metadata and tarball flows pass with clean client caches | Pass for npm spike |
| Local setup time | Under 10 minutes | One `docker compose up --build -d` command | Pending measurement |
| Two ecosystems | One configuration | npm adapter only | Pending |
| Cache/storage visibility | Visible without filesystem access | Dashboard, JSON status, Prometheus metrics | Pass for spike |
| Private publish feasibility | One ecosystem | Not analyzed | Pending |
| Lighter than Nexus | Qualitative operator test | One process, one config, one volume | Pending validation |

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
- [ ] Analyze OCI `/v2/` pull/auth flow
- [ ] Implement one OCI cache path or record why Option B fails
- [ ] Add lazy retention cleanup or document the required storage redesign
- [ ] Assess private npm publish path
- [ ] Record go, cut, or kill decision
