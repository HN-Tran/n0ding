#!/usr/bin/env bash
set -Eeuo pipefail
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd); tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/cache"; printf '#!/bin/sh\nexit 0\n' >"$tmp/candidate"; chmod +x "$tmp/candidate"
cat >"$tmp/config.toml" <<EOF
[storage]
path = "$tmp/cache"
EOF
cat >"$tmp/unit" <<EOF
[Service]
ExecStart=$tmp/candidate -config $tmp/config.toml
EOF
printf '%s\n' "$$" >"$tmp/pid"
cat >"$tmp/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
case "$1 $2" in
 "is-active --quiet") exit 0;;
 "restart fake") printf '1\n' >"$FAKE_ROOT/pid";;
 "stop fake"|"start fake") :;;
 "cat fake") cat "$FAKE_ROOT/unit";;
 "show fake")
  args="$*"
  if [[ $args == *'-p FragmentPath --value'* ]]; then printf '%s\n' "$FAKE_ROOT/unit"
  elif [[ $args == *'-p MainPID --value'* ]]; then cat "$FAKE_ROOT/pid"
  elif [[ $args == *'-p NRestarts --value'* ]]; then printf '0\n'
  elif [[ $args == *'-p ExecMainStartTimestampMonotonic --value'* ]]; then printf 'start-%s\n' "$(cat "$FAKE_ROOT/pid")"
  else
   [[ $args == *ExecStart* ]] && printf 'ExecStart=%s -config %s\n' "$FAKE_ROOT/candidate" "$FAKE_ROOT/config.toml"
   [[ $args == *FragmentPath* ]] && printf 'FragmentPath=%s\n' "$FAKE_ROOT/unit"
   [[ $args == *MainPID* ]] && printf 'MainPID=%s\n' "$(cat "$FAKE_ROOT/pid")"
   [[ $args == *NRestarts* ]] && printf 'NRestarts=0\n'
   [[ $args == *ExecMainStartTimestampMonotonic* ]] && printf 'ExecMainStartTimestampMonotonic=start-%s\n' "$(cat "$FAKE_ROOT/pid")"
  fi
  exit 0
  ;;
 *) exit 0;;
