# Architecture

Status: `v0.1-private` read-only hardening architecture, 2026-07-26.

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

Docker / OCI client
   |
   | Registry HTTP API V2 (/v2/)
   v
n0ding OCI adapter
   |-- authorize upstream with HEAD
   |-- cache hit --> verified manifest/blob on local storage
   |
   `-- cache miss --> OCI registry --> verify SHA-256 --> atomic cache commit
```

n0ding uses standard npm client behavior. No client plugin is required. Package
metadata is rewritten so upstream tarball URLs point back through the configured
n0ding repository.

The OCI adapter implements the pull subset of the OCI Distribution
Specification. Registry authentication challenges are passed through unchanged,
client Bearer credentials are forwarded upstream, and only successful manifest
and blob `GET` responses are cached. Push methods remain disabled.

## Components

- `cmd/n0ding`: configuration loading, lifecycle, and graceful shutdown.
- `internal/config`: strict parser for the supported TOML subset.
- `internal/repository`: the small protocol-adapter interface and shared status
  shape.
- `internal/npmproxy`: npm-aware reverse proxy, URL rewriting, request
  coalescing, and counters.
- `internal/ociproxy`: OCI pull proxy, auth-challenge forwarding, manifest/blob
  classification, and SHA-256 verification.
- `internal/cache`: filesystem persistence with hashed keys and atomic writes.
- `internal/httppolicy`: shared credential-forwarding, cache-admission, header
  persistence, and public-upstream-display rules.
- `internal/httpserver`: routing, status API, metrics, startup maintenance, and
  periodic GC scheduling.

The `v0.1-private` baseline intentionally uses no third-party Go packages.

## Cache model

Each cache key contains the repository, absolute upstream URL, query string, and
the client's `Accept` header. The `Accept` variation matters because npm can ask
for abbreviated or full package metadata and OCI registries use content
negotiation for image indexes and manifests.

Bodies and JSON response metadata are kept separately:

```text
data/
  npm/
    ab/
      ab...42.body
      ab...42.json
  oci/
    cd/
      cd...91.body
      cd...91.json
```

Writes go to temporary files in the target shard and are renamed only after the
full upstream response has been received. Store-level synchronization keeps
lookup, final commit, and GC deletion from observing one another halfway
through a filesystem change.

OCI manifests are buffered up to 16 MiB and verified before they are sent or
cached. OCI blobs stream to the client and a temporary cache file while their
SHA-256 digest is calculated. The temporary file is committed only if the
calculated digest matches the digest in the request or
`Docker-Content-Digest`. Docker clients independently verify the received
content as part of a pull.

Repository TTL remains lazy: an expired object becomes a miss and is replaced
when requested. Physical retention is separate:

- startup removes cache-created temporary files older than
  `storage.stale_temp_age`;
- startup and periodic GC remove complete objects older than
  `storage.max_age`;
- GC runs every `storage.gc_interval`;
- GC ignores every temporary file and skips malformed, incomplete, or
  size-mismatched object pairs.

Age is measured from atomic cache commit, not last access. The MVP deliberately
does not implement LRU or a strict size quota.

## Concurrency model

Requests for the same cache key are coalesced by an in-process keyed lock. One
client fetches and commits the body; waiters re-check the cache after acquiring
the lock. npm waiters are served directly from the resulting entry. OCI waiters
each perform the required upstream authorization/digest `HEAD` before local
bytes are served.

Duplicate upstream body fetches for one exact key are not expected inside one
process. Duplicate OCI `HEAD` requests, requests with different `Accept`
headers, and duplicate fetches across separate n0ding processes are acceptable
MVP behavior. Multiple processes must not share a writable cache directory
because there is no distributed lock.

## Trust boundaries

The preview assumes a trusted local network. It does not authenticate clients.
Client authorization is not forwarded upstream unless explicitly enabled.
When authorization forwarding is enabled for a request, that response is not
read from or written to the shared cache.

The shared HTTP policy strips known client credential/identity headers except
an adapter-approved `Authorization` header. It rejects persistent storage for
private/no-store/no-cache responses, authentication metadata, unsafe cookies,
and `Vary` dimensions outside the adapter's cache key. Cookie-bearing responses
must be explicitly public, and the cache store still strips their cookie fields
before writing JSON metadata.

OCI is the exception because Registry V2 pulls require Bearer tokens. n0ding
forwards those tokens but does not store them. Before serving an OCI cache hit,
it sends an upstream `HEAD` request with the current token. A cached object is
served only when the upstream confirms access. This retains upstream dependency
for authorization while keeping manifest and layer bytes local.

TLS and user authentication should terminate at a trusted reverse proxy until a
unified auth layer exists.

## Deliberate constraints

- npm proxy cache and OCI pull-through cache only
- `GET` and `HEAD` only
- local filesystem storage only
- one process; no distributed locking
- no private publish path
- age-based retention only; no byte quota or LRU
- metadata rewrite limit of 64 MiB
- OCI manifest limit of 16 MiB
- SHA-256 OCI digests only
- no OCI push, catalog, deletion, referrers, or signature policy

These are MVP boundaries.

## Next architecture gate

The next slice is explicit private-upstream credential design and canary
testing, not another protocol adapter. Recovery, soak, real-client/TLS, and
PyPI work then follow the ordered
[v0.1-private roadmap](release-checklist.md).

PyPI has a separate [design gate](pypi-design.md). Private publishing remains
outside the read-only architecture.
