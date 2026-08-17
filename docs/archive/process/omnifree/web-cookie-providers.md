# Web-Cookie 逆向 Provider 实现状态 (Backlog)

本文档列出所有 web-cookie 类免费 provider 的 Go executor 实现状态。
这些 provider 通过逆向浏览器聊天接口提供免费 LLM 访问 (无官方免费 API)。

## ⚠️ 合规与稳定性提示

- **ToS 风险**: 多数 web-cookie provider 的服务条款明确禁止自动化/代理使用。
  本网关的 `tos_verdict` 多标记为 `avoid` 或 `caution`, auto-combo 模板默认
  `tos_filter: ["ok","caution"]` 会排除 `avoid` provider。**仅人工显式启用。**
- **稳定性**: 依赖第三方前端, 反爬升级随时可能导致失效。需长期维护。
- **实施方式**: 每个 provider 需用浏览器 DevTools 抓包分析其实际 HTTP/WS/SockJS
  协议, 然后在 `domains/streaming/executors/webcookie/` 下实现 `Adapter` 接口
  (`Login` / `RefreshSession` / `Chat` / `ParseStream`)。

## 框架

`domains/streaming/executors/webcookie/base.go` 提供:
- `Session` — 浏览器会话 (cookies + http.Client with cookie jar)
- `Adapter` 接口 — 每个 provider 实现的协议适配器
- `Registry` — 按 provider code 注册/查找 adapter
- `SessionManager` — 会话池 (从 `webcookie_sessions` 表加载, 过期自动刷新)

## 实现状态 (2026-08-10)

| Provider | 域名 | ToS | 协议 | 状态 |
|----------|------|-----|------|------|
| **deepseek-web** | chat.deepseek.com | caution | SockJS/HTTP | 🟡 骨架 (待抓包) |
| chatgpt-web | chatgpt.com | avoid | TLS指纹+arkose+SockJS | ❌ 待实现 |
| claude-web | claude.ai | avoid | TLS指纹+SockJS | ❌ 待实现 |
| gemini-web | gemini.google.com | avoid | OAuth cookie | ❌ 待实现 |
| grok-web | grok.com | avoid | HTTP | ❌ 待实现 |
| copilot-web | copilot.microsoft.com | avoid | HTTP | ❌ 待实现 |
| kimi-web | kimi.moonshot.cn | caution | HTTP | ❌ 待实现 |
| doubao-web | www.doubao.com | ambiguous | HTTP | ❌ 待实现 |
| qwen-web | chat.qwen.ai | avoid | HTTP | ❌ 待实现 |
| yuanbao-web | yuanbao.tencent.com | caution | HTTP | ❌ 待实现 |
| perplexity-web | www.perplexity.ai | avoid | HTTP | ❌ 待实现 |
| huggingchat | huggingface.co | caution | HTTP | ❌ 待实现 |
| lmarena | lmarena.ai | caution | HTTP | ❌ 待实现 |
| poe-web | poe.com | avoid | HTTP | ❌ 待实现 |
| v0-vercel-web | v0.dev | avoid | HTTP | ❌ 待实现 |
| blackbox-web | www.blackbox.ai | avoid | HTTP | ❌ 待实现 |
| muse-spark-web | www.meta.ai | avoid | SockJS | ❌ 待实现 |
| t3-web | t3.chat | avoid | HTTP | ❌ 待实现 |
| hailuo-web | hailuo.com | caution | HTTP | ❌ 待实现 |
| zai-web | chat.z.ai | caution | HTTP | ❌ 待实现 |
| venice-web | venice.ai | avoid | HTTP | ❌ 待实现 |
| notion-web | www.notion.so | avoid | HTTP | ❌ 待实现 |
| inner-ai | inner.ai | avoid | HTTP | ❌ 待实现 |
| adapta-web | adapta.org | avoid | HTTP | ❌ 待实现 |
| microsoft-designer-web | designer.microsoft.com | avoid | HTTP | ❌ 待实现 |
| copilot-m365-web | microsoft365.com | avoid | HTTP | ❌ 待实现 |

图例: 🟡 = 骨架已建 (返回 ErrNotImplemented); ❌ = 待实现

## 实现一个新 provider 的步骤

1. **抓包**: 打开目标站点 → 浏览器 DevTools → Network → 发一条聊天消息 →
   记录 bootstrap 端点 (session/token)、chat 端点、流式格式。
2. **建文件**: `domains/streaming/executors/webcookie/<provider>.go`,
   实现 `Adapter` 接口的 4 个方法。
3. **注册**: 在文件 `init()` 里 `DefaultRegistry.Register(&MyAdapter{})`。
4. **测试**: 用 mock server (httptest) 模拟站点响应, 单测 `Login`/`Chat`/`ParseStream`。
5. **ToS 标注**: 在 `free_resource_catalog` 确认该 provider 的 `tos_verdict`;
   若为 `avoid`, 确认它不会进默认 auto-combo 池。

## 参考

OmniRoute 的 TypeScript 实现位于 `~/workspace/ai/OmniRoute/open-sse/executors/*-web.ts`
(如 `chatgpt-web.ts` ~120KB, 含 TLS 指纹 + arkose + SockJS)。Go 实现需对齐其
协议逻辑, 但注意 OmniRoute 用 Node 的 `ws`/`sockjs-client`, Go 需用等效库
(如 `gorilla/websocket` 或裸 HTTP for SockJS)。
