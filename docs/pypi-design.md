# PyPI read-only adapter design gate

Status: design only; no PyPI runtime support exists.

PyPI is planned for `v0.1-private`, but it must reuse the shared cache and
credential-safety foundations instead of becoming a generic reverse proxy.

## Protocol scope

The adapter should implement the read-only
[PyPA Simple Repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/).
That API has HTML and JSON representations selected through content
negotiation, normalized project names, trailing-slash behavior, project pages,
distribution links, optional hash fragments, yanked markers,
`Requires-Python`, and metadata sidecars. The historical HTML baseline is
[PEP 503](https://peps.python.org/pep-0503/) and the JSON representation is
[PEP 691](https://peps.python.org/pep-0691/).

Publishing, the legacy upload API, warehouse administration, search, and
mirroring the complete project list are out of scope.

## Proposed request surface

```text
/pypi/simple/                     optional root index pass-through/cache
/pypi/simple/<normalized-name>/   project detail HTML or JSON
/pypi/files/...                   rewritten distribution and metadata URLs
```

The exact external file path is not decided. PyPI can return absolute,
relative, or cross-host distribution URLs, so blindly replacing one upstream
origin is insufficient.

## Blocking design decisions

### 1. HTML and JSON rewriting

The JSON representation can be decoded and rewritten with the Go standard
library. Correct HTML5 rewriting needs a real parser; the standard library has
no HTML5 parser. Before implementation, choose one of:

- accept `golang.org/x/net/html` as a narrowly justified dependency;
- require JSON-capable clients and explicitly reject HTML-only compatibility;
- implement a constrained link proxy that does not parse HTML only if real pip
  evidence proves it correct.

String replacement is not acceptable for arbitrary HTML.

### 2. Distribution origin policy

Distribution links may use another host. The adapter needs an allowlist derived
from the configured index, explicit additional origins, or a signed mapping.
It must not become an open fetch proxy.

Redirects must be checked against the same policy and must not forward
credentials to a different origin.

### 3. Integrity and immutability

When a file link includes a supported hash fragment, n0ding should verify that
digest before committing the distribution. The fragment must remain visible to
pip/uv after URL rewriting.

For links without a usable hash, decide whether to:

- pass through without caching; or
- cache with a short TTL and an upstream revalidation rule.

Treating an unhashed filename as immutable is not acceptable.

### 4. Content negotiation and cache keys

Project pages vary between HTML and JSON. `Accept` is already part of the cache
key. Any additional upstream `Vary` field must either be represented in the
adapter key or cause storage to be skipped by the shared policy.

### 5. Private upstream authentication

PyPI must not invent a third credential model. It should use the private
upstream design selected for npm:

- explicit supported `Authorization` schemes;
- authenticated request cache bypass or identity partitioning;
- no cookies, OTPs, or custom secrets forwarded by default;
- redirect protection and credential canary tests.

Private PyPI support cannot start before the shared private-auth gate.

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

## Implementation gate

Do not add a `pypi` repository type until:

1. the five blocking decisions above are resolved in this document;
2. fixtures cover both selected representation paths and cross-origin links;
3. cache-key, redirect, and credential behavior have negative tests;
4. the dependency decision is explicit;
5. the threat model is updated with the final design.
