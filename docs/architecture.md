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
- `internal/httpserver`: routing, status API, metrics, and minimal dashboard.

The spike intentionally uses no third-party Go packages.

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
full upstream response has been received. Requests for the same missing key are
coalesced in-process.

OCI manifests are buffered up to 16 MiB and verified before they are sent or
cached. OCI blobs stream to the client and a temporary cache file while their
SHA-256 digest is calculated. The temporary file is committed only if the
calculated digest matches the digest in the request or
`Docker-Content-Digest`. Docker clients independently verify the received
content as part of a pull.

The current TTL is lazy: an expired object becomes a miss and is replaced when
requested. Background deletion, quotas, and configurable retention policies are
post-spike work.

## Trust boundaries

The spike assumes a trusted local network. It does not authenticate clients.
Client authorization is not forwarded upstream unless explicitly enabled.
When authorization forwarding is enabled for a request, that response is not
read from or written to the shared cache.

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
- no automated eviction
- metadata rewrite limit of 64 MiB
- OCI manifest limit of 16 MiB
- SHA-256 OCI digests only
- no OCI push, catalog, deletion, referrers, or signature policy

These are spike boundaries, not promises for version 1.

## Next architecture gate

The npm and OCI client spikes now pass. The next architecture gate is hardening,
not protocol breadth: tag-mutation races, interrupted/range downloads, cache
retention, private-registry behavior, and compatibility tests across Docker and
Podman. PyPI should wait until those operating properties remain simple.
