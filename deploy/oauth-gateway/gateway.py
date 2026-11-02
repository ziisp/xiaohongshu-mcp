"""Stateless MCP resource server with explicit OAuth tool permissions."""

import asyncio
import hmac
import hashlib
import json
import logging
import math
import os
import re
import time
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from urllib.parse import quote, urlencode, urlsplit

import httpx
from fastapi import Depends, FastAPI, HTTPException, Request
from fastapi.exception_handlers import http_exception_handler
from fastapi.responses import JSONResponse, Response
from starlette.middleware.trustedhost import TrustedHostMiddleware
from publish_jobs import PUBLISH_TOOLS, PublishJobs, tool_result

PUBLIC_ORIGIN = os.environ.get("GATEWAY_PUBLIC_ORIGIN", "https://43.133.185.155")
_origin = urlsplit(PUBLIC_ORIGIN)
if (_origin.scheme != "https" or not _origin.hostname or _origin.username is not None or
        _origin.path or _origin.query or _origin.fragment or
        any(char.isspace() for char in PUBLIC_ORIGIN)):
    raise ValueError("Gateway public origin must be a canonical HTTPS origin without a path")
PUBLIC_HOST = _origin.netloc
PUBLIC_HOSTNAME = _origin.hostname
RESOURCE = PUBLIC_ORIGIN + "/mcp"
ISSUER = PUBLIC_ORIGIN + "/auth"
TOKEN_CLIENT_ID = "xhs-chatgpt"
METADATA_URL = PUBLIC_ORIGIN + "/.well-known/oauth-protected-resource"
BACKEND_URL = "http://127.0.0.1:18060/mcp"
BACKEND_HOST = "127.0.0.1:18060"
INTROSPECTION_URL = "http://127.0.0.1:9091/auth/api/oidc/introspection"
MAX_REQUEST = 1024 * 1024
MAX_RESPONSE = 8 * 1024 * 1024
MAX_INTROSPECTION = 64 * 1024
READ_TOOLS = frozenset({
    "search_feeds", "list_feeds", "get_feed_detail", "user_profile",
    "get_my_profile", "get_unread_count", "list_notifications",
})
FULL_TOOLS = READ_TOOLS | frozenset({
    "check_login_status", "get_login_qrcode", "delete_cookies",
    "publish_content", "publish_with_video", "post_comment_to_feed",
    "reply_comment_in_feed", "reply_notification", "like_feed",
    "favorite_feed", "like_notification", "get_publish_status",
})
FULL_SCOPE_ENABLED = os.environ.get("GATEWAY_FULL_SCOPE_ENABLED") == "1"
METHODS = frozenset({
    "initialize", "notifications/initialized", "ping", "tools/list", "tools/call",
})
DIAGNOSTICS_ENABLED = os.environ.get("GATEWAY_DIAGNOSTICS") == "1"
DIAGNOSTIC_METHODS = METHODS | frozenset({
    "resources/list", "resources/templates/list", "resources/read",
    "prompts/list", "prompts/get", "logging/setLevel", "completion/complete",
    "notifications/cancelled", "notifications/progress", "notifications/roots/list_changed",
})
DIAGNOSTIC_REJECTIONS = frozenset({
    "Origin is not allowed", "Required scope is missing", "MCP method is not allowed",
    "Tool is not allowed", "Invalid access token", "A valid Bearer access token is required",
})
CHALLENGE = f'Bearer resource_metadata="{METADATA_URL}", scope="mcp.read"'
FULL_AUTH_CHALLENGE = (
    f'Bearer resource_metadata="{METADATA_URL}", scope="mcp.read mcp.full", '
    'error="insufficient_scope", error_description="Full tool access requires mcp.full consent"'
)
BEARER = re.compile(r"Bearer ([A-Za-z0-9._~+/-]+=*)", re.IGNORECASE)
SAFE_HEADERS = {"cache-control": "no-store", "x-content-type-options": "nosniff"}
ALLOWED_ORIGINS = frozenset({PUBLIC_ORIGIN, "https://chatgpt.com"})


