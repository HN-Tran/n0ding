# Changelog

All notable changes to n0ding are documented in this file. The project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) for release tags.

## [Unreleased]

### Changed

- Added a shared logical `max_bytes` budget with concurrent admission
  reservations, filesystem headroom, high/low-watermark global LRU pressure
  collection, startup accounting, and bypass metrics. This does not claim a
  hard quota over every byte on the filesystem.
- Changed public release automation to require a manual approved `main` SHA,
  an existing signed SemVer tag at that SHA, a successful exact-SHA push CI
  run, and the `public-release` environment before publishing.
- Classified downstream client cancellations separately from repository
  failures in JSON status, Prometheus metrics, and logs for npm, OCI, and
  PyPI proxies.
- Added a cross-ecosystem release-candidate smoke for npm lockfile installs,
  pip, uv, and Docker pulls across a persistent n0ding restart.

### Added

- Added the read-only PyPI Simple API and distribution cache for pip and uv,
  including HTML/JSON link rewriting, allowed file origins, hash verification,
  and PEP 658/714 metadata sidecars.
- Added checksum-verified installers for Linux, macOS, and Windows plus a
  loopback-safe deployment Compose file.
- Added a multi-architecture GHCR release workflow with SBOM, provenance, and
  versioned GitHub Release assets.

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
- Added a native Linux/systemd 72-hour real-use gate for the `v0.1.0` Public
  Preview, including exact-candidate/config invariants, five-minute evidence,
  six-hour restarts, capacity and process monitoring, canary scans, and
  explicit abort/rollback hooks. The isolated seven-day profile remains the
  future stable/availability hardening gate.

### Documentation

- Defined the narrow v0.1.0 public-preview boundary and its release gate.
- Retained stricter private-provider and long-duration work as a separate
  hardening roadmap.
- Documented PyPI support for pip and uv without claiming package publishing.
- Added a disposable manual npm/OCI private-service drill, secret-free example
  config, and streaming credential-canary scanner without claiming an external
  provider run.
- Documented measured same-version Compose restore behavior, unsupported
  corrupt/cross-version cases, and the rollback path.
- Documented soak pass/fail criteria, durable progress/results, safe artifact
  handling, and the explicit rule that a smoke run is not seven-day evidence.
- Documented safe native RC migration, off-host stopped backup, versioned
  binary switching, fresh-cache rollback boundaries, and real-client
  acceptance for the 72-hour Public Preview gate.
- Recorded the earlier conditional age-only-retention decision, the subsequent
  logical byte-budget design, disk-capacity guardrails, and the remaining
  whole-filesystem disk-full risk.
- Added a private self-use checklist that ties together cache volume isolation,
  capacity alerts, backup/restore, canary scans, status checks, safe restarts,
  rollback, cleanup, and remaining not-ready warnings.

## [0.1.0] - Unreleased (public preview)

This section records the narrow public-registry, read-only preview baseline.
No tag, public release, or published image exists yet.

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
- No private publishing, client authentication, users, RBAC, or package
  ecosystems beyond npm, PyPI, and OCI.
- Podman is untested.
- Range requests are proxied but partial responses are not cached.
- Complete cache objects have age expiry and optional global LRU pressure
  collection under a logical byte budget; there is no hard whole-filesystem
  quota.
- A writable cache directory supports one n0ding process only.

See the [MVP readiness scorecard](docs/spike-scorecard.md) for exact commands,
client versions, counters, and digests.
