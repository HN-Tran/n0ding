# Configuration reference

n0ding reads one TOML file. The parser intentionally supports only the fields
documented here. Unknown fields fail startup instead of being ignored.
Environment variables are expanded inside string values.

[`config/n0ding.example.toml`](../config/n0ding.example.toml) is a commented
local starting point. The container uses
[`config/n0ding.toml`](../config/n0ding.toml), where Compose supplies
`N0DING_PUBLIC_URL`. The manual private-upstream drill uses
[`config/n0ding.private-drill.toml`](../config/n0ding.private-drill.toml);
it contains environment placeholders but intentionally no credential field.

## Complete MVP example

```toml
[server]
listen = ":8080"
public_base_url = "https://packages.example.com"
log_level = "info"

[storage]
path = "/data"
max_age = "720h"
gc_interval = "1h"
stale_temp_age = "1h"
max_bytes = 107374182400
high_watermark = 0.90
low_watermark = 0.75
min_free_bytes = 10737418240

[operator]
token_file = "/run/secrets/n0ding-operator-token"

[repository.npm]
type = "npm"
path = "/npm/"
upstream = "https://registry.npmjs.org"
ttl = "24h"
forward_authorization = false

[repository.oci]
type = "oci"
path = "/v2/"
upstream = "https://registry-1.docker.io"
ttl = "1h"

[repository.pypi]
type = "pypi"
path = "/pypi/simple/"
upstream = "https://pypi.org/simple"
ttl = "24h"
allowed_file_origins = "https://files.pythonhosted.org"
forward_authorization = false
# publish_token_file = "../secrets/pypi-publish-token"
```

Validate a file without starting the listener:

```sh
n0ding -config /etc/n0ding/n0ding.toml -check-config
```

## Server

| Key | Default | Meaning |
|---|---|---|
| `listen` | `:8080` | HTTP listen address inside the process |
| `public_base_url` | `http://localhost:8080` | Client-visible base URL used in npm/PyPI rewrites and setup snippets |
| `log_level` | `info` | `debug`, `info`, `warn`, or `error` |

`public_base_url` must be the URL used by clients. Behind a TLS reverse proxy it
must therefore start with `https://` and name the reverse proxy, not the
internal n0ding container.

## Storage and retention

| Key | Default | Meaning |
|---|---|---|
| `path` | `./data` | Root for repository-specific filesystem caches |
| `max_age` | `720h` | Delete complete objects this old, measured from cache commit time |
| `gc_interval` | `1h` | Interval between runtime GC passes |
| `stale_temp_age` | `1h` | Minimum age for `.body-*` and `.metadata-*` cleanup during startup |
| `max_bytes` | `0` | Shared byte budget across every repository; `0` disables the byte budget |
| `high_watermark` | `0.90` | Usage ratio that activates pressure GC |
| `low_watermark` | `0.75` | Pressure GC target ratio |
| `min_free_bytes` | `0` | Filesystem headroom reserved before cache admission |

All durations use Go duration syntax, for example `30m`, `24h`, or `720h`, and
must be positive.

Repository `ttl` and storage `max_age` solve different problems:

- `ttl` controls freshness. An expired lookup is fetched again and atomically
  replaces the same cache key.
- `max_age` controls disk retention. Startup and periodic GC remove old,
  complete body/metadata pairs even if they are never requested again.

Age GC uses the trusted commit timestamp. When the shared budget reaches the
high watermark, pressure GC removes the globally least-recently-used complete
objects across npm, PyPI, and OCI until usage reaches the low watermark.
Accessing an object updates its pressure-GC access hint but does not extend its
`max_age`; a refetch does.

The default `720h` is a compatibility-oriented starting value, not a capacity
recommendation. Age alone cannot bound disk use when unique package/image
ingress is unbounded. Private-alpha operators must select `max_age` from their
volume capacity and credible ingress rate and monitor real filesystem free
space. See the [retention policy decision](retention-policy.md).

Known download sizes are atomically reserved before cache writes. Downloads
with unknown sizes, objects that do not fit the budget, and objects that would
consume reserved filesystem headroom are proxied without caching. Parallel
downloads therefore cannot independently overbook the same available bytes.

