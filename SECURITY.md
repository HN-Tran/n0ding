# Security policy

n0ding is an early proof of concept. Do not expose it directly to the public
internet or place it in a production software supply chain.

## Current boundaries

- There is no client authentication or authorization.
- Upstream authorization is disabled by default.
- Cached content is trusted as received from the configured upstream.
- There is no malware, signature, provenance, or vulnerability scanning.
- There is no cache quota or automated retention cleanup.

For private testing, bind it to a trusted interface, put authentication and TLS
at a reverse proxy, and back up the cache only if it is costly to recreate.

Please report vulnerabilities privately to the repository owner. Do not open a
public issue containing secrets, exploit details, or user data.
