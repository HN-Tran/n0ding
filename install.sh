#!/usr/bin/env sh
set -eu

REPOSITORY="HN-Tran/n0ding"
INSTALL_DIR="${N0DING_INSTALL_DIR:-$HOME/.n0ding}"
VERSION="${N0DING_VERSION:-latest}"

fail() {
  printf 'n0ding: %s\n' "$1" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker is required: https://docs.docker.com/get-docker/"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPOSITORY/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [ -n "$VERSION" ] || fail "No published release found"
fi

case "$VERSION" in
  v*) IMAGE_VERSION=${VERSION#v} ;;
  *) IMAGE_VERSION=$VERSION ;;
esac

ASSET_BASE="https://github.com/$REPOSITORY/releases/download/$VERSION"
mkdir -p "$INSTALL_DIR"

download() {
  destination=$1
  source=$2
  temporary="$destination.tmp"
  curl -fsSL "$source" -o "$temporary" || fail "Could not download $source"
  mv "$temporary" "$destination"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    fail "sha256sum or shasum is required"
  fi
}

download "$INSTALL_DIR/compose.yaml" "$ASSET_BASE/compose.yaml"
download "$INSTALL_DIR/n0ding.toml" "$ASSET_BASE/n0ding.toml"
download "$INSTALL_DIR/SHA256SUMS" "$ASSET_BASE/SHA256SUMS"

(
  cd "$INSTALL_DIR"
  expected_compose="$(sed -n 's/  compose.yaml$//p' SHA256SUMS)"
  expected_config="$(sed -n 's/  n0ding.toml$//p' SHA256SUMS)"
  actual_compose="$(sha256_file compose.yaml)"
  actual_config="$(sha256_file n0ding.toml)"
  [ "$actual_compose" = "$expected_compose" ] || fail "compose.yaml checksum mismatch"
  [ "$actual_config" = "$expected_config" ] || fail "n0ding.toml checksum mismatch"
)

cat >"$INSTALL_DIR/.env" <<EOF
N0DING_IMAGE=ghcr.io/hn-tran/n0ding:${IMAGE_VERSION}
N0DING_BIND_ADDRESS=127.0.0.1
N0DING_PORT=8080
N0DING_PUBLIC_URL=http://localhost:8080
EOF

docker compose --project-directory "$INSTALL_DIR" pull
docker compose --project-directory "$INSTALL_DIR" up -d

attempt=0
until curl -fsS "http://localhost:8080/healthz" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 30 ] || fail "Service did not become healthy; run: docker compose --project-directory $INSTALL_DIR logs"
  sleep 2
done

printf '\nn0ding %s is running at http://localhost:8080\n' "$VERSION"
printf 'Install directory: %s\n' "$INSTALL_DIR"
printf 'Status: curl http://localhost:8080/api/v1/status\n'
printf 'Logs:   docker compose --project-directory %s logs -f\n' "$INSTALL_DIR"
printf '\nClient setup is opt-in. See: https://github.com/%s#client-setup\n' "$REPOSITORY"
