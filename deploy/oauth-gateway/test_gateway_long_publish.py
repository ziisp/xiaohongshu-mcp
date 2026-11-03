"""Real loopback HTTP response after 31s: the former 30s timeout regression."""
import asyncio
import json
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import httpx
from fastapi.testclient import TestClient
from test_gateway_origins import load_gateway, DOMAIN


class LongPublishTests(unittest.TestCase):
    def test_31_second_backend_effect_has_queryable_result_and_exactly_one_dispatch(self):
        effects = []
        class Backend(BaseHTTPRequestHandler):
            def log_message(self,*args): pass
            def do_POST(self):
                message = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                time.sleep(20)
                effects.append('synthetic_commit')
                time.sleep(11)
                body = json.dumps({'jsonrpc':'2.0','id':message['id'],'result':{
                    'structuredContent':{'status':'submitted','retry_safe':False}}}).encode()
                self.send_response(200)
                self.send_header('Content-Type','application/json')
                self.send_header('Content-Length',str(len(body)))
                self.end_headers()
                self.wfile.write(body)
        server = ThreadingHTTPServer(('127.0.0.1',0),Backend)
        thread = threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        self.addCleanup(server.server_close);self.addCleanup(server.shutdown)
        tmp = tempfile.TemporaryDirectory();self.addCleanup(tmp.cleanup)
        module = load_gateway(DOMAIN,full_scope_enabled=True)
        async def upstream(request):
            if request.url.port==9091:
                return httpx.Response(200,json={'active':True,'iss':DOMAIN+'/auth','aud':[DOMAIN+'/mcp'],
                    'client_id':'xhs-chatgpt','scope':'mcp.read mcp.full','sub':'synthetic-user','exp':time.time()+600})
            self.assertEqual(request.extensions['timeout']['read'],660)
            async with httpx.AsyncClient(trust_env=False,timeout=660) as client:
                return await client.post('http://127.0.0.1:'+str(server.server_port),content=request.content)
        app = module.create_app(module.Settings('fake-introspector','fake-introspection-secret','fake-backend-token'),
            transport=httpx.MockTransport(upstream),publish_db=Path(tmp.name)/'jobs.sqlite3')
        def call(client,name,args):
            response=client.post('/mcp',headers={'Authorization':'Bearer authelia_at_synthetic'},json={
                'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':name,'arguments':args}})
            self.assertEqual(response.status_code,200,response.text)
            return response.json()['result']['structuredContent']
        with TestClient(app,base_url=DOMAIN) as client:
            start=time.monotonic()
            first=call(client,'publish_with_video',{'title':'test','content':'test','video':'synthetic.mp4','idempotency_key':'long-call'})
            self.assertLess(time.monotonic()-start,4)
            self.assertIn(first['status'],('queued','running'))
            retry=call(client,'publish_with_video',{'title':'test','content':'test','video':'synthetic.mp4','idempotency_key':'long-call'})
            self.assertEqual(first['publication_id'],retry['publication_id'])
            for _ in range(3):
                result=call(client,'get_publish_status',{'publication_id':first['publication_id'],'wait_seconds':15})
                if result['status']=='submitted': break
            self.assertEqual(result['status'],'submitted')
            self.assertEqual(effects,['synthetic_commit'])

if __name__=='__main__': unittest.main()
