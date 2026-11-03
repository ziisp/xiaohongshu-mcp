# OAuth 云端网关与可靠发布

此目录保存云端网关源码。Go 后端是 18 个工具；启用完整权限的域名网关增加 `get_publish_status`，共 19 个工具。直接调用 Go MCP/REST 仍是同步执行，后台作业能力由此网关提供。

## 发布后查询同一作业

图文 `publish_content`、视频 `publish_with_video` 保留原参数，另外可传 `idempotency_key`。耗时操作先返回 `publication_id`，再查询：

```json
{
  "name": "get_publish_status",
  "arguments": {
    "publication_id": "pub_返回的作业编号",
    "wait_seconds": 10
  }
}
```

- `queued/running`：已受理或处理中，继续查同一作业，不是提交成功。
- `submitted`：可信创作平台成功页已确认提交，不保证已经公开或审核通过。
- `failed`：点击发布前失败，`retry_safe=true`，修正原因后再提交新请求。
- `unknown`：提交结果未知，先检查平台笔记管理，不得自动重发。

省略 `publication_id` 列出当前 OAuth 账号最近 10 次作业；`wait_seconds` 为 0–15 的整数。关闭聊天不取消已受理作业。刷新插件工具后，建议在新聊天中使用，避免旧工具缓存。

同一次发布的重试复用同一 `idempotency_key`，不能换键绕过相同内容的在途/未知作业。无键的相同已提交内容另保护 10 分钟；未知作业不会自动解除保护，必须先核实平台状态后由运维处置。

图文和视频的内容声明默认选“笔记含AI合成内容”。明确选择其他声明时，使用 `content_declaration`，可选平台实际文案：`笔记含AI合成内容`、`自主拍摄`、`来源转载`、`虚构演绎，仅供娱乐`、`内容包含营销广告`、`无需声明`。来源声明通过原生二级菜单选择；“无需声明”通过悬停清除按钮执行。选项不可用或值未确认均在发布前停止，不以正文文字代替平台声明。

来源弹窗的必填信息通过 `declaration_details` 传入：自主拍摄填写 `shoot_location`（唯一匹配的真实地点名）及 `shoot_date`（真实日期，YYYY-MM-DD）；来源转载填写 `source_media`（真实媒体名）。缺信息在处理素材/创建作业前拒绝；地点匹配不唯一、信息未保留或弹窗未确认关闭都会停发，不编造字段。来源信息计入防重指纹；省略补充信息的旧默认 AI 指纹不变。

旧 `ai_generated` 参数继续兼容：未指定内容声明时，省略/true 默认 AI，false 不主动修改控件。显式 true + 非 AI 声明、false + AI 声明会在受理前拒绝。图文和视频均支持独立的 `is_original` 原创声明，发布前会再次复核开关；不得把原创声明与 AI 声明混淆。

输入不满足发布 schema（如空图片数组）时直接返回提交前失败，不创建未知作业。新声明参数与旧默认请求使用兼容的幂等表示；视频显式 `is_original:false` 不会绕过旧视频作业保护。

## 接入已有后端与 OAuth 服务

运行条件：Python 3.11+（已验证 Python 3.14）、Linux/macOS。`fcntl` 持锁，不支持 Windows；必须单进程运行，不能增加 Uvicorn workers。

1. 将此目录中的文件部署到 `/opt/xhs-mcp-gateway`，创建虚拟环境并安装 `requirements.txt`。
2. 已有 Go 后端监听 `127.0.0.1:18060`，并启用 `AUTH_TOKEN`；网关使用相同后端令牌。
3. 已有 Authelia introspection 服务监听 `127.0.0.1:9091/auth/api/oidc/introspection`；在其客户端配置中为网关提供 introspection 客户端凭据。`GATEWAY_INTROSPECTION_URL` 可覆盖回环地址。
4. 复制 `.env.example` 为仓库外的 `/etc/xhs-mcp-gateway/domain.env`，填写自己的 HTTPS origin 和凭据。模板里的空凭据会阻止启动。文件只允许 root 与 `xhs-gateway` 服务组读取，禁止提交真实配置。
5. 已有 `xhs-gateway` 系统用户后，参考 `xhs-mcp-domain-gateway.service.example` 配置服务。`StateDirectory` 必须可写；网关只监听回环，通过已有 HTTPS 反向代理暴露 `/mcp` 和资源元数据端点。

OAuth 绑定规则：

- resource 为 `${GATEWAY_PUBLIC_ORIGIN}/mcp`，issuer 为 `${GATEWAY_PUBLIC_ORIGIN}/auth`，公共客户端 ID 为 `xhs-chatgpt`。
- 所有访问都需要 `mcp.read`；启用 `GATEWAY_FULL_SCOPE_ENABLED=1` 后，额外工具还需 `mcp.full`。发布与状态查询也需要稳定、非空的 OAuth `sub`，作业按 subject/resource/client 隔离。
- 未启用完整权限时只保留原七个只读工具。直接后端令牌兼容访问始终只读，不会自动提权。
- 为保留原 IP 入口的既有行为，源码仍保留原部署的默认 origin；**新部署必须显式设置自己的 `GATEWAY_PUBLIC_ORIGIN`**。测试中的 IP/域名只用于 mock 验证，不访问实际站点。

参考启动命令（先通过受限环境文件提供凭据）：

```sh
/opt/xhs-mcp-gateway/venv/bin/python -m uvicorn gateway:create_app \
  --factory --app-dir /opt/xhs-mcp-gateway \
  --host 127.0.0.1 --port 18062 --no-access-log --no-proxy-headers
```

网关只接受 stateless JSON MCP 响应；后端应使用本分支的 Streamable HTTP 配置。发布入口短暂等待后返回作业，状态查询最多等待 15 秒；无需把外部代理时限延长到视频上传的数分钟。

## 状态与恢复

默认状态文件为 `/var/lib/xhs-mcp-domain-gateway/publish.sqlite3`，可用 `GATEWAY_PUBLISH_DB` 覆盖。目录 0700、数据库 0600；单进程最多受理 8 个待处理作业，一次执行 1 个。数据库保存状态、幂等摘要和有界结果说明，结果可能包含笔记内容，不保存 OAuth/后端凭据。

网关异常退出或服务重启后，未完成作业转为 `unknown`，不自动重新执行。升级、回滚前先停止接收新发布并核查在途作业；保留 SQLite 数据库，不能因为恢复旧程序就认定之前没有发布。

## 回归测试不会发布真实笔记

```sh
cd deploy/oauth-gateway
python3 -m unittest discover -v
```

测试使用合成 OAuth/后端或临时回环 HTTP 服务，不读取真实凭据、不调用业务账号。长调用测试约需 31 秒：第 20 秒模拟提交，第 31 秒返回确认，验证重复请求只派发一次。

Go 测试沿用仓库根目录入口：

```sh
go test ./...
XHS_TEST_BROWSER=/path/to/chromium go test ./xiaohongshu -count=1 -timeout=6m
```

Go 后端要求 Go 1.27.3+。设置 `XHS_TEST_BROWSER` 会开启隔离浏览器和本地页面测试，不使用业务账号。`ai_live_test.go` 和 `content_declaration_live_test.go` 属于另行手动开启的真实平台草稿测试：它们会上传独立测试素材、不会点击发布；本目录回归和默认 `go test ./...` 不会运行这些测试。
