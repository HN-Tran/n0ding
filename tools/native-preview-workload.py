#!/usr/bin/env python3
"""Deployment adapter for the native focused Public Preview gate.

This file is intentionally host-specific at the boundary (Docker daemon and
cache path), but contains no credentials. The gate calls it with five args.
"""
import hashlib, json, os, pathlib, shutil, signal, subprocess, sys, threading, time, urllib.parse, urllib.request

TARGETS={"npm":"testdata/npm-compat/package-lock.json","pip":"idna==3.10","uv":"idna==3.10","oci":"library/alpine:3.20"}

def die(message): raise SystemExit(f"native-preview-workload: {message}")
def ms(): return time.time_ns()//1_000_000
def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()
def write_json(path,value): path.write_text(json.dumps(value,indent=2,sort_keys=True)+"\n",encoding="utf-8")
def wait_concurrently(processes,clock=ms):
    results={}; lock=threading.Lock()
    def wait(name,process):
        rc=process.wait(); ended=clock()
        with lock: results[name]=(rc,ended)
    threads=[threading.Thread(target=wait,args=item,daemon=False) for item in processes.items()]
    for thread in threads: thread.start()
    for thread in threads: thread.join()
    return results
def fetch(url,accept=None):
    request=urllib.request.Request(url,headers={"Accept":accept} if accept else {})
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=30) as response: return response.read()

if len(sys.argv)!=6: die("expected BASE_URL SNAPSHOT_DIR EVIDENCE_ROOT ROUND PHASE")
base=sys.argv[1].rstrip("/"); snapshot=pathlib.Path(sys.argv[2]).resolve(); evidence=pathlib.Path(sys.argv[3]).resolve()
round_number=int(sys.argv[4]); phase=sys.argv[5]
repo=pathlib.Path(os.environ.get("N0DING_SOURCE_ROOT",pathlib.Path(__file__).resolve().parent.parent)).resolve()
if not (repo/"tools/validate-native-preview-workload.py").is_file(): die("N0DING_SOURCE_ROOT must name the exact candidate checkout")
cache=pathlib.Path(os.environ.get("N0DING_CACHE_PATH","")).resolve()
if not cache.is_dir(): die("N0DING_CACHE_PATH must name the dedicated gate cache")
if os.environ.get("N0DING_EXPECTED_BASE_URL",base).rstrip("/")!=base: die("unexpected deployment base URL")
origin=urllib.parse.urlsplit(base)
if origin.scheme!="http" or origin.path not in ("","/") or not origin.netloc: die("gate base URL must be an HTTP origin")
if (origin.hostname,origin.port)!=("100.91.139.14",8081) and os.environ.get("N0DING_ALLOW_OTHER_ORIGIN")!="1": die("adapter expects 100.91.139.14:8081")
if (origin.hostname,origin.port)==("100.91.139.14",8081) and cache!=pathlib.Path("/var/lib/n0ding-preview"): die("real preview origin requires /var/lib/n0ding-preview")
work=evidence/f"workload-round-{round_number:02d}-{phase}"
work.mkdir(mode=0o700)
write_json(work/"deployment.json",{"base_url":base,"server_cache_path":str(cache),"round":round_number,"phase":phase})
for command in ("bash","npm","python3","uv","docker","curl"):
    if shutil.which(command) is None: die(f"missing real client: {command}")

def rel(path): return str(path.resolve().relative_to(evidence))
def bind(event,name,path): event[name]=rel(path); event[name+"_sha256"]=sha(path)
def status(path=None):
    value=json.loads(fetch(base+"/api/v1/status"))
    if path: write_json(path,value)
    return value
def hits(value,ecosystem):
    kind="pypi" if ecosystem in ("pip","uv") else ecosystem
    return next(row["cache_hits"] for row in value["repositories"] if row["type"]==kind)
def command_version(argv): return subprocess.check_output(argv,stderr=subprocess.STDOUT,text=True,env=clean_env).splitlines()[0]

clients={}; launch={}; events=[]; start_barrier=work/"start-barrier"
lock_source=repo/TARGETS["npm"]
lock=json.loads(lock_source.read_text())
npm_dir=work/"npm"; npm_dir.mkdir(); shutil.copy(repo/"testdata/npm-compat/package.json",npm_dir/"package.json")
npm_lock=json.loads(lock_source.read_text())
for value in npm_lock["packages"].values():
    if isinstance(value,dict) and "resolved" in value: value["resolved"]=value["resolved"].replace("http://127.0.0.1:18080",base)
write_json(npm_dir/"package-lock.json",npm_lock)