Startup cleanup only considers cache-created temporary filenames older than
`stale_temp_age`. Runtime GC ignores all temporary files and deletes an object
only when both its metadata and body exist, metadata parses, and the recorded
body size matches the file.

## Operator authorization

| Key | Default | Meaning |
|---|---|---|
| `token_file` | empty | File containing the bearer token for mutating operator endpoints |

`[operator]` is optional. Without `token_file`, mutating operator routes are
not registered as usable and return `404`. The token must contain 32 to 4096
non-whitespace bytes. Keep the file outside source control with owner-only
permissions. Read-only status, metrics, and dashboard access remain governed
by the deployment network or mTLS boundary.

## Repositories

The table name determines the repository name, such as `npm` in
`[repository.npm]`.

| Key | Default | Meaning |
|---|---|---|
| `type` | table name | Supported values are `npm`, `oci`, and `pypi` |
| `path` | `/<name>/` | Client-facing path; OCI must be exactly `/v2/`, and PyPI must end in `/simple/` |
| `upstream` | none | Absolute HTTP(S) upstream URL |
| `ttl` | `24h` | Freshness duration for cached responses |
| `forward_authorization` | `false` | npm/PyPI opt-in for upstream credentials |
| `allowed_file_origins` | empty | PyPI-only comma-separated HTTP(S) origins whose distribution files may be proxied and cached |
| `publish_token_file` | empty | PyPI-only token file enabling immutable private uploads at `/pypi/legacy/` |

When npm or PyPI `forward_authorization` is enabled, authenticated requests
bypass the shared persistent cache. OCI has separate Registry V2 Bearer
handling: tokens are forwarded but never persisted, and cache hits require an
authorized upstream `HEAD`.

Only one OCI repository can exist because the standard Registry V2 client path
is fixed at `/v2/`.

PyPI implements the read-only Simple Repository API for project pages and
distribution files. HTML and JSON project pages are rewritten so allowed
distribution origins point through the repository's `/files/` sibling path. A
configured `[repository.pypi]` path of `/pypi/simple/` therefore uses
`/pypi/files/` for cached distributions. File origins are intentionally
allowlisted to avoid turning the adapter into an open fetch proxy. When a link
contains a `#sha256=...` fragment, the file body must match before n0ding
commits it to cache.

`publish_token_file` is resolved relative to the configuration file. The file
must contain a 32-4096 byte token. Keep it outside source control and restrict
its filesystem permissions. When configured, Twine can publish with
`--repository-url <base>/pypi/legacy/ -u __token__ -p <token>`. Existing
distribution filenames cannot be replaced.

## Credential and cache-safety behavior

- `forward_authorization = false` is the npm and PyPI default.
- Enabling it accepts the client's `Authorization` header as the only supported
  npm/PyPI credential header; authenticated npm/PyPI responses bypass
  persistent caching.
- OCI forwards `Authorization` because Registry V2 pulls require it. Cookies,
  proxy credentials, npm OTP fields, forwarded identity headers, and Docker
  registry-auth transport headers are stripped for all adapters.
- On redirects, `Authorization` is retained only for the exact same scheme,
  host, and effective port. Cross-origin registry/CDN redirects remain allowed
  but receive no client credentials; redirect URL userinfo is discarded.
- An OCI cache hit requires a successful upstream `HEAD` with the exact cached
  digest. A missing or different digest forces an upstream miss.
- Responses marked `private`, `no-store`, or `no-cache`, responses carrying
  authentication metadata, and responses with unsupported `Vary` dimensions
  are not cached. Cookie-bearing responses require explicit
  `Cache-Control: public`.
- Known credential-bearing headers are removed again before cache metadata is
  written, including cookies on explicitly public responses.
- Status and explicit upstream/error URL log fields omit userinfo, query, and
  fragment. The config file and process memory do not; do not put secrets in
  the file until the private-upstream secret-input design is complete.

Cookie, OTP, proxy, and arbitrary custom-header authentication are unsupported.
Client `Authorization` forwarding is the only fixture-tested private npm/PyPI
input; OCI fixture coverage currently uses Bearer tokens. Basic-auth and real
private upstreams remain unverified. Follow the
[disposable private-upstream drill](private-upstream-drill.md) rather than
putting credentials into an upstream URL.
See the [threat model](threat-model.md) and
[private roadmap](release-checklist.md).
