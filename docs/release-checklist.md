# Private-upstream and long-term hardening roadmap

The public-preview criteria are tracked separately in the
[v0.1.0 release gate](v0.1-release-gate.md). This document retains the stricter
private-provider, credential-revocation, TLS, Podman, and long-duration work.

Target: a trustworthy private-use hardening milestone. This document does not
authorize a tag, GitHub Release, image publication, or announcement.

## Working definition of trustworthy

`v0.1-private` is trustworthy enough for repeated use on a private network when:

- standard clients can use npm, OCI, and PyPI read-only caches without plugins;
- private-upstream credentials are scoped, never persisted in cache metadata,
  and covered by negative tests;
- cache admission, concurrency, retention, crash cleanup, backup, and restore
  have repeatable evidence;
- compatibility and known limitations are explicit;
- the threat model has no unowned critical risks.

It is still not a production supply-chain security platform.

## 0. npm + OCI baseline

- [x] npm scoped packages, lockfiles, and integrity accepted by real npm.
- [x] OCI manifests, indexes, configs, and blobs accepted by real Docker.
- [x] OCI SHA-256 verification before cache commit.
- [x] Persistent local filesystem cache with atomic writes.
- [x] Startup stale-temp cleanup, age-based GC, and same-key coalescing.
- [x] One binary, one config, no database. PyPI adds the narrowly scoped
  `golang.org/x/net/html` dependency for HTML rewriting.

Evidence remains in [compatibility.md](compatibility.md) and
[spike-scorecard.md](spike-scorecard.md).

## 1. Shared-cache and credential-safety foundation

- [x] Central request/response header policy shared by npm and OCI.
- [x] Client cookies, proxy credentials, OTP headers, forwarded identity
  headers, and Docker registry-auth transport headers are not sent upstream.
- [x] `Authorization` is forwarded only where the adapter explicitly permits
  it.
- [x] Responses marked `private`, `no-store`, or `no-cache` are not stored.
- [x] Authentication metadata is not stored; cookie-bearing responses require
  explicit `Cache-Control: public`, and cookie fields are still scrubbed.
- [x] Responses with `Vary` dimensions outside the cache key are not stored.
- [x] Persistent metadata has a second credential-header scrub at the storage
  boundary.
- [x] Status and explicit upstream/error URL log fields remove URL userinfo,
  query, and fragment.
- [x] Negative unit/integration tests cover these boundaries.
- [x] Initial [threat model](threat-model.md) records controls and residual
  risks.

This is the completed first private-hardening slice.

## 2. Private upstreams

- [ ] Choose an explicit secret-input model; do not silently normalize
  credentials embedded in ordinary config strings.
- [x] Define the current npm boundary: only client `Authorization` is forwarded
  when enabled, and every such request bypasses persistent caching.
- [ ] Define supported OCI token/basic-auth flows and authorization behavior
  for tags and digests.
- [x] Require a successful OCI authorization `HEAD` with the exact cached digest
  before serving shared content-addressed bytes.
- [x] Retain credentials only across exact-origin redirects; prove
  cross-origin redirects continue without credentials and discard redirect
  userinfo.
- [x] Exercise npm and OCI fixtures with identity A, identity B, and denied
  access; prove no identity receives another identity's private response.
- [x] Exercise the fixtures through the real n0ding server, revoke identities
  without restarting it, and prove denied/revoked OCI identities cannot use
  existing cached bytes.
- [x] Scan fixture cache files, metadata, stopped cache copies, restored fixture
  copies, proxy failure logs, status, metrics, and client errors for credential
  canaries.
- [ ] Test one real private npm upstream with two identities and denied access.
- [ ] Test one real private OCI upstream with two identities and denied access.
- [ ] Prove real-provider logout/token revocation takes effect without a
  process restart and record provider token TTL/propagation behavior.
- [ ] Repeat the cache, status, metrics, log, and error canary scan against
  those real private upstreams.
- [x] Document which custom credential headers are unsupported.

Fixture evidence is recorded in [compatibility.md](compatibility.md). The
reproducible external procedure is
[private-upstream-drill.md](private-upstream-drill.md). Deterministic local
revocation reduces implementation risk but does not count as real-provider
revocation or token-lifetime evidence.

## 3. Retention, concurrency, and recovery

- [x] Commit a configurable, deterministic retention/concurrency runner with
  separate short-smoke and 168-hour profiles.
