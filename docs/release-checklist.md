# Release checklist

Target: `v0.1.0` narrow MVP preview.

## Scope and documentation

- [x] Scope is read-only npm + OCI pull-through caching.
- [x] README states what n0ding is and is not.
- [x] Docker Compose, npm, OCI/Docker, TLS, backup, and restore guidance exists.
- [x] Example configuration and complete configuration reference exist.
- [x] Architecture and troubleshooting documents match current behavior.
- [x] Known limitations explicitly reject offline-mirror, private-registry,
  auth/RBAC, and production-stability claims.
- [x] Apache-2.0, contributing guidance, security policy, and changelog exist.

## Engineering evidence

- [x] `go test ./...`
- [x] `go vet ./...`
- [x] release-shaped Go build
- [x] config validation
- [x] container build
- [x] `docker compose config --quiet`
- [x] real npm scoped-package and lockfile/integrity validation
- [x] real Docker multi-image, multi-arch, restart, cache-count, and digest
  validation
- [x] startup cleanup, age-based GC, and same-key concurrency tests

The exact client versions, commands, counters, and digests are recorded in
[compatibility.md](compatibility.md) and [spike-scorecard.md](spike-scorecard.md).

## Before creating the `v0.1.0` tag

- [ ] Confirm the CI run is green on the exact release commit.
- [ ] Confirm a monitored private vulnerability-reporting path.
- [ ] Confirm `git status --short` is empty and `main` matches `origin/main`.
- [ ] Create an annotated `v0.1.0` tag from that commit.
- [ ] Publish release notes from `CHANGELOG.md`, retaining the preview warning.
- [ ] Build the release container with `VERSION=v0.1.0` and record its immutable
  digest if an image is published.

Creating the tag, GitHub release, or publishing an image is intentionally not
part of documentation preparation.

## Exact gates for v0.2.0

v0.2.0 should remain read-only until all gates below pass:

1. Run a seven-day retention and concurrency soak with forced expiry, restart,
   and disk-usage evidence; observe no corrupt complete objects.
2. Back up a stopped Compose cache, restore it into a fresh volume, and
   revalidate npm lockfile integrity plus all recorded OCI digests.
3. Verify npm and Docker through a trusted TLS reverse proxy, including
   `public_base_url`, certificate trust, large blobs, and restart behavior.
4. Run the OCI image/restart/digest matrix with a current Podman client.
5. Test authenticated/private npm and OCI upstreams; prove tokens are neither
   persisted nor shared and document logout/revocation behavior.
6. Automate the real-client npm and OCI compatibility matrix in an isolated
   release environment with repeatable cache counters.
7. Perform a focused threat-model/security review of proxy headers, cache-key
   isolation, credential forwarding, filesystem permissions, and dependency
   pinning.
8. Decide the v0.2.0 support matrix and storage-format compatibility policy,
   then update migration and rollback guidance.

PyPI and private publish require separate design reviews after these gates;
they are not implied v0.2.0 features.
