# Stopped Compose backup/restore drill

Status: deterministic drill passed on 2026-07-26. This is private-hardening
evidence, not a stable storage-format guarantee.

The drill proves that a stopped n0ding Compose deployment can archive its cache
and config, restore both into a fresh volume, and preserve the expected npm and
OCI cache behavior without weakening identity safety. It uses only committed
local fixtures and conspicuous fake credential canaries.

## Run it

Requirements:

- Docker Engine with Compose;
- PowerShell 7 (`pwsh`);
- network access only to obtain the declared Go, Alpine, and Node container
  images when they are not already local.

From the repository root:

```powershell
.\tools\backup-restore-drill.ps1
```

No registry credential is required. The script:

- generates unique project, volume, image, and evidence names;
- refuses to overwrite an existing evidence directory;
- never deletes a pre-existing Docker volume;
- removes only its generated Compose projects, volumes, and local image tags;
- leaves the ignored `.tmp/backup-restore-drill/<id>` evidence directory for
  review.

The same script runs in GitHub Actions. It does not tag a release, push an
image, or contact an npm/OCI artifact service.

## What the drill does

1. Builds an isolated n0ding image and local fixture service.
2. Creates a new source volume and starts the source Compose project.
3. Warms npm metadata and tarball paths. A real `node:24-alpine` npm client
   runs `npm view` and `npm ci` from the committed lockfile with a clean npm
   client cache.
4. Warms a digest-bearing OCI manifest with identity A, confirms identity B
   receives a digest-authorized hit, and confirms a denied identity cannot use
   the object.
5. Sends identity-specific npm requests, including a query and transient
   authentication-response canary. Authenticated npm responses remain misses
   and are not persisted.
6. Captures status, metrics, logs, response/error records, and the npm
   lockfile hash.
7. Stops the n0ding service and verifies the container is not running.
8. Creates one uncompressed tar archive containing `data/` and
   `config/n0ding.toml`, records its SHA-256, and rejects absolute,
   parent-traversing, or unexpected archive members.
9. Extracts the archive into a newly created empty volume and obtains the
   restored config from the same archive.
10. Starts a separate restored Compose project and repeats the npm and OCI
    checks from clean clients.
11. Confirms npm lockfile integrity, OCI digest equality, object/byte counts,
    restored cache hits, and npm/OCI identity denial.
12. Stops the restored service, returns to the untouched source volume, and
    proves the rollback path still serves the original OCI object.
13. Restores the archive into a third volume, deliberately truncates one npm
    body, and proves n0ding rejects it, logs a size mismatch, refetches it, and
    completes `npm ci`.
14. Scans the archive, restored files, cache metadata, config, status, metrics,
    logs, client errors/responses, npm output, and result manifest for every
    fake credential canary.

The fixture source and Compose definition are under
`testdata/backup-restore/`. The fast store-level corruption cases also run in
`go test ./...`.

## Recorded local result

Environment:

- Docker Desktop 4.79.0;
- Docker Engine 29.5.3;
- Docker Compose 5.1.4;
- Node 24 Alpine client;
- Windows host with Linux containers.

| Measurement | Result |
|---|---:|
| Backup archive size | 11,776 bytes |
| Backup duration | 715 ms |
| Restore duration | 676 ms |
| npm complete objects / bytes | 2 / 561 |
| npm hits after restore | 2 |
| npm lockfile SHA-256 | `898aa0f994a55dfa9944624d9303018aa5e9cfd25504b287e2cd0226e1a91b61` |
| OCI complete objects / bytes | 1 / 246 |
| OCI hits after restore | 2 |
| OCI digest before/after | `sha256:f20c43161d73848408ef247f0ec7111b19fe58ffebc0cbcaa0d2c8bda4967268` |
| Canary scan | 63 files, 8 values, 0 findings |
| Rollback to untouched source | Pass |
| Truncated-body rejection/refetch | Pass |

Archive hashes and durations are recorded in each run's `result.json` and can
vary because cache commit timestamps and host performance vary. Object counts,
body integrity, identity behavior, and before/after digests are deterministic.

## Corrupt and incompatible restore behavior

n0ding does not promise that an arbitrary or cross-version cache archive is
valid merely because it extracts:

- the drill aborts before restore when tar extraction, required paths, archive
  member validation, or the empty-volume precondition fails;
- missing bodies are safe cache misses;
- malformed metadata, truncated bodies, and metadata/body size disagreement
  are not counted as complete and are never served;
- on access, a corrupt object produces a cache lookup warning and is refetched
  from the upstream;
- if the upstream is unavailable, that refetch cannot succeed. Operators must
  roll back to the untouched source volume or discard the restored cache and
  repopulate it.

Restoring into a live or non-empty volume, merging archives, editing individual
objects, multiple writers, and cross-version format compatibility remain
unsupported.

## Operator evidence and cleanup

The script prints the evidence path and a compact result. Inspect at minimum:

```powershell
Get-Content .tmp\backup-restore-drill\<id>\result.json
Get-Content .tmp\backup-restore-drill\<id>\archive-contents.txt
Get-Content .tmp\backup-restore-drill\<id>\status-after.json
Get-Content .tmp\backup-restore-drill\<id>\compose-corrupt.log
```

The Docker resources are already removed after either success or failure.
Delete the exact evidence directory only after recording the non-secret result
needed for the private operations log.
