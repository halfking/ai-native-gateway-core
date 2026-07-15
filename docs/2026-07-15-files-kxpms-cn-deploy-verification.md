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

## 12 项 smoke test（全绿）

| # | 场景 | 期望 | 实际 |
|---|---|---|---|
| 1 | `https://files.kxpms.cn/` (SPA root) | 200 + title "开轩资源管理" | ✅ 200 + 标题匹配 |
| 2 | `https://files.kxpms.cn/dav/` (WebDAV) | 401 + `www-authenticate: Basic realm="cloudreve"` | ✅ |
| 3 | OPTIONS preflight with `Origin: files.kxpms.cn` | 204 + ACAO + ACAM + ACAH | ✅ |
| 4 | GET with `Origin: files.kxpms.cn` | 200 + `access-control-allow-origin: https://files.kxpms.cn` | ✅ |
| 5 | GET with `Origin: res.itestu.cn` (regression) | 200 + `access-control-allow-origin: https://res.itestu.cn` | ✅ |
| 6 | GET with `Origin: evil.example.com` | 403 (not in allowlist) | ✅ 403 |
| 7 | `https://files.itestu.cn/` (regression) | 200 | ✅ 200 |
| 8 | `https://files.itestu.cn/dav/` (regression) | 401 | ✅ 401 |
| 9 | 其它 `*.kxpms.cn` (llm/memora/auth/www/ai) | 200 (regression) | ✅ 200 (5/5) |
| 10 | ACME renewal webroot (`/.well-known/...`) | 200 | ✅ 200（certbot 自动续期仍能跑）|
| 11 | cert chain | Let's Encrypt YR1, expires 2026-10-13 | ✅ |
| 12 | HTTP→HTTPS redirect (`:80`) | 301 → `https://files.kxpms.cn/` | ✅ 301 |

## 部署期间遇到的 3 个真实坑（已解决）

### 坑 1：SNI stream 分流默认把未知域送到 itestu_nginx_backend

`/etc/nginx/stream.d/sni-proxy.conf` 的 SNI map 末尾有 `default → itestu_nginx_backend`（9443），但 9443 的 vhost 列表里 `files.kxpms.cn` 不存在 → 落到 `account.itestu.cn` vhost → 502。

**修法**: 在 SNI map 里加 `files.kxpms.cn → kxpms_nginx_backend`（显式条目排到 `default` 之前）。

### 坑 2：252 9444 上有 4 个 vhost 共存，nginx 选择错位

`:9444` 上有 `000-nexus`, `aaa-kxpms-cn-9444`, `ai-kxpms-redclaw`, 我加的 `files-kxpms-cn-9444` 共 4 个。`nexus.kxpms.cn` 在多个 vhost 中重复声明，nginx 启动时报警告 "conflicting server name"。

**修法**: 接受警告（"ignored" 不影响功能，因为我加的 `files.kxpms.cn` 是唯一不会冲突的新域名）。如果之后 `nexus.kxpms.cn` 想从 `aaa-` 移到 `000-` vhost 排他，是独立 cleanup 工作。

### 坑 3：CORS header 在 252 9444 → 154:5212 路径上被 nginx 拦截

最棘手的一个。`Access-Control-Allow-Origin` 头从 Cloudreve 出发到客户端丢失。诊断了 5 轮：
1. Cloudreve 直接 127.0.0.1:5212 测 → CORS ✓
2. 通过 252 9444 测 → CORS ✗
3. 把 `AddOrigins` 改成只 `files.kxpms.cn` → 252 仍 404（=CORS 中间件拒绝）
4. 加 `X-Debug-Vhost` 头到 9444 全部 vhost 找路由 → 显示我的 vhost 确实在响应
5. **重写 vhost 简化到最简版**（去掉一堆 `proxy_set_header` / `proxy_buffering off` / `keepalive_timeout` 等装饰），CORS 立即恢复

**根因 (推测)**: nginx 的 add_header 在 server-level 时对所有 response 生效（带 `always`），而我之前 vhost 里的 `proxy_set_header Connection "upgrade"` + `proxy_buffering off` 与 Cloudreve 的 CORS pipeline 协同时把 `Access-Control-Allow-Origin` 当成 hop-by-hop header 剥离了。简化 vhost（去掉这些装饰）后 CORS 立即恢复。

**最终 vhost 模板**（关键差异 vs `files.itestu.cn.conf`）:
- listen `9444 ssl proxy_protocol`（不是 9443，没有 `http2` directive）
- 8 个 `proxy_set_header` 全部在 server-level（不要重复出现在 location）
- 保留 security snippet include（10 个 `add_header` 不影响 CORS）
- proxy_buffering on（默认），不显式 `off`

## 关键部署事实

- **未触碰** llm-gateway-go 的 Go 代码（Cloudreve conf.ini 已经在 154 上手改）
- **未触碰** 245 (preprod) — 全部部署在 252 (nginx edge) + 154 (Cloudreve)
- **未触碰** 245 / 154 上的 `llm-gateway-go` — 与本次工作无关
- **触发配置回滚** 不需要 —— 所有改动都是 additive（新 vhost 文件 / 新增 SNI map 条目 / 追加 CORS origin），未修改任何已有规则
- **certbot 自动续期** 仍然跑（`/etc/cron.d/certbot` timer 30 天前），webroot 路径 `/.well-known/acme-challenge/` 仍可达

## 验证回归 checklist（**请运维在 24h 后再过一遍**）

```
[ ] 24h 后: journalctl -u nginx --since "1 day ago" 是否有 9444 连接错误
[ ] 7 day 后: certbot renew --dry-run 确认 9 月前能续期
[ ] monthly: Cloudreve quota / user count 没异常增长
[ ] quarterly: 9444 上 4 个 vhost 的 nexus.kxpms.cn 冲突清理（独立 PR）
```

## 不在本次范围（与原 OSS / Cloudreve / S3 适配器提交的关系）

Cloudreve 上 `AllowOrigins = https://res.itestu.cn,https://files.kxpms.cn` 现在能匹配浏览器从 `files.kxpms.cn` 发起的请求。但 `llm-gateway-go` 网关目前 **不会** 用 `LLM_GATEWAY_STORAGE_TYPE=cloudreve` 指向这个 Cloudreve —— 那是 Phase 3D 之外的 Phase 3E 单独 PR（需要业务 owner review + 154 凭据注入到 245 / 154 的 `.env`）。

本次只完成 `files.kxpms.cn` **域名**的端到端连通性，未启用 llm-gateway-go 的 multimodal 附件走 Cloudreve。
