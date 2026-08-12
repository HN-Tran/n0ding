# PyPI proxy and private publishing design

Status: read-only adapter validated with pip and uv; optional private uploads
validated with Twine.

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

Warehouse administration, search, and mirroring the complete upstream project
list are out of scope.

## Request surface

```text
/pypi/simple/                     optional root index pass-through/cache
/pypi/simple/<normalized-name>/   project detail HTML or JSON
/pypi/files/?url=...              rewritten distribution and metadata URLs
/pypi/legacy/                     optional authenticated Twine upload
/pypi/packages/<project>/<file>   immutable private distributions
```

The original hash fragment remains visible to pip/uv, and n0ding copies a
supported `sha256` fragment into the query so it can verify the body before
committing a cached file. PyPI can return absolute, relative, or cross-host
distribution URLs, so file rewrites are limited to configured allowed origins.

## Optional private publishing

Setting `publish_token_file` enables the Twine-compatible legacy upload
endpoint. Basic authentication uses the token as the password (`__token__` is
the conventional username); Bearer authentication is also accepted. Uploaded
wheels, source archives, and zip distributions are stored below the repository
data directory and exposed through PEP 503 HTML and PEP 691 JSON project pages.

Publishing is disabled without a token file. Distribution filenames are
immutable: every repeated upload receives HTTP 409. A private project shadows
an upstream project with the same normalized name, preventing fallback-driven
dependency confusion. This private-self-use feature intentionally has no
users, RBAC, deletion, signing, scanning, replication, or Warehouse API.
Private distribution bytes participate in the global storage quota and
filesystem-reserve admission check. They are persistent artifacts and are not
removed by cache TTL garbage collection.

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
