#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; work="$(mktemp -d)"; suffix="${RANDOM}${RANDOM}"
network="n0ding-public-$suffix"; n0ding="n0ding-public-$suffix"; caddy="n0ding-caddy-$suffix"
cleanup(){
  result=$?
  if [ "$result" -ne 0 ]; then
    docker logs "$caddy" 2>&1 || true
    docker logs "$n0ding" 2>&1 || true
  fi
  docker rm -f "$caddy" "$n0ding" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$work"
  return "$result"
}
trap cleanup EXIT
openssl genpkey -algorithm ED25519 -out "$work/ca.key"
openssl req -x509 -new -key "$work/ca.key" -days 1 -subj '/CN=n0ding CI CA' -out "$work/ca.pem"
openssl genpkey -algorithm ED25519 -out "$work/client.key"
openssl req -new -key "$work/client.key" -subj '/CN=n0ding CI client' -out "$work/client.csr"
cat >"$work/client.ext" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid
EOF
openssl x509 -req -in "$work/client.csr" -CA "$work/ca.pem" -CAkey "$work/ca.key" -CAcreateserial -days 1 -extfile "$work/client.ext" -out "$work/client.crt"
cat "$work/client.crt" "$work/client.key" > "$work/client.pem"
openssl rand -hex 32 > "$work/operator-token"
cat >"$work/Caddyfile" <<'EOF'
{
  admin off
}
n0ding.test {
  tls internal {
    client_auth {
      mode require_and_verify
      trust_pool file {
        pem_file /etc/caddy/client-ca.pem
      }
    }
  }
  reverse_proxy n0ding:8080
}
EOF
docker network create "$network" >/dev/null
docker run -d --name "$n0ding" --network "$network" --network-alias n0ding -e N0DING_PUBLIC_URL=https://n0ding.test -v "$root/deploy/public/n0ding.toml:/etc/n0ding/n0ding.toml:ro" -v "$work/operator-token:/run/secrets/n0ding-operator-token:ro" n0ding:public-vps-ci >/dev/null
docker run -d --name "$caddy" --network "$network" -p 443:443 -v "$work/Caddyfile:/etc/caddy/Caddyfile:ro" -v "$work/ca.pem:/etc/caddy/client-ca.pem:ro" caddy:2.10.0-alpine >/dev/null
caddy_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$caddy")
test -n "$caddy_ip"
echo "$caddy_ip n0ding.test" | sudo tee -a /etc/hosts >/dev/null
for _ in $(seq 1 30); do docker exec "$caddy" test -f /data/caddy/pki/authorities/local/root.crt && break; sleep 1; done
docker cp "$caddy:/data/caddy/pki/authorities/local/root.crt" "$work/server-ca.crt"
mtls=(--cacert "$work/server-ca.crt" --cert "$work/client.crt" --key "$work/client.key")
ready=false
for _ in $(seq 1 30); do
  if curl -fsS "${mtls[@]}" https://n0ding.test/healthz >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
if [ "$ready" != true ]; then echo 'authenticated HTTPS endpoint did not become ready' >&2; exit 1; fi
if curl -fsS --cacert "$work/server-ca.crt" https://n0ding.test/healthz >/dev/null 2>&1; then echo 'unauthenticated request succeeded' >&2; exit 1; fi
curl -fsS "${mtls[@]}" https://n0ding.test/healthz >/dev/null
if curl -fsS "${mtls[@]}" -X POST https://n0ding.test/api/v1/operator/gc >/dev/null 2>&1; then echo 'operator GC accepted without token' >&2; exit 1; fi
curl -fsS "${mtls[@]}" -H "Authorization: Bearer $(cat "$work/operator-token")" -X POST https://n0ding.test/api/v1/operator/gc >/dev/null
python -m pip install --no-deps --target "$work/pip" --no-cache-dir --cert "$work/server-ca.crt" --client-cert "$work/client.pem" --index-url https://n0ding.test/pypi/simple/ idna==3.10
SSL_CLIENT_CERT="$work/client.pem" uv pip install --system --no-cache --cert "$work/server-ca.crt" --index-url https://n0ding.test/pypi/simple/ idna==3.10
cat >"$work/npmrc" <<EOF
registry=https://n0ding.test/npm/
cafile=$work/server-ca.crt
//n0ding.test/npm/:certfile=$work/client.crt
//n0ding.test/npm/:keyfile=$work/client.key
EOF
npm view is-number version --userconfig "$work/npmrc" --cache "$work/npm-cache" --prefer-online >/dev/null
sudo mkdir -p '/etc/docker/certs.d/n0ding.test:443'
sudo cp "$work/server-ca.crt" '/etc/docker/certs.d/n0ding.test:443/ca.crt'
sudo cp "$work/client.crt" '/etc/docker/certs.d/n0ding.test:443/client.cert'
sudo cp "$work/client.key" '/etc/docker/certs.d/n0ding.test:443/client.key'
docker pull n0ding.test:443/library/alpine:3.20 >/dev/null
curl -fsS "${mtls[@]}" https://n0ding.test/api/v1/status >"$work/status.json"
python - "$work/status.json" <<'PY'
import json, sys
status = json.load(open(sys.argv[1], encoding="utf-8"))
assert all(r["errors"] == 0 and r["requests"] > 0 for r in status["repositories"]), status
PY
