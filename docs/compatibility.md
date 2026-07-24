# Compatibility matrix

This matrix records verified behavior. An unchecked row is unknown, not
implicitly supported.

| Ecosystem | Client | Operation | Status | Evidence |
|---|---|---|---|---|
| npm | npm 11.16.0 | package metadata | Verified 2026-07-24 | Two clean-cache `npm view is-number version` runs; second served from n0ding |
| npm | npm 11.16.0 | tarball download | Verified 2026-07-24 | Two clean-cache `npm pack is-number@7.0.0` runs; integrity accepted and second served from n0ding |
| npm | npm CLI | scoped package | Not verified | Spike task |
| npm | npm CLI | install with lockfile | Not verified | Spike task |
| npm | npm CLI | private publish | Not implemented | Post-spike gate |
| OCI | Docker Engine 29.6.2 | `linux/amd64` image pull | Verified 2026-07-24 | Two clean-daemon pulls of `alpine:3.20`; second pull after n0ding restart produced 8 cache hits |
| OCI | Docker Engine 29.6.2 | manifest/blob digest integrity | Verified 2026-07-24 | Both pulls returned the same repo digest; zero n0ding digest errors |
| OCI | Docker Engine 29.6.2 | persistent local cache | Verified 2026-07-24 | 8 objects / 3,721,009 bytes survived n0ding restart; second clean client reused them |
| OCI | Podman | image pull | Not verified | Compatibility follow-up |
| OCI | Docker/Podman | private registry pull | Not verified | Auth/security follow-up |
| OCI | Docker/Podman | image push | Not implemented | Explicit non-goal for this spike |
| PyPI | pip/uv | package install | Not implemented | After npm/OCI gate |

Automated tests also exercise the same HTTP behavior against an in-process
upstream. The real-client check used Node.js 24.18.0 LTS with npm 11.16.0 and
separate empty npm client caches so that n0ding, rather than npm's local cache,
had to serve the second requests.

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
