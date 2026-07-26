# Retention policy decision

Status: accepted for `v0.1-private` alpha use with operational conditions,
2026-07-26. This is not a production capacity guarantee.

## Decision

`v0.1-private` keeps age-only retention. The default remains:

```toml
[storage]
max_age = "720h"
gc_interval = "1h"
stale_temp_age = "1h"
```

There is no `max_bytes` setting and no claim that n0ding bounds total disk
usage. Age-only retention is acceptable for this private alpha only when the
operator:

- treats the cache as disposable;
- gives `/data` a dedicated filesystem or volume with known capacity;
- monitors actual filesystem free space as well as n0ding's complete-object
  metric;
- chooses `max_age` from the available capacity and worst credible ingress
  rate rather than accepting `720h` blindly;
- stops n0ding before clearing or replacing a cache under disk pressure.

The roadmap keeps disk exhaustion as an open acceptance gate until the real
seven-day soak has passed and the intended private deployment has a recorded
capacity/alert drill. A strict internal byte quota remains a later hardening
option, not a hidden requirement that this decision pretends is implemented.

## Why age alone is not a size bound

The retained complete-object bytes are approximately bounded by:

```text
unique cacheable ingress rate × max_age
```

Neither term has a configured upper bound. A burst of new OCI layers or npm
tarballs can therefore fill the volume before the first objects reach
`max_age`. A single in-flight object can also consume the remaining space
before it becomes a complete cache entry.

If the filesystem fills, an active cache write or atomic commit can fail. The
client may receive a partial/failed response and retry; n0ding should not serve
the incomplete cache file later, but the same full filesystem can also affect
logs, other containers, or the host when storage is not isolated.

`n0ding_repository_storage_bytes` counts only complete, parseable,
size-matched bodies. It deliberately excludes metadata overhead, temporary
writes, malformed/orphaned files, filesystem allocation overhead, and
unrelated data on the volume. It is therefore useful for cache trends but is
not a disk-free-space alarm.

## Operator guardrails

Record both the logical complete-cache size and real volume usage:

```sh
curl -fsS http://localhost:8080/metrics
docker compose exec -T n0ding du -sk /data
docker compose exec -T n0ding df -Pk /data
```

For the private alpha, use these minimum alert thresholds:

- warning when the filesystem has less than 20% free;
- critical when it has less than 10% free;
- warning when the observed growth rate projects exhaustion before the next
  `max_age` window can expire.

The exact thresholds can be stricter for a volume shared with anything else.
An external filesystem/volume quota provides a stronger host boundary, but
Docker named volumes do not expose one portable quota mechanism across
supported hosts.

If growth is unsafe:

1. lower `storage.max_age`;
2. restart n0ding so startup GC applies the new age immediately;
3. confirm `/api/v1/status`, `/metrics`, `du`, and `df` converge as expected;
4. if space remains critical, stop n0ding and replace the disposable cache
   volume rather than deleting individual object files while it is running.

The default `720h` is a compatibility-oriented starting value, not a capacity
recommendation. For example, a dedicated 20 GiB volume with only 15 GiB
allocated to cache and a credible 3 GiB/day unique ingress rate should use no
more than roughly five days before additional safety margin.

## Why a strict byte limit is not a small patch

The current store has a natural safe deletion primitive for complete
body/metadata pairs. Sorting those pairs by `stored_at` and deleting the oldest
would be straightforward, but it would not create a strict disk limit.
A correct implementation also needs:

1. **One aggregate budget.** `storage.path` contains separate npm and OCI
   stores. A storage-wide limit needs one coordinator rather than independently
   granting the full limit to each repository.
2. **In-flight reservations.** Temporary bytes must count while an upstream
   response is streaming. Unknown-length or over-limit objects must continue
   to the client without being persisted; reaching the cache limit must not
   truncate a valid proxy response.
3. **Active-reader handling.** Eviction must not break an open cache hit.
   Unix can unlink an open body while Windows normally cannot, so explicit
   entry leases or a deferred-delete model are required for consistent
   behavior.
4. **Atomic complete-pair eviction.** New lookups must never observe metadata
   without its body. Delete failures must leave a safe miss and must not claim
   that the configured bound was achieved.
5. **All disk consumers.** Temporary, malformed, orphaned, metadata, and
   allocation-overhead bytes need accounting or a separate cleanup/quarantine
   policy. Counting only valid bodies is not a strict filesystem bound.
6. **Observable pressure.** Status and metrics need configured capacity,
   current accounted bytes, eviction count/bytes, skipped active entries, and
   an explicit over-limit signal.

Until those properties and concurrent failure tests exist, adding a periodic
size-sort pass would be named a best-effort target, not `max_bytes`.

## Integrity rules for future eviction

Size eviction must operate only on complete cache objects that pass the same
metadata/body size validation used by lookup and `Size`. It must ignore active
temporary files and must never edit a body.

- For npm, eviction removes the complete body and metadata pair. The next
  request is an ordinary miss/refetch, and npm continues to enforce any
  lockfile or package-integrity value it has.
- For OCI, only objects already digest-verified at cache commit are eligible.
  Eviction removes the complete pair. A later miss must fetch and verify the
  digest again before a new commit; an authenticated hit still requires the
  existing upstream digest-confirming `HEAD`.

Eviction therefore changes availability and hit rate, not package bytes,
identity keys, npm integrity checks, or OCI digest verification.
