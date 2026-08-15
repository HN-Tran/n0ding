# Native 72-hour public-preview validation

The `v0.1.0` Public Preview uses a 72-hour real-use observation of the exact
release-candidate SHA. This narrower gate is suitable for a best-effort
preview, not a production-availability claim. The isolated seven-day
Docker/PowerShell soak remains the gate before a later stable or
production-ready claim.

## Safe migration

Prefer a fresh RC cache path. Keep the old cache and immutable old binary as a
rollback boundary; never merge cache trees or restore over a live/non-empty
cache.

1. Record the old version, binary/config/unit hashes, systemd status and
   `NRestarts`, status, metrics, journal, capacity, and cache size.
2. Briefly stop n0ding and stream a consistent backup off-host:

   ```sh
   ssh root@host 'set -e; systemctl stop n0ding; trap "systemctl start n0ding" EXIT; tar -C / -cf - var/lib/n0ding etc/n0ding' >n0ding-pre-rc.tar
   sha256sum n0ding-pre-rc.tar >n0ding-pre-rc.tar.sha256
   ```

3. Build with the full commit as version, transfer under a versioned filename,
   verify its checksum, and validate configuration.
4. Make `ExecStart` use a stable symlink, with immutable binaries beside it.
   The helper requires this layout and atomically switches the binary. Its
   rollback is **binary-only**: it does not restore config, unit files, or
   cache data, which must remain separate and externally backed up:

   ```sh
   sudo tools/prepare-native-preview.sh preflight --service n0ding \
     --binary /usr/local/lib/n0ding/current --config /etc/n0ding/n0ding.toml \
     --cache /var/lib/n0ding-preview --expected-sha 81036ff0e284577c4186cb0b68ae3e768450160b
   sudo tools/prepare-native-preview.sh switch --service n0ding \
     --binary /usr/local/lib/n0ding/current \
     --candidate /usr/local/lib/n0ding/n0ding-81036ff0e284577c4186cb0b68ae3e768450160b \
     --config /etc/n0ding/n0ding.toml --cache /var/lib/n0ding-preview \
     --expected-sha 81036ff0e284577c4186cb0b68ae3e768450160b
   ```

Retain old cache, binary, backup, hashes, and deployment state until acceptance.
Restore only into a separate empty path.

## Real clients and observation

Run each client cold, warm with a new client-side cache, and after restart.
Preserve outputs, versions, before/after metrics, and hashes:

- npm: `npm view @types/node@22.10.0` plus `npm ci --ignore-scripts
  --no-audit --no-fund` using `testdata/npm-compat` and `<url>/npm/`;
- pip and uv: separate empty caches/targets installing `idna==3.10` through
  `<url>/pypi/simple/`, with matching installed-file hashes;
- OCI: a reachable real Docker Engine pulls `alpine:3.20`,
  `nginx:1.27-alpine`, and `busybox:1.36` twice through n0ding, removes local
  client copies between passes, and observes identical repo digests.

Raw registry requests can additionally verify manifest negotiation and blob
SHA-256 values, but do not replace the real Docker run. Exercise one canceled
large transfer and require no committed partial or persistent temp file.

The workload hook receives the base URL, snapshot directory, and evidence
root. It
must be non-destructive and save only secret-free output. Put fake canaries in
a root-readable file outside cache and evidence paths.

```sh
sudo tools/native-preview-soak.sh --service n0ding \
  --mode gate \
  --expected-version 81036ff0e284577c4186cb0b68ae3e768450160b \
  --expected-binary-sha256 <trusted-sha256> \
  --binary /usr/local/lib/n0ding/current --config /etc/n0ding/n0ding.toml \
  --cache /var/lib/n0ding-preview --forbidden-old-cache /var/lib/n0ding \
  --evidence /var/lib/n0ding-evidence/rc-81036ff-72h \
  --workload-hook /root/n0ding-real-use-hook --canary-file /root/n0ding-preview-canaries \
  --anchor-hook /root/copy-evidence-manifest-off-host \
  --max-rss-kib 262144 --max-fds 1024 \
  --max-cache-growth-bytes 3221225472 --max-temp-files 0
```

Gate mode refuses durations below 259,200 seconds and requires the workload,
canary, anchor, and resource-bound inputs above. Short development runs must
use `--mode smoke`; their result is explicitly `nonqualifying-smoke`.
Every five minutes the runner captures health, status, metrics, effective
systemd unit/drop-ins/environment,
journal, RSS, file descriptors, cache usage, temp files, and capacity. It
checks exact version and immutable binary/config/unit hashes, restarts every
six hours, atomically replaces `progress.json`, and produces `result.json`
plus evidence hashes. The anchor hook must copy the supplied pre-anchor
manifest off-host. It receives the manifest SHA-256 as its third argument and
must write `anchor-receipt.json` containing that exact value as
`preanchor_sha256` and `"copied_off_host": true`. The manifest includes the
stopped cache content hashes. A failed run
cannot be resumed into a pass. Abort and
rollback hooks run only when explicitly supplied.

The workload hook maintains `workload-summary.json` in the evidence root.
Its `ecosystems` object must contain `npm`, `pip`, `uv`, and `oci`;
each has integer `cold`, `warm`, and `post_restart` counts of at least
one plus `"integrity_pass": true`. Gate mode rejects a missing or incomplete
summary. The runner hashes hook and canary-definition files without copying or
printing canary values.
Review every hook, keep it root-owned and non-writable by other users, and
prove it with a nonqualifying smoke before starting the gate.

## Gates

Pass requires at least 259,200 seconds and 12 ledgered planned restarts; the
same candidate checksum, config, effective unit, and dedicated cache location;
successful cold/warm/post-restart clients;
matching SRI, installed hashes, and OCI digests; health recovery; bounded RSS,
FD, cache and temp growth within explicit bounds; at least 10% and 2 GiB free
space throughout; no unplanned process start,
panic, integrity error, canary finding, stale temp/orphan accumulation, or
unexplained server error; and a stopped final cache content manifest with
per-body hashes and integrity review. The anchor proves this evidence was
copied off-host; the runner does not claim a complete external cache archive.

Any integrity mismatch, corrupt metadata/body pair, unexpected restart, panic,
persistent unexplained 5xx, growing temp/orphan set, invariant change, or less
than 10% or 2 GiB free space fails the gate. At completion the runner stops the
service, scans complete body/metadata pairs, OCI digests, temps, orphans, and
canaries, records a quiesced listing, then restores the prior active state.
Pause workload after two health failures.
Preserve evidence before rollback or remediation.