pip_target=work/"pip-target"; pip_cache=work/"pip-cache"; pip_target.mkdir(); pip_cache.mkdir()
uv_env=work/"uv-env"; uv_cache=work/"uv-cache"; uv_cache.mkdir()
controlled_home=work/"home"; controlled_home.mkdir(); docker_config=work/"docker-config"; docker_config.mkdir()
clean_env={"PATH":os.environ.get("PATH","/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"),"LANG":"C.UTF-8","LC_ALL":"C.UTF-8","HOME":str(controlled_home),"XDG_CONFIG_HOME":str(controlled_home/".config"),"PIP_CONFIG_FILE":os.devnull,"NPM_CONFIG_USERCONFIG":os.devnull,"DOCKER_CONFIG":str(docker_config)}
subprocess.run(["uv","venv",str(uv_env)],check=True,stdout=subprocess.DEVNULL,env=clean_env)
oci_ref=f"{origin.netloc}/{TARGETS['oci']}"
subprocess.run(["docker","image","rm",oci_ref],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env=clean_env)
if subprocess.run(["docker","image","inspect",oci_ref],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,env=clean_env).returncode==0: die("proxy OCI reference remains in Docker client state")
registry_config=json.loads(subprocess.check_output(["docker","info","--format","{{json .RegistryConfig.IndexConfigs}}"],text=True,env=clean_env))
if origin.netloc not in registry_config or registry_config[origin.netloc].get("Secure",True): die(f"Docker daemon does not trust HTTP registry {origin.netloc}")

specs={
 "npm":{"argv":["npm","ci","--ignore-scripts","--no-audit","--no-fund","--registry",base+"/npm/","--cache",str(work/"npm-cache"),"--prefer-online"],"cwd":npm_dir,"cache":work/"npm-cache"},
 "pip":{"argv":[sys.executable,"-m","pip","install","--disable-pip-version-check","--no-deps","--target",str(pip_target),"--cache-dir",str(pip_cache),"--index-url",base+"/pypi/simple/","--trusted-host",origin.hostname,TARGETS["pip"]],"cwd":work,"cache":pip_cache},
 "uv":{"argv":["uv","pip","install","--python",str(uv_env/"bin/python"),"--index-url",base+"/pypi/simple/",TARGETS["uv"]],"cwd":work,"cache":uv_cache,"env":{"UV_CACHE_DIR":str(uv_cache)}},
 "oci":{"argv":["docker","pull",oci_ref],"cwd":work,"cache":None},
}

for ecosystem,spec in specs.items():
    command_file=work/f"{ecosystem}-command.json"; command_value={"argv":spec["argv"]}
    if ecosystem=="npm": command_value["fixture_sha256"]=sha(lock_source)
    write_json(command_file,command_value)
    pre=work/f"{ecosystem}-cache-prestate.json"
    identity=f"round-{round_number}-{ecosystem}-{hashlib.sha256(str(spec.get('cache') or oci_ref).encode()).hexdigest()[:16]}"
    entries=sorted(str(p.relative_to(docker_config if ecosystem=="oci" else spec["cache"])) for p in (docker_config if ecosystem=="oci" else spec["cache"]).rglob("*") if p.is_file())
    prestate={"identity":identity,"entries":entries}
    if ecosystem=="oci": prestate.update({"proxy_reference":oci_ref,"proxy_image_present":False,"retained_layer_blobs_allowed":True})
    write_json(pre,prestate)
    if entries: die(f"{ecosystem} client cache is not empty")
    before_file=work/f"{ecosystem}-status-before.json"; before=status(before_file)
    output=work/f"{ecosystem}.log"; stream=output.open("wb")
    env=clean_env.copy(); env.update(spec.get("env",{})); start_file=work/f"{ecosystem}-started-ms"
    wrapped=["bash","-c",'while [ ! -e "$1" ]; do sleep 0.02; done; python3 -c "import time; print(time.time_ns()//1000000)" >"$2"; shift 2; exec "$@"',"gate-launch",str(start_barrier),str(start_file),*spec["argv"]]
    process=subprocess.Popen(wrapped,cwd=spec["cwd"],env=env,stdout=stream,stderr=subprocess.STDOUT,start_new_session=True)
    clients[ecosystem]={"process":process,"stream":stream,"start_file":start_file,"output":output,"before":before,"before_file":before_file,"command":command_file,"pre":pre,"identity":identity}

start_barrier.touch()
completed=wait_concurrently({name:item["process"] for name,item in clients.items()})
for ecosystem,item in clients.items():
    rc,ended=completed[ecosystem]; item["stream"].close(); started=int(item["start_file"].read_text())
    launch[ecosystem]={"pid":item["process"].pid,"started_epoch_ms":started,"ended_epoch_ms":ended,"exit_code":rc}
    item.update(rc=rc,started=started,ended=ended)
