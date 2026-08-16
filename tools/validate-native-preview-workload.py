#!/usr/bin/env python3
"""Validate append-only focused Public Preview workload events."""
import argparse, base64, hashlib, json, os, re, sys

ECOSYSTEMS = ("npm", "pip", "uv", "oci")
TARGETS = {"npm":"testdata/npm-compat/package-lock.json", "pip":"idna==3.10", "uv":"idna==3.10", "oci":"library/alpine:3.20"}

def fail(message): raise ValueError(message)
def integer(value, label, minimum=0):
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum: fail(f"{label} must be integer >= {minimum}")
def artifact(root, event, field):
    rel=event.get(field); digest=event.get(field+"_sha256")
    if not isinstance(rel,str) or os.path.isabs(rel) or ".." in rel.split(os.sep): fail(f"invalid {field}")
    path=os.path.realpath(os.path.join(root,rel))
    if os.path.commonpath((root,path)) != root or not os.path.isfile(path): fail(f"missing {field}: {rel}")
    with open(path,"rb") as source: actual=hashlib.sha256(source.read()).hexdigest()
    if digest != actual: fail(f"{field} hash mismatch")
    return path
def json_artifact(root,event,field):
    with open(artifact(root,event,field),encoding="utf-8") as source: return json.load(source)
def status_hits(root,event,field,ecosystem):
    value=json_artifact(root,event,field); kind="pypi" if ecosystem in ("pip","uv") else ecosystem
    matches=[r for r in value.get("repositories",[]) if r.get("type")==kind]
    if len(matches)!=1: fail(f"{field} missing {kind} repository")
    integer(matches[0].get("cache_hits"),f"{field}.cache_hits"); return matches[0]["cache_hits"]
def launch(root,event):
    value=json_artifact(root,event,"launch_artifact")
    row=value.get(event.get("ecosystem")) if isinstance(value,dict) else None
    if not isinstance(row,dict): fail("launch ledger missing ecosystem")
    for field in ("started_epoch_ms","ended_epoch_ms","exit_code"): integer(row.get(field),f"launch.{field}",1 if field!="exit_code" else 0)
    return row
def command(root,event,ecosystem):
    value=json_artifact(root,event,"command_artifact"); argv=value.get("argv") if isinstance(value,dict) else None
    if not isinstance(argv,list) or not all(isinstance(x,str) and x for x in argv): fail("invalid raw command argv")
    required={"npm":("npm","ci"),"pip":("pip","install","idna==3.10"),"uv":("uv","pip","install","idna==3.10"),"oci":("docker","pull","library/alpine:3.20")}[ecosystem]
    position=0
    for token in argv:
        wanted=required[position] if position<len(required) else None
        if wanted is not None and (token==wanted or (wanted=="pip" and token.endswith("/pip")) or (wanted=="library/alpine:3.20" and token.endswith("/"+wanted))): position+=1
    if position!=len(required): fail(f"command does not bind required {ecosystem} target")
    if ecosystem=="npm":
        fixture=os.path.join(os.path.dirname(os.path.dirname(__file__)),TARGETS["npm"])
        with open(fixture,"rb") as source: expected=hashlib.sha256(source.read()).hexdigest()
        if value.get("fixture_sha256")!=expected: fail("npm command does not bind committed lock fixture")
    return argv
def interval(event):
    integer(event.get("started_epoch_ms"),"started_epoch_ms",1); integer(event.get("ended_epoch_ms"),"ended_epoch_ms",1)
    if event["ended_epoch_ms"] < event["started_epoch_ms"]: fail("negative event interval")
    return event["started_epoch_ms"],event["ended_epoch_ms"]
def integrity(event, ecosystem):
    value=event.get("integrity")
    if not isinstance(value,dict): fail("invalid integrity evidence")
    algorithm,digest=value.get("algorithm"),value.get("value")
    if ecosystem=="npm":
        if algorithm!="sri-sha512" or not isinstance(digest,str) or not digest.startswith("sha512-"): fail("invalid npm SRI")
        try: raw=base64.b64decode(digest[7:],validate=True)
        except Exception: fail("invalid npm SRI base64")
        if len(raw)!=64: fail("invalid npm SRI length")
    elif ecosystem in ("pip","uv"):
        if algorithm!="sha256" or not isinstance(digest,str) or not re.fullmatch(r"[0-9a-f]{64}",digest): fail(f"invalid {ecosystem} sha256")
    elif ecosystem=="oci":
        if algorithm!="oci-repo-digest" or not isinstance(digest,str) or not re.fullmatch(r"sha256:[0-9a-f]{64}",digest): fail("invalid OCI RepoDigest")
    return digest
