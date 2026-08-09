#!/usr/bin/env sh
set -eu

suffix="${GITHUB_RUN_ID:-$$}"
network="n0ding-rc-$suffix"
volume="n0ding-rc-$suffix"
server="n0ding-rc-server-$suffix"
dind="n0ding-rc-dind-$suffix"
image="n0ding:release-candidate"
work="$(mktemp -d)"

cleanup() {
  docker rm -f "$dind" "$server" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  docker run --rm -v "$work:/work" alpine:3.22 chmod -R a+rwX /work >/dev/null 2>&1 || true
  rm -rf "$work" || true
}
trap cleanup EXIT INT TERM

wait_for_server() {
  attempt=0
  until docker exec "$server" wget -qO- http://localhost:8080/healthz >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
      docker logs "$server" || true
      return 1
    fi
    sleep 2
  done
}

start_dind() {
  docker run -d --privileged --name "$dind" --network "$network" \
    docker:29-dind --insecure-registry=n0ding:8080 >/dev/null
  attempt=0
  until docker exec "$dind" docker info >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 30 ] || return 1
    sleep 2
  done
}

run_npm() {
  destination=$1
  mkdir -p "$destination"
  cp testdata/npm-compat/package.json testdata/npm-compat/package-lock.json "$destination/"
  sed -i 's#http://127.0.0.1:18080#http://n0ding:8080#g' "$destination/package-lock.json"
  docker run --rm --network "$network" -v "$destination:/work" -w /work node:24-alpine \
    npm ci --ignore-scripts --no-audit --no-fund \
      --registry http://n0ding:8080/npm/ --cache /tmp/npm-cache --prefer-online
}

run_pip() {
  docker run --rm --network "$network" python:3.13-alpine \
    python -m pip install --disable-pip-version-check --no-cache-dir \
      --trusted-host n0ding \
      --index-url http://n0ding:8080/pypi/simple/ \
      requests==2.32.5
}

run_uv() {
  docker run --rm --network "$network" ghcr.io/astral-sh/uv:python3.13-alpine \
    uv pip install --system --no-cache \
      --allow-insecure-host n0ding \
      --index-url http://n0ding:8080/pypi/simple/ \
      idna==3.10
}

run_oci() {
  for image_name in alpine:3.20 busybox:1.36; do
    docker exec "$dind" docker pull --platform linux/amd64 "n0ding:8080/library/$image_name"
    docker exec "$dind" docker image inspect "n0ding:8080/library/$image_name" \
      --format '{{index .RepoDigests 0}}'
  done
}

assert_status() {
  minimum_hits=$1
  docker exec "$server" wget -qO- http://localhost:8080/api/v1/status >"$work/status.json"
  python3 - "$work/status.json" "$minimum_hits" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    status = json.load(stream)
minimum_hits = int(sys.argv[2])
repositories = {repo["type"]: repo for repo in status["repositories"]}
for name in ("npm", "oci", "pypi"):
    repo = repositories[name]
    assert repo["errors"] == 0, repo
    assert repo["cache_objects"] > 0, repo
    if minimum_hits:
        assert repo["cache_hits"] >= minimum_hits, repo
print(json.dumps(status, indent=2, sort_keys=True))
PY
}

docker build --build-arg VERSION=release-candidate -t "$image" .
docker network create "$network" >/dev/null
docker volume create "$volume" >/dev/null
docker run -d --name "$server" --network "$network" --network-alias n0ding \
  -e N0DING_PUBLIC_URL=http://n0ding:8080 -v "$volume:/data" "$image" >/dev/null
wait_for_server

run_npm "$work/npm-first"
run_pip
run_uv
start_dind
run_oci
assert_status 0

docker rm -f "$dind" >/dev/null
docker restart "$server" >/dev/null
wait_for_server

run_npm "$work/npm-second"
run_pip
run_uv
start_dind
run_oci
assert_status 1
