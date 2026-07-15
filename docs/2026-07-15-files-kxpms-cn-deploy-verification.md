# 2026-07-15 — files.kxpms.cn 部署 + 验证报告

## 概要

将 `files.kxpms.cn`（之前返回 502）端到端修复，桥接到 154 上的 Cloudreve 实例。本次工作 **不在 `llm-gateway-go` 仓库内修改任何 Go 代码** —— 所有改动是 live ops 落在 252 (nginx + certbot + stream) 和 154 (Cloudreve conf.ini) 上。

涉及 `official-deploy` 仓库的文档记录（本次 commit），但**不在本仓库内修改 nginx / systemd / certbot 脚本**—— 那些配置由 252 维护者线下 git 仓库控制（252 是独立 ECS 实例，本 workspace 无访问权）。

## 改了什么

| 机器 | 文件 | 改动 |
|---|---|---|
| 252 | `/etc/nginx/conf.d/kxpms-on-252.conf` (`:80` vhost) | `server_name` 列表追加 `files.kxpms.cn`，让 certbot webroot ACME challenge 能命中 |
| 252 | `/etc/nginx/stream.d/sni-proxy.conf` (SNI map) | 新增 `files.kxpms.cn → kxpms_nginx_backend` 路由（public 443 SNI 分流）|
| 252 | `/etc/letsencrypt/live/files.kxpms.cn/` | certbot 颁发新 cert（CN=files.kxpms.cn，Let's Encrypt YR1，2026-10-13 到期）|
| 252 | `/etc/nginx/conf.d/files-kxpms-cn-9444.conf` (新) | 9444 端口的 SNI-routed vhost，proxy_pass 到 154:5212 |
| 154 | `/opt/res-manager/conf.ini` (`[CORS]`) | `AllowOrigins` 追加 `https://files.kxpms.cn` |
| 154 | Cloudreve 进程 | SIGTERM 旧 PID 4433 → nohup 起新 PID 26382 |

## 13 项 smoke test（全绿）

| # | 场景 | 期望 | 实际 |
|---|---|---|---|
| 1 | `https://files.kxpms.cn/` (SPA root) | 200 + title "开轩资源管理" | ✅ 200 + 标题匹配 |
| 2 | `https://files.kxpms.cn/dav/` (WebDAV) | 401 + `www-authenticate: Basic realm="cloudreve"` | ✅ |
| 3 | Same-origin `Origin: files.kxpms.cn` | 200 (CORS not needed for same-origin) | ✅ 200 |
| 4 | Cross-origin `Origin: res.itestu.cn` | 200 + `access-control-allow-origin: https://res.itestu.cn` | ✅ |
| 5 | Cross-origin with trailing slash | 403 (different from host, not in allowlist) | ✅ 403 |
| 6 | `Origin: evil.example.com` | 403 (not in allowlist) | ✅ 403 |
| 7 | `/dav/` OPTIONS preflight with res.itestu.cn origin | 204 + ACAO + ACAM + ACAH | ✅ |
| 8 | `https://files.itestu.cn/` (regression) | 200 | ✅ 200 |
| 9 | 其它 `*.kxpms.cn` (llm/memora/auth/www/ai) | 200 (regression) | ✅ 200 (5/5) |
| 10 | ACME renewal webroot (`/.well-known/...`) | 200 | ✅ 200（certbot 自动续期仍能跑）|
| 11 | cert chain | Let's Encrypt YR1, expires 2026-10-13 | ✅ |
| 12 | HTTP→HTTPS redirect (`:80`) | 301 → `https://files.kxpms.cn/` | ✅ 301 |
| 13 | 245 / 154 `llm-gateway-go` healthz | 200 (regression, no impact) | ✅ 200 / 200 |

## CORS 行为的「真相」（踩坑笔记）

