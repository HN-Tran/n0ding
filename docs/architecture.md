# Architecture

Status: spike architecture, 2026-07-24.

## Request flow

```text
npm client
   |
   | standard registry HTTP requests
   v
n0ding /npm/
   |-- cache hit --> persistent local body + response metadata
   |
   `-- cache miss --> configured npm upstream
                         |
                         `--> atomic local cache write
```

n0ding uses standard npm client behavior. No client plugin is required. Package
metadata is rewritten so upstream tarball URLs point back through the configured
n0ding repository.

## Components

- `cmd/n0ding`: configuration loading, lifecycle, and graceful shutdown.
- `internal/config`: strict parser for the supported TOML subset.
- `internal/npmproxy`: npm-aware reverse proxy, URL rewriting, request
  coalescing, and counters.
- `internal/cache`: filesystem persistence with hashed keys and atomic writes.
- `internal/httpserver`: routing, status API, metrics, and minimal dashboard.

The spike intentionally uses no third-party Go packages.

## Cache model

Each cache key contains the repository, absolute upstream URL, query string, and
the client's `Accept` header. The `Accept` variation matters because npm can ask
for abbreviated or full package metadata.

Bodies and JSON response metadata are kept separately:

```text
data/
  npm/
    ab/
      ab...42.body
      ab...42.json
```

Writes go to temporary files in the target shard and are renamed only after the
full upstream response has been received. Requests for the same missing key are
coalesced in-process.

The current TTL is lazy: an expired object becomes a miss and is replaced when
requested. Background deletion, quotas, and configurable retention policies are
post-spike work.

## Trust boundaries

The spike assumes a trusted local network. It does not authenticate clients.
Client authorization is not forwarded upstream unless explicitly enabled.
When authorization forwarding is enabled for a request, that response is not
read from or written to the shared cache.

TLS and user authentication should terminate at a trusted reverse proxy until a
unified auth layer exists.

## Deliberate constraints

- npm only
- `GET` and `HEAD` only
- local filesystem storage only
- one process; no distributed locking
- no private publish path
- no automated eviction
- metadata rewrite limit of 64 MiB

These are spike boundaries, not promises for version 1.

## Next architecture gate

After validating npm with real clients, implement a separate OCI experiment.
The shared HTTP/cache/storage primitives may be reused, but registry-specific
protocol behavior must remain in an adapter. Do not force OCI into npm's
metadata model.