launch_file=work/"launch.json"; write_json(launch_file,launch)

for ecosystem,item in clients.items():
    if item["rc"]!=0: die(f"{ecosystem} client failed; see {item['output']}")
    after_file=work/f"{ecosystem}-status-after.json"; after=status(after_file)
    event={"kind":"client","round":round_number,"phase":phase,"ecosystem":ecosystem,"target":TARGETS[ecosystem],"client":ecosystem,"client_version":"unknown","cache_identity":item["identity"],"started_epoch_ms":item["started"],"ended_epoch_ms":item["ended"],"exit_code":item["rc"],"hits_before":hits(item["before"],ecosystem),"hits_after":hits(after,ecosystem)}
    if ecosystem=="npm":
        locked=lock["packages"]["node_modules/is-number"]; integrity={key:locked[key] for key in ("version","resolved","integrity")}; integrity_file=work/"npm-integrity.json"; write_json(integrity_file,integrity); event["integrity"]={"algorithm":"sri-sha512","value":integrity["integrity"]}; event["client_version"]=command_version(["npm","--version"])
    elif ecosystem in ("pip","uv"):
        roots=[pip_target] if ecosystem=="pip" else list((uv_env/"lib").glob("python*/site-packages"))
        installed=next(root/"idna/__init__.py" for root in roots if (root/"idna/__init__.py").is_file())
        integrity_file=work/f"{ecosystem}-idna-init.py"; shutil.copyfile(installed,integrity_file); event["integrity"]={"algorithm":"sha256","value":sha(integrity_file)}; event["client_version"]=command_version(([sys.executable,"-m","pip","--version"] if ecosystem=="pip" else ["uv","--version"]))
    else:
        inspect=json.loads(subprocess.check_output(["docker","image","inspect",oci_ref],text=True,env=clean_env))[0]
        integrity_file=work/"oci-inspect.json"; write_json(integrity_file,inspect)
        canonical=oci_ref.rsplit(":",1)[0]; digest=next(value.split("@",1)[1] for value in inspect["RepoDigests"] if value.startswith(canonical+"@")); event["integrity"]={"algorithm":"oci-repo-digest","value":digest}; event["client_version"]=command_version(["docker","version","--format","{{.Client.Version}}"])
    for name,path in (("output_artifact",item["output"]),("command_artifact",item["command"]),("cache_prestate_artifact",item["pre"]),("status_before_artifact",item["before_file"]),("status_after_artifact",after_file),("launch_artifact",launch_file),("integrity_artifact",integrity_file)): bind(event,name,path)
    if phase=="post_restart": event["restart"]={"index":int(os.environ["N0DING_RESTART_INDEX"]),"pid":int(os.environ["N0DING_RESTART_PID"]),"start":os.environ["N0DING_RESTART_START"]}
    events.append(event)

def raw_metrics(path):
    files=[]; metadata=[]
    for candidate in cache.rglob("*"):
        if candidate.is_file():
            relative=str(candidate.relative_to(cache)); files.append(relative)
            if candidate.name.endswith(".json") and len(candidate.name)==69:
                try:
                    value=json.loads(candidate.read_text()); stem=candidate.name[:-5]
                    metadata.append({"metadata":relative,"body":str((candidate.parent/(value.get("body_file") or stem+".body")).relative_to(cache)),"content_digest":value.get("content_digest")})
                except Exception: pass
    write_json(path,{"status":status(),"metrics":fetch(base+"/metrics").decode(),"cache_files":sorted(files),"cache_metadata":metadata})

