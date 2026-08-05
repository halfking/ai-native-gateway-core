# 2026-08-05 — 标题生成隔离到低优先级模型池 + 防链式自触发

## 变更摘要

apiclaude 事故（标题请求走 claude-opus-5/provider 587 relay，两条卡死 in_progress）后续加固的前两项：

- **模型隔离（a）**：标题生成不再 `model:"auto"`（V2 decider 可能选到用户的昂贵 relay），改为 pin 到 `work_type_model_route` 的 session_title 便宜池（minimax-m2.7 等），同时完全绕过 decider，标题请求不再占用用户热路径。
- **防链式自触发（b）**：标题生成触发点排除网关内部自动请求；补上入口侧读取 `X-Gw-Is-Auto` header 的缺口（此前从不读取，`is_auto_request` 日志全 NULL），使标题请求被正确标记为内部请求。

## 触发原因

| 项 | 现状 (154 事故窗口实测) |
|---|---|
| **标题请求模型** | 4 次标题生成 3/4 次被 V2 decider 选到 `claude-opus-5` / provider 587 / cred 17 relay（与主任务同一坏链路），其中 1 次卡死 in_progress |
| **payload 极小无关** | 标题请求 body 仅 ~1.1-1.4KB，仍重新走 V2 auto-route，不是必然选便宜模型 |
| **链式自触发风险** | 标题请求自身满足 `Success && GwSessionID != ""`，会在新隔离 session 内再次触发标题生成 |
| **is_auto_request 缺口** | 发起方设置 `X-Gw-Is-Auto: true`，但入口侧无代码读取该 header 写 logCtx，日志该字段全为 NULL |

## 改动清单

| 文件 | 改动 | 行为 |
|------|------|------|
| `admin/auto_title_generator.go` | 改 | 新增 `resolveAutoTitleModel()`：pin 到 session_title 便宜池（复用 `resolveAdminLLMFallbackModel`，env `LLM_GATEWAY_ADMIN_LLM_FALLBACK_MODEL` 可覆盖；池空回退 `auto`）。`callAutoTitleLLM` 由 `model:"auto"` 改为该 model，`resolvedModel` 同步 |
| `domains/streaming/handler.go` | 改 | 入口读取 `X-Gw-Is-Auto: true` → `logCtx.IsAutoRequest = true`；标题生成触发点加 `!shouldSkipAutoTitleGeneration(logCtx)` 排除；新增该谓词函数 |
| `domains/streaming/auto_route.go` | 改 | 新增常量 `autoIsAutoHeader = "X-Gw-Is-Auto"` |
| `admin/auto_title_generator_test.go` | 改 | 新增 `TestResolveAutoTitleModel`（nil handler→auto / 无 db→minimax-m2.7 / env override） |
| `domains/streaming/handler_test.go` | 改 | 新增 `TestShouldSkipAutoTitleGeneration`（nil/正常→false，auto→true） |

## 验证

- `go build ./...` ✅ / `go vet ./domains/streaming/... ./admin/...` ✅
- `go test ./admin/ ./domains/streaming/` 全绿（含新增 2 用例）
- 全仓 `go test ./...`：仅 `autoupdate` / `domains/providerprofile` 的 DB 集成测试因测试库表权限缺失失败，与本改动无关

## 遗留与风险

- 标题生成 pin 到 minimax-m2.7 后若不在租户 model policy 白名单 → 403 → 回退本地 auto-extract 标题（标题仍生成，仅不经 LLM），部署后需在 245 实测确认。
- `model:"auto"` 的普通用户请求现在也被排除出标题生成（auto 请求不再自动产标题），合理 tradeoff。