@dataclass(frozen=True)
class AuthenticatedAccess:
    token: str = field(repr=False)
    allowed_tools: frozenset = READ_TOOLS
    visible_tools: frozenset = READ_TOOLS
    subject: str = ""


@dataclass(frozen=True)
class Settings:
    introspection_client_id: str
    introspection_client_secret: str = field(repr=False)
    backend_token: str = field(repr=False)
    introspection_url: str = INTROSPECTION_URL

    def __post_init__(self):
        credentials = (
            self.introspection_client_id, self.introspection_client_secret, self.backend_token,
        )
        if any(not isinstance(value, str) or not value or any(
            ord(char) < 33 or ord(char) > 126 for char in value
        ) for value in credentials):
            raise ValueError("Gateway credentials must be nonempty printable strings without whitespace")
        target = urlsplit(self.introspection_url)
        if (target.scheme != "http" or target.hostname != "127.0.0.1" or
                target.username is not None or target.password is not None or
                target.query or target.fragment or not target.path.startswith("/")):
            raise ValueError("Introspection endpoint must be a loopback HTTP URL without credentials or query")

    @classmethod
    def from_env(cls):
        names = (
            "GATEWAY_INTROSPECTION_CLIENT_ID", "GATEWAY_INTROSPECTION_CLIENT_SECRET",
        )
        backend_token = os.environ.get("AUTH_TOKEN", os.environ.get("GATEWAY_BACKEND_TOKEN"))
        if any(not os.environ.get(name) for name in names) or not backend_token:
            raise ValueError("Required gateway credential environment is missing")
        return cls(
            introspection_client_id=os.environ[names[0]],
            introspection_client_secret=os.environ[names[1]],
            backend_token=backend_token,
            introspection_url=os.environ.get("GATEWAY_INTROSPECTION_URL", INTROSPECTION_URL),
        )


def reject(status, detail):
    headers = dict(SAFE_HEADERS)
    if status == 401:
        headers["WWW-Authenticate"] = CHALLENGE
    if status == 403:
        headers["WWW-Authenticate"] = CHALLENGE + ', error="insufficient_scope"'
    raise HTTPException(status, detail, headers=headers)


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("Duplicate JSON member")
            result[key] = value
        return result

    def invalid_constant(_):
        raise ValueError("Invalid JSON constant")

    return json.loads(raw, object_pairs_hook=pairs, parse_constant=invalid_constant)


async def read_bounded(response, limit):
    length = response.headers.get("content-length")
    if length is not None and (not length.isdecimal() or int(length) > limit):
        raise ValueError("Invalid or excessive response size")
    if response.headers.get("content-encoding", "identity").lower() != "identity":
        raise ValueError("Compressed upstream responses are unsupported")
    body = bytearray()
    async for chunk in response.aiter_bytes(chunk_size=65536):
        if len(body) + len(chunk) > limit:
            raise ValueError("Excessive response size")
        body.extend(chunk)
    return bytes(body)


def check_claims(claims):
    exp = claims.get("exp")
    try:
        valid_exp = type(exp) in (int, float) and math.isfinite(exp) and exp > time.time()
    except OverflowError:
        valid_exp = False
    aud = claims.get("aud")
    if isinstance(aud, str):
        aud = [aud]
    valid_aud = isinstance(aud, list) and bool(aud) and all(
        isinstance(item, str) and item == RESOURCE for item in aud
    )
    if (claims.get("active") is not True or not valid_exp or not valid_aud or
            claims.get("iss") != ISSUER or claims.get("client_id") != TOKEN_CLIENT_ID):
        reject(401, "Invalid access token")
    scope = claims.get("scope")
    if not isinstance(scope, str):
        reject(403, "Required scope is missing")
    scopes = frozenset(scope.split())
    if "mcp.read" not in scopes:
        reject(403, "Required scope is missing")
    return scopes


