# Compatibility matrix

This matrix records verified behavior. An unchecked row is unknown, not
implicitly supported.

| Ecosystem | Client | Operation | Status | Evidence |
|---|---|---|---|---|
| npm | npm 11.16.0 | package metadata | Verified 2026-07-24 | Two clean-cache `npm view is-number version` runs; second served from n0ding |
| npm | npm 11.16.0 | tarball download | Verified 2026-07-24 | Two clean-cache `npm pack is-number@7.0.0` runs; integrity accepted and second served from n0ding |
| npm | npm 11.16.0 | scoped package | Revalidated 2026-07-25 | `npm view @types/node@22.10.0` and installation through n0ding succeeded |
| npm | npm 11.16.0 | install with lockfile and SRI | Revalidated 2026-07-25 | Two clean-client `npm ci` runs accepted all SHA-512 integrity values; second run was 5/5 cache hits |
| npm | npm CLI | private publish | Not implemented | Excluded from the read-only MVP |
| OCI | Docker Engine 29.6.2 | three `linux/amd64` image pulls | Revalidated 2026-07-25 | `alpine:3.20`, `nginx:1.27-alpine`, and `busybox:1.36` pulled before and after both services restarted |
| OCI | Docker Engine 29.6.2 | multi-arch index selection | Revalidated 2026-07-25 | `busybox:1.36` returned an OCI index and Docker selected its `linux/amd64` manifest |
| OCI | Docker Engine 29.6.2 | manifest/blob digest integrity | Revalidated 2026-07-25 | All three repo digests matched across clean daemons; normal pulls recorded zero errors |
| OCI | Docker Engine 29.6.2 | persistent local cache | Revalidated 2026-07-25 | 30 objects / 27,820,024 bytes survived n0ding and Docker-daemon replacement; second run produced 30 hits |
| OCI | Docker Engine 29.6.2 | Range/resume | Revalidated 2026-07-25 | No Range header on normal or interrupted-pull retry; explicit Range pass-through is covered by an automated 206 test |
| OCI | Podman | image pull | Unknown | `podman version` was unavailable on the Windows test host |
| OCI | Docker/Podman | private registry pull | Not verified | Auth/security follow-up |
| OCI | Docker/Podman | image push | Not implemented | Explicit non-goal for this spike |
| PyPI | pip/uv | package install | Not implemented | Planned only after the private [PyPI design gate](pypi-design.md) |

Automated tests also exercise the same HTTP behavior against an in-process
upstream. The real-client check used Node.js 24.18.0 LTS with npm 11.16.0 and
separate empty npm client caches so that n0ding, rather than npm's local cache,
had to serve the second requests.

## Compatibility-hardening spike

- Test date: 2026-07-24
- Host engine: Docker Desktop Engine 29.5.3
- Pull clients: clean Docker Engine 29.6.2 daemons (`docker:29-dind`)
- npm client: Node.js 24.18.0 with npm 11.16.0
- Platform: `linux/amd64`
- n0ding design: one binary, one TOML file, one local filesystem volume, no
  database

