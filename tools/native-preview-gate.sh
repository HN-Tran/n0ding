#!/usr/bin/env bash
set -Eeuo pipefail

mode=gate; rounds=3; planned_restart_target=1; round_budget=2400; hook_timeout=900
service=n0ding; base_url=http://127.0.0.1:8080; min_free_percent=10; min_free_bytes=2147483648
evidence_root=; expected_version=; binary=; config=; cache=; forbidden_old_cache=
expected_binary_sha256=; workload_hook=; abort_hook=; rollback_hook=; canary_file=; anchor_hook=
max_rss_kib=; max_fds=; max_cache_growth_bytes=; max_temp_files=
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

usage() { cat <<'EOF'
Usage: sudo tools/native-preview-gate.sh [options]
Required: --expected-version SHA --expected-binary-sha256 HASH --binary PATH
          --config PATH --cache PATH --forbidden-old-cache PATH --evidence NEW_PATH
Options:  --service NAME --url URL --rounds N --planned-restarts N
          --round-budget SEC --hook-timeout SEC --min-free-percent N --min-free-bytes N
          --mode gate|smoke --workload-hook PATH --canary-file PATH
          --max-rss-kib N --max-fds N --max-cache-growth-bytes N
          --max-temp-files N --anchor-hook PATH --abort-hook PATH
          --rollback-hook PATH

The qualifying Public Preview gate is coverage based: cold, warm with fresh
client caches, then one restart and a post-restart phase. There is no minimum
elapsed time. The default 40-minute per-round budget and the hard per-hook
timeout are fail-safes, not acceptance criteria. The runner observes a native systemd
deployment; it never installs software, changes configuration, or deletes data.
EOF
}
die() { printf 'native-preview-gate: %s\n' "$*" >&2; exit 1; }
while (($#)); do case "$1" in
  --mode) mode=${2:?}; shift 2;;
  --expected-version) expected_version=${2:?}; shift 2;; --binary) binary=${2:?}; shift 2;;
  --expected-binary-sha256) expected_binary_sha256=${2:?}; shift 2;;
  --config) config=${2:?}; shift 2;; --cache) cache=${2:?}; shift 2;;
  --forbidden-old-cache) forbidden_old_cache=${2:?}; shift 2;;
  --evidence) evidence_root=${2:?}; shift 2;; --service) service=${2:?}; shift 2;;
  --url) base_url=${2%/}; shift 2;; --rounds) rounds=${2:?}; shift 2;;
  --planned-restarts) planned_restart_target=${2:?}; shift 2;; --round-budget) round_budget=${2:?}; shift 2;;
  --hook-timeout) hook_timeout=${2:?}; shift 2;;
  --min-free-percent) min_free_percent=${2:?}; shift 2;; --workload-hook) workload_hook=${2:?}; shift 2;;
  --min-free-bytes) min_free_bytes=${2:?}; shift 2;; --max-rss-kib) max_rss_kib=${2:?}; shift 2;;
  --max-fds) max_fds=${2:?}; shift 2;; --max-cache-growth-bytes) max_cache_growth_bytes=${2:?}; shift 2;;
  --max-temp-files) max_temp_files=${2:?}; shift 2;; --anchor-hook) anchor_hook=${2:?}; shift 2;;
  --canary-file) canary_file=${2:?}; shift 2;; --abort-hook) abort_hook=${2:?}; shift 2;;
  --rollback-hook) rollback_hook=${2:?}; shift 2;; -h|--help) usage; exit 0;; *) die "unknown option: $1";;
esac; done

[[ $EUID -eq 0 ]] || die "run as root"
[[ $mode == gate || $mode == smoke ]] || die "mode must be gate or smoke"
[[ -n $expected_version && -n $expected_binary_sha256 && -n $binary && -n $config && -n $cache && -n $evidence_root ]] || { usage >&2; exit 2; }
[[ $rounds =~ ^[0-9]+$ && $rounds -gt 0 ]] || die "rounds must be a positive integer"
[[ $planned_restart_target =~ ^[0-9]+$ ]] || die "planned restarts must be a non-negative integer"
((planned_restart_target < rounds)) || die "planned restarts must be fewer than rounds"
[[ $round_budget =~ ^[0-9]+$ ]] || die "round budget must be a non-negative integer"
[[ $hook_timeout =~ ^[0-9]+$ && $hook_timeout -gt 0 ]] || die "hook timeout must be positive"
if [[ $mode == gate ]]; then
  ((rounds==3)) || die "the qualifying gate has exactly three coverage phases"
  ((planned_restart_target==1)) || die "the qualifying gate has exactly one planned restart"
  [[ -n $workload_hook ]] || die "gate mode requires a workload hook"
  [[ -n $forbidden_old_cache ]] || die "gate mode requires --forbidden-old-cache"
  [[ -n $max_rss_kib && -n $max_fds && -n $max_cache_growth_bytes && -n $max_temp_files ]] || die "gate mode requires explicit resource bounds"
