# Compatibility matrix

This matrix records verified behavior. An unchecked row is unknown, not
implicitly supported.

| Ecosystem | Client | Operation | Status | Evidence |
|---|---|---|---|---|
| npm | npm 11.16.0 | package metadata | Verified 2026-07-24 | Two clean-cache `npm view is-number version` runs; second served from n0ding |
| npm | npm 11.16.0 | tarball download | Verified 2026-07-24 | Two clean-cache `npm pack is-number@7.0.0` runs; integrity accepted and second served from n0ding |
| npm | npm 11.16.0 | scoped package | Verified 2026-07-24 | `npm view @types/node@22.10.0` and installation through n0ding succeeded |
| npm | npm 11.16.0 | install with lockfile and SRI | Verified 2026-07-24 | Two clean-client `npm ci` runs accepted all SHA-512 integrity values; second run was 5/5 cache hits |
| npm | npm CLI | private publish | Not implemented | Post-spike gate |
| OCI | Docker Engine 29.6.2 | three `linux/amd64` image pulls | Verified 2026-07-24 | `alpine:3.20`, `nginx:1.27-alpine`, and `busybox:1.36` pulled before and after both services restarted |
| OCI | Docker Engine 29.6.2 | multi-arch index selection | Verified 2026-07-24 | `busybox:1.36` returned an OCI index and Docker selected its `linux/amd64` manifest |
| OCI | Docker Engine 29.6.2 | manifest/blob digest integrity | Verified 2026-07-24 | All three repo digests matched across clean daemons; normal pulls recorded zero errors |
| OCI | Docker Engine 29.6.2 | persistent local cache | Verified 2026-07-24 | 30 objects / 27,820,024 bytes survived n0ding and Docker-daemon replacement; second run produced 30 hits |
| OCI | Docker Engine 29.6.2 | Range/resume | Partially verified 2026-07-24 | No Range header on normal or interrupted-pull retry; explicit Range pass-through is covered by an automated 206 test |
| OCI | Podman | image pull | Unknown | `podman version` was unavailable on the Windows test host |
| OCI | Docker/Podman | private registry pull | Not verified | Auth/security follow-up |
| OCI | Docker/Podman | image push | Not implemented | Explicit non-goal for this spike |
| PyPI | pip/uv | package install | Not implemented | After npm/OCI gate |

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
- An ungraceful process kill can leave `.body-*` temporary files; startup
  scavenging is not implemented.
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