- [x] Pass the short smoke with concurrent npm/OCI clients, mixed identities,
  a canceled download, forced process termination/restart, forced expiry,
  disk/status/metrics evidence, cache-pair validation, and canary scanning.
- [ ] Run a seven-day soak with concurrent npm and OCI clients, forced expiry,
  process restarts, and disk-usage evidence.
- [x] Confirm no corrupt complete objects after forced client disconnects and
  process termination.
- [x] Back up a stopped Compose cache and config.
- [x] Restore into a fresh volume and revalidate npm lockfile integrity plus
  recorded OCI digests.
- [x] Record restore duration, failure handling, and rollback procedure.
- [x] Record the `v0.1-private` decision: age-only retention is conditionally
  acceptable for a disposable, capacity-monitored private-alpha cache.
- [ ] Close the disk-capacity gate after the real seven-day soak by recording
  the intended deployment's volume capacity, alert thresholds, growth
  headroom, and explicit acceptance of the missing strict byte quota; otherwise
  implement the aggregate `max_bytes` design first.

The deterministic [stopped Compose backup/restore drill](backup-restore-drill.md)
also proves restored identity checks, archive/restored-state canary scanning,
rollback to the untouched source volume, and safe refetch of a deliberately
truncated cache body. The
[retention/concurrency soak](retention-soak.md) now passes the separate short
forced-disconnect/process-termination gate, but `seven_day_completed` remains
false until one uninterrupted 168-hour run finishes. The conditional retention
decision, disk-full failure mode, operator guardrails, and future strict-limit
requirements are in [retention-policy.md](retention-policy.md).

## Private self-use readiness slice

The focused self-use checklist in [private-self-use.md](private-self-use.md)
is the current operator gate for a disposable private-alpha deployment. It
requires a dedicated cache volume, free-space alerts, capacity planning from
observed ingress, backup/restore validation, canary-scan references,
status/metrics checks, safe shutdown/restart commands, rollback steps, and
explicit unsupported-use warnings.

Passing that checklist does not close the full `v0.1-private` trust gate. It
does not prove real private-provider credentials, trusted TLS client matrices,
real pip/uv compatibility, strict byte-quota behavior, or the uninterrupted
seven-day soak.

## 4. Real-client and TLS compatibility

- [ ] Repeat the npm matrix on a supported current npm client.
- [ ] Repeat the Docker multi-image/multi-arch/restart matrix.
- [ ] Run the OCI matrix with a current Podman client.
- [ ] Verify npm, Docker, and Podman through a trusted TLS reverse proxy.
- [ ] Automate the repeatable portion of the client matrix in an isolated
  release environment.
- [ ] Commit a private-use support matrix with tested versions and platforms.

## 5. PyPI read-only design and implementation

- [x] Record the protocol, URL-rewrite, integrity, auth, and dependency
  decisions in [pypi-design.md](pypi-design.md).
- [x] Resolve every blocking design decision in that document.
- [x] Add a PyPI adapter without weakening the shared HTTP/cache policy.
- [x] Support both required Simple API representation paths selected by the
  design.
- [x] Verify distribution hashes before cache commit where the index supplies
  them.
- [ ] Test normalized names, trailing-slash redirects, wheels, source
  distributions, yanked files, `Requires-Python`, and metadata sidecars with
  real pip/uv clients.
- [x] Run two clean-client installs with pip and uv, including restart and
  offline-client-cache isolation.
- [ ] Test a private PyPI-compatible upstream only after the private-auth model
  is approved.

PyPI publishing remains out of scope.

## v0.1-private trust gate

All of the following must be true before calling the private milestone
trustworthy:

- [ ] Sections 2 through 5 have no unresolved required item.
- [ ] `go test -race ./...`, `go vet ./...`, build, container build, and Compose
  validation pass on the exact commit.
- [ ] Real-client evidence is reproducible from committed commands.
- [ ] A backup/restore drill and seven-day soak have passed.
- [ ] No live or reusable test-environment credential appears in cache
  metadata, logs, metrics, status, fixtures, or Git history; committed
  credential-like strings are clearly fake canaries.
- [ ] The threat model is reviewed and all critical risks are closed or
  explicitly accepted for private use.
- [ ] README, compatibility matrix, configuration, security policy, and known
  limitations match observed behavior.

No public release follows automatically from this gate. Public readiness
requires a separate decision.
