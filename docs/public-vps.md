# Authenticated public VPS deployment

This profile exposes n0ding through Caddy with automatic HTTPS and mandatory
mutual TLS (mTLS). It is intended for a single operator or a small trusted
team. It is not anonymous hosting, multi-tenancy, or RBAC.

mTLS authenticates the connection with a client certificate. It deliberately
does not consume the HTTP `Authorization` header, which npm, PyPI, and OCI may
need for their own registry authentication flows.

## Security boundary

- Only Caddy publishes host ports. The n0ding container has outbound registry
  access but no host port.
- Every public route, including status and metrics, requires a client
  certificate signed by the configured client CA.
- Deployment authentication cannot leak as an upstream HTTP credential because
  it exists only in the TLS layer.
- Caddy obtains and renews the public server certificate automatically after
  the domain resolves to the VPS and ports 80 and 443 reach Caddy.
- The profile targets public upstream registries. Private upstream support is
  outside the v0.1 boundary.

The current age-based retention policy is not a strict storage quota. Use a
dedicated volume, alert at 20% free space, and treat 10% free space as
critical. Do not expose the profile before those alerts exist.

## Create the client CA and certificate

Create these files on an offline administration machine, not on the VPS. Keep
`client-ca.key` in a password manager or another protected backup and never
copy it to the server.

```sh
openssl genpkey -algorithm ED25519 -out client-ca.key
openssl req -x509 -new -key client-ca.key -days 3650 \
  -subj '/CN=n0ding client CA' -out client-ca.pem

openssl genpkey -algorithm ED25519 -out client.key
openssl req -new -key client.key -subj '/CN=n0ding preview client' \
  -out client.csr
openssl x509 -req -in client.csr -CA client-ca.pem -CAkey client-ca.key \
  -CAcreateserial -days 365 -out client.crt

cat client.crt client.key > client.pem
```

Protect private keys with filesystem permissions appropriate for the client
operating system. The shared certificate is the v0.1 credential. Anyone who
possesses `client.key` can use the deployment.

## Configure and start the VPS

From `deploy/public`:

```sh
cp .env.example .env
```

Set the real domain and a pinned n0ding image in `.env`. Copy only
`client-ca.pem` to this directory on the VPS. Do not copy `client-ca.key`,
`client.key`, or `client.pem` to the server.

```sh
docker compose config --quiet
docker compose pull
docker compose up -d
docker compose ps
curl --cert client.crt --key client.key \
  https://packages.example.com/healthz
```

An ordinary request without a trusted client certificate must fail during the
TLS handshake.

## Client authentication

### pip and uv

`client.pem` contains the client certificate followed by its private key.

```sh
python -m pip install \
  --client-cert /secure/path/client.pem \
  --index-url https://packages.example.com/pypi/simple/ \
  requests
```

Do not commit private keys or certificate-bearing configuration. Store client
material in the CI secret facility and write it to a temporary protected file.
The uv mTLS compatibility command will be added after the release client matrix
records the exact supported uv version.

### npm

npm accepts PEM client material through its `cert` and `key` configuration.
Keep both values in user-level or CI-secret configuration, never a committed
project `.npmrc`. The exact npm command will be published after the real-client
mTLS matrix passes.

### Docker

Docker discovers client certificates by registry hostname. Place the files as
`client.cert` and `client.key` in the Docker certificate directory for the
deployment hostname, then restart Docker if required by the platform:

```text
/etc/docker/certs.d/packages.example.com/client.cert
/etc/docker/certs.d/packages.example.com/client.key
```

Docker Desktop uses the equivalent host-side `certs.d` directory. Follow the
current Docker documentation for that platform. Then pull normally:

```sh
docker pull packages.example.com/library/alpine:latest
```

## Rotation and emergency removal

Issue a replacement client certificate from the offline CA, distribute it,
and retire the old certificate. v0.1 initially trusts certificates by their
issuing CA, so immediate revocation of one leaked leaf certificate requires a
new client CA and recreation of Caddy.

To stop public access without deleting cache data:

```sh
docker compose stop caddy
```

Deleting volumes is intentionally a separate, destructive operation.
