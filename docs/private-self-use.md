# Private self-use checklist

Status: operator checklist for private alpha use. This is not production
readiness, a public launch gate, or permission to tag or publish an artifact.

Use this checklist only for a disposable self-hosted npm + OCI cache on a
trusted private network. PyPI is not implemented, private publishing is not
implemented, there is no n0ding client authentication or RBAC, and retention
does not enforce a strict byte quota. The seven-day soak and real private
provider drills remain open.

## Before starting

- Confirm the service is bound to loopback, a private network, or a trusted TLS
  reverse proxy. Do not expose plain HTTP on an untrusted network.
- Use exactly one n0ding process for the writable cache path. Shared-volume
  multi-writer operation is unsupported.
- Put `/data` on a dedicated Docker volume, filesystem, or host quota boundary
  with known capacity. Do not share it with unrelated data.
- Choose `storage.max_age` from usable capacity and worst credible unique npm
  plus OCI ingress. Do not treat the default `720h` as a capacity plan.
- Keep provider credentials out of committed config, command lines, Git
  history, support logs, and public issues. The real private-upstream drill has
  procedure only; no real provider credential evidence is claimed.

## Operate

- Start and inspect the service:

```sh
docker compose config --quiet
docker compose up --build -d
docker compose ps
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/api/v1/status
curl -fsS http://localhost:8080/metrics
```

- For a hostname or reverse proxy, set `N0DING_PUBLIC_URL` to the exact client
  origin before starting. See [operations.md](operations.md#tls-and-reverse-proxy).
- Verify cache behavior with empty npm client caches and, for OCI, a client or
  daemon that is not already satisfying pulls from its own local cache. See
  [compatibility.md](compatibility.md).
- Watch both n0ding's logical complete-object metrics and real filesystem
  usage:

```sh
curl -fsS http://localhost:8080/metrics
docker compose exec -T n0ding du -sk /data
docker compose exec -T n0ding df -Pk /data
```

- Alert below 20% free space, treat below 10% as critical, and alert when the
  observed growth rate predicts exhaustion before `storage.max_age` can expire.
  The repository storage metric is not a filesystem free-space alarm.

## Backup and restore references

- Back up only after stopping n0ding so body and metadata files cannot change
  during the copy:

```sh
docker compose stop n0ding
test "$(docker inspect --format '{{.State.Running}}' \
  "$(docker compose ps --all -q n0ding)")" = "false"
```

- Use the stopped backup/restore procedure in
  [operations.md](operations.md#backup-and-restore), and keep config outside
  `/data` backed up separately.
- Restore into a new empty volume first, then validate `/healthz`,
  `/api/v1/status`, `/metrics`, `npm ci` from a committed lockfile with an
  empty npm cache, and one OCI digest check.
- Keep the untouched source volume until the restored deployment passes. If
  restore validation fails, stop the restored deployment and roll back to the
  original volume or discard the restored cache and repopulate it.
- Run the deterministic drill when changing backup, restore, storage, or
  identity behavior:

```powershell
.\tools\backup-restore-drill.ps1
```

## Canary and safety checks

- Use [private-upstream-drill.md](private-upstream-drill.md) before claiming
  real private npm or OCI compatibility. It requires disposable provider
  resources and short-lived credentials and includes credential-canary scans.
- Use [retention-soak.md](retention-soak.md) for retention and concurrency
  evidence. The smoke profile is useful before storage changes, but it is not
  seven-day evidence:

```powershell
.\tools\retention-soak.ps1 -Mode Smoke
```

- Preserve non-secret evidence for private notes: status snapshots, metrics,
  logs with credentials redacted or absent, disk samples, restore results, and
  canary scan outcomes.

## Shutdown, restart, and cleanup

- Safe restart after config or retention changes:

```sh
docker compose restart n0ding
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/api/v1/status
```

- Safe shutdown that preserves the cache:

```sh
docker compose down
```

- Do not run `docker compose down --volumes` unless you intentionally want to
  delete the disposable cache.
- Under disk pressure, lower `storage.max_age`, restart so startup GC runs, and
  re-check status, metrics, `du`, and `df`. If space remains critical, stop
  n0ding before replacing the whole cache volume.
- Never delete or edit individual cache objects while n0ding is running.

## Not ready for

- Public release, hosted multi-tenant service, production supply-chain security
  claims, or an availability commitment.
- PyPI caching, package publishing, client auth, users, RBAC, signing, malware
  scanning, policy enforcement, or general artifact caching.
- Real private npm/OCI provider claims until the manual drill has been run with
  dated provider/client evidence and clean canary scans.
- Strict disk-byte quota claims. Age-only retention still needs dedicated
  capacity, alerts, and explicit risk acceptance.
- Cross-version cache-format guarantees, shared writable cache directories,
  distributed locking, or multiple n0ding writers.
- Closing `v0.1-private` while the seven-day soak, PyPI implementation, TLS
  client matrix, and real-provider credential gates remain open.