def check_envelope(message, *, allowed_tools=READ_TOOLS):
    if (not isinstance(message, dict) or message.get("jsonrpc") != "2.0" or
            not isinstance(message.get("method"), str) or
            set(message) - {"jsonrpc", "id", "method", "params"}):
        reject(400, "Invalid JSON-RPC envelope")
    method = message["method"]
    if DIAGNOSTICS_ENABLED:
        logging.getLogger("xhs_gateway_rejections").warning(
            "mcp_rpc_method=%s", method if method in DIAGNOSTIC_METHODS else "other",
        )
    if "params" in message and not isinstance(message["params"], dict):
        reject(400, "MCP params must be an object")
    if method not in METHODS:
        if "id" in message and type(message["id"]) not in (str, int):
            reject(400, "MCP request requires a string or integer id")
        return method
    if method == "notifications/initialized":
        if "id" in message:
            reject(400, "Initialized notification cannot have an id")
    elif type(message.get("id")) not in (str, int):
        reject(400, "MCP request requires a string or integer id")
    if method == "tools/call":
        params = message.get("params", {})
        if not isinstance(params.get("name"), str):
            reject(400, "Tool name is required")
        if params["name"] not in allowed_tools:
            reject(403, "Tool is not allowed")
        if (set(params) - {"name", "arguments", "_meta"} or
                ("arguments" in params and not isinstance(params["arguments"], dict)) or
                ("_meta" in params and not isinstance(params["_meta"], dict))):
            reject(400, "Invalid tool call params")
    return method


def ensure_no_credential_echo(value, sensitive):
    """Inspect decoded JSON keys and values, independent of wire escaping."""
    if isinstance(value, str):
        if any(secret in value for secret in sensitive):
            raise ValueError("Credential echo rejected")
    elif isinstance(value, dict):
        for key, item in value.items():
            ensure_no_credential_echo(key, sensitive)
            ensure_no_credential_echo(item, sensitive)
    elif isinstance(value, list):
        for item in value:
            ensure_no_credential_echo(item, sensitive)


def sanitize_reply(message, request_message, settings, access_token, *, visible_tools=READ_TOOLS):
    if (not isinstance(message, dict) or message.get("jsonrpc") != "2.0" or
            type(message.get("id")) is not type(request_message.get("id")) or
            message.get("id") != request_message.get("id") or
            ("result" in message) == ("error" in message)):
        raise ValueError("Invalid backend JSON-RPC response")
    ensure_no_credential_echo(message, (
        access_token, settings.backend_token, settings.introspection_client_secret,
    ))
    if "error" in message:
        return {"jsonrpc": "2.0", "id": request_message["id"], "error": {
            "code": -32603, "message": "Backend request failed",
        }}
    result = message["result"]
    if not isinstance(result, dict):
        raise ValueError("Invalid backend result")  # noqa: TRY004 - invalid wire payload, not caller API type
    if request_message["method"] == "tools/list":
        tools = result.get("tools")
        if not isinstance(tools, list) or "nextCursor" in result:
            raise ValueError("Invalid backend tool list")
        selected = [tool for tool in tools if isinstance(tool, dict) and
                    isinstance(tool.get("name"), str) and tool["name"] in visible_tools and
                    tool["name"] != "get_publish_status"]
        if FULL_SCOPE_ENABLED and visible_tools == FULL_TOOLS:
            selected = [publication_tool(tool) if tool["name"] in PUBLISH_TOOLS else tool for tool in selected]
            selected.append(PUBLISH_STATUS_TOOL)
        if FULL_SCOPE_ENABLED and visible_tools == FULL_TOOLS:
            annotated = []
            for tool in selected:
                scopes = ["mcp.read"] if tool["name"] in READ_TOOLS else ["mcp.read", "mcp.full"]
                schemes = [{"type": "oauth2", "scopes": scopes}]
                annotated.append(dict(tool, securitySchemes=schemes,
                                      _meta=dict(tool.get("_meta", {}), securitySchemes=schemes)))
            selected = annotated
        result = {"tools": selected}
    elif (request_message["method"] == "tools/call" and result.get("isError") and
          request_message["params"]["name"] in READ_TOOLS):
        result = {"isError": True, "content": [{"type": "text", "text": "Backend tool failed"}]}
    reply = {"jsonrpc": "2.0", "id": request_message["id"], "result": result}
    return reply


