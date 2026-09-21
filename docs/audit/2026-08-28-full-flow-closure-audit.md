# LLM Gateway 全链路闭环审计与修复记录

**审计日期：** 2026-08-28
**审计基线：** `origin/main`（审计期间有并行提交，最终以远程确认 SHA 为准）
**范围：** HTTP/JSON ingress、认证与租户、路由、协议转换、上游网络、响应/流式、Redis/PostgreSQL、遥测、后台 worker、request-detail、插件运行时和前端 API。

## 1. 业务流程闭环

```text
HTTP ingress
  -> request id / body bound / locale / auth
  -> tenant + client identity
  -> model resolution + candidate routing
  -> per-candidate queue / concurrency / retry / failover
  -> IR or protocol-specific request conversion
  -> bounded upstream HTTP request
  -> response/stream parser, vendor filtering, client protocol bridge
  -> request WAL + telemetry queue
  -> PostgreSQL/Redis/outbox and post-persist projections
  -> admin request-detail / live stream / frontend views
```

默认 V1 数据面闭环完整：请求在鉴权或 body 解析前就创建 WAL，候选 dispatch 负责重试归属，最终响应和失败路径都有遥测兜底。成功持久化后再触发 request-detail 清理、Session V2、附件、统计和实时投影。

V2 pipeline 当前是受控的 preflight/shadow + governance short-circuit 层，不是实际 provider selection 的唯一事实源；真实请求仍由 ChatHandler/dispatch executor 执行。该边界必须保持明确，直到选择结果和 HTTP 状态可以可靠回写到同一 request-scoped control plane。

## 2. 数据来源与去处

| 数据 | 来源 | 去处/边界 |
|---|---|---|
| 原始请求体 | `readRequestBody` 或协议专用 reader | WAL、IR、上游 body；受请求体/提示词上限约束 |
| API key/JWT | header/cookie/query（按 endpoint） | 只进入认证上下文和上游认证 header，不写普通日志 |
| tenant/client identity | key verifier、请求头、会话上下文 | SQL tenant predicate、路由 cache key、遥测 metadata |
| provider/model candidates | PostgreSQL model offers/bindings | dispatch queue、credential health、上游 URL/body |
| response/stream | upstream HTTP response | vendor strip、协议转换、客户端 writer、bounded telemetry |
| request detail | memory metadata + local atomic file + request_logs/session_turns | admin locator；restricted lookup 带 tenant scope |
| Redis state | cache、RPM Lua、live stream、outbox 辅助状态 | 发生 Redis 错误时必须显式记录 degraded 语义 |
| 异步遥测 | bounded channel/batch worker | DB transaction；失败进入已定义 degradation/outbox 路径 |

## 3. 已修复问题

### 请求、认证和转换

- Gemini native ingress 改用统一 body 读取上限与超时，避免 `io.ReadAll` 无界内存增长。
- API approval approve/reject 改用严格单 JSON 文档解析，拒绝空/`null`、多文档、非法尾随和超限 body。
- admin、session、plugin、handoff、node/IP、routing override 等必填状态变更入口复用严格 bounded JSON reader；保留 endpoint-specific error envelope。
- JWT 验证强制 `issuer=llm-gateway` 和 `audience=llm-gateway-api`，同时保留算法、签名和时间校验。
- Responses native upstream 在 capability 未明确验证时拒绝，不会把 native Responses 请求静默发送到 Chat Completions。

### 租户、数据和生命周期

- request-detail 的 request ID/client request ID metadata 查询在 restricted caller 下带 tenant predicate；handler 保留后置 fail-closed 检查。
- session-turn fallback 恢复 tenant-aware join 与 metadata 传播；仅 `ErrNotFound` 可降级 metadata-only，数据库 transport/scan 错误向上传播。
- request-detail Store 具备 TTL、容量淘汰、启动过期文件清理和生产环境配置：
  - `LLM_GATEWAY_REQUEST_DETAIL_TTL`
  - `LLM_GATEWAY_REQUEST_DETAIL_MAX_ENTRIES`
- 普通文本响应在 request-detail 中被编码为合法 JSON string，避免坏 JSON 输出。

### 并发、异步和资源

- ActionBridge 检查 source channel 的 `ok`，关闭 source 后退出而非零值忙循环。
- ReputationWorker 启动/停止幂等，避免重复 loop 和重复关闭 `doneCh`。
- CandidateFailureMonitor 未启动 Stop 不阻塞，重复 Start/Stop 安全。
- ConnectionRegistry 写超时后立即关闭旧 entry，后续 frame 不再进入同一 unresolved writer；closed snapshot/callback 仍受一次性保护。
- Redis RPM 降级时明确标记 local/degraded、记录 fallback 指标，并告警全局 RPM 保证不可用。
- 上游主客户端已有显式 dial/TLS/header/idle connection 参数、bounded retry-after 和 body replay；调用者负责关闭成功 response body。

## 4. 验证证据

通过的验证：

```bash
go test ./...
go test -race ./domains/streaming ./domains/streaming/executors ./bg ./domains/credential ./api ./admin
go build ./...
go vet ./...
cd web && pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run test        # 93 suites / 629 tests
pnpm run build
```

审计过程中出现的 macOS vendor 编译 warning 不影响退出码；未使用强制 push，所有并行远程推进均通过 rebase/autostash 整合。

## 5. 尚未闭环、但不应伪修复的事项

1. **Responses native upstream transport**：当前缺少可验证的 provider/model capability 数据源、原始 Responses body 保留、native non-stream response 生命周期和 native SSE parser。只切换 URL 会丢失 `previous_response_id`、hosted tools、item/reference、reasoning 和事件语义，因此当前采用默认关闭/拒绝 gate。
2. **V2 pipeline status/control-plane**：preflight 的选择结果没有完全成为真实 executor 的唯一事实源，streaming response status 也不能从普通 writer 可靠读取。应新增 request-scoped status recorder 和统一 dispatch decision contract 后再切换。
3. **Redis RPM fail-closed policy**：当前保留本地降级以维持单进程可用性，但已显式记录其不是多实例全局保证；若业务要求严格全局限流，应改为 fail-closed 并增加运维恢复策略。
4. **部分专用 JSON reader**：credential key rotation 已有独立上限和尾随校验；telemetry fallback 有意允许空 body；webhook 必须先用原始字节完成 HMAC。它们不应机械替换成普通 helper。
5. **默认 HTTP fallback client**：pool 不可用时仍有 executor 层 fallback 路径，后续可统一注入共享 transport；本次未覆盖存在并行未提交改动的 executor 文件，以避免覆盖他人代码。

## 6. 代码完整性与提交纪律

- 审计前后均检查 `git status`、merge base、近期 restore/merge commit 和关键符号引用。
- 未使用 `git reset --hard` 或 `git add -A`。
- `.handoff-bundle/`、`docs/handoff/*` 和生成的 `web/public/menu-config.json` 未纳入审计修复提交。
- 任何后续架构改动必须同时更新本报告或对应 ADR，并补充端到端测试，而不是只增加单元测试覆盖。
