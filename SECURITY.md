# Security policy

n0ding is an early proof of concept. Do not expose it directly to the public
internet or place it in a production software supply chain.

## Current boundaries

- There is no client authentication or authorization.
- npm upstream authorization is disabled by default.
- OCI Bearer credentials are forwarded to the configured upstream. A cached OCI
  response is served only after the upstream authorizes the same request with a
  `HEAD` response.
- Cached content is trusted as received from the configured upstream.
- OCI manifests and blobs are SHA-256 verified before cache commit.
- There is no malware, signature, provenance, or vulnerability scanning.
- There is no cache quota or automated retention cleanup.

For private testing, bind it to a trusted interface, put authentication and TLS
at a reverse proxy, and back up the cache only if it is costly to recreate.

Please report vulnerabilities privately to the repository owner. Do not open a
public issue containing secrets, exploit details, or user data.
