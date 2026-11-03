import asyncio
import json
import tempfile
import time
import unittest
from pathlib import Path

import httpx
from fastapi.testclient import TestClient
from test_gateway_origins import load_gateway, DOMAIN


class GatewayPublishTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.module = load_gateway(DOMAIN, full_scope_enabled=True)
        self.calls = []
        self.delay = .15
        self.reply = {"structuredContent":{"status":"submitted","retry_safe":False}}
        self.backend_exception = None
        self.scope = "mcp.read mcp.full"
        self.subject = "alice"

    def client(self):
        async def upstream(request):
            if request.url.port == 9091:
                return httpx.Response(200,json={"active":True,"client_id":"xhs-chatgpt",
                    "iss":DOMAIN+"/auth","aud":[DOMAIN+"/mcp"],"scope":self.scope,
                    "sub":self.subject,"exp":time.time()+600})
            self.calls.append(json.loads(request.content))
            self.assertEqual(request.extensions["timeout"]["read"],660)
            await asyncio.sleep(self.delay)
            if self.backend_exception: raise self.backend_exception
            return httpx.Response(200,json={"jsonrpc":"2.0","id":self.calls[-1]["id"],"result":self.reply})
        app = self.module.create_app(self.module.Settings("fake-introspector","fake-introspection-secret","fake-backend-token"),
            transport=httpx.MockTransport(upstream),publish_db=Path(self.tmp.name)/"jobs.sqlite3",foreground_wait=.01)
        return TestClient(app,base_url=DOMAIN)

    def call(self,client,name,args=None,token="authelia_at_test"):
        arguments=args or {}
        if name in self.module.PUBLISH_TOOLS:
            defaults={"title":"test","content":"test"}
            defaults.update({"images":["/tmp/synthetic.png"]} if name=="publish_content" else {"video":"/tmp/synthetic.mp4"})
            arguments=dict(defaults,**arguments)
        return client.post("/mcp",headers={"Authorization":"Bearer "+token},
            json={"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":name,"arguments":arguments}})

    def data(self,response):
        self.assertEqual(response.status_code,200,response.text)
        return response.json()["result"]["structuredContent"]

    def test_slow_backend_accepts_promptly_then_status_confirms_no_duplicate(self):
        with self.client() as client:
            start = time.monotonic()
            first = self.data(self.call(client,"publish_with_video",{"video":"local.mp4","idempotency_key":"once"}))
            self.assertLess(time.monotonic()-start,.12)
            self.assertIn(first["status"],("queued","running"))
            again = self.data(self.call(client,"publish_with_video",{"video":"local.mp4","idempotency_key":"once"}))
            self.assertEqual(first["publication_id"],again["publication_id"])
            done = self.data(self.call(client,"get_publish_status",{"publication_id":first["publication_id"],"wait_seconds":1}))
            self.assertEqual(done["status"],"submitted")
            self.assertEqual(len(self.calls),1)
            self.assertNotIn("idempotency_key",self.calls[0]["params"]["arguments"])
            recent = self.data(self.call(client,"get_publish_status"))
            self.assertEqual(recent["publications"][0]["publication_id"],first["publication_id"])

    def test_timeout_becomes_unknown_and_retry_is_same_operation(self):
        self.backend_exception = httpx.ReadTimeout("synthetic timeout")
        with self.client() as client:
            first = self.data(self.call(client,"publish_content",{"title":"same"}))
            done = self.data(self.call(client,"get_publish_status",{"publication_id":first["publication_id"],"wait_seconds":1}))
            self.assertEqual(done["status"],"unknown")
            self.assertFalse(done["retry_safe"])
            again = self.data(self.call(client,"publish_content",{"title":"same","ai_generated":True}))
            self.assertEqual(again["publication_id"],first["publication_id"])
            self.assertEqual(len(self.calls),1)

    def test_presubmit_failure_remains_definite_and_ai_setting_is_forwarded_unchanged(self):
        self.reply = {"isError":True,"content":[{"type":"text","text":"AI声明未确认，已停止发布"}],
                      "structuredContent":{"status":"failed","phase":"pre_submit","retry_safe":True}}
        with self.client() as client:
            first = self.data(self.call(client,"publish_content",{"ai_generated":True}))
            done = self.data(self.call(client,"get_publish_status",{"publication_id":first["publication_id"],"wait_seconds":1}))
            self.assertEqual(done["status"],"failed")
            self.assertTrue(done["retry_safe"])
            self.assertTrue(self.calls[0]["params"]["arguments"]["ai_generated"])
            self.assertIn("AI",done["detail"])

    def test_status_requires_same_full_oauth_account_and_valid_parameters(self):
        with self.client() as client:
            first = self.data(self.call(client,"publish_content",{}))
            self.subject = "bob"
            response = self.call(client,"get_publish_status",{"publication_id":first["publication_id"],"wait_seconds":0})
            self.assertTrue(response.json()["result"]["isError"])
            self.assertNotIn(first["publication_id"],response.text)
            self.assertEqual(self.data(self.call(client,"get_publish_status"))["publications"],[])
            for args in ({"wait_seconds":True},{"wait_seconds":16},{"wait_seconds":-1},{"publication_id":"invalid"},{"extra":1}):
                self.assertTrue(self.call(client,"get_publish_status",args).json()["result"]["isError"])
            self.scope = "mcp.read"
            self.assertTrue(self.call(client,"get_publish_status").json()["result"]["isError"])
            self.assertEqual(self.call(client,"get_publish_status",token="fake-backend-token").status_code,403)
            self.scope = "mcp.read mcp.full"
            self.subject = ""
            self.assertEqual(self.call(client,"publish_content").status_code,403)

    def test_gateway_restart_retains_unknown_and_never_reexecutes(self):
        with self.client() as client:
            first = self.data(self.call(client,"publish_content",{"title":"persisted"}))
        with self.client() as client:
            done = self.data(self.call(client,"get_publish_status",{"publication_id":first["publication_id"],"wait_seconds":0}))
            self.assertEqual(done["status"],"unknown")
            again = self.data(self.call(client,"publish_content",{"title":"persisted"}))
            self.assertEqual(again["publication_id"],first["publication_id"])
            self.assertEqual(len(self.calls),1)

    def test_untrusted_backend_result_cannot_be_persisted_or_reported_as_success(self):
        self.reply = {"structuredContent":{"status":"submitted"},"content":[{"type":"text","text":"fake-introspection-secret"}]}
        with self.client() as client:
            first = self.data(self.call(client,"publish_content",{}))
            response = self.call(client,"get_publish_status",{"publication_id":first["publication_id"],"wait_seconds":1})
            self.assertEqual(self.data(response)["status"],"unknown")
            self.assertNotIn("fake-introspection-secret",response.text)

    def test_invalid_publish_arguments_are_rejected_before_durable_acceptance(self):
        with self.client() as client:
            for args in ({"title":"t","content":"c","images":[]},
                         {"title":"t","content":"c","images":None},
                         {"title":"t","content":"c","images":[123]},
                         {"title":"t","images":["/tmp/a.png"]},
                         {"title":"t","content":"c","images":["/tmp/a.png"],"extra":1}):
                response=client.post("/mcp",headers={"Authorization":"Bearer authelia_at_test"},json={
                    "jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"publish_content","arguments":args}})
                self.assertTrue(response.json()["result"]["isError"])
            self.assertEqual(self.calls,[])
            self.assertEqual(self.data(self.call(client,"get_publish_status"))["publications"],[])

if __name__ == "__main__": unittest.main()