if round_number==1:
    simple=json.loads(fetch(base+"/pypi/simple/pip/","application/vnd.pypi.simple.v1+json"))
    wheel=next(value for value in simple["files"] if value["filename"]=="pip-25.2-py3-none-any.whl")
    wheel_url=urllib.parse.urljoin(base+"/pypi/simple/pip/",wheel["url"]); wheel_digest=wheel["hashes"]["sha256"]
    before=work/"cancel-attempt-before.json"; after=work/"cancel-attempt-after.json"; raw_metrics(before)
    output=work/"cancel-attempt.log"; stream=output.open("wb"); started=ms(); admission=work/"cancel-attempt-admission.json"
    parsed_wheel=urllib.parse.urlsplit(wheel_url)
    request_target=urllib.parse.urlunsplit(("","",parsed_wheel.path,parsed_wheel.query,""))
    abort_client=(
        "import socket,sys,time; "
        "s=socket.create_connection((sys.argv[1],int(sys.argv[2])),10); "
        "s.setsockopt(socket.SOL_SOCKET,socket.SO_RCVBUF,1024); "
        "req=('GET '+sys.argv[3]+' HTTP/1.1\\r\\nHost: '+sys.argv[1]+':'+sys.argv[2]+'\\r\\nConnection: close\\r\\n\\r\\n').encode(); "
        "s.sendall(req); print('request sent',flush=True); time.sleep(600)"
    )
    process=subprocess.Popen([sys.executable,"-c",abort_client,parsed_wheel.hostname,str(parsed_wheel.port or 80),request_target],stdout=stream,stderr=subprocess.STDOUT,start_new_session=True,env=clean_env)
    admission_epoch=None
    for _ in range(300):
        if process.poll() is not None: die("cancellation transfer exited before in-flight admission")
        raw_metrics(admission); observed=json.loads(admission.read_text())
        reserved=next(int(line.rsplit(" ",1)[1]) for line in observed["metrics"].splitlines() if line.startswith("n0ding_storage_reserved_bytes "))
        if reserved>0: admission_epoch=ms(); break
        time.sleep(.1)
    if admission_epoch is None: os.killpg(process.pid,signal.SIGTERM); process.wait(); die("cancellation transfer never reached in-flight admission")
    os.killpg(process.pid,signal.SIGTERM); raw_rc=process.wait(); rc=128+(-raw_rc) if raw_rc<0 else raw_rc; ended=ms(); stream.close()
    cancellation_settled=False
    for _ in range(50):
        raw_metrics(after)
        raw=json.loads(after.read_text()); pypi=next(x for x in raw["status"]["repositories"] if x["type"]=="pypi")
        old=next(x for x in json.loads(before.read_text())["status"]["repositories"] if x["type"]=="pypi")
        reserved=next(int(line.rsplit(" ",1)[1]) for line in raw["metrics"].splitlines() if line.startswith("n0ding_storage_reserved_bytes "))
        temps=[p for p in raw["cache_files"] if pathlib.Path(p).name.startswith((".body-",".metadata-"))]
        refs={m["body"] for m in raw["cache_metadata"]}; bodies={p for p in raw["cache_files"] if pathlib.Path(p).name.split(".",1)[0].isalnum() and ".body" in pathlib.Path(p).name}
        if pypi["client_canceled"]>old["client_canceled"] and reserved==0 and not temps and not (bodies-refs): cancellation_settled=True; break
        time.sleep(.2)
    if not cancellation_settled: die("fixed cold-miss cancellation did not settle cleanly")
    before_raw=json.loads(before.read_text())
    if any(m.get("content_digest")=="sha256:"+wheel_digest for m in before_raw["cache_metadata"]): die("fixed cancellation wheel was already present in dedicated server cache")
    attempt={"kind":"failure_path","path":"cancellation","stage":"attempt","started_epoch_ms":started,"admission_epoch_ms":admission_epoch,"ended_epoch_ms":ended,"exit_code":rc,"terminated":True,"ecosystem":"pip","object":"pip==25.2","integrity":{"algorithm":"sha256","value":wheel_digest}}
    for name,path in (("output_artifact",output),("before_metrics_artifact",before),("admission_metrics_artifact",admission),("after_metrics_artifact",after)): bind(attempt,name,path)
    retry_download=work/"cancel-retry-download"; retry_download.mkdir()
    retry_command=[sys.executable,"-m","pip","download","--disable-pip-version-check","--no-deps","--dest",str(retry_download),"--index-url",base+"/pypi/simple/","--trusted-host",origin.hostname,"pip==25.2"]
    command_file=work/"cancel-retry-command.json"; write_json(command_file,{"argv":retry_command}); before=work/"cancel-retry-before.json"; after=work/"cancel-retry-after.json"; raw_metrics(before)
    output=work/"cancel-retry.log"; started=ms()
    with output.open("wb") as stream: result=subprocess.run(retry_command,stdout=stream,stderr=subprocess.STDOUT,env=clean_env)
    ended=ms(); raw_metrics(after)
    downloaded=retry_download/"pip-25.2-py3-none-any.whl"; integrity_file=work/"cancel-retry.whl"; shutil.copyfile(downloaded,integrity_file)
    retry={"kind":"failure_path","path":"cancellation","stage":"retry","started_epoch_ms":started,"ended_epoch_ms":ended,"exit_code":result.returncode,"terminated":False,"ecosystem":"pip","object":"pip==25.2","integrity":{"algorithm":"sha256","value":sha(integrity_file)}}
    for name,path in (("output_artifact",output),("before_metrics_artifact",before),("after_metrics_artifact",after),("command_artifact",command_file),("integrity_artifact",integrity_file)): bind(retry,name,path)
    events.extend((attempt,retry))

with (evidence/"workload-events.jsonl").open("a",encoding="utf-8") as target:
    for event in events: target.write(json.dumps(event,separators=(",",":"))+"\n")