PUBLISH_STATUS_TOOL = {
    "name": "get_publish_status", "title": "查询发布提交状态",
    "description": "查询后台发布作业。发布工具受理不等于已提交；对queued/running继续查询同一作业。unknown时先核查创作平台笔记管理，禁止自动重发。省略publication_id返回本账号最近10次作业，可找回断连前请求。submitted仅确认提交，不保证公开或审核通过。",
    "annotations": {"readOnlyHint": True, "destructiveHint": False, "openWorldHint": False},
    "inputSchema": {"type": "object", "additionalProperties": False, "properties": {
        "publication_id": {"type": "string", "description": "发布工具返回的作业号；省略列出最近作业"},
        "wait_seconds": {"type": "integer", "minimum": 0, "maximum": 15, "default": 10,
                         "description": "等待状态变化的秒数（0-15），默认10"}}},
}


def publication_tool(tool):
    schema = dict(tool.get("inputSchema", {"type": "object"}))
    schema["properties"] = dict(schema.get("properties", {}), idempotency_key={
        "type": "string", "minLength": 1, "maxLength": 128,
        "description": "一次发布意图的唯一键；断连重试必须复用同一键，修改内容或明确新发布用新键。未知结果先核查，不能换键重发。省略时服务器对相同内容进行防重复保护。"})
    result = dict(tool, inputSchema=schema)
    result["description"] = tool.get("description", "") + (
        " 云端后台执行，返回publication_id。queued/running仅表示已受理，请调用get_publish_status直到终态；"
        "submitted仅表示已确认提交，不代表公开；unknown禁止自动重发，先检查平台笔记管理。AI声明默认开启，未确认则停止发布。")
    result.pop("outputSchema", None)
    return result


