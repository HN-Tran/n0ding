# Native focused Public Preview validation

The `v0.1.0` Public Preview uses an event- and coverage-based real-client gate
on the exact release-candidate SHA. It is not an uptime test. The isolated
seven-day Docker/PowerShell soak remains the gate for a later stable,
production-ready, or availability-oriented claim.

## Safe deployment

Use a fresh RC cache and preferably a side-by-side systemd service. Preserve
the old cache, immutable binary, config, unit, and off-host backup as rollback
boundaries. Record hashes, status, metrics, journal, capacity, cache size,
limits, and `NRestarts` before starting. Never merge cache trees or restore
over a live cache.

## Required coverage

Run exactly three complete mixed-client phases:

1. cold server cache with fresh client caches;
2. warm server cache with new, empty client caches;
3. one planned service restart, then new client caches and post-restart use.

Every phase runs real npm, pip, uv, and Docker clients concurrently. Use fixed
fixtures such as `testdata/npm-compat`, `idna==3.10`, and fixed Docker Hub
tags. Raw OCI requests may supplement but cannot replace a real Docker pull.
The evidence must show npm SRI/install integrity, matching pip and uv installed
file hashes, identical OCI RepoDigests, and positive warm/post-restart cache-
hit deltas.

The workload hook receives base URL, snapshot directory, evidence root,
one-based round, and phase. It appends one receipt per client invocation to
`workload-events.jsonl`. Each receipt binds round/phase, ecosystem, client and
version, the fixed target, fresh client-cache identity, start/end epoch
milliseconds, exit code, output, integrity, and before/after status. Every raw
artifact is named relative to the evidence root and SHA-256 bound. A single
central per-round launch ledger binds all four process intervals and exit
codes; an empty-cache prestate artifact proves each client cache is new. The
validator derives cache-hit deltas from raw status JSON, verifies npm integrity
against the exact committed lock object, recomputes Python installed-file
hashes, and derives the OCI digest from raw `docker image inspect` JSON.
Post-restart receipts additionally bind
the runner restart-ledger index, PID, and process-start value. Client intervals
must overlap in every phase.

The hook also appends an ordered cancellation attempt and successful retry,
each with output and raw before/after status, Prometheus text, cache-file and
metadata inventories plus SHA-256. The validator derives cancellation and
reservation deltas and temp/orphan counts rather than trusting summaries. The canceled
cacheable transfer must leave no committed partial, persistent temp, orphan,
or corrupt object. Synthetic DNS disruption is not a Public Preview gate.

For npm, the evidence must contain the exact `is-number@7.0.0` lock object from
the committed compatibility lockfile; a different self-consistent lock object
is rejected. OCI RepoDigests must name the same proxy reference passed to
`docker pull`, not merely contain the claimed digest. Cancellation counters are
read only from the repository type targeted by the canceled request. The retry
has its own hashed integrity artifact, which is recomputed; a boolean success
claim is not evidence.

The repository validates the hook contract but cannot supply the deployment-
specific Docker endpoint. The adapter must be reviewed with the final host
configuration and must identify a reachable real Docker Engine; without that
endpoint the focused gate cannot qualify.

## Running the gate

```sh
sudo tools/native-preview-gate.sh --service n0ding-preview \
  --mode gate --rounds 3 --planned-restarts 1 --round-budget 2400 \
  --expected-version <full-rc-sha> \
  --expected-binary-sha256 <trusted-sha256> \
  --binary /usr/local/lib/n0ding-preview/current \
  --config /etc/n0ding/preview.toml \
  --cache /var/lib/n0ding-preview --forbidden-old-cache /var/lib/n0ding \
  --evidence /var/lib/n0ding-evidence/<rc>-focused \
  --workload-hook /root/n0ding-focused-client-hook \
  --max-rss-kib 262144 --max-fds 1024 \
  --max-cache-growth-bytes 1073741824 --max-temp-files 0
```

There is no minimum elapsed time. `--round-budget` is checked after each round
and `--hook-timeout` is a hard per-hook bound; neither is a pass duration.
Gate mode requires exactly three phases and one restart;
smaller smoke runs are always non-qualifying. The runner verifies exact binary,
config, unit, PID ledger, trusted-input hashes and permissions, health, status,
metrics, disk, cache, RSS, file descriptors, temp files, journal, and canaries
when configured. It checks resources again after every hook.

The runner copies the prior event log before each hook and rejects rewriting,
truncation, or a call that appends nothing. The committed validator derives
coverage directly from receipts and rejects aggregate claims, missing,
duplicate, extra, wrong-phase, non-overlapping, hash-mismatched, changed-
integrity, hitless warm/post-restart, or forged-restart evidence.

At completion the service is stopped for a cache-body hash, metadata/body,
OCI-digest, temp, orphan, and optional canary scan, then returned to its prior
active state. Optional off-host anchoring binds the final manifest. A failed
run cannot be resumed into a pass.

Passing supports only the documented best-effort Public Preview. It does not
establish long-duration stability, production readiness, or availability.
