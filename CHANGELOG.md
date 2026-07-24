# Changelog

All notable changes to n0ding are documented in this file. The project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) for release tags.

## [Unreleased]

No changes yet.

## [0.1.0] - 2026-07-25 (MVP preview)

This is the first narrow, read-only preview. It is for evaluation on trusted
networks and is not a production supply-chain stability release.

### Added

- npm metadata and tarball pull-through caching, including scoped packages,
  lockfile installs, metadata URL rewriting, and npm integrity compatibility.
- OCI Distribution pull support for image indexes, manifests, configs, and
  blobs, with SHA-256 verification before cache commit.
- Persistent local filesystem storage with atomic writes and no database.
- Startup cleanup of stale temporary files.
- Configurable age-based startup and periodic garbage collection of complete
  cache objects.
- In-process same-key request coalescing.
- Health, JSON status, setup-snippet, and Prometheus-compatible metrics
  endpoints.
- One-binary, one-config Docker image and Docker Compose quickstart.
- Real-client compatibility records for npm and Docker Engine.

### Known limitations

- Not an offline mirror; OCI cache hits still contact the upstream.
- No private publishing, client authentication, users, RBAC, or additional
  package ecosystems.
- Podman is untested.
- Range requests are proxied but partial responses are not cached.
- Retention is age-based and does not enforce a strict disk quota.
- A writable cache directory supports one n0ding process only.

See the [MVP readiness scorecard](docs/spike-scorecard.md) for exact commands,
client versions, counters, and digests.
