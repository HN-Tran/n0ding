# Client setup

n0ding does not change package-manager or Docker settings during installation.
Configure only the clients you want to route through it. The examples below
use the default loopback deployment at `http://localhost:8080`.

## npm

For a project-local setup, create `.npmrc` next to `package.json`:

```ini
registry=http://localhost:8080/npm/
```

Or set the current user's registry:

```sh
npm config set registry http://localhost:8080/npm/
```

Use npm normally:

```sh
npm view @types/node version
npm ci
```

n0ding rewrites package tarball URLs to its configured `public_base_url`, so
that URL must be reachable from the client. Responses expose
`X-N0ding-Cache: MISS` or `X-N0ding-Cache: HIT`.

## pip and uv

Use n0ding's PyPI Simple API endpoint:

```sh
python -m pip install --index-url http://localhost:8080/pypi/simple/ requests
uv pip install --index-url http://localhost:8080/pypi/simple/ requests
```

To make the pip setting persistent for the current user:

```sh
python -m pip config set global.index-url http://localhost:8080/pypi/simple/
```

Public PyPI distribution links are cached only when
`https://files.pythonhosted.org` is present in `allowed_file_origins`. The
shipped deployment configuration includes that origin. When PyPI provides a
SHA-256 fragment, n0ding verifies it before committing the file to cache.

## Docker / OCI

Docker expects registries to use trusted TLS. For local evaluation over HTTP,
add `localhost:8080` to the Docker daemon's `insecure-registries` list and
restart Docker. In Docker Desktop this setting is under **Settings → Docker
Engine**:

```json
{
  "insecure-registries": ["localhost:8080"]
}
```

Pull a Docker Hub image through n0ding:

```sh
docker pull localhost:8080/library/alpine:3.20
```

Official Docker Hub images retain the `library/` namespace. For shared
deployments, use trusted TLS instead of an insecure-registry exception. See
the [operations guide](operations.md) for reverse-proxy and private-CA
options, or use the [authenticated public deployment profile](public-vps.md).

## Verify caching

Repeat a request with a fresh client cache, then inspect n0ding:

```sh
curl http://localhost:8080/api/v1/status
curl http://localhost:8080/metrics
```

The status response reports requests, cache hits and misses, stored objects,
bytes, errors, and client cancellations for each repository.
