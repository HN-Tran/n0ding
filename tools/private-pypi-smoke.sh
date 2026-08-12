#!/usr/bin/env bash
set -euo pipefail

workspace="$(mktemp -d)"
container="n0ding-private-pypi-smoke"
token="0123456789abcdef0123456789abcdef"

cleanup() {
  docker logs "$container" 2>/dev/null || true
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$workspace"
}
trap cleanup EXIT

mkdir -p "$workspace/data" "$workspace/package/src/n0ding_private_smoke"
chmod 0777 "$workspace/data"
printf '%s\n' "$token" >"$workspace/publish-token"
chmod 0644 "$workspace/publish-token"

cat >"$workspace/n0ding.toml" <<'TOML'
[server]
listen = ":8080"
public_base_url = "http://127.0.0.1:18082"
log_level = "info"

[storage]
path = "/data"
max_age = "24h"
gc_interval = "1h"
stale_temp_age = "1h"

[repository.pypi]
type = "pypi"
path = "/pypi/simple/"
upstream = "https://pypi.org/simple"
ttl = "1h"
allowed_file_origins = "https://files.pythonhosted.org"
publish_token_file = "/run/secrets/pypi-publish-token"
TOML

cat >"$workspace/package/pyproject.toml" <<'TOML'
[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[project]
name = "n0ding-private-smoke"
version = "0.1.0"
requires-python = ">=3.11"
TOML
printf 'VALUE = "private-smoke-ok"\n' >"$workspace/package/src/n0ding_private_smoke/__init__.py"

docker build --build-arg VERSION=private-pypi-smoke --tag n0ding:private-pypi-smoke .
docker run -d --name "$container" \
	--user "$(id -u):$(id -g)" \
	--publish 127.0.0.1:18082:8080 \
  --volume "$workspace/n0ding.toml:/etc/n0ding/n0ding.toml:ro" \
  --volume "$workspace/publish-token:/run/secrets/pypi-publish-token:ro" \
  --volume "$workspace/data:/data" \
  n0ding:private-pypi-smoke

for attempt in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:18082/healthz >/dev/null; then
    break
  fi
  if [[ "$attempt" == 30 ]]; then
    exit 1
  fi
  sleep 2
done

(cd "$workspace/package" && python -m build)
python -m twine upload \
  --repository-url http://127.0.0.1:18082/pypi/legacy/ \
  --username __token__ \
  --password "$token" \
  "$workspace/package/dist/"*

python -m pip install --no-deps \
  --target "$workspace/pip-install" \
  --index-url http://127.0.0.1:18082/pypi/simple/ \
  n0ding-private-smoke==0.1.0
PYTHONPATH="$workspace/pip-install" python -c \
  'import n0ding_private_smoke; assert n0ding_private_smoke.VALUE == "private-smoke-ok"'

uv venv "$workspace/uv-env"
uv pip install \
  --python "$workspace/uv-env/bin/python" \
  --index-url http://127.0.0.1:18082/pypi/simple/ \
  n0ding-private-smoke==0.1.0
"$workspace/uv-env/bin/python" -c \
  'import n0ding_private_smoke; assert n0ding_private_smoke.VALUE == "private-smoke-ok"'

if python -m twine upload \
  --repository-url http://127.0.0.1:18082/pypi/legacy/ \
  --username __token__ \
  --password "$token" \
  "$workspace/package/dist/n0ding_private_smoke-0.1.0-py3-none-any.whl"; then
  echo "duplicate distribution upload unexpectedly succeeded" >&2
  exit 1
fi

docker restart "$container" >/dev/null
for attempt in $(seq 1 30); do
  if curl -fsS http://127.0.0.1:18082/healthz >/dev/null; then
    break
  fi
  if [[ "$attempt" == 30 ]]; then
    exit 1
  fi
  sleep 2
done

python -m pip install --no-deps \
  --target "$workspace/pip-after-restart" \
  --index-url http://127.0.0.1:18082/pypi/simple/ \
  n0ding-private-smoke==0.1.0
PYTHONPATH="$workspace/pip-after-restart" python -c \
  'import n0ding_private_smoke; assert n0ding_private_smoke.VALUE == "private-smoke-ok"'
