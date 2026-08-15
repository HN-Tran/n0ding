# Retention and concurrency soak

Status: infrastructure implemented and deterministic smoke passed on
2026-07-26. A seven-day run has **not** been performed.

The soak exercises the existing production cache, GC, restart, status, metrics,
and identity-safety paths. It does not add a runtime test endpoint or bypass
normal retention behavior. Short retention values exist only in the isolated
soak configuration.

## Commands

Requirements:

- Docker Engine with Compose;
- PowerShell 7 (`pwsh`);
- network access only to obtain the declared Go and Alpine base images when
  they are not already local.

Run the deterministic short smoke:

```powershell
.\tools\retention-soak.ps1 -Mode Smoke
```

Start the actual seven-day run:

```powershell
.\tools\retention-soak.ps1 -Mode SevenDay
```

The SevenDay profile defaults to exactly seven days. Overriding `-Duration`
with a shorter value is useful while developing the runner but does not satisfy
the release gate; `result.json` reports `seven_day_completed: false`.

Optional parameters accept PowerShell `TimeSpan` values:

```powershell
.\tools\retention-soak.ps1 -Mode Smoke `
  -Duration 00:01:00 `
  -SampleInterval 00:00:03 `
  -RetentionAge 00:00:20 `
  -GCInterval 00:00:02 `
  -Workers 8
```

| Profile | Duration | Sample interval | Retention | GC interval | Scheduled restarts | Workers/key |
|---|---:|---:|---:|---:|---:|---:|
| Smoke | 45 seconds | 2 seconds | 15 seconds | 1 second | Initial forced restart | 6 |
| SevenDay | 168 hours | 5 minutes | 10 minutes | 1 minute | Initial plus every 6 hours | 8 |

The intentionally short SevenDay retention window creates many real expiry
cycles during the long run. It does not claim that ten minutes is a recommended
operator value.

## Workload and failure phases

Each cache cycle launches concurrent clients for the same uncached:

- npm metadata;
- npm tarball;
- OCI manifest;
- OCI blob.

For a cold key, exactly one upstream `GET` and one cache miss are required;
the remaining clients must be hits. OCI clients alternate authorized
identities A and B. Every cycle also checks distinct authenticated npm
responses and denied npm/OCI identities. Credentialed npm responses must
remain misses and a denied OCI identity must not consume a cached object.

Before the timed loop, the runner:

1. warms four complete objects and records status, metrics, fixture counters,
   and volume usage;
2. closes a digest-bearing slow OCI download after 64 KiB and requires no
   complete or temporary object to remain;
3. starts another slow download, verifies an active cache temp write, sends
   `SIGKILL` to n0ding, ages the temp file, restarts the same container, and
   requires startup cleanup plus cache hits for the previously complete
   objects;
4. goes quiet for `max_age + 2 * gc_interval`, requires both repositories to
   reach zero complete objects/bytes and lower disk usage, then requires the
   same keys to miss and refetch;
5. runs the configured mixed workload until the requested wall-clock duration
   has elapsed.

At the end n0ding is stopped, the volume and exact soak config are copied into
an uncompressed backup-like snapshot, and every body/metadata pair is checked.
OCI body hashes must match their persisted digest and no temp or orphan file
may remain.

## Pass/fail criteria

A run passes only when all of these are true:

- the requested duration actually elapses;
- concurrent cold keys produce one upstream `GET`, one miss, and only hits for
  waiters; warm replay after restart produces no upstream `GET`;
- npm A/B responses stay distinct and uncached;
- denied npm/OCI identities receive HTTP 403 and never cached private bytes;
- the canceled download creates no complete object or remaining temp file;
- the forced process kill leaves a temp write which startup cleanup removes;
- forced GC reaches zero complete objects/bytes, reduces disk use, and a later
  request refetches;
- every status snapshot matches the corresponding Prometheus metrics;
- the stopped final snapshot contains only complete size-matched cache pairs,
  with OCI SHA-256 integrity intact;
- archive, exported cache, metadata, config, status, metrics, logs, client
  results/errors, progress, and result files contain none of the eight fake
  credential canaries.

The SevenDay gate additionally requires:

- `mode` is `SevenDay`;
- requested and elapsed duration are at least 604,800 seconds;
- `seven_day_completed` is `true`;
- the final result remains `pass` without manually deleting failed intervals
  or combining partial runs.

## Recorded smoke result

Local environment: Docker Desktop 4.79.0, Engine 29.5.3, Compose 5.1.4,
Windows host with Linux containers.

| Measurement | Result |
|---|---:|
| Requested / elapsed duration | 45 / 48.011 seconds |
| Workload cycles | 8 |
| Workers per cache key | 6 |
| Client-observed hits / misses | 164 / 60 |
| Denied identity checks | 16 |
| Controlled process kills/restarts | 1 |
| Server npm hits / misses | 82 / 38 |
| Server OCI hits / misses | 82 / 24 |
| Expected server errors | 1 (`context canceled`) |
| Retention-peak disk usage | 320 KiB |
| Disk usage after forced expiry | 36 KiB |
| Maximum disk usage, including killed temp write | 1,820 KiB |
| Final complete objects / temp files | 20 / 0 |
| Canary scan | 115 files, 8 values, 0 findings |
| Seven-day completed | No |

Counts after the forced-expiry phase depend on host speed because the runner
continues until the wall-clock deadline. The concurrency result for each
individual key, identity checks, zero-object forced-expiry point, integrity
checks, and minimum duration are deterministic pass conditions.

## Artifacts and cleanup

Each run writes ignored evidence to:

```text
.tmp/retention-soak/<id>/
```

Important files:

- `result.json` — final pass/fail and aggregate measurements;
- `progress.json` — most recent durable progress during a long run;
- `snapshots/status-*.json`, `metrics-*.txt`, and `volume-*.json`;
- `workload/*.json` — secret-free per-cycle client observations;
- `compose.log` — complete fixture/n0ding logs across restarts;
- `cache-snapshot.tar` — uncompressed stopped backup-like snapshot;
- `cache-state/` — readable exported cache used for integrity and canary scans.

The script refuses to overwrite an evidence directory. Generated Compose
projects, volumes, and local image tags use random names and are removed after
success or failure. The evidence directory remains for review. Preserve only
the exact non-secret directory after confirming `credential_canary_scan` is
`pass`; never add it to Git.

## What remains unproven

The smoke proves the runner and short-timescale invariants, not seven days of
availability, bounded whole-filesystem growth, repeated expiry/restart races,
or cumulative resource behavior. It also does not replace real npm/Docker
clients, real private providers, TLS testing, or network-partition testing.
Its disk samples include real volume usage. The separate deterministic storage
integration test proves concurrent `max_bytes` admission, bypass accounting,
restart reconstruction, and global LRU pressure collection, but neither test
turns the logical complete-object budget into a hard filesystem quota.

Normal script failures run cleanup. Abrupt host shutdown, Docker daemon loss,
or forced termination of the PowerShell process can prevent that `finally`
block. In that case, inspect resources whose exact names contain the evidence
directory's `<id>` before removing them; do not use broad Docker cleanup
commands.

The [retention policy decision](retention-policy.md) records why the earlier
age-only private-alpha position was replaced by the implemented logical byte
budget, admission reservations, filesystem headroom, and LRU pressure GC.
After a real seven-day pass, the next gate is to record the intended
deployment's capacity, alerts, observed growth headroom, and explicit residual
risk acceptance for bytes outside that budget. The real-client/TLS
compatibility matrix follows that gate.
