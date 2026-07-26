# Troubleshooting

This guide covers the `v0.1-private` read-only npm + OCI hardening phase. Start
with:

```sh
docker compose ps
docker compose logs --tail=200 n0ding
curl http://localhost:8080/healthz
curl http://localhost:8080/api/v1/status
```

Do not include credentials, registry tokens, private artifact names, or raw
cache contents in a public issue.

## Docker reports an HTTPS/HTTP mismatch

Typical message:

```text
http: server gave HTTP response to HTTPS client
```

Docker expects a registry to use TLS. For local evaluation, add
`localhost:8080` to the daemon's `insecure-registries` and restart Docker. For
shared use, terminate trusted TLS at a reverse proxy instead. Do not use an
insecure-registry exception across an untrusted network.

## Docker Hub image is not found

Official Docker Hub images use the `library/` namespace through n0ding:

```sh
docker pull localhost:8080/library/alpine:3.20
```

Use `localhost:8080/owner/image:tag` for non-official repositories.

## A second Docker pull does not increment n0ding hits

Docker may satisfy the second pull from its own local content store without
contacting n0ding. Remove the local image or use a clean test daemon when
measuring the server cache. Different `Accept` headers also create different
cache keys, and OCI hits still make an upstream `HEAD` request.

Inspect n0ding's counters before and after the pull:

```sh
curl http://localhost:8080/api/v1/status
```

## OCI cache hits fail while the upstream is unavailable

This is expected in `v0.1-private`. n0ding is not an offline mirror. Before
serving a cached OCI response, it asks the upstream to authorize the request
and confirm the digest with `HEAD`.

## npm downloads bypass n0ding or use the wrong hostname

npm package metadata contains tarball URLs. n0ding rewrites them using
`server.public_base_url`. Set that value, or `N0DING_PUBLIC_URL` in Compose, to
the exact origin reachable by the npm client, including `https://` and any
non-default port. Restart n0ding after changing it.

Check the generated client command:

```sh
curl http://localhost:8080/api/v1/repositories/npm/setup
```

## npm appears not to exercise the server cache

npm has its own client cache. Use a new empty directory for each measurement:

```sh
npm view @types/node version --registry http://localhost:8080/npm/ \
  --cache .tmp/npm-cache-new --prefer-online
```

The n0ding response header is `X-N0ding-Cache: HIT` or `MISS`; aggregate
counters are exposed by `/api/v1/status`.

## Configuration validation fails

The parser intentionally rejects unknown fields, invalid paths, non-positive
durations, and unsupported repository types. Validate the mounted file without
starting the service:

```sh
docker run --rm \
  -v "$PWD/config/n0ding.example.toml:/etc/n0ding/n0ding.toml:ro" \
  n0ding:dev -config /etc/n0ding/n0ding.toml -check-config
```

Compare the file with [the configuration reference](configuration.md).

## Cache directory permission is denied

The container runs as UID/GID `65532:65532`. The provided named volume is
initialized correctly. If you use a bind mount, make the directory writable by
that identity without making it world-writable. Confirm the resolved mount:

```sh
docker compose config
docker compose logs n0ding
```

## Disk usage does not fall immediately

Retention is based on object commit age. It runs at startup and every
`storage.gc_interval`; it is not LRU and does not enforce a byte quota.
Accessing an object does not renew its age, while refetching it does. Review
`storage.max_age`, restart once to trigger a maintenance pass, and inspect
status counters.

Never manually delete individual cache files while n0ding is running. Stop the
service first, or delete the entire disposable cache volume if repopulating it
is acceptable.

## Restore validation fails

Restoring into a non-empty live cache is unsupported. Restore a quiesced backup
into a new empty volume, then validate health/status, `npm ci` with a clean
client cache, and an OCI digest.

Messages containing `decode cache metadata` or `cache body size mismatch` mean
the restored object is invalid. It is not counted or served; n0ding attempts
to fetch it again when requested. If the upstream is unavailable, the request
cannot be repaired from that object.

Keep the original volume untouched until validation completes. If extraction,
required config, identity checks, or client integrity validation fails, stop
the restored deployment and return to the original volume. The cache format is
not yet a stable cross-version API; an incompatible restored cache may be
discarded and repopulated.

See the [operator procedure](operations.md#backup-and-restore) and the
[deterministic stopped Compose drill](backup-restore-drill.md).
