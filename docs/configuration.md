# Configuration reference

n0ding reads one TOML file. The parser intentionally supports only the fields
documented here. Unknown fields fail startup instead of being ignored.
Environment variables are expanded inside string values.

[`config/n0ding.example.toml`](../config/n0ding.example.toml) is a commented
local starting point. The container uses
[`config/n0ding.toml`](../config/n0ding.toml), where Compose supplies
`N0DING_PUBLIC_URL`.

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
```

Validate a file without starting the listener:

```sh
n0ding -config /etc/n0ding/n0ding.toml -check-config
```

## Server

| Key | Default | Meaning |
|---|---|---|
| `listen` | `:8080` | HTTP listen address inside the process |
| `public_base_url` | `http://localhost:8080` | Client-visible base URL used in npm tarball rewrites and setup snippets |
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

All durations use Go duration syntax, for example `30m`, `24h`, or `720h`, and
must be positive.

Repository `ttl` and storage `max_age` solve different problems:

- `ttl` controls freshness. An expired lookup is fetched again and atomically
  replaces the same cache key.
- `max_age` controls disk retention. Startup and periodic GC remove old,
  complete body/metadata pairs even if they are never requested again.

GC is intentionally age-based because the existing metadata already records a
trusted commit timestamp. It does not implement LRU or a strict byte quota.
Accessing an object does not extend its `max_age`; a refetch does.

Startup cleanup only considers cache-created temporary filenames older than
`stale_temp_age`. Runtime GC ignores all temporary files and deletes an object
only when both its metadata and body exist, metadata parses, and the recorded
body size matches the file.

## Repositories

The table name determines the repository name, such as `npm` in
`[repository.npm]`.

| Key | Default | Meaning |
|---|---|---|
| `type` | table name | Supported values are `npm` and `oci` |
| `path` | `/<name>/` | Client-facing path; OCI must be exactly `/v2/` |
| `upstream` | none | Absolute HTTP(S) upstream URL |
| `ttl` | `24h` | Freshness duration for cached responses |
| `forward_authorization` | `false` | npm-only opt-in for upstream credentials |

When npm `forward_authorization` is enabled, authenticated requests bypass the
shared npm cache. OCI has separate Registry V2 Bearer handling: tokens are
forwarded but never persisted, and cache hits require an authorized upstream
`HEAD`.

Only one OCI repository can exist because the standard Registry V2 client path
is fixed at `/v2/`.

## Credential and cache-safety behavior

- `forward_authorization = false` is the npm default.
- Enabling it accepts the client's `Authorization` header as the only supported
  npm credential header; authenticated npm responses bypass persistent caching.
- OCI forwards `Authorization` because Registry V2 pulls require it. Cookies,
  proxy credentials, npm OTP fields, forwarded identity headers, and Docker
  registry-auth transport headers are stripped for both adapters.
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
See the [threat model](threat-model.md) and
[private roadmap](release-checklist.md).
