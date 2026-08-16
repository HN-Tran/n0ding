#!/usr/bin/env python3
import copy, importlib.util, json, pathlib, tempfile, unittest

MODULE_PATH=pathlib.Path(__file__).with_name("validate-native-preview-workload.py")
SPEC=importlib.util.spec_from_file_location("preview_validator",MODULE_PATH); V=importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(V)

class EvidenceTest(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory(); self.root=pathlib.Path(self.tmp.name)
  (self.root/"out").mkdir(); self.ledger=[]
  self.events=[]
  for r,phase in ((1,"cold"),(2,"warm"),(3,"post_restart")):
   ledger={eco:{"started_epoch_ms":1000+r,"ended_epoch_ms":2000+r,"exit_code":0} for eco in V.ECOSYSTEMS}
   ledger_rel=f"out/{r}-launch.json"; (self.root/ledger_rel).write_text(json.dumps(ledger))
   for eco in V.ECOSYSTEMS:
    rel=f"out/{r}-{eco}.txt"; (self.root/rel).write_text(f"{r}-{eco}")
    import hashlib
    digest=hashlib.sha256((self.root/rel).read_bytes()).hexdigest()
    payload=b'idna-file'
    if eco=="npm":
     locked=json.loads((MODULE_PATH.parent.parent/V.TARGETS["npm"]).read_text())["packages"]["node_modules/is-number"]
     value=locked["integrity"]
    elif eco in ("pip","uv"): value=__import__('hashlib').sha256(payload).hexdigest()
    else: value="sha256:"+"b"*64
    values={"npm":{"algorithm":"sri-sha512","value":value},"pip":{"algorithm":"sha256","value":value},"uv":{"algorithm":"sha256","value":value},"oci":{"algorithm":"oci-repo-digest","value":value}}
    irel=f"out/{r}-{eco}-integrity"
    if eco=="oci": (self.root/irel).write_text(json.dumps({"RepoDigests":["library/alpine@"+value]}))
    elif eco=="npm": (self.root/irel).write_text(json.dumps({key:locked[key] for key in ("version","resolved","integrity")}))
    else: (self.root/irel).write_bytes(payload)
    before_rel=f"out/{r}-{eco}-before.json"; after_rel=f"out/{r}-{eco}-after.json"
    kind="pypi" if eco in ("pip","uv") else eco
    (self.root/before_rel).write_text(json.dumps({"repositories":[{"type":kind,"cache_hits":0}]})); (self.root/after_rel).write_text(json.dumps({"repositories":[{"type":kind,"cache_hits":0 if r==1 else 1}]}))
    pre_rel=f"out/{r}-{eco}-pre.json"; (self.root/pre_rel).write_text(json.dumps({"identity":f"{eco}-{r}","entries":[]}))
    command_rel=f"out/{r}-{eco}-command.json"
    argv={"npm":["npm","ci"],"pip":["pip","install","idna==3.10"],"uv":["uv","pip","install","idna==3.10"],"oci":["docker","pull","library/alpine:3.20"]}[eco]
    command={"argv":argv}
    if eco=="npm": command["fixture_sha256"]=__import__('hashlib').sha256((MODULE_PATH.parent.parent/V.TARGETS["npm"]).read_bytes()).hexdigest()
    (self.root/command_rel).write_text(json.dumps(command))
    event={"kind":"client","round":r,"phase":phase,"ecosystem":eco,"target":V.TARGETS[eco],"client":eco,"client_version":"1","started_epoch_ms":1000+r,"ended_epoch_ms":2000+r,"exit_code":0,"cache_identity":f"{eco}-{r}","output_artifact":rel,"output_artifact_sha256":digest,"integrity":values[eco],"hits_before":0,"hits_after":0 if r==1 else 1}
    for field,path in (("launch_artifact",ledger_rel),("command_artifact",command_rel),("integrity_artifact",irel),("status_before_artifact",before_rel),("status_after_artifact",after_rel),("cache_prestate_artifact",pre_rel)):
     event[field]=path; event[field+"_sha256"]=__import__('hashlib').sha256((self.root/path).read_bytes()).hexdigest()
    if r==3: event["restart"]={"index":1,"pid":22,"start":"start-22"}
    self.events.append(event)
  for stage,code in (("attempt",28),("retry",0)):
   event={"kind":"failure_path","path":"cancellation","stage":stage,"started_epoch_ms":3000,"ended_epoch_ms":4000,"exit_code":code,"terminated":stage=="attempt","ecosystem":"pip","object":"idna==3.10","integrity":{"algorithm":"sha256","value":__import__('hashlib').sha256(b'idna-file').hexdigest()}}
   for field in ("output_artifact","before_metrics_artifact","after_metrics_artifact"):
    rel=f"out/cancel-{stage}-{field}.txt"
    if "metrics" in field:
     canceled=0 if field.startswith("before") else (1 if stage=="attempt" else 1)
     value={"status":{"repositories":[{"type":"pypi","client_canceled":canceled},{"type":"npm","client_canceled":0}]},"metrics":"n0ding_storage_reserved_bytes 0\n","cache_files":[],"cache_metadata":[]}; (self.root/rel).write_text(json.dumps(value))
    else: (self.root/rel).write_text(rel)
    import hashlib; event[field]=rel; event[field+"_sha256"]=hashlib.sha256((self.root/rel).read_bytes()).hexdigest()
   self.events.append(event)
   if stage=="retry":
    for field,value in (("integrity_artifact",b'idna-file'),("command_artifact",json.dumps({"argv":["pip","install","idna==3.10"]}).encode())):
     rel=f"out/cancel-retry-{field}"; (self.root/rel).write_bytes(value); event[field]=rel; event[field+"_sha256"]=__import__('hashlib').sha256(value).hexdigest()
  self.ledger=[{"index":1,"pid":22,"start":"start-22"}]
 def tearDown(self): self.tmp.cleanup()
 def valid(self): V.validate(copy.deepcopy(self.events),str(self.root),3,1,self.ledger)
 def test_valid(self): self.valid()
 def reject(self,mutate,text):
  events=copy.deepcopy(self.events); mutate(events)
  with self.assertRaisesRegex(ValueError,text): V.validate(events,str(self.root),3,1,self.ledger)
 def test_duplicate(self): self.reject(lambda e:e.append(copy.deepcopy(e[0])),"duplicate")
 def test_missing_ecosystem(self): self.reject(lambda e:e.pop(0),"coverage mismatch")
 def test_wrong_phase(self): self.reject(lambda e:e[0].update(phase="warm"),"wrong phase")
 def test_hash_forgery(self): self.reject(lambda e:e[0].update(output_artifact_sha256="0"*64),"hash mismatch")
 def test_restart_forgery(self): self.reject(lambda e:e[8]["restart"].update(pid=99),"restart ledger")
 def test_restart_ledger_length(self):
  with self.assertRaisesRegex(ValueError,"ledger length"):
   V.validate(copy.deepcopy(self.events),str(self.root),3,1,[])
 def test_no_overlap(self): self.reject(lambda e:e[0].update(ended_epoch_ms=1001),"launch ledger")
 def test_no_warm_hit(self): self.reject(lambda e:e[4].update(hits_after=0),"fabricated cache hit delta")
 def test_changed_integrity(self): self.reject(lambda e:e[1]["integrity"].update(value="c"*64),"fabricated pip integrity")
 def test_wrong_target(self): self.reject(lambda e:e[0].update(target="left-pad"),"wrong target")
 def test_fabricated_integrity(self): self.reject(lambda e:e[0]["integrity"].update(value="sha512-"+__import__('base64').b64encode(b'x'*64).decode()),"exact committed")
 def test_fabricated_hit_delta(self): self.reject(lambda e:e[4].update(hits_after=2),"fabricated cache hit delta")
 def test_reused_cache(self):
  def mutate(events):
   p=self.root/events[0]["cache_prestate_artifact"]; p.write_text(json.dumps({"identity":events[0]["cache_identity"],"entries":["old"]})); events[0]["cache_prestate_artifact_sha256"]=__import__('hashlib').sha256(p.read_bytes()).hexdigest()
  self.reject(mutate,"reused or nonempty")
 def test_forged_overlap(self): self.reject(lambda e:e[0].update(started_epoch_ms=1999),"launch ledger")
 def test_fabricated_cancellation_metrics(self):
  def mutate(events):
   event=events[-2]; p=self.root/event["after_metrics_artifact"]; raw=json.loads(p.read_text()); raw["status"]["repositories"][0]["client_canceled"]=0; p.write_text(json.dumps(raw)); event["after_metrics_artifact_sha256"]=__import__('hashlib').sha256(p.read_bytes()).hexdigest()
  self.reject(mutate,"client_canceled did not increase")
 def test_unrelated_repo_cancellation_increment(self):
  def mutate(events):
   event=events[-2]; p=self.root/event["after_metrics_artifact"]; raw=json.loads(p.read_text()); raw["status"]["repositories"][0]["client_canceled"]=0; raw["status"]["repositories"][1]["client_canceled"]=1; p.write_text(json.dumps(raw)); event["after_metrics_artifact_sha256"]=__import__('hashlib').sha256(p.read_bytes()).hexdigest()
  self.reject(mutate,"client_canceled did not increase")
 def test_cancellation_retry_integrity_mismatch(self): self.reject(lambda e:e[-1]["integrity"].update(value="0"*64),"fabricated pip integrity")
 def test_npm_wrong_self_consistent_artifact(self):
  def mutate(events):
   event=events[0]; p=self.root/event["integrity_artifact"]; value={"version":"7.0.1","resolved":"x","integrity":"sha512-"+__import__('base64').b64encode(b'x'*64).decode()}; p.write_text(json.dumps(value)); event["integrity"]["value"]=value["integrity"]; event["integrity_artifact_sha256"]=__import__('hashlib').sha256(p.read_bytes()).hexdigest()
  self.reject(mutate,"exact committed")
 def test_oci_unrelated_repo_digest(self):
  def mutate(events):
   event=events[3]; p=self.root/event["integrity_artifact"]; p.write_text(json.dumps({"RepoDigests":["evil/other:latest@"+event["integrity"]["value"]]})); event["integrity_artifact_sha256"]=__import__('hashlib').sha256(p.read_bytes()).hexdigest()
  self.reject(mutate,"not present")
 def test_oci_registry_port_canonicalization(self):
  events=copy.deepcopy(self.events); event=events[3]
  command_path=self.root/event["command_artifact"]
  command={"argv":["docker","pull","registry.example:5000/library/alpine:3.20"]}; command_path.write_text(json.dumps(command)); event["command_artifact_sha256"]=__import__('hashlib').sha256(command_path.read_bytes()).hexdigest()
  inspect_path=self.root/event["integrity_artifact"]
  inspect_path.write_text(json.dumps({"RepoDigests":["registry.example:5000/library/alpine@"+event["integrity"]["value"]]})); event["integrity_artifact_sha256"]=__import__('hashlib').sha256(inspect_path.read_bytes()).hexdigest()
  V.validate(events,str(self.root),3,1,self.ledger)

if __name__=="__main__": unittest.main()
