import importlib.util
import json
import os
import sys
import time
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import httpx
from fastapi.testclient import TestClient


IP = 'https://43.133.185.155'
DOMAIN = 'https://xhs.wa.ee'
FULL_TOOLS = (
    'check_login_status', 'delete_cookies', 'favorite_feed', 'get_feed_detail',
    'get_login_qrcode', 'get_my_profile', 'get_unread_count', 'like_feed',
    'like_notification', 'list_feeds', 'list_notifications', 'post_comment_to_feed',
    'publish_content', 'publish_with_video', 'reply_comment_in_feed',
    'reply_notification', 'search_feeds', 'user_profile', 'get_publish_status',
)


def load_gateway(origin, diagnostics=False, full_scope_enabled=False):
    name = 'gateway_probe_' + ('domain' if origin == DOMAIN else 'ip')
    env = {'GATEWAY_PUBLIC_ORIGIN': origin} if origin else {}
    if diagnostics:
        env['GATEWAY_DIAGNOSTICS'] = '1'
    if full_scope_enabled:
        env['GATEWAY_FULL_SCOPE_ENABLED'] = '1'
    with patch.dict(os.environ, env, clear=True):
        spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name('gateway.py'))
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
    return module


class GatewayOriginTests(unittest.TestCase):
    def make_client(self, origin, *, token_origin=None, expired=False, diagnostics=False,
                    full_scope_enabled=False, scopes='mcp.read offline_access',
                    backend_tools=None, backend_calls=None, backend_error=None):
        module = load_gateway(origin, diagnostics, full_scope_enabled)
        expected = origin or IP
        claim_origin = token_origin or expected
        host = expected.removeprefix('https://')

        def upstream(request):
            if request.url.port == 9091:
                if (request.headers.get('host') != host or
                        request.headers.get('x-forwarded-host') != host or
                        request.headers.get('x-forwarded-proto') != 'https'):
                    return httpx.Response(200, json={'active': False})
                return httpx.Response(200, json={
                    'active': True, 'client_id': 'xhs-chatgpt',
                    'iss': claim_origin + '/auth', 'aud': [claim_origin + '/mcp'],
                    'scope': scopes, 'sub': 'test-subject',
                    'iat': int(time.time()) - 60,
                    'exp': int(time.time()) + (-1 if expired else 600),
                })
            if request.url.port == 18060:
                message = json.loads(request.content)
                if backend_calls is not None:
                    backend_calls.append(message)
                if message['method'] == 'tools/call':
                    if message['params']['name'] in module.PUBLISH_TOOLS and backend_error is None:
                        return httpx.Response(200, json={'jsonrpc':'2.0', 'id':message['id'], 'result':{
                            'structuredContent': {'status':'submitted','retry_safe':False}}})
                    return httpx.Response(200, json={'jsonrpc': '2.0', 'id': message['id'],
                                                    'result': backend_error if backend_error is not None else
                                                    {'checked': message['params']['name']}})
                tools = backend_tools if backend_tools is not None else [
                    {'name': 'search_feeds', 'inputSchema': {'type': 'object'}},
                    {'name': 'get_my_profile', 'inputSchema': {'type': 'object'}},
                    {'name': 'publish_content', 'inputSchema': {'type': 'object'}},
                ]
                return httpx.Response(200, json={'jsonrpc': '2.0', 'id': message['id'],
                                                'result': {'tools': tools}})
            raise AssertionError('Unexpected upstream destination')

        settings = module.Settings('fake-introspector', 'fake-introspection-secret', 'fake-backend-token')
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        app = module.create_app(settings, transport=httpx.MockTransport(upstream),
                                publish_db=Path(tmp.name)/'jobs.sqlite3', foreground_wait=.1)
        return TestClient(app, base_url=expected)

    def test_default_ip_entry_point_keeps_original_metadata_and_challenge(self):
        with self.make_client(None) as client:
            self.assertEqual(client.get('/.well-known/oauth-protected-resource').json()['resource'], IP + '/mcp')
            response = client.post('/mcp', json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
            self.assertEqual(response.status_code, 401)
            self.assertIn(IP + '/.well-known/oauth-protected-resource', response.headers['www-authenticate'])

    def test_domain_metadata_and_challenge_are_bound_to_the_domain(self):
        with self.make_client(DOMAIN) as client:
            response = client.get('/.well-known/oauth-protected-resource')
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.json()['resource'], DOMAIN + '/mcp')
            self.assertEqual(response.json()['authorization_servers'], [DOMAIN + '/auth'])
            response = client.post('/mcp', json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
            self.assertEqual(response.status_code, 401)
            self.assertIn(DOMAIN + '/.well-known/oauth-protected-resource', response.headers['www-authenticate'])

    def test_ip_and_domain_each_accept_only_their_matching_oauth_claims(self):
        for origin in (None, DOMAIN):
            with self.subTest(origin=origin), self.make_client(origin) as client:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
                self.assertEqual(response.status_code, 200)
                self.assertEqual([t['name'] for t in response.json()['result']['tools']],
                                 ['search_feeds', 'get_my_profile'])

    def test_cross_issuer_tokens_are_rejected_in_both_directions(self):
        for origin, token_origin in ((None, DOMAIN), (DOMAIN, IP)):
            with self.subTest(origin=origin), self.make_client(origin, token_origin=token_origin) as client:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
                self.assertEqual(response.status_code, 401)

    def test_expired_token_and_write_tool_remain_rejected_on_both_origins(self):
        for origin in (None, DOMAIN):
            with self.subTest(origin=origin), self.make_client(origin, expired=True) as client:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
                self.assertEqual(response.status_code, 401)
            with self.subTest(origin=origin), self.make_client(origin) as client:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                             'params': {'name': 'publish_content', 'arguments': {}}})
                self.assertEqual(response.status_code, 403)

    def test_native_key_compatibility_keeps_the_same_read_only_filter(self):
        with self.make_client(None) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer fake-backend-token'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
            self.assertEqual(response.status_code, 200)
            self.assertEqual([t['name'] for t in response.json()['result']['tools']],
                             ['search_feeds', 'get_my_profile'])

    def test_diagnostics_record_static_rejection_without_token_or_arguments(self):
        with self.make_client(DOMAIN, diagnostics=True) as client:
            with self.assertLogs('xhs_gateway_rejections', level='WARNING') as captured:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                             'params': {'name': 'publish_content',
                                                        'arguments': {'private': 'must-not-log-body'}}})
            self.assertEqual(response.status_code, 403)
            messages = '\n'.join(captured.output)
            self.assertIn('tools/call', messages)
            self.assertIn('Tool is not allowed', messages)
            self.assertNotIn('authelia_at_synthetic', messages)
            self.assertNotIn('must-not-log-body', messages)

    def test_unsupported_requests_return_method_not_found_without_scope_challenge(self):
        for origin in (None, DOMAIN):
            for request_id in (1, 'discovery-probe'):
                with self.subTest(origin=origin, request_id=request_id), self.make_client(origin) as client:
                    response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                           json={'jsonrpc': '2.0', 'id': request_id,
                                                 'method': 'unsupported/probe', 'params': {}})
                    self.assertEqual(response.status_code, 200)
                    self.assertEqual(response.json(), {'jsonrpc': '2.0', 'id': request_id,
                                                      'error': {'code': -32601, 'message': 'Method not found'}})
                    self.assertNotIn('www-authenticate', response.headers)

    def test_unsupported_notifications_are_acknowledged_without_a_jsonrpc_reply(self):
        with self.make_client(DOMAIN) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'method': 'notifications/unsupported_probe', 'params': {}})
            self.assertEqual(response.status_code, 202)
            self.assertEqual(response.content, b'')

    def test_unknown_method_does_not_bypass_authentication_or_id_validation(self):
        with self.make_client(DOMAIN) as client:
            response = client.post('/mcp', json={'jsonrpc': '2.0', 'id': 1, 'method': 'unsupported/probe'})
            self.assertEqual(response.status_code, 401)
            for invalid_id in (True, None):
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': invalid_id, 'method': 'unsupported/probe'})
                self.assertEqual(response.status_code, 400)

    def test_full_mode_advertises_explicit_full_scope(self):
        with self.make_client(DOMAIN, full_scope_enabled=True) as client:
            metadata = client.get('/.well-known/oauth-protected-resource').json()
            self.assertEqual(metadata['scopes_supported'], ['mcp.read', 'mcp.full'])

    def test_full_mode_discovers_all_tools_with_per_tool_scope_policy(self):
        tools = [{'name': name, 'inputSchema': {'type': 'object'},
                  'annotations': {'destructiveHint': name == 'delete_cookies'}} for name in FULL_TOOLS]
        tools.append({'name': 'future_unapproved_tool', 'inputSchema': {'type': 'object'}})
        with self.make_client(DOMAIN, full_scope_enabled=True, backend_tools=tools) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
            self.assertEqual(response.status_code, 200)
            found = response.json()['result']['tools']
            self.assertEqual([t['name'] for t in found], list(FULL_TOOLS))
            for tool in found:
                required = ['mcp.read'] if tool['name'] in load_gateway(DOMAIN).READ_TOOLS else ['mcp.read', 'mcp.full']
                self.assertEqual(tool['securitySchemes'], [{'type': 'oauth2', 'scopes': required}])
                self.assertEqual(tool['_meta']['securitySchemes'], tool['securitySchemes'])
            self.assertTrue(next(t for t in found if t['name'] == 'delete_cookies')['annotations']['destructiveHint'])

    def test_full_token_can_route_every_current_tool_without_real_side_effects(self):
        calls = []
        with self.make_client(DOMAIN, full_scope_enabled=True, scopes='mcp.read mcp.full offline_access',
                              backend_calls=calls) as client:
            for name in FULL_TOOLS:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                             'params': {'name': name, 'arguments': {}}})
                self.assertEqual(response.status_code, 200, name)
                if name in ('publish_content','publish_with_video'):
                    self.assertEqual(response.json()['result']['structuredContent']['status'],'submitted')
                elif name == 'get_publish_status':
                    self.assertEqual(len(response.json()['result']['structuredContent']['publications']),2)
                else:
                    self.assertEqual(response.json()['result'], {'checked': name})
        self.assertEqual([call['params']['name'] for call in calls], [n for n in FULL_TOOLS if n!='get_publish_status'])

    def test_read_token_keeps_reads_but_new_tools_require_reauthorization_before_forwarding(self):
        calls = []
        read_tools = load_gateway(DOMAIN).READ_TOOLS
        with self.make_client(DOMAIN, full_scope_enabled=True, backend_calls=calls) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                         'params': {'name': 'get_unread_count', 'arguments': {}}})
            self.assertEqual(response.json()['result'], {'checked': 'get_unread_count'})
            calls.clear()
            for name in set(FULL_TOOLS) - read_tools:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                             'params': {'name': name, 'arguments': {}}})
                self.assertEqual(response.status_code, 200, name)
                result = response.json()['result']
                self.assertTrue(result['isError'])
                challenge = result['_meta']['mcp/www_authenticate'][0]
                self.assertIn('scope="mcp.read mcp.full"', challenge)
                self.assertIn('error="insufficient_scope"', challenge)
                self.assertIn('error_description=', challenge)
            self.assertEqual(calls, [])

    def test_new_full_scope_does_not_replace_the_existing_read_scope(self):
        with self.make_client(DOMAIN, full_scope_enabled=True, scopes='mcp.full') as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                         'params': {'name': 'check_login_status', 'arguments': {}}})
            self.assertEqual(response.status_code, 403)

    def test_new_tool_errors_remain_actionable_while_old_read_errors_keep_their_contract(self):
        error = {'isError': True, 'content': [{'type': 'text', 'text': '缺少 comment_id 或 user_id'}]}
        with self.make_client(DOMAIN, full_scope_enabled=True, scopes='mcp.read mcp.full', backend_error=error) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                         'params': {'name': 'reply_comment_in_feed', 'arguments': {}}})
            self.assertEqual(response.json()['result'], error)
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                         'params': {'name': 'get_unread_count', 'arguments': {}}})
            self.assertEqual(response.json()['result'], {'isError': True, 'content': [
                {'type': 'text', 'text': 'Backend tool failed'}]})

    def test_full_tool_errors_cannot_echo_server_credentials(self):
        error = {'isError': True, 'content': [{'type': 'text', 'text': 'fake-introspection-secret'}]}
        with self.make_client(DOMAIN, full_scope_enabled=True, scopes='mcp.read mcp.full', backend_error=error) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                         'params': {'name': 'reply_comment_in_feed', 'arguments': {}}})
            self.assertEqual(response.status_code, 502)
            self.assertNotIn('fake-introspection-secret', response.text)

    def test_ip_and_native_key_contracts_are_not_promoted_by_domain_full_mode(self):
        for origin, full_mode, bearer in ((None, False, 'authelia_at_synthetic'),
                                         (DOMAIN, True, 'fake-backend-token')):
            with self.subTest(origin=origin, bearer=bearer), self.make_client(
                    origin, full_scope_enabled=full_mode, scopes='mcp.read mcp.full') as client:
                response = client.post('/mcp', headers={'Authorization': 'Bearer ' + bearer},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
                self.assertEqual([t['name'] for t in response.json()['result']['tools']],
                                 ['search_feeds', 'get_my_profile'])
                response = client.post('/mcp', headers={'Authorization': 'Bearer ' + bearer},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                             'params': {'name': 'delete_cookies', 'arguments': {}}})
                self.assertEqual(response.status_code, 403)

    def test_full_scope_does_not_bypass_issuer_expiry_or_tool_inventory(self):
        for bad in ({'token_origin': IP}, {'expired': True}):
            with self.make_client(DOMAIN, full_scope_enabled=True, scopes='mcp.read mcp.full', **bad) as client:
                response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                       json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/list'})
                self.assertEqual(response.status_code, 401)
        calls = []
        with self.make_client(DOMAIN, full_scope_enabled=True, scopes='mcp.read mcp.full', backend_calls=calls) as client:
            response = client.post('/mcp', headers={'Authorization': 'Bearer authelia_at_synthetic'},
                                   json={'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call',
                                         'params': {'name': 'future_unapproved_tool', 'arguments': {}}})
            self.assertEqual(response.status_code, 403)
            self.assertEqual(calls, [])


if __name__ == '__main__':
    unittest.main()
