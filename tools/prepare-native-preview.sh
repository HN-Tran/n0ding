#!/usr/bin/env bash
set -Eeuo pipefail
usage() { echo "Usage: sudo $0 {preflight|switch|rollback} --service NAME --binary SYMLINK --config PATH --cache PATH --expected-sha SHA [--candidate PATH] [--url URL] [--state-dir PATH]"; }
die() { printf 'prepare-native-preview: %s\n' "$*" >&2; exit 1; }
mode=${1:-}; [[ $mode =~ ^(preflight|switch|rollback)$ ]] || { usage >&2; exit 2; }; shift
service=; binary=; config=; cache=; expected_sha=; candidate=; base_url=http://127.0.0.1:8080; state_dir=/var/lib/n0ding-preview-deploy
while (($#)); do case "$1" in
  --service) service=${2:?}; shift 2;; --binary) binary=${2:?}; shift 2;; --config) config=${2:?}; shift 2;;
  --cache) cache=${2:?}; shift 2;; --expected-sha) expected_sha=${2:?}; shift 2;; --candidate) candidate=${2:?}; shift 2;;
  --url) base_url=${2%/}; shift 2;;
  --state-dir) state_dir=${2:?}; shift 2;; *) die "unknown option: $1";; esac; done
[[ $EUID -eq 0 ]] || die "run as root"
[[ -n $service && -n $binary && -n $config && -n $cache && -n $expected_sha ]] || die "missing required option"
for cmd in curl python3 readlink sha256sum systemctl; do command -v "$cmd" >/dev/null || die "missing command: $cmd"; done
[[ -L $binary ]] || die "binary path must be a symlink for atomic rollback"
[[ -r $config && -d $cache ]] || die "config or cache is missing"
preflight() {
  local target free; target=$(readlink -f "$binary"); [[ -x $target ]] || die "current target is not executable"
  free=$(df -P "$cache"|awk 'NR==2 {gsub(/%/,"",$5); print 100-$5}'); ((free>=20)) || die "less than 20% free"
  free_bytes=$(df -PB1 "$cache"|awk 'NR==2 {print $4}'); ((free_bytes>=2147483648)) || die "less than 2 GiB free"
  "$target" -config "$config" -check-config; systemctl cat "$service" >/dev/null
  printf 'service=%s\nbinary_link=%s\ncurrent_target=%s\ncurrent_sha256=%s\nconfig_sha256=%s\ncache=%s\nfree_percent=%s\n' "$service" "$binary" "$target" "$(sha256sum "$target"|awk '{print $1}')" "$(sha256sum "$config"|awk '{print $1}')" "$cache" "$free"
}
rollback() {
  [[ -r $state_dir/previous-target ]] || die "no recorded previous target"
  local old; old=$(cat "$state_dir/previous-target"); [[ -x $old ]] || die "previous target is unavailable"
  ln -sfn "$old" "$binary.next"; mv -Tf "$binary.next" "$binary"; systemctl restart "$service"
  curl -fsS --max-time 2 --retry 30 --retry-delay 1 --retry-max-time 60 --retry-connrefused "$base_url/healthz" >/dev/null
  printf 'rolled back to %s\n' "$old"
}
case "$mode" in
  preflight) preflight;; rollback) rollback;;
  switch)
    [[ -n $candidate && -x $candidate ]] || die "--candidate must be executable"
    [[ $(basename "$candidate") == *"$expected_sha"* ]] || die "candidate filename must contain expected SHA"
    "$candidate" -config "$config" -check-config; preflight
    mkdir -p "$state_dir"; chmod 700 "$state_dir"; old=$(readlink -f "$binary"); printf '%s\n' "$old" >"$state_dir/previous-target"; chmod 600 "$state_dir/previous-target"
    ln -sfn "$candidate" "$binary.next"; mv -Tf "$binary.next" "$binary"
    if ! systemctl restart "$service" || ! curl -fsS --max-time 2 --retry 30 --retry-delay 1 --retry-max-time 60 --retry-connrefused "$base_url/healthz" >/dev/null; then rollback || true; die "candidate failed health check"; fi
    version=$(curl -fsS --max-time 10 "$base_url/api/v1/status"|python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')
    if [[ $version != "$expected_sha" ]]; then rollback; die "running version is $version, expected $expected_sha"; fi
    printf 'candidate active: %s\n' "$version";;
esac