def create_app(settings=None, *, transport=None, publish_db=None, foreground_wait=2):
    settings = settings if settings is not None else Settings.from_env()

    @asynccontextmanager
    async def lifespan(app):
        async with httpx.AsyncClient(
            transport=transport, trust_env=False, follow_redirects=False,
            timeout=httpx.Timeout(30, connect=3, pool=3),
            limits=httpx.Limits(max_connections=20, max_keepalive_connections=10),
        ) as outbound:
            app.state.outbound = outbound
            jobs = None
            if FULL_SCOPE_ENABLED:
                jobs = PublishJobs(publish_db or os.environ.get(
                    "GATEWAY_PUBLISH_DB", "/var/lib/xhs-mcp-domain-gateway/publish.sqlite3"))
            app.state.publish_jobs = jobs
            try:
                yield
            finally:
                if jobs is not None:
                    await jobs.close()

    app = FastAPI(
        lifespan=lifespan, debug=False, docs_url=None, redoc_url=None, openapi_url=None,
        redirect_slashes=False,
    )
    app.add_middleware(TrustedHostMiddleware, allowed_hosts=[PUBLIC_HOSTNAME, "127.0.0.1"])

    if DIAGNOSTICS_ENABLED:
        @app.exception_handler(HTTPException)
        async def trace_rejection(request, error):
            reason = error.detail if error.detail in DIAGNOSTIC_REJECTIONS else "other_rejection"
            logging.getLogger("xhs_gateway_rejections").warning(
                "gateway_reject status=%s reason=%s", error.status_code, reason,
            )
            return await http_exception_handler(request, error)

    @app.get("/.well-known/oauth-protected-resource")
    @app.get("/.well-known/oauth-protected-resource/mcp")
    async def metadata():
        return JSONResponse({
            "resource": RESOURCE, "authorization_servers": [ISSUER],
            "scopes_supported": ["mcp.read", "mcp.full"] if FULL_SCOPE_ENABLED else ["mcp.read"],
            "bearer_methods_supported": ["header"],
        }, headers=SAFE_HEADERS)

    async def authenticate(request: Request):
        origins = request.headers.getlist("origin")
        if origins and (len(origins) != 1 or origins[0] not in ALLOWED_ORIGINS):
            raise HTTPException(403, "Origin is not allowed", headers=SAFE_HEADERS)
        values = request.headers.getlist("authorization")
        match = BEARER.fullmatch(values[0]) if len(values) == 1 else None
        if match is None:
            reject(401, "A valid Bearer access token is required")
        token = match[1]
        if hmac.compare_digest(token, settings.backend_token):
            # Native-key compatibility authenticates only; the same read-only routing follows.
            return AuthenticatedAccess(token)
        if not token.startswith("authelia_at_"):
            reject(401, "A valid Bearer access token is required")
        form = urlencode({"token": token, "token_type_hint": "access_token"})
        auth = httpx.BasicAuth(
            quote(settings.introspection_client_id, safe=""),
            quote(settings.introspection_client_secret, safe=""),
        )
        as_request = httpx.Request("POST", settings.introspection_url, content=form, headers={
            "content-type": "application/x-www-form-urlencoded", "accept": "application/json",
            "accept-encoding": "identity",
            "host": PUBLIC_HOST, "x-forwarded-host": PUBLIC_HOST,
            "x-forwarded-proto": "https",
        })
        try:
            async with asyncio.timeout(10):
                response = await request.app.state.outbound.send(
                    as_request, auth=auth, stream=True, follow_redirects=False,
                )
                try:
                    if response.status_code != 200:
                        reject(503, "Authorization service unavailable")
                    raw = await read_bounded(response, MAX_INTROSPECTION)
                finally:
                    await response.aclose()
            claims = strict_json(raw)
            if not isinstance(claims, dict):
                raise ValueError("Invalid introspection response")  # noqa: TRY004 - invalid wire payload
        except (httpx.HTTPError, TimeoutError, ValueError, RecursionError, OverflowError):
            reject(503, "Authorization service unavailable")
        scopes = check_claims(claims)
        return AuthenticatedAccess(
            token,
            allowed_tools=FULL_TOOLS if FULL_SCOPE_ENABLED and "mcp.full" in scopes else READ_TOOLS,
            visible_tools=FULL_TOOLS if FULL_SCOPE_ENABLED else READ_TOOLS,
            subject=claims.get("sub") if isinstance(claims.get("sub"), str) else "",
        )

    @app.api_route("/mcp", methods=["POST", "GET", "DELETE", "PUT", "PATCH", "HEAD", "OPTIONS"])
    async def mcp(request: Request, access: AuthenticatedAccess = Depends(authenticate)):
        if request.method != "POST":
            reject(405, "Only stateless MCP POST is supported")
        if request.headers.get("content-type", "").split(";", 1)[0].lower() != "application/json":
            reject(415, "Content-Type must be application/json")
        if request.headers.get("content-encoding", "identity").lower() != "identity":
            reject(415, "Compressed requests are unsupported")
        length = request.headers.get("content-length")
        if length is not None and (not length.isdecimal() or int(length) > MAX_REQUEST):
            reject(413, "Request body exceeds the limit")
        raw = bytearray()
        try:
            async with asyncio.timeout(10):
                async for chunk in request.stream():
                    if len(raw) + len(chunk) > MAX_REQUEST:
                        reject(413, "Request body exceeds the limit")
                    raw.extend(chunk)
        except TimeoutError:
            reject(408, "Request body timeout")
        try:
            message = strict_json(raw)
        except (ValueError, RecursionError, OverflowError):
            reject(400, "Invalid JSON body")
        method = check_envelope(message, allowed_tools=access.visible_tools)
        if method not in METHODS:
            if "id" not in message:
                return Response(status_code=202, headers=SAFE_HEADERS)
            return JSONResponse({
                "jsonrpc": "2.0", "id": message["id"],
                "error": {"code": -32601, "message": "Method not found"},
            }, headers=SAFE_HEADERS)
        if method == "tools/call" and message["params"]["name"] not in access.allowed_tools:
            return JSONResponse({
                "jsonrpc": "2.0", "id": message["id"], "result": {
                    "isError": True,
                    "content": [{"type": "text", "text": "Full tool access requires mcp.full authorization."}],
                    "_meta": {"mcp/www_authenticate": [FULL_AUTH_CHALLENGE]},
                },
            }, headers=SAFE_HEADERS)
        headers = {
            "host": BACKEND_HOST, "authorization": "Bearer " + settings.backend_token,
            "content-type": "application/json", "accept": "application/json, text/event-stream",
            "accept-encoding": "identity",
        }
        if "mcp-protocol-version" in request.headers:
            headers["mcp-protocol-version"] = request.headers["mcp-protocol-version"]
        if method == "tools/call" and message["params"]["name"] in PUBLISH_TOOLS | {"get_publish_status"}:
            if not access.subject:
                reject(403, "Publishing requires an OAuth account subject")
            owner = hashlib.sha256(json.dumps([RESOURCE, TOKEN_CLIENT_ID, access.subject]).encode()).hexdigest()
            name = message["params"]["name"]
            args = message["params"].get("arguments", {})
            jobs = request.app.state.publish_jobs
            try:
                if name == "get_publish_status":
                    if set(args) - {"publication_id", "wait_seconds"}:
                        raise ValueError("Unsupported status query argument")
                    seconds = args.get("wait_seconds", 10)
                    if type(seconds) is not int or not 0 <= seconds <= 15:
                        raise ValueError("wait_seconds must be an integer from 0 to 15")
                    job_id = args.get("publication_id")
                    if job_id is None:
                        data = {"publications": jobs.recent(owner)}
                    else:
                        if not isinstance(job_id, str) or not re.fullmatch(r"pub_[0-9a-f]{32}", job_id):
                            raise ValueError("Invalid publication_id")
                        data = await jobs.wait(owner, job_id, seconds)
                else:
                    async def execute(arguments):
                        backend_message = dict(message, params={"name": name, "arguments": arguments})
                        publication_request = httpx.Request("POST", BACKEND_URL, headers=headers,
                            content=json.dumps(backend_message).encode(),
                            extensions={"timeout": {"connect": 3, "pool": 3, "read": 660, "write": 15}})
                        async with asyncio.timeout(665):
                            response = await request.app.state.outbound.send(publication_request, stream=True, follow_redirects=False)
                            try:
                                if response.status_code != 200 or response.headers.get("content-type", "").split(";", 1)[0].lower() != "application/json":
                                    raise ValueError("Invalid publication backend response")
                                body = await read_bounded(response, MAX_RESPONSE)
                            finally:
                                await response.aclose()
                        return sanitize_reply(strict_json(body), backend_message, settings, access.token,
                                              visible_tools=access.visible_tools).get("result", {})
                    data = await jobs.submit(owner, name, args, execute)
                    data = await jobs.wait(owner, data["publication_id"], foreground_wait)
                result = tool_result(data)
            except ValueError as error:
                result = tool_result({"message": str(error)}, error=True)
            return JSONResponse({"jsonrpc": "2.0", "id": message["id"], "result": result}, headers=SAFE_HEADERS)
        backend_request = httpx.Request("POST", BACKEND_URL, headers=headers, content=bytes(raw))
        try:
            async with asyncio.timeout(35):
                response = await request.app.state.outbound.send(
                    backend_request, stream=True, follow_redirects=False,
                )
                try:
                    if not 200 <= response.status_code < 300:
                        reject(502, "Backend request failed")
                    if method == "notifications/initialized":
                        return Response(status_code=202, headers=SAFE_HEADERS)
                    if response.headers.get("content-type", "").split(";", 1)[0].lower() != "application/json":
                        reject(502, "Backend must return stateless JSON")
                    raw_reply = await read_bounded(response, MAX_RESPONSE)
                finally:
                    await response.aclose()
            reply = sanitize_reply(strict_json(raw_reply), message, settings, access.token,
                                   visible_tools=access.visible_tools)
            return JSONResponse(reply, headers=SAFE_HEADERS)
        except (httpx.TimeoutException, TimeoutError):
            reject(504, "Backend request timed out")
        except (httpx.HTTPError, ValueError, RecursionError, OverflowError):
            reject(502, "Backend request failed")

    return app
