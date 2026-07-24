# Security policy

n0ding is an early read-only MVP. Do not expose it directly to the public
internet or treat it as a complete production supply-chain security control.

## Current boundaries

- There is no client authentication or authorization.
- npm upstream authorization is disabled by default.
- OCI Bearer credentials are forwarded to the configured upstream. A cached OCI
  response is served only after the upstream authorizes the same request with a
  `HEAD` response.
- Cached content is trusted as received from the configured upstream.
- OCI manifests and blobs are SHA-256 verified before cache commit.
- There is no malware, signature, provenance, or vulnerability scanning.
- Retention is age-based and does not enforce a strict disk quota.
- Range responses are proxied but not partially cached.
- The local cache supports one n0ding process, not shared-volume writers.

Bind it to a trusted interface, terminate TLS at a reverse proxy, and back up
the cache only if it is costly to recreate. See
[`docs/operations.md`](docs/operations.md) for deployment boundaries.

Please report vulnerabilities privately to the repository owner. Do not open a
public issue containing secrets, exploit details, or user data.
