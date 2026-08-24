# 2026-08-24 数据面 sk-key 被全局静态门拦截修复（Invalid or expired API key 间歇性 401 事故）

## 背景

用户报告网关"经常出现 Invalid or expired API key"。经排查（nginx 252 入口日志 + 双实例交叉实验）定位为**认证架构缺陷**，非单点故障。

## 根因

1. `/v1/*` 数据面请求的第一关是全局静态门（`middleware/auth_mw.go`），它对 `Authorization: Bearer <token>` 与 `LLM_GATEWAY_API_KEY` 做 constant-time 比对，不匹配直接 401 `invalid_key`（文案与 DB 验证失败完全相同，无法从响应区分）。
2. 生产 154 与备机 245 的 `LLM_GATEWAY_API_KEY` 配置了**不同的值，且各自复用了一把用户 sk-key**（154=key89/sk-RZ8…，245=key105/sk-jyb…）。任何"另一台的全局 key"、以及其他所有 DB 合法 key，在静态门即被拒。
3. nginx upstream：154 primary / 245 backup（`max_fails=2`）。154 每次部署重启的优雅关闭窗口（约 90s）流量切到 245，持有 key89 的用户在该窗口内全部 401 —— 与用户 20:29:54/56 的 401（154 于 20:29:42-20:31:12 重启）精确吻合；持有 key105 的用户则相反（平时 401、仅切换窗口 200）。
4. 证据链：nginx 252 24h 内 96,805 次 `/api/*` 401（Cursor 会话过期轮询，另案）与 41 次 `/v1/*` 401；交叉实验"key105 → 245 得 200 / → 154 得 401"实锤。

## 修复

- **middleware/auth_mw.go**：静态门放行 `sk-` 前缀的 Bearer，交给 `domains/authentication.KeyVerifier` 做 DB 校验（key_hash + enabled + status + expires_at）。静态 key 仅继续保护非 sk- 内部调用（部署探测、self-check 等）。对齐 rule 20 §2（数据面只认 sk-*，由 DB verifier 判定）。
- **domains/streaming/models.go + cmd/gateway/main.go**：`/v1/models` 此前无自身认证（靠静态门"恰好"保护，sk-* 放行后暴露缺口），补接 `SetKeyVerifier` 与其他 /v1 端点一致的验证。
- **middleware/auth_mw_test.go**：新增 `TestAuthMiddleware_PassesSkKeysToDBVerifier`（sk-* 放行且不打 global-auth 标记）。

## 运维配套（待办，需人工）

- 建议将 154/245 的 `LLM_GATEWAY_API_KEY` 从"用户 sk-key"更换为独立的非 `sk-` 前缀 ops key 并保持两台一致（当前代码修复后已不影响数据面，但静态门 key 仍是 llmgo 健康探测等内部调用的凭据）。
- 96k 次 `/api/*` 401 来自挂着的管理面板（Cursor 内嵌浏览器无 cookie）持续轮询：`web/src/composables/usePolling.ts` 吞错不停止，401 后仍按间隔轮询，建议后续在 401 时停止轮询。
- 认证失败（401）不写 gateway.log / request_logs，形成监控盲区，建议后续补充结构化日志。

## 验证

- 单测：`go test ./middleware/ ./domains/authentication/ ./domains/streaming/ ./admin/` 全绿。
- 245（seq 1723/1725）实测：key89 → 245 models 200（修复前 401）；bogus sk- 401；无 auth 401。
- 154（seq 1726）实测：key105 → 154 models/chat 200（修复前 401）；bogus sk- 401。
- 端到端：`https://llm.kxpms.cn/v1/chat/completions`（经 252 nginx → 154）用 key105 真实调用 glm-4-flash 返回正常补全。

## 影响

- 修复后两实例各自配置的全局 key 不再影响数据面互认；154 重启窗口切 245 时用户 key 仍可通过 DB 验证（前提 DB verifier 可用）。
- 若 DB verifier 未启用（DB 不可用），sk-* 请求按既有 fail-open 语义放行（历史行为，未在本次改变）。