def recompute_integrity(root,event,ecosystem):
    claimed=integrity(event,ecosystem)
    if ecosystem=="oci":
        value=json_artifact(root,event,"integrity_artifact")
        refs=value.get("RepoDigests")
        if not isinstance(refs,list): fail("invalid OCI inspect evidence")
        argv=command(root,event,ecosystem); pulled=argv[-1]
        head,sep,tail=pulled.rpartition("/")
        if ":" in tail: tail=tail.rsplit(":",1)[0]
        canonical_repo=(head+sep+tail) if sep else tail
        digests=[x.rsplit("@",1)[1] for x in refs if isinstance(x,str) and x.startswith(canonical_repo+"@")]
        if claimed not in digests: fail("OCI integrity not present in inspect evidence")
    elif ecosystem=="npm":
        value=json_artifact(root,event,"integrity_artifact")
        fixture=os.path.join(os.path.dirname(os.path.dirname(__file__)),TARGETS["npm"])
        with open(fixture,encoding="utf-8") as source: locked=json.load(source)["packages"]["node_modules/is-number"]
        exact={key:locked[key] for key in ("version","resolved","integrity")}
        if value!=exact or value.get("version")!="7.0.0" or claimed!=exact["integrity"]: fail("npm integrity does not bind exact committed is-number@7.0.0 lock object")
    else:
        path=artifact(root,event,"integrity_artifact")
        algorithm="sha512" if ecosystem=="npm" else "sha256"
        h=hashlib.new(algorithm)
        with open(path,"rb") as source:
            for chunk in iter(lambda:source.read(1024*1024),b""): h.update(chunk)
        actual="sha512-"+base64.b64encode(h.digest()).decode() if ecosystem=="npm" else h.hexdigest()
        if claimed!=actual: fail(f"fabricated {ecosystem} integrity")
    return claimed
def metrics(root,event,field):
    raw=json_artifact(root,event,field)
    status=raw.get("status"); prometheus=raw.get("metrics"); files=raw.get("cache_files")
    if not isinstance(status,dict) or not isinstance(prometheus,str) or not isinstance(files,list): fail(f"invalid raw {field}")
    kind="pypi" if event.get("ecosystem") in ("pip","uv") else event.get("ecosystem")
    repos=[repo for repo in status.get("repositories",[]) if repo.get("type")==kind]
    if len(repos)!=1: fail(f"{field} missing cancellation target repository")
    integer(repos[0].get("client_canceled"),f"{field}.client_canceled"); canceled=repos[0]["client_canceled"]
    match=re.search(r"^n0ding_storage_reserved_bytes ([0-9]+)$",prometheus,re.M)
    if not match: fail(f"{field} missing reservation metric")
    temps=sum(1 for p in files if isinstance(p,str) and os.path.basename(p).startswith((".body-",".metadata-")))
    bodies={p for p in files if isinstance(p,str) and re.search(r"/[0-9a-f]{64}\.body(?:\..+)?$","/"+p)}
    refs={x.get("body") for x in raw.get("cache_metadata",[]) if isinstance(x,dict) and isinstance(x.get("body"),str)}
    return {"client_canceled":canceled,"temp_files":temps,"orphan_bodies":len(bodies-refs),"reservations":int(int(match.group(1))!=0)}
def load(path):
    events=[]
    with open(path,encoding="utf-8") as source:
        for number,line in enumerate(source,1):
            try: value=json.loads(line)
            except json.JSONDecodeError as error: fail(f"line {number}: {error}")
            if not isinstance(value,dict): fail(f"line {number} is not object")
            events.append(value)
    return events
def restart_rows(path):
    rows=[]
    with open(path,encoding="utf-8") as source:
        next(source,None)
        for line in source:
            epoch,kind,old_pid,new_pid,old_start,new_start=line.rstrip("\n").split("\t")
            if kind != "planned": fail("restart ledger contains non-planned restart")
            if old_pid == new_pid or old_start == new_start: fail("restart ledger identity did not change")
            rows.append({"index":len(rows)+1,"pid":int(new_pid),"start":new_start})
    return rows
def expected_phase(round_number, rounds, restarts):
    if round_number == 1: return "cold"
    before=(round_number-1)*restarts//(rounds-1)
    prior=(round_number-2)*restarts//(rounds-1)
    return "post_restart" if before>prior else "warm"
