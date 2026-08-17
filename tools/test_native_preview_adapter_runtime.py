#!/usr/bin/env python3
import ast,pathlib,threading,time,unittest

source=(pathlib.Path(__file__).with_name("native-preview-workload.py")).read_text()
tree=ast.parse(source); function=next(node for node in tree.body if isinstance(node,ast.FunctionDef) and node.name=="wait_concurrently")
namespace={"threading":threading,"ms":lambda:int(time.time()*1000)}
exec(compile(ast.Module(body=[function],type_ignores=[]),"native-preview-workload.py","exec"),namespace)
wait_concurrently=namespace["wait_concurrently"]

class FakeProcess:
    def __init__(self,delay,rc=0): self.delay,self.rc=delay,rc
    def wait(self): time.sleep(self.delay); return self.rc

class RuntimeTest(unittest.TestCase):
    def test_staggered_clients_are_not_given_sequential_wait_end_times(self):
        started=time.monotonic()
        values=wait_concurrently({"fast":FakeProcess(.02),"slow":FakeProcess(.15)})
        elapsed=time.monotonic()-started
        self.assertLess(values["fast"][1],values["slow"][1]-80)
        self.assertLess(elapsed,.22)

if __name__=="__main__": unittest.main()