fi
[[ $min_free_percent =~ ^[0-9]+$ && $min_free_percent -ge 1 && $min_free_percent -le 99 ]] || die "invalid free-space floor"
[[ $min_free_bytes =~ ^[0-9]+$ && $min_free_bytes -gt 0 ]] || die "minimum free bytes must be positive"
for bound in "$max_rss_kib" "$max_fds" "$max_cache_growth_bytes" "$max_temp_files"; do [[ -z $bound || $bound =~ ^[0-9]+$ ]] || die "resource bounds must be integers"; done
for cmd in cp curl find grep journalctl python3 sha256sum stat systemctl timeout; do command -v "$cmd" >/dev/null || die "missing command: $cmd"; done
[[ -r $script_dir/validate-native-preview-workload.py ]] || die "workload evidence validator is missing"
[[ -x $binary && -r $config && -d $cache ]] || die "binary, config, or cache prerequisite is missing"
cache=$(readlink -f "$cache"); [[ -z $forbidden_old_cache ]] || forbidden_old_cache=$(readlink -f "$forbidden_old_cache")
[[ -z $forbidden_old_cache || $cache != "$forbidden_old_cache" ]] || die "RC cache must differ from forbidden old cache"
configured_cache=$(python3 - "$config" <<'PY'
import os,sys,tomllib
path=os.path.realpath(sys.argv[1])
with open(path,"rb") as f: value=tomllib.load(f)["storage"]["path"]
print(os.path.realpath(value if os.path.isabs(value) else os.path.join(os.path.dirname(path),value)))
PY
)
[[ $configured_cache == "$cache" ]] || die "effective TOML storage.path does not equal --cache"
if [[ $mode == gate ]] && [[ -n $(find "$cache" -mindepth 1 -print -quit) ]]; then die "qualifying gate requires an empty dedicated cache"; fi
[[ ! -e $evidence_root ]] || die "refusing to overwrite evidence path: $evidence_root"
for hook in "$workload_hook" "$abort_hook" "$rollback_hook" "$anchor_hook"; do [[ -z $hook || -x $hook ]] || die "hook is not executable: $hook"; done
[[ -z $canary_file || -r $canary_file ]] || die "canary file is not readable"
trusted_file() { local owner mode; owner=$(stat -c %u "$1"); mode=$(stat -c %a "$1"); [[ $owner == 0 && $((8#$mode & 8#022)) -eq 0 ]]; }
if [[ $mode == gate ]]; then
  for trusted in "$workload_hook" "$anchor_hook" "$canary_file" "$script_dir/validate-native-preview-workload.py"; do [[ -z $trusted ]] || trusted_file "$trusted" || die "gate input must be root-owned and not group/world writable: $trusted"; done
fi

umask 077; mkdir -p "$evidence_root/snapshots"
exec >>"$evidence_root/runner.log" 2>&1
started_epoch=$(date +%s); started_utc=$(date -u +%FT%TZ)
planned_restarts=0; completed_rounds=0; samples=0; failure=
binary_hash=$(sha256sum "$binary"|awk '{print $1}'); [[ $binary_hash == "$expected_binary_sha256" ]] || die "binary checksum is not trusted expected value"
config_hash=$(sha256sum "$config"|awk '{print $1}'); workload_hash=none; canary_definitions_hash=none; anchor_hash=none
validator_hash=$(sha256sum "$script_dir/validate-native-preview-workload.py"|awk '{print $1}')
[[ -z $workload_hook ]] || workload_hash=$(sha256sum "$workload_hook"|awk '{print $1}')
[[ -z $canary_file ]] || canary_definitions_hash=$(sha256sum "$canary_file"|awk '{print $1}')
[[ -z $anchor_hook ]] || anchor_hash=$(sha256sum "$anchor_hook"|awk '{print $1}')
unit_fragment=$(systemctl show "$service" -p FragmentPath --value); unit_hash=missing
[[ -n $unit_fragment && -r $unit_fragment ]] && unit_hash=$(sha256sum "$unit_fragment"|awk '{print $1}')
initial_nrestarts=$(systemctl show "$service" -p NRestarts --value)
systemctl cat "$service" >"$evidence_root/effective-unit.txt"
systemctl show "$service" -p ExecStart -p FragmentPath -p DropInPaths >"$evidence_root/effective-unit-properties.txt"
effective_unit_hash=$( { systemctl cat "$service"; systemctl show "$service" -p Environment -p EnvironmentFiles -p FragmentPath -p DropInPaths; } |sha256sum|awk '{print $1}')
grep -F -- "$binary" "$evidence_root/effective-unit-properties.txt" >/dev/null || die "effective ExecStart does not contain expected binary"
grep -F -- "$config" "$evidence_root/effective-unit-properties.txt" >/dev/null || die "effective ExecStart does not contain expected config"
initial_cache_bytes=$(du -sb "$cache"|awk '{print $1}'); expected_pid=$(systemctl show "$service" -p MainPID --value)
printf 'epoch\tevent\told_pid\tnew_pid\told_start\tnew_start\n' >"$evidence_root/restarts.tsv"

write_progress() {
  local now tmp; now=$(date +%s); tmp=$evidence_root/progress.json.tmp
  python3 - "$tmp" "$started_epoch" "$now" "$rounds" "$completed_rounds" "$samples" "$planned_restarts" "$planned_restart_target" "$failure" <<'PY'
import json,sys
p,s,n,target,done,sm,r,restart_target,f=sys.argv[1:]
with open(p,"w") as out:
 json.dump({"start_epoch":int(s),"now_epoch":int(n),"elapsed_seconds":int(n)-int(s),"rounds_target":int(target),"rounds_completed":int(done),"samples":int(sm),"planned_restarts":int(r),"planned_restarts_target":int(restart_target),"failure":f or None},out,indent=2); out.write("\n")
PY
  mv "$tmp" "$evidence_root/progress.json"
}
validate_workload() {
  [[ -r $evidence_root/workload-events.jsonl ]] || return 1
  [[ $(sha256sum "$script_dir/validate-native-preview-workload.py"|awk '{print $1}') == "$validator_hash" ]] || return 1
  python3 "$script_dir/validate-native-preview-workload.py" \
    "$evidence_root/workload-events.jsonl" --evidence-root "$evidence_root" \
    --restart-ledger "$evidence_root/restarts.tsv" --rounds "$rounds" \
    --planned-restarts "$planned_restart_target"
}
verify_gate_inputs() {
  [[ -z $workload_hook || $(sha256sum "$workload_hook"|awk '{print $1}') == "$workload_hash" ]] || fail_run "workload hook changed"
  [[ $(sha256sum "$script_dir/validate-native-preview-workload.py"|awk '{print $1}') == "$validator_hash" ]] || fail_run "workload evidence validator changed"
  [[ -z $canary_file || $(sha256sum "$canary_file"|awk '{print $1}') == "$canary_definitions_hash" ]] || fail_run "canary definitions changed"
  [[ -z $anchor_hook || $(sha256sum "$anchor_hook"|awk '{print $1}') == "$anchor_hash" ]] || fail_run "anchor hook changed"
}
scan_cache() {
  python3 - "$cache" "$evidence_root/cache-integrity.json" <<'PY'
import hashlib,json,os,re,stat,sys
root,out=map(os.path.realpath,sys.argv[1:]); refs=set(); errors=[]; objects=0; content=[]
meta_re=re.compile(r"^[0-9a-f]{64}\.json$")
for base,_,files in os.walk(root):
 for name in files:
  if not meta_re.match(name): continue
  path=os.path.join(base,name)
  try:
   m=json.load(open(path)); stem=name[:-5]; body_file=m.get("body_file")
   if body_file:
    if os.path.basename(body_file)!=body_file or not body_file.startswith(stem+".body."): raise ValueError("invalid body_file")
   else: body_file=stem+".body"
   body=os.path.realpath(os.path.join(base,body_file))
   if os.path.commonpath((root,body))!=root or os.path.dirname(body)!=os.path.realpath(base): raise ValueError("body escapes metadata directory")
   info=os.lstat(body)
   if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode): raise ValueError("body is not a regular non-symlink file")
   if info.st_size!=int(m["content_bytes"]): raise ValueError("size mismatch")
   h=hashlib.sha256()
   with open(body,"rb") as f:
    for chunk in iter(lambda:f.read(1024*1024),b""): h.update(chunk)
   body_sha=h.hexdigest(); content.append((os.path.relpath(body,root),body_sha))
   digest=m.get("content_digest","")
   if digest.startswith("sha256:") and body_sha!=digest[7:]: raise ValueError("digest mismatch")
   refs.add(os.path.realpath(body)); objects+=1
  except Exception as e: errors.append({"metadata":path,"error":str(e)})
temps=[]; orphans=[]
for base,_,files in os.walk(root):
 for name in files:
  path=os.path.join(base,name)
  if name.startswith((".body-",".metadata-")): temps.append(path)
  if re.match(r"^[0-9a-f]{64}\.body(?:\..+)?$",name) and os.path.realpath(path) not in refs: orphans.append(path)
with open(os.path.join(os.path.dirname(out),"cache-content-sha256.txt"),"w") as f:
 for path,digest in sorted(content): f.write(f"{digest}  {path}\n")
result={"objects":objects,"hashed_bodies":len(content),"temp_files":len(temps),"orphan_bodies":len(orphans),"errors":errors,"pass":not errors and not temps and not orphans}
json.dump(result,open(out,"w"),indent=2); open(out,"a").write("\n")
sys.exit(0 if result["pass"] else 1)
PY
}
finish() {
  local rc=$? ended elapsed result was_active=false; trap - EXIT; set +e
  ended=$(date +%s); elapsed=$((ended-started_epoch)); result=pass
  if ((rc != 0)) || [[ -n $failure ]] || ((completed_rounds < rounds)) || ((planned_restarts < planned_restart_target)); then result=fail; fi
  if [[ $result == pass && $mode == gate ]] && ! validate_workload; then failure="workload event receipts are missing, forged, or incomplete"; result=fail; fi
  if [[ $result == pass ]] && ! verify_gate_inputs; then failure="trusted gate input changed"; result=fail; fi
  if [[ $result == pass ]]; then
    systemctl is-active --quiet "$service" && was_active=true
    systemctl stop "$service" || { failure="could not quiesce service for final snapshot"; result=fail; }
    if [[ $result == pass ]]; then
      find "$cache" -xdev -printf '%P\t%y\t%s\t%T@\n' | sort >"$evidence_root/cache-snapshot.tsv"
      scan_cache || { failure="final cache integrity/temp/orphan scan failed"; result=fail; }
      if [[ -n $canary_file ]]; then
        findings=0
        while IFS= read -r value; do [[ -z $value ]] && continue; grep -R -F -l -- "$value" "$cache" "$evidence_root" >/dev/null 2>&1 && findings=$((findings+1)); done <"$canary_file"
        printf '{"definitions":%s,"findings":%s,"pass":%s}\n' "$(grep -cve '^$' "$canary_file")" "$findings" "$([[ $findings -eq 0 ]] && echo true || echo false)" >"$evidence_root/canary-scan.json"
        if ((findings>0)); then failure="final canary scan failed"; result=fail; fi
      fi
    fi
    [[ $was_active == false ]] || systemctl start "$service" || { failure="service did not restart after final snapshot"; result=fail; }
    [[ $was_active == false ]] || expected_pid=$(systemctl show "$service" -p MainPID --value)
  fi
  [[ $mode == gate ]] || { [[ $result == pass ]] && result=nonqualifying-smoke; }
  journalctl -u "$service" --since "@$started_epoch" --no-pager >"$evidence_root/journal-final.log" || true
  if [[ -n $canary_file ]]; then
    findings=0
    while IFS= read -r value; do [[ -z $value ]] && continue; grep -R -F -l -- "$value" "$cache" "$evidence_root" >/dev/null 2>&1 && findings=$((findings+1)); done <"$canary_file"
    printf '{"definitions":%s,"findings":%s,"pass":%s}\n' "$(grep -cve '^$' "$canary_file")" "$findings" "$([[ $findings -eq 0 ]] && echo true || echo false)" >"$evidence_root/canary-scan.json"
    if ((findings>0)); then failure="final whole-evidence canary scan failed"; result=fail; fi
  fi
  (cd "$evidence_root" && find . -type f ! -name SHA256SUMS ! -name PREANCHOR-SHA256SUMS ! -name result.json -print0 | sort -z | xargs -0 sha256sum >PREANCHOR-SHA256SUMS)
  preanchor_sha256=$(sha256sum "$evidence_root/PREANCHOR-SHA256SUMS"|awk '{print $1}')
  if [[ -n $anchor_hook ]]; then
    "$anchor_hook" "$evidence_root" "$evidence_root/PREANCHOR-SHA256SUMS" "$preanchor_sha256" || { failure="off-host anchor hook failed"; result=fail; }
    if ! python3 - "$evidence_root/anchor-receipt.json" "$preanchor_sha256" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
assert set(d)=={"preanchor_sha256","copied_off_host"}
assert d.get("preanchor_sha256")==sys.argv[2]
assert d.get("copied_off_host") is True
PY
    then failure="anchor receipt does not bind exact pre-anchor manifest"; result=fail; fi
    verify_gate_inputs || { failure="trusted gate input changed after anchor hook"; result=fail; }
    systemctl is-active --quiet "$service" || { failure="service inactive after anchor hook"; result=fail; }
    status=$(curl -fsS --max-time 15 "$base_url/api/v1/status") || { failure="status failed after anchor hook"; result=fail; }
    version=$(printf '%s' "$status"|json_value version) || { failure="invalid status after anchor hook"; result=fail; }
    [[ $version == "$expected_version" && $(systemctl show "$service" -p MainPID --value) == "$expected_pid" && $(systemctl show "$service" -p NRestarts --value) == "$initial_nrestarts" ]] || { failure="runtime invariant changed after anchor hook"; result=fail; }
    [[ $(sha256sum "$binary"|awk '{print $1}') == "$binary_hash" && $(sha256sum "$config"|awk '{print $1}') == "$config_hash" ]] || { failure="binary/config changed after anchor hook"; result=fail; }
    unit_effective_now=$( { systemctl cat "$service"; systemctl show "$service" -p Environment -p EnvironmentFiles -p FragmentPath -p DropInPaths; } |sha256sum|awk '{print $1}'); [[ $unit_effective_now == "$effective_unit_hash" ]] || { failure="unit changed after anchor hook"; result=fail; }
    free_pct=$(df -P "$cache"|awk 'NR==2 {gsub(/%/,"",$5); print 100-$5}'); free_bytes=$(df -PB1 "$cache"|awk 'NR==2 {print $4}'); ((free_pct>=min_free_percent && free_bytes>=min_free_bytes)) || { failure="disk reserve breached after anchor hook"; result=fail; }
    pid=$expected_pid; rss=$(awk '/VmRSS:/ {print $2}' "/proc/$pid/status" 2>/dev/null||printf 0); fds=$(find "/proc/$pid/fd" -mindepth 1 -maxdepth 1 2>/dev/null|wc -l); temps=$(find "$cache" -type f \( -name '.body-*' -o -name '.metadata-*' \)|wc -l)
    [[ -z $max_rss_kib ]] || ((rss<=max_rss_kib)) || { failure="RSS breached after anchor hook"; result=fail; }; [[ -z $max_fds ]] || ((fds<=max_fds)) || { failure="FDs breached after anchor hook"; result=fail; }; [[ -z $max_temp_files ]] || ((temps<=max_temp_files)) || { failure="temp files after anchor hook"; result=fail; }
    journalctl -u "$service" --since "@$started_epoch" --no-pager >"$evidence_root/journal-post-anchor.log" || true; ! grep -Eiq 'panic|fatal error|cache body size mismatch|digest mismatch' "$evidence_root/journal-post-anchor.log" || { failure="fatal journal signature after anchor hook"; result=fail; }
  fi
  if [[ -n $canary_file ]]; then
    while IFS= read -r value; do
      [[ -z $value ]] && continue
      if grep -R -F -l -- "$value" "$cache" "$evidence_root" >/dev/null 2>&1; then failure="post-anchor whole-evidence canary scan failed"; result=fail; fi
    done <"$canary_file"
  fi
  python3 - "$evidence_root/result.json" "$result" "$mode" "$started_utc" "$(date -u +%FT%TZ)" "$rounds" "$completed_rounds" "$round_budget" "$elapsed" "$samples" "$planned_restart_target" "$planned_restarts" "$expected_version" "$binary_hash" "$config_hash" "$effective_unit_hash" "$workload_hash" "$validator_hash" "$canary_definitions_hash" "$anchor_hash" "$failure" <<'PY'
import json,sys
p,result,mode,start,end,rounds,completed,round_budget,elapsed,samples,restart_target,restarts,version,bh,ch,uh,wh,vh,canh,ah,failure=sys.argv[1:]
with open(p,"w") as out:
 json.dump({"result":result,"mode":mode,"qualifying_gate":result=="pass" and mode=="gate","started_utc":start,"ended_utc":end,"rounds_target":int(rounds),"rounds_completed":int(completed),"round_budget_seconds":int(round_budget),"elapsed_seconds":int(elapsed),"samples":int(samples),"planned_restarts_target":int(restart_target),"planned_restarts":int(restarts),"expected_version":version,"binary_sha256":bh,"config_sha256":ch,"effective_unit_sha256":uh,"workload_hook_sha256":wh,"workload_validator_sha256":vh,"canary_definitions_sha256":canh,"anchor_hook_sha256":ah,"failure":failure or None},out,indent=2); out.write("\n")
PY
  if [[ $result == fail ]]; then
    if [[ -n $abort_hook ]]; then "$abort_hook" "$evidence_root"; printf '%s\n' "$?" >"$evidence_root/abort-hook.exit"; fi
    if [[ -n $rollback_hook ]]; then "$rollback_hook" "$evidence_root"; printf '%s\n' "$?" >"$evidence_root/rollback-hook.exit"; fi
  fi
  (cd "$evidence_root" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
  if [[ $result == fail ]]; then exit 1; fi
}
trap finish EXIT; trap 'failure="interrupted by signal"; exit 130' INT TERM HUP
fail_run() { failure=$*; write_progress; return 1; }
json_value() { python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"; }
check_canaries() {
  [[ -z $canary_file ]] && return 0
  while IFS= read -r value; do [[ -z $value ]] && continue; ! grep -R -F -l -- "$value" "$cache" "$evidence_root/snapshots" >/dev/null 2>&1 || fail_run "credential canary found"; done <"$canary_file"
}
sample() {
  local round=$1 phase=$2 now stamp dir status version free_pct free_bytes cache_bytes cache_growth pid rss fds temps current unit_now nrestarts unit_effective_now
  now=$(date +%s); printf -v stamp '%s-round-%02d-%s' "$(date -u +%Y%m%dT%H%M%SZ)" "$round" "$phase"; dir=$evidence_root/snapshots/$stamp; mkdir "$dir"
  verify_gate_inputs; systemctl is-active --quiet "$service" || fail_run "service is not active"
  curl -fsS --max-time 10 "$base_url/healthz" >"$dir/health.json" || fail_run "health failed"
  status=$(curl -fsS --max-time 15 "$base_url/api/v1/status") || fail_run "status failed"; printf '%s\n' "$status" >"$dir/status.json"
  curl -fsS --max-time 15 "$base_url/metrics" >"$dir/metrics.txt" || fail_run "metrics failed"
  version=$(printf '%s' "$status"|json_value version) || fail_run "invalid status JSON"; [[ $version == "$expected_version" ]] || fail_run "version changed: $version"
  current=$(sha256sum "$binary"|awk '{print $1}'); [[ $current == "$binary_hash" ]] || fail_run "binary changed"
  current=$(sha256sum "$config"|awk '{print $1}'); [[ $current == "$config_hash" ]] || fail_run "config changed"
  current=$(sha256sum "$script_dir/validate-native-preview-workload.py"|awk '{print $1}'); [[ $current == "$validator_hash" ]] || fail_run "workload evidence validator changed"
  unit_now=missing; [[ -n $unit_fragment && -r $unit_fragment ]] && unit_now=$(sha256sum "$unit_fragment"|awk '{print $1}'); [[ $unit_now == "$unit_hash" ]] || fail_run "unit fragment changed"
  unit_effective_now=$( { systemctl cat "$service"; systemctl show "$service" -p Environment -p EnvironmentFiles -p FragmentPath -p DropInPaths; } |sha256sum|awk '{print $1}')
  [[ $unit_effective_now == "$effective_unit_hash" ]] || fail_run "effective unit changed"
  nrestarts=$(systemctl show "$service" -p NRestarts --value); [[ $nrestarts == "$initial_nrestarts" ]] || fail_run "systemd reports an unplanned restart"
  pid=$(systemctl show "$service" -p MainPID --value); [[ $pid == "$expected_pid" ]] || fail_run "unexplained process start: PID changed outside restart ledger"
  free_pct=$(df -P "$cache"|awk 'NR==2 {gsub(/%/,"",$5); print 100-$5}'); ((free_pct >= min_free_percent)) || fail_run "only ${free_pct}% free"
  free_bytes=$(df -PB1 "$cache"|awk 'NR==2 {print $4}'); ((free_bytes >= min_free_bytes)) || fail_run "free bytes below absolute reserve"
  cache_bytes=$(du -sb "$cache"|awk '{print $1}'); cache_growth=$((cache_bytes-initial_cache_bytes))
  [[ -z $max_cache_growth_bytes ]] || ((cache_growth<=max_cache_growth_bytes)) || fail_run "cache growth exceeds bound"
  rss=0; fds=0
  if [[ $pid =~ ^[0-9]+$ && $pid -gt 0 ]]; then rss=$(awk '/VmRSS:/ {print $2}' "/proc/$pid/status" 2>/dev/null||printf 0); fds=$(find "/proc/$pid/fd" -mindepth 1 -maxdepth 1 2>/dev/null|wc -l); fi
  temps=$(find "$cache" -type f \( -name '.body-*' -o -name '.metadata-*' \)|wc -l)
  [[ -z $max_rss_kib ]] || ((rss<=max_rss_kib)) || fail_run "RSS exceeds bound"
  [[ -z $max_fds ]] || ((fds<=max_fds)) || fail_run "file descriptors exceed bound"
  [[ -z $max_temp_files ]] || ((temps<=max_temp_files)) || fail_run "temporary files exceed bound"
  systemctl show "$service" -p ActiveEnterTimestampMonotonic -p ExecMainStartTimestamp -p MainPID -p NRestarts >"$dir/systemd.txt"
  journalctl -u "$service" --since "@$started_epoch" --until "@$now" --no-pager >"$dir/journal.log" || true
  du -sk "$cache" >"$dir/cache-du.txt"; df -Pk "$cache" >"$dir/filesystem.txt"
  printf 'epoch\tfree_percent\tfree_bytes\tcache_bytes\tcache_growth_bytes\trss_kib\tfds\ttemp_files\n%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$now" "$free_pct" "$free_bytes" "$cache_bytes" "$cache_growth" "${rss:-0}" "$fds" "$temps" >"$dir/process.tsv"
  ! grep -Eiq 'panic|fatal error|cache body size mismatch|digest mismatch' "$dir/journal.log" || fail_run "fatal or integrity signature in journal"
  check_canaries
  if [[ -n $workload_hook ]]; then
    events=$evidence_root/workload-events.jsonl; touch "$events"; cp "$events" "$dir/events-before.jsonl"
    N0DING_RESTART_INDEX=${last_restart_index:-0} N0DING_RESTART_PID=${last_restart_pid:-0} N0DING_RESTART_START=${last_restart_start:-none} \
      timeout --foreground "$hook_timeout" "$workload_hook" "$base_url" "$dir" "$evidence_root" "$round" "$phase" || fail_run "workload hook failed or timed out in round $round ($phase)"
    python3 - "$dir/events-before.jsonl" "$events" <<'PY' || fail_run "workload event log was rewritten"
import sys
before=open(sys.argv[1],"rb").read(); after=open(sys.argv[2],"rb").read()
assert after.startswith(before) and len(after)>len(before)
PY
  fi
  verify_gate_inputs
  systemctl is-active --quiet "$service" || fail_run "service became inactive after hook"
  curl -fsS --max-time 10 "$base_url/healthz" >/dev/null || fail_run "post-hook health failed"
  status=$(curl -fsS --max-time 15 "$base_url/api/v1/status") || fail_run "post-hook status failed"
  version=$(printf '%s' "$status"|json_value version) || fail_run "invalid post-hook status JSON"; [[ $version == "$expected_version" ]] || fail_run "version changed after hook"
  [[ $(sha256sum "$binary"|awk '{print $1}') == "$binary_hash" ]] || fail_run "binary changed after hook"
  [[ $(sha256sum "$config"|awk '{print $1}') == "$config_hash" ]] || fail_run "config changed after hook"
  unit_now=missing; [[ -n $unit_fragment && -r $unit_fragment ]] && unit_now=$(sha256sum "$unit_fragment"|awk '{print $1}'); [[ $unit_now == "$unit_hash" ]] || fail_run "unit changed after hook"
  unit_effective_now=$( { systemctl cat "$service"; systemctl show "$service" -p Environment -p EnvironmentFiles -p FragmentPath -p DropInPaths; } |sha256sum|awk '{print $1}'); [[ $unit_effective_now == "$effective_unit_hash" ]] || fail_run "effective unit changed after hook"
  nrestarts=$(systemctl show "$service" -p NRestarts --value); [[ $nrestarts == "$initial_nrestarts" ]] || fail_run "unplanned restart after hook"
  pid=$(systemctl show "$service" -p MainPID --value); rss=$(awk '/VmRSS:/ {print $2}' "/proc/$pid/status" 2>/dev/null||printf 0); fds=$(find "/proc/$pid/fd" -mindepth 1 -maxdepth 1 2>/dev/null|wc -l)
  [[ $pid == "$expected_pid" ]] || fail_run "PID changed after hook"
  free_pct=$(df -P "$cache"|awk 'NR==2 {gsub(/%/,"",$5); print 100-$5}'); ((free_pct>=min_free_percent)) || fail_run "post-hook free percentage below reserve"
  free_bytes=$(df -PB1 "$cache"|awk 'NR==2 {print $4}'); cache_bytes=$(du -sb "$cache"|awk '{print $1}'); cache_growth=$((cache_bytes-initial_cache_bytes)); temps=$(find "$cache" -type f \( -name '.body-*' -o -name '.metadata-*' \)|wc -l)
  ((free_bytes>=min_free_bytes)) || fail_run "post-hook free bytes below reserve"
  [[ -z $max_cache_growth_bytes ]] || ((cache_growth<=max_cache_growth_bytes)) || fail_run "post-hook cache growth exceeds bound"
  [[ -z $max_rss_kib ]] || ((rss<=max_rss_kib)) || fail_run "post-hook RSS exceeds bound"
  [[ -z $max_fds ]] || ((fds<=max_fds)) || fail_run "post-hook file descriptors exceed bound"
  [[ -z $max_temp_files ]] || ((temps<=max_temp_files)) || fail_run "post-hook temporary files exceed bound"
  journalctl -u "$service" --since "@$started_epoch" --no-pager >"$dir/journal-post-hook.log" || true
  ! grep -Eiq 'panic|fatal error|cache body size mismatch|digest mismatch' "$dir/journal-post-hook.log" || fail_run "fatal or integrity signature after hook"
  check_canaries
  samples=$((samples+1)); write_progress
}

python3 - "$evidence_root/run.json" "$mode" "$started_utc" "$rounds" "$planned_restart_target" "$round_budget" "$service" "$base_url" "$expected_version" "$binary_hash" "$config_hash" "$effective_unit_hash" "$workload_hash" "$validator_hash" "$canary_definitions_hash" "$anchor_hash" "$min_free_percent" "$min_free_bytes" "${max_rss_kib:-null}" "${max_fds:-null}" "${max_cache_growth_bytes:-null}" "${max_temp_files:-null}" <<'PY'
import json,sys
p,mode,start,rounds,restarts,round_budget,service,url,version,bh,ch,uh,wh,vh,canh,ah,freepct,freebytes,rss,fds,growth,temps=sys.argv[1:]
num=lambda x: None if x=="null" else int(x)
with open(p,"w") as out:
 json.dump({"mode":mode,"started_utc":start,"rounds":int(rounds),"planned_restarts":int(restarts),"round_budget_seconds":int(round_budget),"acceptance_is_elapsed_time":False,"service":service,"base_url":url,"expected_version":version,"binary_sha256":bh,"config_sha256":ch,"effective_unit_sha256":uh,"workload_hook_sha256":wh,"workload_validator_sha256":vh,"canary_definitions_sha256":canh,"anchor_hook_sha256":ah,"minimum_free_percent":int(freepct),"minimum_free_bytes":int(freebytes),"bounds":{"max_rss_kib":num(rss),"max_fds":num(fds),"max_cache_growth_bytes":num(growth),"max_temp_files":num(temps)}},out,indent=2); out.write("\n")
PY
for round in $(seq 1 "$rounds"); do
  round_started=$(date +%s)
  phase=warm
  ((round == 1)) && phase=cold
  desired_restarts=$(((round-1)*planned_restart_target/(rounds-1)))
  if ((desired_restarts > planned_restarts)); then
    old_pid=$expected_pid; old_start=$(systemctl show "$service" -p ExecMainStartTimestampMonotonic --value)
    systemctl restart "$service" || fail_run "planned restart failed"; planned_restarts=$((planned_restarts+1))
    for _ in $(seq 1 60); do curl -fsS --max-time 2 "$base_url/healthz" >/dev/null 2>&1 && break; sleep 1; done
    curl -fsS --max-time 2 "$base_url/healthz" >/dev/null || fail_run "health did not recover"
    expected_pid=$(systemctl show "$service" -p MainPID --value); new_start=$(systemctl show "$service" -p ExecMainStartTimestampMonotonic --value)
    [[ $expected_pid != "$old_pid" && $new_start != "$old_start" ]] || fail_run "planned restart did not create a new process"
    printf '%s\tplanned\t%s\t%s\t%s\t%s\n' "$(date +%s)" "$old_pid" "$expected_pid" "$old_start" "$new_start" >>"$evidence_root/restarts.tsv"
    last_restart_index=$planned_restarts; last_restart_pid=$expected_pid; last_restart_start=$new_start
    phase=post_restart
  fi
  sample "$round" "$phase"
  completed_rounds=$round
  write_progress
  round_elapsed=$(($(date +%s)-round_started))
  ((round_budget == 0 || round_elapsed <= round_budget)) || fail_run "round runtime budget exceeded"
done
((planned_restarts>=planned_restart_target)) || fail_run "planned restart count is incomplete"
