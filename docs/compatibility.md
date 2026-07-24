# Compatibility matrix

This matrix records verified behavior. An unchecked row is unknown, not
implicitly supported.

| Ecosystem | Client | Operation | Status | Evidence |
|---|---|---|---|---|
| npm | npm 11.16.0 | package metadata | Verified 2026-07-24 | Two clean-cache `npm view is-number version` runs; second served from n0ding |
| npm | npm 11.16.0 | tarball download | Verified 2026-07-24 | Two clean-cache `npm pack is-number@7.0.0` runs; integrity accepted and second served from n0ding |
| npm | npm CLI | scoped package | Not verified | Spike task |
| npm | npm CLI | install with lockfile | Not verified | Spike task |
| npm | npm CLI | private publish | Not implemented | Post-spike gate |
| OCI | Docker/Podman | image pull | Not implemented | Week 2 candidate |
| PyPI | pip/uv | package install | Not implemented | After npm/OCI gate |

Automated tests also exercise the same HTTP behavior against an in-process
upstream. The real-client check used Node.js 24.18.0 LTS with npm 11.16.0 and
separate empty npm client caches so that n0ding, rather than npm's local cache,
had to serve the second requests.