esac
EOF
cat >"$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
url=${*: -1}
case "$url" in */api/v1/status) printf '{"version":"test"}\n';; *) printf '{}\n';; esac
EOF
printf '#!/bin/sh\nexit 0\n' >"$tmp/bin/journalctl"
chmod +x "$tmp/bin/"*
hash=$(sha256sum "$tmp/candidate"|awk '{print $1}')
run=("$repo/tools/native-preview-gate.sh" --mode smoke --rounds 3 --planned-restarts 1 --round-budget 60 --min-free-bytes 1 --service fake --expected-version test --expected-binary-sha256 "$hash" --binary "$tmp/candidate" --config "$tmp/config.toml" --cache "$tmp/cache" --evidence "$tmp/evidence" --url http://fake)
privileged() { if ((EUID)); then sudo env PATH="$tmp/bin:$PATH" FAKE_ROOT="$tmp" "$@"; else env PATH="$tmp/bin:$PATH" FAKE_ROOT="$tmp" "$@"; fi; }
if ((EUID)); then
  if ! command -v sudo >/dev/null; then printf 'SKIP: runner integration needs root or sudo\n'; exit 0; fi
fi
privileged "${run[@]}"
python3 - "$tmp/evidence/result.json" "$tmp/evidence/restarts.tsv" <<'PY'
import json,sys
result=json.load(open(sys.argv[1])); assert result["result"]=="nonqualifying-smoke"; assert result["rounds_completed"]==3; assert result["planned_restarts"]==1
rows=open(sys.argv[2]).read().strip().splitlines(); fields=rows[1].split("\t"); assert len(rows)==2 and fields[1]=="planned" and fields[2]!=fields[3] and fields[4]!=fields[5]
PY
printf '#!/bin/sh\nsleep 2\n' >"$tmp/timeout-hook"; chmod +x "$tmp/timeout-hook"; printf '1\n' >"$tmp/pid"
negative=("$repo/tools/native-preview-gate.sh" --mode smoke --rounds 1 --planned-restarts 0 --round-budget 60 --hook-timeout 1 --min-free-bytes 1 --service fake --expected-version test --expected-binary-sha256 "$hash" --binary "$tmp/candidate" --config "$tmp/config.toml" --cache "$tmp/cache" --evidence "$tmp/evidence-timeout" --url http://fake --workload-hook "$tmp/timeout-hook")
if privileged "${negative[@]}"; then echo 'timeout hook unexpectedly passed' >&2; exit 1; fi
printf '#!/usr/bin/env bash\nprintf "{}\\n" >>"$3/workload-events.jsonl"\ntouch "$FAKE_ROOT/cache/.body-breach"\n' >"$tmp/breach-hook"; chmod +x "$tmp/breach-hook"; printf '1\n' >"$tmp/pid"; rm -f "$tmp/cache/.body-breach"
breach=("$repo/tools/native-preview-gate.sh" --mode smoke --rounds 1 --planned-restarts 0 --round-budget 60 --min-free-bytes 1 --service fake --expected-version test --expected-binary-sha256 "$hash" --binary "$tmp/candidate" --config "$tmp/config.toml" --cache "$tmp/cache" --evidence "$tmp/evidence-breach" --url http://fake --workload-hook "$tmp/breach-hook" --max-temp-files 0)
if privileged "${breach[@]}"; then echo 'post-hook temp breach unexpectedly passed' >&2; exit 1; fi

# A running n0ding instance creates empty repository directories at startup.
# They must not invalidate a genuinely cold qualifying cache, but any
# state-bearing entry still must.
mkdir -p "$tmp/cache/npm/aa" "$tmp/cache/pypi/bb" "$tmp/cache/oci/cc" "$tmp/old-cache"
printf '1\n' >"$tmp/pid"
mkdir -p "$tmp/gate-tools"
cp "$repo/tools/native-preview-gate.sh" "$repo/tools/validate-native-preview-workload.py" "$tmp/gate-tools/"
chown -R root:root "$tmp/gate-tools"
chmod -R go-w "$tmp/gate-tools"
empty_dirs_gate=("$tmp/gate-tools/native-preview-gate.sh" --mode gate --rounds 3 --planned-restarts 1 --round-budget 60 --hook-timeout 10 --min-free-bytes 1 --service fake --expected-version test --expected-binary-sha256 "$hash" --binary "$tmp/candidate" --config "$tmp/config.toml" --cache "$tmp/cache" --forbidden-old-cache "$tmp/old-cache" --evidence "$tmp/evidence-empty-dirs" --url http://fake --workload-hook /bin/true --max-rss-kib 999999 --max-fds 999999 --max-cache-growth-bytes 999999 --max-temp-files 0)
if privileged "${empty_dirs_gate[@]}"; then echo 'receipt-less gate unexpectedly passed' >&2; exit 1; fi
[[ -s $tmp/evidence-empty-dirs/result.json ]] || { echo 'empty cache directories were rejected before gate execution' >&2; exit 1; }

printf 'state\n' >"$tmp/cache/npm/aa/object"
state_gate=("${empty_dirs_gate[@]}")
for i in "${!state_gate[@]}"; do
  [[ ${state_gate[$i]} == "$tmp/evidence-empty-dirs" ]] && state_gate[$i]="$tmp/evidence-state-entry"
done
if privileged "${state_gate[@]}" >"$tmp/state-gate.out" 2>&1; then echo 'state-bearing cache unexpectedly passed preflight' >&2; exit 1; fi
grep -F 'qualifying gate requires an empty dedicated cache' "$tmp/state-gate.out" >/dev/null || { echo 'state-bearing cache did not fail the empty-cache preflight' >&2; exit 1; }