我一开始误判「CORS header 在 252 → 154 路径上被 nginx 拦截」，调试了一小时。**实际上 CORS 是正确工作的**，只是我对 gin-contrib/cors 的 short-circuit 行为理解错了：

```go
// gin-contrib/cors (v1.5.0+) applyCors:
if origin == "http://"+host || origin == "https://"+host {
    return  // ← same-origin 请求：CORS middleware 直接返回，不发 ACAO
}
```

Cloudreve v4 用 gin-contrib/cors 做 CORS。当 `Origin: https://files.kxpms.cn` 且 request `Host: files.kxpms.cn` 时，gin-contrib/cors 判定为 same-origin，**不发 `Access-Control-Allow-Origin`** —— 这是符合 CORS 规范的：浏览器对 same-origin 请求不做 CORS 强制检查，所以不需要 ACAO 头。

实测覆盖 5 个 origin 场景：

| Origin | Same-Origin? | 期望 | 实际 |
|---|---|---|---|
| `https://files.kxpms.cn` | ✓ | 200, no ACAO (same-origin) | ✅ |
| `https://res.itestu.cn` | ✗ | 200 + ACAO=res.itestu.cn | ✅ |
| `https://res.itestu.cn/` | ✗ (trailing slash) | 403 (not in allowlist) | ✅ |
| `https://evil.example.com` | ✗ | 403 | ✅ |
| `http://files.kxpms.cn` (HTTP, not HTTPS) | ✗ (different scheme) | 200 (host same, gin short-circuits) | ✅ |

**修正后的结论**:
- Same-origin（`files.kxpms.cn` → `files.kxpms.cn`）：CORS 跳过，正确。
- Cross-origin（`res.itestu.cn` → `files.kxpms.cn`）：CORS 触发，ACAO 发出。
- Cross-origin 失败（`evil.example.com`）：403 拒绝。

我之前误把 same-origin 的"无 ACAO"当作"bug"，实质上是规范行为。

## 部署期间遇到的 4 个真实坑（已解决）

### 坑 1：SNI stream 分流默认把未知域送到 itestu_nginx_backend

`/etc/nginx/stream.d/sni-proxy.conf` 的 SNI map 末尾有 `default → itestu_nginx_backend`（9443），但 9443 的 vhost 列表里 `files.kxpms.cn` 不存在 → 落到 `account.itestu.cn` vhost → 502。

**修法**: 在 SNI map 里加 `files.kxpms.cn → kxpms_nginx_backend`（显式条目排到 `default` 之前）。

### 坑 2：252 9444 上有 4 个 vhost 共存，nginx 选择错位

`:9444` 上有 `000-nexus`, `aaa-kxpms-cn-9444`, `ai-kxpms-redclaw`, 我加的 `files-kxpms-cn-9444` 共 4 个。`nexus.kxpms.cn` 在多个 vhost 中重复声明，nginx 启动时报警告 "conflicting server name"。

**修法**: 接受警告（"ignored" 不影响功能，因为我加的 `files.kxpms.cn` 是唯一不会冲突的新域名）。如果之后 `nexus.kxpms.cn` 想从 `aaa-` 移到 `000-` vhost 排他，是独立 cleanup 工作。

### 坑 3：acme.sh 80 端口的 ACME challenge 走错 vhost

certbot webroot 模式需要 `.well-known/acme-challenge/` 在 :80 路由可达。但 `kxpms-on-252.conf :80` vhost `server_name` 列表没包含 `files.kxpms.cn` → challenge 落到默认 vhost → 502。

**修法**: 把 `files.kxpms.cn` 加到 `kxpms-on-252.conf` 的 :80 vhost `server_name` 列表。

### 坑 4（误判）：CORS 在 252 → 154 路径上被拦截（**实际是规范行为**）

我误以为 same-origin 请求的"无 ACAO header"是 nginx 拦截 CORS 的 bug。调试一轮后才意识到这是 gin-contrib/cors 的 same-origin short-circuit 行为。

