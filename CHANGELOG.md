# Changelog

All notable changes to n0ding are documented in this file. The project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) for release tags.

## [Unreleased]

### Security

- Added one shared request/response header policy for npm and OCI.
- Prevented persistent caching of private/no-store/no-cache,
  authentication-metadata, unsafe-cookie, and unsupported-`Vary` responses.
- Added a storage-boundary scrub for known credential-bearing cache metadata.
- Removed upstream URL userinfo, query, and fragment from status and explicit
  upstream/error URL log fields.
- Added focused negative tests and an initial threat model.
- Added two-identity npm and OCI fixtures with raw-cache credential canaries.
- Required an exact, non-empty upstream digest before an OCI cached object can
  be served to the current identity.
- Limited credential forwarding across redirects to the exact same origin;
  cross-origin redirects continue without client credentials.
- Added a deterministic full-server private-upstream drill covering two
  identities, denied access, revocation, OCI digest authorization, redirects,
  and credential-canary scans of cache and operator outputs.
- Added a stopped Compose backup/restore drill with npm lockfile integrity, OCI
  digest/identity checks, rollback, corrupt-object refetch, and archive plus
  restored-state credential-canary scanning.
- Added configurable short and seven-day retention/concurrency soak profiles
  with concurrent npm/OCI fixtures, mixed identities, client cancellation,
  forced restart/expiry, disk measurements, integrity checks, and artifact
  canary scanning.

### Documentation

- Reframed v0.1 as a private hardening phase with no public launch.
- Replaced the public release checklist with an ordered private-use roadmap.
- Added a PyPI design gate instead of a partial implementation.
- Added a disposable manual npm/OCI private-service drill, secret-free example
  config, and streaming credential-canary scanner without claiming an external
  provider run.
- Documented measured same-version Compose restore behavior, unsupported
  corrupt/cross-version cases, and the rollback path.
- Documented soak pass/fail criteria, durable progress/results, safe artifact
  handling, and the explicit rule that a smoke run is not seven-day evidence.

## [0.1.0] - Unreleased (private hardening baseline)

This section records the narrow npm + OCI baseline being hardened privately.
No tag, public release, or published image exists.

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
