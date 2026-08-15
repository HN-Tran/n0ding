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
   for eco in V.ECOSYSTEMS:
    rel=f"out/{r}-{eco}.txt"; (self.root/rel).write_text(f"{r}-{eco}")
    import hashlib
    digest=hashlib.sha256((self.root/rel).read_bytes()).hexdigest()
    values={"npm":{"algorithm":"sri-sha512","value":"sha512-"+__import__('base64').b64encode(b'n'*64).decode()},"pip":{"algorithm":"sha256","value":"a"*64},"uv":{"algorithm":"sha256","value":"a"*64},"oci":{"algorithm":"oci-repo-digest","value":"sha256:"+"b"*64}}
    event={"kind":"client","round":r,"phase":phase,"ecosystem":eco,"client":eco,"client_version":"1","started_epoch_ms":1000+r,"ended_epoch_ms":2000+r,"exit_code":0,"cache_identity":f"{eco}-{r}","output_artifact":rel,"output_artifact_sha256":digest,"integrity":values[eco],"hits_before":0,"hits_after":0 if r==1 else 1}
    if r==3: event["restart"]={"index":1,"pid":22,"start":"start-22"}
    self.events.append(event)
  for stage,code in (("attempt",28),("retry",0)):
   event={"kind":"failure_path","path":"cancellation","stage":stage,"started_epoch_ms":3000,"ended_epoch_ms":4000,"exit_code":code,"terminated":stage=="attempt","integrity_pass":stage=="retry","ecosystem":"pip","object":"idna==3.10","integrity":{"algorithm":"sha256","value":"a"*64}}
   for field in ("output_artifact","before_metrics_artifact","after_metrics_artifact"):
    rel=f"out/cancel-{stage}-{field}.txt"
    if "metrics" in field:
     value={"client_canceled":0 if field.startswith("before") else (1 if stage=="attempt" else 1),"temp_files":0,"orphan_bodies":0,"reservations":0}; (self.root/rel).write_text(json.dumps(value))
    else: (self.root/rel).write_text(rel)
    import hashlib; event[field]=rel; event[field+"_sha256"]=hashlib.sha256((self.root/rel).read_bytes()).hexdigest()
   self.events.append(event)
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
 def test_no_overlap(self): self.reject(lambda e:e[0].update(ended_epoch_ms=1001),"did not overlap")
 def test_no_warm_hit(self): self.reject(lambda e:e[4].update(hits_after=0),"did not increase")
 def test_changed_integrity(self): self.reject(lambda e:e[1]["integrity"].update(value="c"*64),"integrity changed")

if __name__=="__main__": unittest.main()
