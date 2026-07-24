# Operations guide

The read-only MVP is one process, one configuration file, and one local
filesystem cache. Run exactly one n0ding process against a cache directory;
shared-volume multi-writer operation is unsupported.

For failure symptoms and diagnostic commands, see
[the troubleshooting guide](troubleshooting.md).

## Docker Compose quickstart

For local evaluation:

```sh
docker compose config --quiet
docker compose up --build -d
docker compose ps
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
```

The Compose project uses a named volume mounted at `/data`. A normal
`docker compose down` removes containers and the project network but preserves
that named volume. `docker compose down --volumes` deletes the cache.

Set the client-visible URL before using a hostname or reverse proxy:

```sh
N0DING_PUBLIC_URL=https://packages.example.com docker compose up --build -d
```

On PowerShell:

```powershell
$env:N0DING_PUBLIC_URL = "https://packages.example.com"
docker compose up --build -d
```

## TLS and reverse proxy

n0ding serves HTTP itself. Terminate TLS at a trusted reverse proxy and keep
port 8080 on a private network or loopback interface. The proxy must preserve
the request method, path, query, `Authorization`, `Accept`, `Range`, and
`WWW-Authenticate` response headers.

A minimal Caddyfile is:

```caddyfile
packages.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

Caddy documents both
[automatic HTTPS](https://caddyserver.com/docs/automatic-https) and its
[reverse proxy behavior](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy).

A minimal nginx server block is:

```nginx
server {
    listen 443 ssl;
    server_name packages.example.com;

    ssl_certificate     /etc/nginx/tls/fullchain.pem;
    ssl_certificate_key /etc/nginx/tls/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header Authorization $http_authorization;
        proxy_http_version 1.1;
        proxy_buffering off;
        proxy_read_timeout 900s;
    }
}
```

See nginx's official
[`proxy_pass` documentation](https://nginx.org/en/docs/http/ngx_http_proxy_module.html).

Configure `public_base_url`/`N0DING_PUBLIC_URL` as
`https://packages.example.com`. Docker expects registries to use trusted TLS.
For a private CA, install its CA certificate for the registry hostname as
described in Docker's
[registry certificate guide](https://docs.docker.com/engine/security/certificates/).
Docker's `insecure-registry` mode is appropriate only for isolated testing;
the [dockerd reference](https://docs.docker.com/reference/cli/dockerd/#insecure-registries)
describes the security tradeoff.

## Backup and restore

The cache is disposable: deleting it loses performance, not source artifacts.
Back up the configuration separately because it is not stored in `/data`.

For a consistent cache backup, stop n0ding first so no commit can occur between
copying a body and its metadata:

```sh
docker compose stop n0ding
docker run --rm \
  --volumes-from "$(docker compose ps --all -q n0ding)" \
  -v "$PWD:/backup" \
  alpine:3.22 \
  tar -czf /backup/n0ding-cache.tgz -C /data .
docker compose start n0ding
```

Docker's official
[volume backup and restore guide](https://docs.docker.com/engine/storage/volumes/#back-up-restore-or-migrate-data-volumes)
uses the same temporary-container pattern.

Restore into a new empty volume first:

```sh
docker volume create n0ding-restore
docker run --rm \
  -v n0ding-restore:/data \
  -v "$PWD:/backup:ro" \
  alpine:3.22 \
  tar -xzf /backup/n0ding-cache.tgz -C /data
```

Then point a stopped test deployment at `n0ding-restore` and validate
`/healthz`, `/api/v1/status`, one npm request, and one OCI pull before replacing
the original volume. Restoring into a non-empty live cache is unsupported.

The body and metadata format is intentionally backup-friendly, but it is not a
stable cross-version storage API yet. If restore fails, start with an empty
cache and let clients repopulate it.

## Concurrency model

- Requests for the same cache key are coalesced inside one n0ding process.
  Automated tests use eight simultaneous npm requests and eight simultaneous
  OCI requests and observe one upstream body fetch.
- OCI still performs an upstream authorization/digest `HEAD` for each waiting
  client before serving cached bytes. Those duplicate HEADs are acceptable for
  the MVP.
- Different `Accept` headers are different keys and can intentionally fetch
  different representations.
- Multiple n0ding processes can duplicate upstream fetches and must not share a
  writable cache directory. There is no distributed lock.
- Atomic temporary-file commits and digest verification prevent an incomplete
  response from becoming a complete cached object.

## Known limitations

- n0ding is not an offline mirror. OCI cache hits still depend on an upstream
  authorization/digest `HEAD`, and cache misses always need the upstream.
- There is no private publish support.
- There is no n0ding client authentication, user management, or RBAC. Only
  upstream credential handling exists.
- Podman remains untested.
- Range requests are proxied but partial responses are not cached.
- Retention is maximum age from commit time, not LRU and not a strict size
  quota.
- There is no multi-process or shared-volume locking.
- Only local filesystem storage is supported.
