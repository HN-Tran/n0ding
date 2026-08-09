# PyPI read-only adapter design

Status: read-only adapter implemented and validated with pip and uv.

The PyPI adapter is included in the `v0.1.0` public-preview boundary. It reuses
the shared cache and credential-safety foundations instead of becoming a
generic reverse proxy.

## Protocol scope

The adapter implements the read-only
[PyPA Simple Repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/).
That API has HTML and JSON representations selected through content
negotiation, normalized project names, trailing-slash behavior, project pages,
distribution links, optional hash fragments, yanked markers,
`Requires-Python`, and metadata sidecars. The historical HTML baseline is
[PEP 503](https://peps.python.org/pep-0503/) and the JSON representation is
[PEP 691](https://peps.python.org/pep-0691/).

Publishing, the legacy upload API, warehouse administration, search, and
mirroring the complete project list are out of scope.

## Request surface

```text
/pypi/simple/                     optional root index pass-through/cache
/pypi/simple/<normalized-name>/   project detail HTML or JSON
/pypi/files/?url=...              rewritten distribution and metadata URLs
```

The original hash fragment remains visible to pip/uv, and n0ding copies a
supported `sha256` fragment into the query so it can verify the body before
committing a cached file. PyPI can return absolute, relative, or cross-host
distribution URLs, so file rewrites are limited to configured allowed origins.

## Resolved private-use decisions

### 1. HTML and JSON rewriting

The adapter accepts `golang.org/x/net/html` as a narrowly justified dependency
for Simple API HTML rewriting. JSON responses are decoded and rewritten as
structured data.

### 2. Distribution origin policy

Distribution links may use another host. The adapter allows the configured
upstream origin and explicit `allowed_file_origins`. It rejects direct
`/pypi/files/` requests for any other origin.

Redirects use the shared safe redirect client. Credentials survive only exact
same-origin redirects.

### 3. Integrity and immutability

When a file link includes a supported SHA-256 hash fragment, n0ding verifies
that digest before committing the distribution. The fragment remains visible to
pip/uv after URL rewriting.

Links without a usable hash are cached with the repository TTL and normal
cache-admission policy. They are not treated as immutable.

### 4. Content negotiation and cache keys

Project pages vary between HTML and JSON. `Accept` is part of the cache key.
Any additional upstream `Vary` field causes storage to be skipped by the
shared policy.

### 5. Private upstream authentication

PyPI uses the private upstream design selected for npm:

- explicit supported `Authorization` schemes;
- authenticated request cache bypass;
- no cookies, OTPs, or custom secrets forwarded by default;
- redirect protection and credential canary tests.

Authenticated PyPI requests are forwarded only when `forward_authorization` is
enabled and they bypass persistent caching.

## Minimum real-client matrix

- Current pip and uv, each with a clean client cache.
- Normalized and mixed-spelling project names.
- Wheel and source distribution.
- Requirements file with hashes.
- `Requires-Python` selection.
- Yanked release behavior.
- PEP 658/714 metadata sidecar when advertised.
- HTML and JSON representation paths selected by the final design.
- n0ding restart followed by a second clean-client install.
- One denied private-upstream identity after the auth model exists.

## Current evidence and remaining hardening

Current pip and uv clients install a wheel from separate empty client caches,
including the PEP 658/714 metadata sidecar path. The test repeats after a
n0ding restart and verifies persistent server-cache hits and matching installed
file hashes.

Additional hardening beyond the narrow v0.1.0 preview should cover:

1. normalized/mixed project names, sdists, yanked markers,
   `Requires-Python`, and metadata sidecars;
2. requirements-file hash enforcement with multiple distributions;
3. one private PyPI-compatible upstream after the broader private-provider
   evidence gate is approved.