**没有修复** —— 因为本来就不需要修复。Same-origin 请求浏览器不做 CORS 强制检查，server 不发 ACAO 头是正确行为。

## 最终 vhost 模板（关键参数）

`/etc/nginx/conf.d/files-kxpms-cn-9444.conf`:
- `listen 9444 ssl proxy_protocol`（不要加 `http2`，保持与 9443 区分；某些 bug 在 9443/9444 mix 中出现）
- `set_real_ip_from 127.0.0.1; real_ip_header proxy_protocol;`（让 stream 层的 PROXY 头能解出真实 client IP）
- `proxy_set_header` 5 个 directives 全部在 `location / { }` 内部（**不要**在 server-level —— 行为不同）
- `proxy_read_timeout 1200s`（Cloudreve 大文件上传需要）
- `client_max_body_size 1024m`（与 files.itestu.cn vhost 对齐）
- `keepalive_timeout 300s`
- `include /etc/nginx/conf.d/00-security-hardening.snippet;`（**在 server-level**，紧跟 autoindex off）
- 6 个 `location ~ ... { deny all; return 404; }` 敏感文件 block + exploit pattern block

## 关键部署事实

- **未触碰** llm-gateway-go 的 Go 代码（Cloudreve conf.ini 已经在 154 上手改）
- **未触碰** 245 (preprod) — 全部部署在 252 (nginx edge) + 154 (Cloudreve)
- **未触碰** 245 / 154 上的 `llm-gateway-go` — 与本次工作无关
- **触发配置回滚** 不需要 —— 所有改动都是 additive（新 vhost 文件 / 新增 SNI map 条目 / 追加 CORS origin），未修改任何已有规则
- **certbot 自动续期** 仍然跑（`/etc/cron.d/certbot` timer 30 天前），webroot 路径 `/.well-known/acme-challenge/` 仍可达
- **Cloudreve 配置** 是 SIGTERM 旧 PID + nohup 新 PID 重启，未用 systemd（与现有运维模式一致）

## 验证回归 checklist（**请运维在 24h 后再过一遍**）

```
[ ] 24h 后: journalctl -u nginx --since "1 day ago" 是否有 9444 连接错误
[ ] 24h 后: journalctl -u certbot 看到自动续期定时器已记录
[ ] 7 day 后: certbot renew --dry-run 确认 9 月前能续期
[ ] monthly: Cloudreve quota / user count 没异常增长
[ ] quarterly: 9444 上 4 个 vhost 的 nexus.kxpms.cn 冲突清理（独立 PR）
```

## 不在本次范围（与原 OSS / Cloudreve / S3 适配器提交的关系）

Cloudreve 上 `AllowOrigins = https://res.itestu.cn,https://files.kxpms.cn` 现在能匹配浏览器从 `files.kxpms.cn` 发起的请求。但 `llm-gateway-go` 网关目前 **不会** 用 `LLM_GATEWAY_STORAGE_TYPE=cloudreve` 指向这个 Cloudreve —— 那是 Phase 3D 之外的 Phase 3E 单独 PR（需要业务 owner review + 154 凭据注入到 245 / 154 的 `.env`）。

本次只完成 `files.kxpms.cn` **域名**的端到端连通性，未启用 llm-gateway-go 的 multimodal 附件走 Cloudreve。

## 业务 owner 复审问题（建议在下个 sprint 答）

- 1.x 阶段 245 / 154 的 `.env` 是否注入 Cloudreve / OSS / S3 凭据？
- 2.x 阶段 `LLM_GATEWAY_STORAGE_TYPE` 切到 `cloudreve` 时，是否需要数据迁移（从 local FS 把已有 attachments 推到 Cloudreve）？还是接受「新数据走 Cloudreve、旧数据继续 local FS」？
- 3.x 阶段 Cloudreve 容量上限：1 GiB cap（来自 client_max_body_size）是上限还是 baseline？是否需要 quota enforcement？
