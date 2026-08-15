#!/usr/bin/env python3
"""Validate append-only focused Public Preview workload events."""
import argparse, base64, hashlib, json, os, re, sys

ECOSYSTEMS = ("npm", "pip", "uv", "oci")

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
def metrics(root,event,field):
    artifact(root,event,field)
    with open(os.path.join(root,event[field]),encoding="utf-8") as source: value=json.load(source)
    for name in ("client_canceled","temp_files","orphan_bodies","reservations"):
        integer(value.get(name),f"{field}.{name}")
    return value
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
        for field in ("client","client_version","cache_identity"):
            if not isinstance(event.get(field),str) or not event[field]: fail(f"missing {field}")
        integer(event.get("exit_code"),"exit_code");
        if event["exit_code"] != 0: fail("client exit was nonzero")
        interval(event); artifact(root,event,"output_artifact")
        for field in ("hits_before","hits_after"):
            integer(event.get(field),field)
        if phase in ("warm","post_restart") and event["hits_after"] <= event["hits_before"]: fail(f"{eco} {phase} did not increase cache hits")
        integrity(event,eco)
        if phase=="post_restart":
            binding=event.get("restart")
            index=(r-1)*restarts//(rounds-1)
            row=ledger[index-1]
            if binding != row: fail(f"round {r} is not bound to restart ledger")
    if actual != expected: fail(f"client coverage mismatch: missing {sorted(expected-actual)}")
    for eco in ECOSYSTEMS:
        group=[event for event in clients if event["ecosystem"]==eco]
        if len({event["cache_identity"] for event in group}) != rounds: fail(f"{eco} did not use fresh client caches")
        if len({integrity(event,eco) for event in group}) != 1: fail(f"{eco} integrity changed across phases")
    for r in range(1,rounds+1):
        group=[event for event in clients if event["round"]==r]
        if max(interval(e)[0] for e in group) >= min(interval(e)[1] for e in group): fail(f"round {r} clients did not overlap")
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
        if pair[1]["exit_code"] != 0 or pair[1].get("integrity_pass") is not True: fail(f"{name} retry did not recover")
        if pair[0]["ecosystem"]!=pair[1]["ecosystem"] or pair[0]["object"]!=pair[1]["object"] or pair[0]["integrity"]!=pair[1]["integrity"]: fail("cancellation retry target/integrity changed")

def main():
    parser=argparse.ArgumentParser(); parser.add_argument("events"); parser.add_argument("--evidence-root",required=True); parser.add_argument("--restart-ledger",required=True); parser.add_argument("--rounds",type=int,required=True); parser.add_argument("--planned-restarts",type=int,required=True); args=parser.parse_args()
    try: validate(load(args.events),os.path.realpath(args.evidence_root),args.rounds,args.planned_restarts,restart_rows(args.restart_ledger))
    except (OSError,ValueError,KeyError) as error: print(f"invalid native preview workload evidence: {error}",file=sys.stderr); return 1
    return 0
if __name__=="__main__": raise SystemExit(main())
