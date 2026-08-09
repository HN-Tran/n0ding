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

pip / uv client
   |
   | PyPI Simple API requests
   v
n0ding /pypi/simple/
   |-- cache hit --> rewritten Simple page or distribution file
   |
   `-- cache miss --> configured Simple upstream
                         |
                         `--> rewrite allowed file links through /pypi/files/
                              and verify SHA-256 fragments before cache commit
```

n0ding uses standard npm client behavior. No client plugin is required. Package
metadata is rewritten so upstream tarball URLs point back through the configured
n0ding repository.

The OCI adapter implements the pull subset of the OCI Distribution
Specification. Registry authentication challenges are passed through unchanged,
client Bearer credentials are forwarded upstream, and only successful manifest
and blob `GET` responses are cached. Push methods remain disabled.

The PyPI adapter implements the read-only Simple Repository API. It rewrites
HTML and JSON project pages, proxies distribution files only from configured
allowed origins, and verifies SHA-256 fragments before cache commit when the
Simple page supplies them. Publishing methods remain disabled.

## Components

- `cmd/n0ding`: configuration loading, lifecycle, and graceful shutdown.
- `internal/config`: strict parser for the supported TOML subset.
- `internal/repository`: the small protocol-adapter interface and shared status
  shape.
- `internal/npmproxy`: npm-aware reverse proxy, URL rewriting, request
  coalescing, and counters.
- `internal/ociproxy`: OCI pull proxy, auth-challenge forwarding, manifest/blob
  classification, and SHA-256 verification.
- `internal/pypiproxy`: PyPI Simple API proxy, HTML/JSON rewriting, distribution
  file allowlisting, SHA-256 verification, and counters.
- `internal/cache`: filesystem persistence with hashed keys and atomic writes.
- `internal/httppolicy`: shared credential-forwarding, cache-admission, header
  persistence, and public-upstream-display rules.
- `internal/httpserver`: routing, status API, metrics, startup maintenance, and
  periodic GC scheduling.

The baseline is almost entirely standard-library Go. PyPI HTML rewriting uses
the narrowly scoped `golang.org/x/net/html` parser rather than ad hoc string
replacement.

## Cache model

Each cache key contains the repository, absolute upstream URL, query string, and
the client's `Accept` header. The `Accept` variation matters because npm can ask
for abbreviated or full package metadata, OCI registries use content
negotiation for image indexes and manifests, and PyPI project pages can be HTML
or JSON.

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
  pypi/
    ef/
      ef...17.body
      ef...17.json
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

PyPI Simple pages are buffered up to 64 MiB for structured rewriting. PyPI
distribution files stream to the client and cache together. When the rewritten
link carries a SHA-256 fragment, the temporary file is committed only if the
calculated digest matches that fragment.

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
does not implement LRU or a strict size quota. This is conditionally accepted
for `v0.1-private` only with a disposable, capacity-monitored cache. A safe
future aggregate limit needs in-flight reservations, active-reader handling,
repository-wide coordination, and accounting beyond valid complete bodies;
the design boundary is recorded in
[retention-policy.md](retention-policy.md).

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

Both adapters use the same redirect policy. `Authorization` is retained only
when the redirect keeps the exact scheme, host, and effective port. A
cross-origin redirect remains usable for registry/CDN downloads but proceeds
without client credentials. Unsupported cookie, OTP, proxy, forwarded-identity,
and Docker auth-transport headers remain stripped on every redirect.

OCI is the exception because Registry V2 pulls require Bearer tokens. n0ding
forwards those tokens but does not store them. Before serving an OCI cache hit,
it sends an upstream `HEAD` request with the current token. A cached object is
served only when that response is successful and carries the exact, non-empty
cached digest. Missing or different digest confirmation forces a fresh
upstream `GET`. This retains upstream dependency for authorization while
keeping content-addressed manifest and layer bytes local.

TLS and user authentication should terminate at a trusted reverse proxy until a
unified auth layer exists.

## Deliberate constraints

- npm proxy cache and OCI pull-through cache only
- `GET` and `HEAD` only
- local filesystem storage only
- one process; no distributed locking
- no private publish path
- age-based retention only; no byte quota or LRU
- npm/PyPI metadata rewrite limit of 64 MiB
- OCI manifest limit of 16 MiB
- SHA-256 OCI digests and PyPI fragments only
- no OCI push, catalog, deletion, referrers, or signature policy
- no PyPI publish, search, project-list mirroring, or administration API

These are MVP boundaries.

## Next architecture gate

The short retention/concurrency smoke, stopped recovery drill, and retention
policy decision are complete. The next retention gate is one uninterrupted
seven-day run followed by deployment-specific disk-capacity evidence and
explicit acceptance of the missing byte quota. Real private-provider and
real-client/TLS work remain ordered separately in the
[v0.1-private roadmap](release-checklist.md).

PyPI has a separate [design note](pypi-design.md) for implemented adapter
decisions and remaining pip/uv evidence. Private publishing remains outside
the read-only architecture.