The scoped npm package behavior follows npm's documented
[`@scope/name` client syntax](https://docs.npmjs.com/using-npm/scope.html/).
The lockfile run used [`npm ci`](https://docs.npmjs.com/cli/commands/npm-ci/),
which requires an existing lockfile and does not rewrite it. OCI object
classification follows the
[OCI Distribution Specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md)'s
index, manifest, config, blob, digest, and optional Range model.

### npm scoped package, lockfile, and integrity

The committed fixture is in `testdata/npm-compat`. It pins:

- `@types/node@22.10.0`
- `is-number@7.0.0`
- transitive `undici-types@6.20.0`

Its lockfile contains the upstream SHA-512 integrity values and n0ding tarball
URLs. These are the core commands used for the first clean server-cache run:

```powershell
docker build --tag n0ding:compat-spike .
docker network create n0ding-hardening
docker volume create n0ding-hardening-data
docker run --detach --name n0ding-hardening-server `
  --network n0ding-hardening --network-alias n0ding `
  --env N0DING_PUBLIC_URL=http://127.0.0.1:18080 `
  --publish 127.0.0.1:18080:8080 `
  --volume n0ding-hardening-data:/data n0ding:compat-spike

$root = (Get-Location).Path
$npm = Join-Path $root `
  ".tmp\node-v24.18.0\node-v24.18.0-win-x64\npm.cmd"
$run1 = Join-Path $root ".tmp\npm-compat-run1"
$cache1 = Join-Path $root ".tmp\npm-client-cache-1"
New-Item -ItemType Directory -Path $run1
Copy-Item testdata\npm-compat\package.json $run1
Copy-Item testdata\npm-compat\package-lock.json $run1
& $npm view "@types/node@22.10.0" version `
  --registry http://127.0.0.1:18080/npm/ `
  --cache $cache1 --prefer-online
Push-Location $run1
& $npm ci --ignore-scripts --no-audit --no-fund `
  --registry http://127.0.0.1:18080/npm/ `
  --cache $cache1 --prefer-online
& $npm ls --depth=1
Pop-Location
```

Then n0ding was restarted and a different project directory and empty npm
client cache were used:

```powershell
docker restart n0ding-hardening-server
$run2 = Join-Path $root ".tmp\npm-compat-run2"
$cache2 = Join-Path $root ".tmp\npm-client-cache-2"
New-Item -ItemType Directory -Path $run2
Copy-Item testdata\npm-compat\package.json $run2
Copy-Item testdata\npm-compat\package-lock.json $run2
& $npm view "@types/node@22.10.0" version `
  --registry http://127.0.0.1:18080/npm/ `
  --cache $cache2 --prefer-online
Push-Location $run2
& $npm ci --ignore-scripts --no-audit --no-fund `
  --registry http://127.0.0.1:18080/npm/ `
  --cache $cache2 --prefer-online
Pop-Location
```

Measured results:

| Measurement | First clean client/cache | Second clean client after n0ding restart |
|---|---:|---:|
| Scoped package version | `22.10.0` | `22.10.0` |
| Installed packages | 3 | 3 |
| Cache hits | 0 | 5 |
| Cache misses | 5 | 0 |
| Errors | 0 | 0 |
| Stored objects | 5 | 5 |
| Stored bytes | 13,956,353 | 13,956,353 |
| Lockfile SHA-256 before/after | `3A270B9B…CC957F` / unchanged | `3A270B9B…CC957F` / unchanged |

Both `npm ci` runs accepted all three SHA-512 integrity values. The second run
used no bytes from the first npm client cache and received all five requests
from n0ding's persisted filesystem cache.

### OCI image matrix and restart persistence

The first pass used a new Docker daemon and an empty n0ding volume:

```powershell
docker rm --force n0ding-hardening-server
docker volume rm n0ding-hardening-data
docker volume create n0ding-hardening-data
docker run --detach --name n0ding-hardening-server `
  --network n0ding-hardening --network-alias n0ding `
  --env N0DING_PUBLIC_URL=http://n0ding:8080 `
  --volume n0ding-hardening-data:/data n0ding:compat-spike
docker run --detach --privileged --name n0ding-hardening-dind `
  --network n0ding-hardening docker:29-dind `
  --insecure-registry=n0ding:8080

$images = @(
  "n0ding:8080/library/alpine:3.20",
  "n0ding:8080/library/nginx:1.27-alpine",
  "n0ding:8080/library/busybox:1.36"
)
foreach ($image in $images) {
  docker exec n0ding-hardening-dind docker pull `
    --platform linux/amd64 $image
  docker exec n0ding-hardening-dind docker image inspect $image `
    --format '{{index .RepoDigests 0}}'
}
docker exec n0ding-hardening-server wget -qO- `
  http://localhost:8080/api/v1/status
```

First-pass cumulative measurements:

| After image | Repo digest | Hits | Misses | Errors | Range | Objects | Bytes |
|---|---|---:|---:|---:|---:|---:|---:|
| `alpine:3.20` | `sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc` | 0 | 9 | 0 | 0 | 8 | 3,721,009 |
| `nginx:1.27-alpine` | `sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10` | 0 | 25 | 0 | 0 | 23 | 25,600,266 |
| `busybox:1.36` | `sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662` | 0 | 33 | 0 | 0 | 30 | 27,820,024 |

The cached metadata and bodies were classified directly from the local
filesystem:

```text
3 OCI image indexes
6 OCI image manifests
21 application/octet-stream blobs
  6 config digests referenced by the manifests
  15 layer digests referenced by the manifests
```

Every referenced config and layer digest had a corresponding digest-verified
cached blob. `busybox:1.36` exercised an OCI image index and platform-manifest
selection, not only a single manifest.

Both n0ding and the Docker daemon were then replaced while the n0ding volume
was retained:

```powershell
docker rm --force n0ding-hardening-dind n0ding-hardening-server
docker run --detach --name n0ding-hardening-server `
  --network n0ding-hardening --network-alias n0ding `
  --env N0DING_PUBLIC_URL=http://n0ding:8080 `
  --volume n0ding-hardening-data:/data n0ding:compat-spike
docker run --detach --privileged --name n0ding-hardening-dind `
  --network n0ding-hardening docker:29-dind `
  --insecure-registry=n0ding:8080
```

Repeating the same three `docker pull` and `docker image inspect` commands
produced:

| After image | Digest match | Hits | Misses | Errors | Range | Objects | Bytes |
|---|---|---:|---:|---:|---:|---:|---:|
| `alpine:3.20` | Yes | 8 | 1 | 0 | 0 | 30 | 27,820,024 |
| `nginx:1.27-alpine` | Yes | 23 | 2 | 0 | 0 | 30 | 27,820,024 |
| `busybox:1.36` | Yes | 30 | 3 | 0 | 0 | 30 | 27,820,024 |

The three misses were uncached request variants. All stored representations
were reused; neither the object count nor storage bytes changed.

### OCI Range and interrupted pull

Range handling is intentionally isolated: n0ding forwards the request and
upstream `206 Partial Content`/`Content-Range` response, increments
`range_requests`, and does not write a partial object into the complete-object
cache. `TestBlobRangeRequestIsForwardedAndNotCached` checks this twice against
an instrumented upstream.

Real Docker sent no Range header during the six normal pulls above. To test a
retry, a larger pull was started and only the isolated n0ding container was
killed once its cache had grown by at least 4 MiB:

```powershell
$dockerExe = (Get-Command docker).Source
$pull = Start-Process -FilePath $dockerExe -ArgumentList @(
  "exec", "n0ding-hardening-dind", "docker", "pull",
  "--platform", "linux/amd64", "n0ding:8080/library/node:24"
) -WindowStyle Hidden -PassThru

# Poll `docker exec n0ding-hardening-server du -sk /data/oci`.
# At a delta >= 4096 KiB:
docker kill --signal KILL n0ding-hardening-server
docker start n0ding-hardening-server
docker exec n0ding-hardening-dind docker pull --platform linux/amd64 `
  n0ding:8080/library/node:24
```

Measured result:

```text
Controlled interruption: yes
Server-cache growth before kill: 4,816 KiB
Docker error: short read; expected 16,079,377 bytes, got 1,440,022
Retry Range requests: 0
Retry digest: sha256:5711a0d445a1af54af9589066c646df387d1831a608226f4cd694fc59e745059
Retry completed: yes
```

Docker reused already completed content but restarted incomplete blobs without
Range. Two retry requests were canceled by the client after concurrent content
resolution and are counted as `context canceled` errors; no digest mismatch
occurred. The forced process kill left seven `.body-*` temporary files.

## Known limitations

- Podman was not installed on the test host, so Podman compatibility is
  unknown.
- Range responses are pass-through only. n0ding has no sparse/partial blob
  cache and Docker 29.6.2 did not use Range for the tested resume.
- An ungraceful process kill can leave `.body-*` temporary files. Later MVP
  hardening added startup cleanup once they exceed `storage.stale_temp_age`.
- A cache hit still performs an upstream authorization/digest `HEAD`, so the
  OCI cache is not an offline registry.
- Cache keys include the client's `Accept` header, which can retain multiple
  valid representations of related content.
- The test registry used plain HTTP inside an isolated Docker network. Shared
  deployments require TLS.

## OCI pull-through spike

- Test date: 2026-07-24
- Host: Docker Desktop Engine 29.5.3
- Pull clients: two separate Docker Engine 29.6.2 daemons (`docker:29-dind`)
- Platform: `linux/amd64`
- Upstream: `https://registry-1.docker.io`
- Image: `library/alpine:3.20`

Two separate Docker daemons were used so the second pull could not reuse the
first daemon's image or content store. n0ding was restarted between pulls while
its named cache volume was retained.

### Exact setup and first pull

```powershell
docker build --tag n0ding:oci-spike .
docker network create n0ding-oci-spike
docker volume create n0ding-oci-spike-data
docker run --detach --name n0ding-oci-spike-server `
  --network n0ding-oci-spike --network-alias n0ding `
  --env N0DING_PUBLIC_URL=http://n0ding:8080 `
  --volume n0ding-oci-spike-data:/data n0ding:oci-spike
docker run --detach --privileged --name n0ding-oci-spike-dind `
  --network n0ding-oci-spike docker:29-dind `
  --insecure-registry=n0ding:8080
docker exec n0ding-oci-spike-dind docker pull --platform linux/amd64 `
  n0ding:8080/library/alpine:3.20
docker exec n0ding-oci-spike-dind docker image inspect `
  n0ding:8080/library/alpine:3.20 --format '{{index .RepoDigests 0}}'
docker exec n0ding-oci-spike-server wget -qO- `
  http://localhost:8080/api/v1/status
```

First-pull result:

```text
Digest: sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
cache_hits: 0
cache_misses: 9
errors: 0
cache_objects: 8
storage_bytes: 3721009
```

### Exact restart and second clean-client pull

```powershell
docker rm --force n0ding-oci-spike-dind
docker restart n0ding-oci-spike-server
docker run --detach --privileged --name n0ding-oci-spike-dind `
  --network n0ding-oci-spike docker:29-dind `
  --insecure-registry=n0ding:8080
docker exec n0ding-oci-spike-dind docker pull --platform linux/amd64 `
  n0ding:8080/library/alpine:3.20
docker exec n0ding-oci-spike-dind docker image inspect `
  n0ding:8080/library/alpine:3.20 --format '{{index .RepoDigests 0}}'
docker exec n0ding-oci-spike-server wget -qO- `
  http://localhost:8080/api/v1/status
```

Second-pull result after the n0ding restart:

```text
Digest: sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
cache_hits: 8
cache_misses: 1
hit_ratio: 0.8888888888888888
errors: 0
cache_objects: 8
storage_bytes: 3721009
```

The identical digest proves the client accepted the cached content. The
unchanged object count and byte count after restart prove that the cache was
filesystem-backed rather than process-local. The one miss was a request variant
that was not present after the first pull; all eight stored manifest/blob
representations were reused.

The test uses an isolated HTTP registry, so the nested daemon explicitly enables
`--insecure-registry=n0ding:8080`. Shared deployments must use TLS. Cache hits
still perform an authenticated upstream `HEAD` request before local bytes are
served; this spike proves pull-through byte caching, not offline operation.

### Cleanup used after the measurement

```powershell
docker rm --force n0ding-oci-spike-dind n0ding-oci-spike-server
docker network rm n0ding-oci-spike
docker volume rm n0ding-oci-spike-data
docker image rm docker:29-dind n0ding:oci-spike
```

## MVP hardening revalidation

Revalidated 2026-07-25 after adding startup cleanup, age-based GC, and
store-level concurrency synchronization.

### npm

Core commands:

```powershell
& $npm view "@types/node@22.10.0" version `
  --registry http://127.0.0.1:18080/npm/ `
  --cache .tmp\npm-mvp-cache1 --prefer-online
& $npm ci --ignore-scripts --no-audit --no-fund `
  --registry http://127.0.0.1:18080/npm/ `
  --cache .tmp\npm-mvp-cache1 --prefer-online

docker restart n0ding-mvp-server

# Repeat from a copied fixture and a separate empty npm cache:
& $npm view "@types/node@22.10.0" version `
  --registry http://127.0.0.1:18080/npm/ `
  --cache .tmp\npm-mvp-cache2 --prefer-online
& $npm ci --ignore-scripts --no-audit --no-fund `
  --registry http://127.0.0.1:18080/npm/ `
  --cache .tmp\npm-mvp-cache2 --prefer-online
```

| Measurement | First run | Second run after restart |
|---|---:|---:|
| Scoped version | `22.10.0` | `22.10.0` |
| Installed packages | 3 | 3 |
| Hits / misses | 0 / 5 | 5 / 0 |
| Errors | 0 | 0 |
| Objects / bytes | 5 / 13,956,353 | 5 / 13,956,353 |

### OCI

Core commands:

```powershell
$images = @(
  "n0ding:8080/library/alpine:3.20",
  "n0ding:8080/library/nginx:1.27-alpine",
  "n0ding:8080/library/busybox:1.36"
)
foreach ($image in $images) {
  docker exec n0ding-mvp-dind docker pull --platform linux/amd64 $image
  docker exec n0ding-mvp-dind docker image inspect $image `
    --format '{{index .RepoDigests 0}}'
}

docker rm --force n0ding-mvp-dind n0ding-mvp-server
# Recreate both containers with the existing n0ding-mvp-data volume.
# Repeat the same pull and inspect loop.
```

| Measurement | First clean daemon | Second clean daemon after both restarts |
|---|---:|---:|
| Matching digests | 3/3 | 3/3 |
| Hits / misses | 0 / 39 | 30 / 9 |
| Errors / Range | 0 / 0 | 0 / 0 |
| Objects / bytes | 30 / 27,820,024 | 30 / 27,820,024 |

Digests:

```text
alpine:3.20       sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
nginx:1.27-alpine sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10
busybox:1.36      sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
```

The revalidation environment was removed after the measurement. Podman remains
unavailable on this host.

## v0.1-private shared-cache safety revalidation

Revalidated 2026-07-26 after introducing the shared HTTP/cache policy.

The first real npm run exposed an important compatibility requirement:
`registry.npmjs.org` explicitly returned `Cache-Control: public` while
Cloudflare also attached a `Set-Cookie` edge cookie. n0ding now permits storage
only because the response is explicitly public, while stripping the cookie
from persistent metadata. Cookie-bearing responses without `public`, and all
`private`, `no-store`, or `no-cache` responses, remain non-cacheable.

Focused automated tests additionally prove:

- npm does not forward client cookies or OTP headers;
- npm forwards `Authorization` only when configured and never caches that
  authenticated request;
- OCI forwards its required `Authorization` header but not client cookies;
- private or credential-bearing responses are fetched twice and create zero
  cache objects;
- unsupported `Vary` fields and `Vary: *` prevent storage;
- cache metadata strips cookie/authentication fields even if an adapter passes
  them to the store;
- status output removes upstream URL userinfo, query, and fragment.

### npm real-client result

Client: npm 11.16.0. The committed scoped-package/lockfile fixture was installed
twice with separate empty npm client caches. n0ding restarted between runs and
retained only its isolated Docker volume.

| Measurement | First clean client | Second clean client after restart |
|---|---:|---:|
| Cache hits | 0 | 5 |
| Cache misses | 5 | 0 |
| Complete objects | 5 | 5 |
| Storage bytes | 13,956,353 | 13,956,353 |
| Packages installed | 3 | 3 |

Result: scoped metadata, rewritten tarball URLs, lockfile SHA-512 integrity,
public edge-cookie handling, and persistent cache reuse all passed.

### OCI real-client result

Client: two separate `docker:29-dind` daemons on Docker Desktop Engine 29.5.3.
Image: `library/alpine:3.20`, platform `linux/amd64`. Both n0ding and the Docker
daemon restarted while the isolated n0ding volume was retained.

| Measurement | First clean daemon | Second clean daemon after restart |
|---|---:|---:|
| Cache hits | 0 | 8 |
| Cache misses | 11 | 3 |
| Complete objects | 8 | 8 |
| Storage bytes | 3,721,009 | 3,721,009 |

Both pulls produced:

```text
sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
```

The unchanged object/byte counts and matching digest show that the new
credential/header policy did not break OCI cache persistence or integrity.
All temporary containers, networks, volumes, and npm client directories were
removed after the measurement.

## v0.1-private identity-safety fixtures

Validated 2026-07-26 with in-process private-upstream fixtures. These tests are
policy evidence, not real private-registry compatibility claims.

Commands:

```sh
go test -count=1 -run 'TestAuthorizedNPMIdentities|TestCrossOriginRedirect|TestProxyFailure' ./internal/npmproxy
go test -count=1 -run 'TestPrivateOCIIdentities|TestOCICacheHitRequires|TestDeniedToken' ./internal/ociproxy
go test -count=1 -run 'TestClientWithSafeRedirects|TestPublicUpstreamURL|TestSafeError' ./internal/httppolicy
go test -count=1 -run TestStatusRedactsUpstreamCredentialComponents ./internal/httpserver
```

Results:

- npm identity A, identity B, identity A again, and a denied identity all
  reached the fixture independently, returned `MISS`, and left zero persistent
  cache objects;
- each npm identity received only its own body;
- OCI identities with different tag digests never received the other
  identity's manifest; a missing `Docker-Content-Digest` on authorization
  `HEAD` also forced a fresh `GET`;
- the existing same-digest OCI fixture still permits a hit for another
  authorized identity after exact digest confirmation;
- raw cache files contained none of the request token or query canaries;
- configured URL userinfo/query canaries were absent from status, proxy failure
  logs, and client-visible proxy errors;
- `Authorization` survived only exact-origin redirects. Cross-origin redirects
  remained usable without it, while cookie, OTP, proxy, forwarded-identity, and
  Docker auth-transport headers stayed stripped.

Still unproven in that adapter-only slice: real private npm/OCI products, Basic
auth, provider token refresh/revocation, identity-provider behavior,
registry/CDN redirect chains, and operator backup/log canary scans.

### Deterministic full-server private-upstream drill

Added 2026-07-26 as an end-to-end companion to the adapter tests:

```sh
go test -race -count=1 -run TestPrivateUpstreamDrill ./internal/httpserver
```

Unlike the earlier adapter-only fixtures, this test sends every client request
through the real n0ding server wiring with both npm and OCI repositories
enabled. It proves deterministically:

- npm identities A and B receive only their own fixture body; denied and
  revoked A fail; all eight credentialed requests are misses and create zero
  npm cache objects;
- OCI identity B receives a shared object only after its own successful
  upstream `HEAD` returns the exact cached digest;
- a changed digest and a missing `Docker-Content-Digest` each perform a fresh
  identity-B `GET` instead of serving identity-A bytes;
- denied and revoked OCI identity B cannot use existing cached bytes, while
  still-authorized A can use the same cached object without restarting n0ding;
- npm and OCI cross-origin redirects complete without forwarding
  `Authorization`, including a deterministic proxy-failure redirect;
- four complete OCI objects survive a stopped fixture cache copy; an authorized
  OCI hit reuses the restored bytes from a fresh directory while revoked B is
  still denied;
- raw cache bodies/metadata, the stopped copy, restored copy, status, metrics,
  structured logs, and client-visible proxy errors contain none of the fake
  token, URL-userinfo, query, or authentication-response canaries.

The stopped copy/restore is security and cache-reuse fixture coverage. It is
not the pending Docker Compose operational backup/restore drill.

### Real private services

Status: **not run**. No external-service credential was available or used in
this change, so the compatibility table still marks private OCI and real
private-upstream behavior as unverified.

The exact disposable setup, npm and Docker commands, provider revocation
pause, expected signals, credential-canary scan, and cleanup are committed in
[private-upstream-drill.md](private-upstream-drill.md). A future run must add
dated provider/client evidence here without committing credentials or raw
private artifacts.
