# Operations guide

The `v0.1.0` public-preview baseline is one process, one
configuration file, and one local filesystem cache for npm, OCI, and PyPI
package loading. Run exactly one n0ding process against a cache directory;
shared-volume multi-writer operation is unsupported.

For failure symptoms and diagnostic commands, see
[the troubleshooting guide](troubleshooting.md).

For a concise private-alpha operator gate before repeated self-use, see the
[private self-use checklist](private-self-use.md).

## Docker Compose quickstart

For local evaluation:

```sh
docker compose config --quiet
docker compose up --build -d
docker compose ps
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
```

The root source-build Compose file publishes only on `127.0.0.1:8080` by
default. The project uses a named volume mounted at `/data`. A normal
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

For access from a trusted private network, an operator may explicitly set
`N0DING_BIND_ADDRESS` to the required host address. This bypasses the safe
loopback default; n0ding has no built-in client authentication or RBAC. For an
internet-reachable host, do not publish this source-build service directly:
use the [authenticated public VPS profile](public-vps.md).

## TLS and reverse proxy

n0ding serves HTTP itself. Terminate TLS at a trusted reverse proxy and keep
port 8080 on loopback (preferred) or a trusted private network. The proxy must
preserve the request method, path, query, `Authorization`, `Accept`, `Range`,
and `WWW-Authenticate` response headers.

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

The repository includes a deterministic, same-version recovery drill:

```powershell
.\tools\backup-restore-drill.ps1
```

It stops n0ding, archives cache plus config, restores into a new empty volume,
revalidates npm lockfile integrity and OCI digests, exercises rollback and a
corrupt-object refetch, and scans the archive and restored evidence for fake
credential canaries. pip and uv compatibility are exercised separately by the
release-candidate smoke across a persistent restart. See the
[stopped Compose backup/restore drill](backup-restore-drill.md) for its exact
scope and measured result.

For a consistent cache backup, stop n0ding first so no commit can occur between
copying a body and its metadata. Verify the service is stopped before copying:

```sh
docker compose stop n0ding
test "$(docker inspect --format '{{.State.Running}}' \
  "$(docker compose ps --all -q n0ding)")" = "false"
docker run --rm \
  --volumes-from "$(docker compose ps --all -q n0ding)" \
  -v "$PWD:/backup" \
  alpine:3.22 \
  tar -cf /backup/n0ding-cache.tar -C /data .
docker compose start n0ding
```

Docker's official
[volume backup and restore guide](https://docs.docker.com/engine/storage/volumes/#back-up-restore-or-migrate-data-volumes)
uses the same temporary-container pattern.

The example stays uncompressed so the existing streaming canary scanner can
inspect the archive directly. Keep the archive and restored tree in the scan
set; do not assume that scanning only filenames or only a compressed wrapper
examines its contents.

Restore into a new empty volume first:

```sh
docker volume create n0ding-restore
docker run --rm \
  -v n0ding-restore:/data \
  -v "$PWD:/backup:ro" \
  alpine:3.22 \
  tar -xf /backup/n0ding-cache.tar -C /data
```

Validate the archive members before extraction and preserve the original
volume until recovery is accepted. Then point a stopped test deployment at
`n0ding-restore` and validate:

- `/healthz`, `/api/v1/status`, and `/metrics`;
- `npm ci` from a committed lockfile with an empty npm client cache;
- one `pip install` or `uv pip install` from an empty client cache when PyPI is
  enabled;
- one OCI request or pull and its recorded digest;
- authorized and denied requests when private upstream handling is enabled;
- logs and restored state with disposable credential canaries.

Restoring into a live or non-empty volume, merging archives, and sharing the
volume between processes are unsupported. The body and metadata format is
backup-friendly but is not a stable cross-version storage API.

Missing bodies are cache misses. Malformed metadata, truncated bodies, and
metadata/body size disagreement are neither counted nor served. On access,
n0ding logs the lookup failure and attempts a fresh upstream fetch. If the
upstream cannot repair the object, switch back to the untouched source volume
or discard the restored cache and repopulate it. Do not delete or edit
individual objects while n0ding is running.

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

## Retention and concurrency soak

Run the committed short smoke before changing cache, GC, proxy streaming, or
identity policy:

```powershell
.\tools\retention-soak.ps1 -Mode Smoke
```

Start the seven-day stable/availability hardening gate only on a host that can
remain available for the full interval:

```powershell
.\tools\retention-soak.ps1 -Mode SevenDay
```

The runner uses an isolated Compose project, volume, local fixture, fake
credentials, and unique local image tags. It preserves evidence under
`.tmp/retention-soak/<id>` while removing only its generated Docker resources.
Do not treat a short or resumed run as seven-day evidence. Exact profiles,
pass/fail criteria, artifacts, and cleanup behavior are in the
[retention/concurrency soak guide](retention-soak.md).

The `v0.1.0` best-effort Public Preview instead requires the exact candidate to
pass the [native 72-hour real-use validation](native-public-preview-validation.md).
That systemd-oriented gate records real npm, pip, uv, and Docker use without
misrepresenting a shorter run as seven-day evidence.

## Disk capacity, byte budget, and retention

`v0.1.0` supports a shared `storage.max_bytes` budget for valid accounted
objects. Known-size writes reserve capacity before caching; pressure GC removes
global least-recently-used complete cache entries from the high watermark to
the low watermark. This is intentionally not a strict whole-filesystem quota:
temporary, malformed/orphaned, metadata, private-PyPI, and filesystem-overhead
bytes can sit outside the evictable cache total. Use a capacity-monitored,
preferably dedicated volume.

Monitor both n0ding's valid complete-object total and the real filesystem:

```sh
curl -fsS http://localhost:8080/metrics
docker compose exec -T n0ding du -sk /data
docker compose exec -T n0ding df -Pk /data
```

Alert at less than 20% free space and treat less than 10% as critical. Also
alert when the measured growth rate predicts exhaustion before the oldest
objects can reach `storage.max_age`. The repository storage metric excludes
temporary, corrupt/orphaned, metadata, and filesystem-overhead bytes, so it
cannot replace `df`.

Set `max_bytes` below usable capacity with headroom for unaccounted bytes, and
choose `max_age` from the worst credible unique-ingress rate. If space remains
critical, stop the service before replacing its disposable cache volume; never
delete individual cache objects while n0ding is running.

The complete decision, remaining disk-full failure mode, capacity example, and
requirements for a future strict aggregate limit are in the
[retention policy decision](retention-policy.md).

## Known limitations

- n0ding is not an offline mirror. OCI cache hits still depend on an upstream
  authorization/digest `HEAD`, and cache misses always need the upstream.
- Private publishing is limited to optional single-token PyPI uploads; npm and
  OCI publishing are unsupported.
- Private PyPI distributions count toward the configured storage budget but,
  unlike cache entries, are not removed by TTL garbage collection.
- There is no n0ding client authentication, user management, or RBAC. Only
  upstream credential handling exists.
- Podman remains untested.
- Range requests are proxied but partial responses are not cached.
- Age expiry uses commit time; pressure collection uses global LRU access hints.
  The logical `max_bytes` budget is not a hard whole-filesystem quota, so disk
  monitoring and capacity planning remain required.
- There is no multi-process or shared-volume locking.
- Only local filesystem storage is supported.
- The deterministic retention smoke has passed. The native 72-hour exact-RC
  observation and per-deployment capacity/risk acceptance remain open for the
  Public Preview; the uninterrupted seven-day soak remains a later
  stable/availability gate.
