# Operator experience

n0ding combines the focused deployment model of a small Go service with the
most useful operating ideas from Harbor, Nexus Repository, and Artifactory. It
does not aim to reproduce their complete feature sets or user interfaces.

## v0.1 principles

- One binary, one writable cache volume, and no external database.
- npm, PyPI, and OCI share one observable storage budget.
- Quotas protect host availability without turning an oversized artifact into
  a failed client download. Content that cannot safely enter the cache is
  proxied without being retained.
- Garbage collection combines maximum age with recent use and explicit
  high/low watermarks.
- Concurrent downloads reserve capacity before writing cache data.
- Active readers are never invalidated by collection.
- The API is the source of truth. The embedded UI renders the same state and
  does not maintain a second model.
- Public deployments protect the UI and administrative API with the same mTLS
  boundary as repository traffic.

## Storage controller

The storage controller owns a repository-wide budget rather than independent
per-protocol guesses. Its state includes:

- committed cache bytes and objects;
- bytes reserved by in-flight downloads;
- configured maximum bytes;
- high and low watermarks;
- minimum filesystem free space;
- cache-bypass counts and bytes;
- the last and next scheduled collection.

When usage reaches the high watermark, collection removes eligible least
recently used objects until the low watermark is reached. Maximum age remains
an independent eligibility rule. An object larger than the available safe
budget is streamed to the client but is not committed to cache storage.

## Garbage collection

Every collection records its trigger, start and finish time, examined objects,
removed objects and bytes, skipped active readers, invalid entries, errors, and
the usage before and after the run. Supported triggers are startup, schedule,
quota pressure, and an authenticated operator request.

Only one collection may run at a time. A manual request starts the same engine
as scheduled and pressure-triggered collection. v0.1 does not expose arbitrary
artifact deletion.

## API and UI

The embedded operator UI provides:

- overall health, version, uptime, and storage pressure;
- quota usage and filesystem reserve;
- repository request, hit, miss, error, object, and byte statistics;
- current and previous garbage-collection state;
- cache-bypass activity;
- client setup snippets;
- a guarded manual collection action.

The interface remains useful on a phone-sized screen and requires no separate
frontend build or runtime. All values come from versioned JSON endpoints and
are also represented in Prometheus metrics where appropriate.

## Deferred

Native users and roles, publishing, replication, universal artifact search,
arbitrary deletion, private-provider compatibility, vulnerability scanning,
and additional repository formats are outside the v0.1 boundary.