def validate(events, root, rounds, restarts, ledger):
    if len(ledger) != restarts: fail("restart ledger length mismatch")
    clients=[event for event in events if event.get("kind")=="client"]
    expected={(r,e) for r in range(1,rounds+1) for e in ECOSYSTEMS}
    actual=set()
    for event in clients:
        r=event.get("round"); eco=event.get("ecosystem"); integer(r,"round",1)
        key=(r,eco)
        if key not in expected or key in actual: fail(f"unexpected/duplicate client event {key}")
        actual.add(key); phase=expected_phase(r,rounds,restarts)
        if event.get("phase") != phase: fail(f"wrong phase for round {r}")
        if event.get("target") != TARGETS[eco]: fail(f"wrong target for {eco}")
        command(root,event,eco)
        for field in ("client","client_version","cache_identity"):
            if not isinstance(event.get(field),str) or not event[field]: fail(f"missing {field}")
        integer(event.get("exit_code"),"exit_code"); row=launch(root,event)
        if any(event.get(k)!=row.get(k) for k in ("started_epoch_ms","ended_epoch_ms","exit_code")): fail("event interval/exit does not match launch ledger")
        if event["exit_code"] != 0: fail("client exit was nonzero")
        interval(event); artifact(root,event,"output_artifact")
        before=status_hits(root,event,"status_before_artifact",eco); after=status_hits(root,event,"status_after_artifact",eco)
        if event.get("hits_before")!=before or event.get("hits_after")!=after: fail("fabricated cache hit delta")
        pre=json_artifact(root,event,"cache_prestate_artifact")
        if pre != {"identity":event.get("cache_identity"),"entries":[]}: fail("client cache was reused or nonempty")
        if phase in ("warm","post_restart") and event["hits_after"] <= event["hits_before"]: fail(f"{eco} {phase} did not increase cache hits")
        recompute_integrity(root,event,eco)
        if phase=="post_restart":
            binding=event.get("restart")
            index=(r-1)*restarts//(rounds-1)
            row=ledger[index-1]
            if binding != row: fail(f"round {r} is not bound to restart ledger")
    if actual != expected: fail(f"client coverage mismatch: missing {sorted(expected-actual)}")
    for eco in ECOSYSTEMS:
        group=[event for event in clients if event["ecosystem"]==eco]
        if len({event["cache_identity"] for event in group}) != rounds: fail(f"{eco} did not use fresh client caches")
        if len({recompute_integrity(root,event,eco) for event in group}) != 1: fail(f"{eco} integrity changed across phases")
    for r in range(1,rounds+1):
        group=[event for event in clients if event["round"]==r]
        ledgers={event["launch_artifact_sha256"] for event in group}
        if len(ledgers)!=1 or len({event["launch_artifact"] for event in group})!=1: fail(f"round {r} lacks one central launch ledger")
        if max(launch(root,e)["started_epoch_ms"] for e in group) >= min(launch(root,e)["ended_epoch_ms"] for e in group): fail(f"round {r} clients did not overlap")
    paths=[event for event in events if event.get("kind")=="failure_path"]
    for name in ("cancellation",):
        pair=[e for e in paths if e.get("path")==name]
        if [e.get("stage") for e in pair] != ["attempt","retry"]: fail(f"{name} requires ordered attempt/retry")
        for event in pair:
            interval(event); artifact(root,event,"output_artifact")
            integer(event.get("exit_code"),"exit_code")
            if event.get("ecosystem") not in ECOSYSTEMS or not isinstance(event.get("object"),str) or not event["object"]: fail("cancellation target missing")
            integrity(event,event["ecosystem"])
            before=metrics(root,event,"before_metrics_artifact"); after=metrics(root,event,"after_metrics_artifact")
            event["_before_metrics"],event["_after_metrics"]=before,after
        if pair[0]["exit_code"] == 0 or pair[0].get("terminated") is not True: fail("cancellation attempt was not terminated")
        if pair[0]["_after_metrics"]["client_canceled"] <= pair[0]["_before_metrics"]["client_canceled"]: fail("client_canceled did not increase")
        for event in pair:
            after=event["_after_metrics"]
            if any(after[name] for name in ("temp_files","orphan_bodies","reservations")): fail("cancellation leaked temporary state or reservation")
        if pair[1]["exit_code"] != 0: fail(f"{name} retry did not recover")
        recompute_integrity(root,pair[1],pair[1]["ecosystem"])
        if pair[0]["ecosystem"]!=pair[1]["ecosystem"] or pair[0]["object"]!=pair[1]["object"] or pair[0]["integrity"]!=pair[1]["integrity"]: fail("cancellation retry target/integrity changed")

def main():
    parser=argparse.ArgumentParser(); parser.add_argument("events"); parser.add_argument("--evidence-root",required=True); parser.add_argument("--restart-ledger",required=True); parser.add_argument("--rounds",type=int,required=True); parser.add_argument("--planned-restarts",type=int,required=True); args=parser.parse_args()
    try: validate(load(args.events),os.path.realpath(args.evidence_root),args.rounds,args.planned_restarts,restart_rows(args.restart_ledger))
    except (OSError,ValueError,KeyError) as error: print(f"invalid native preview workload evidence: {error}",file=sys.stderr); return 1
    return 0
if __name__=="__main__": raise SystemExit(main())
