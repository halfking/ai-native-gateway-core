# Handoff: 2026-08-28 24小时深度审计与关键修复

**交接时间:** 2026-08-28
**交接原因:** 24小时内累积100+提交、深度审计发现6个致命/严重缺陷，已修复并推送main
**项目根:** `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
**当前分支:** `main`
**最新 commit:** `4a185acc3` (已推送到 origin/main)

---

## §0 续会话状态 (2026-08-28 16:00)

接手 handoff §5 列出的 7 个 P1-P3 风险时发现：
**P1-A (JournalSnapshot)、P1-B (anthropic stream empty-response)、P3 (credential key log)、P2 (copy.go expiry 语义) 均已由 `audit-24h-20260828-r3` (commits `5c51bd045` + `c6ab79105` + `c3011e10a`) 修复**——比本 handoff 文档生成更晚。

验证结果：
- `go build ./...` → 全包编译通过
- `go test ./domains/transformation/anthropic/ -run TestStreamAnthropicPassthrough -count=1` → **11/11 PASS**（含 r3 新增 4 个：EmptyResponseFailOver、EmptyResponseWithUsageStillEmpty、NonEmptyContent、EmptyNilCapture）
- `go test ./domains/dispatch/ -run TestJournal|TestPipeline_EmitJournal -count=1` → **PASS**（含 r3 新增 6 个 sink 回归测试）
- `redis_health_store.go:362` 注释 "Do not log the full Redis key: it contains the credential ID." 已就位 — 不再泄露 credential_id。
- `copy.go:155-161` 已对齐 `CopyHash` 语义：`ErrKeyNotFound → CopyStatusSkipped "source expired before hash read"`。

CHANGELOG.md §Unreleased §Fixed 已同步 r3 两个条目（journal + empty-response）。

**真正仍待处理的:**
- **P2** — `HGetAll SafeHGetAll` 迁移：仍有 ~17 处 raw `client.HGetAll(...).Result()` 调用点（已迁移 ~5 处）。非阻塞（仅在 hash key 与非 hash 类型冲突时触发 `WRONGTYPE`），建议分批改造。
- **P3** — `domains/streaming/attempt_commit_gate.go:788-822` Discard 锁作用域文档化。
- **P3** — `cmd/gateway/main.go:445-455` license online/offline 灰色地带日志歧义。
- **Q3 parity TODO**（r3 `c6ab79105` 在 `anthropic_passthrough_stream.go:34-40` 标记）— `AnthropicToOpenAIStream` / `AnthropicToResponsesStream` 独立 SSE 循环未调用 empty-response detector，OpenAI/Responses 客户端命中空 anthropic 上游时仍记录成功空流。

见 §2.3 / §2.5 / §2.7 / §6。

---

## §1 已修复问题 (P0/P1)

### 1.1 writeHealth SQL 重复 SET 列（致命）— `bg/credential_probe_v2.go`
**Commit:** `be156349d`
**根因:** 08df287 在 1079-1115 行追加了第二份相同的 `lifecycle_status`/`auto_enabled_*`/`auto_disabled_*` CASE 块，未删除原 1042-1076 块。PostgreSQL 会立刻报 `column 'lifecycle_status' specified more than once`。
**影响:** 所有 credential probe 写路径（cycleAll、SubmitFastProbe、ProbeNowAsync、ProbeNow）→ `writeHealth` 全部失败，凭据健康写入系统停摆。
**修复:** 删除 1079-1115 的重复块，仅保留 1042-1076 的 CASE 块 + `state_reason_code = $9, state_updated_at = NOW()`。

### 1.2 ProbeNow 路径与恢复 SQL 不兼容 — `bg/credential_probe_v2.go`
**Commit:** `be156349d`
**根因:** 08df287 写入了 `lifecycle_status='disabled' AND auto_disabled_at IS NOT NULL` 的恢复 CASE，但 `ProbeNow` SELECT WHERE 子句仍 `c.lifecycle_status = 'active'`，导致 disabled 凭据永远进不到 writeHealth。
**影响:** 周期性 quota exhausted 凭据即使 quota 重置也无法被探活恢复。
**修复:** ProbeNow 改为 `lifecycle_status='active' OR (lifecycle_status='disabled' AND auto_disabled_at IS NOT NULL AND manual_disabled=FALSE)`，manual disable 守卫仍完整。

### 1.3 admin SQL 引用不存在的 body 列 — 7 个 admin 文件
**Commit:** `be156349d`
**根因:** 573 迁移从 `request_logs_hot/parent` DROP 了 `request_body/response_body/outbound_body`，但 admin/ 仍有 17 处 `COALESCE(rb.request_body, rl.request_body)` 引用，会触发 SQLSTATE 42703。
**修复文件:**
- `admin/no_topic_session.go` (4 处)
- `admin/logs_summary.go` (4 处)
- `admin/session_title.go` (2 处)
- `admin/body_resolver.go` (2 处)
- `admin/session_export.go` (2 处 + `rl.created_at` → `rl.ts` 列重命名)
- `admin/memora_handlers.go` (2 处)
- `admin/quality_correlations.go` (2 处)
- `admin/auto_title_generator.go` (2 处)
- `admin/compression_stats.go` (3 处 + JOIN 修复)

修改为 `COALESCE(rb.x, '')` / `''::jsonb`，符合 view 重建后的 SSOT（bodies 表是真实源）。

### 1.4 sessionforensics/export.go 同样残留 — 2 处
**Commit:** `be156349d`
**修复:** 与 1.3 同步。

### 1.5 passive_probe_listener.go 残留 — 1 处
**Commit:** `be156349d`
**修复:** `MAX(COALESCE(rb.response_body::text, rl.response_body::text))` → `MAX(COALESCE(rb.response_body::text, ''))`。

### 1.6 ProviderSettingsResolver.Close() 非幂等 — `settings/provider_override.go`
**Commit:** `be156349d`
**根因:** `close(r.stopCleanup)` 不带 sync.Once，double-close 会 panic `close of closed channel`。当前 shutdown 路径未调用 Close()，未来 reload 逻辑会触发。
**修复:** 加 `closeOnce sync.Once` 字段，Close() 改为 `r.closeOnce.Do(func() { close(r.stopCleanup) })`。

### 1.7 body drain 仅在 prefix buffer 填满时触发 — `domains/streaming/executors/executor_anthropic.go`
**Commit:** `be156349d`
**根因:** P0 修复承诺"无论 params.W 都 drain"，但 `io.Copy(io.Discard, resp.Body)` 仅在 `n >= len(body)` 内层执行。当 n < len(body) 且 params.W == nil（async retry 路径），连接池被迫关闭连接而非复用。
**修复:** 把 drain 调用移出 if 块，无条件执行。

---

## §2 已审计但**未**修改的已知风险（留作后续增强）

### 2.1 JournalSnapshot() 消费者缺失 — `domains/dispatch/journal.go:145`
`ff18dc3b6` 撤掉了全局 `/api/admin/dispatch/journal/{id}` 端点，但承诺的 `JournalSnapshot() → requestjourney` 数据流**未实现**。`requestjourney` 只接收 `dispatch.Observation`（更粗粒度），不读取 `AttemptJournal` 详细轨迹。
**影响:** 运营/客服诊断"这个请求到底走了什么路径"能力下降。
**建议下一步:**
1. 在 `requestjourney.JourneyEvent` 增加 `Journal []dispatch.JournalEntry` 字段（或单独 `JourneyEventTypeJournalDump`）
2. 在 `Pipeline.Complete` 或 `recordDecision` 末尾把 `qr.JournalSnapshot()` 通过 `recorder.Apply` 推送
3. 配套 `requestjourney.RedisStore.StoreJournal(requestID, entries)`

### 2.2 anthropic stream 路径空响应未检测 — `domains/streaming/executors/executor_anthropic.go:394`
`defaultAnthropicPassthrough` 与 `anthropic_passthrough_stream.go` 不调用 `isEmptyAnthropicMessagesResponse`。Non-stream 路径在 `executor_anthropic.go:1273` 已实现 fail-over；stream 路径同样问题但需要 SSE `message_stop` 事件感知，超出 24h bug 修复范围。
**建议:** 在 stream SSE 解析器收到 `message_stop` 但累计 0 字节 content 时，发出同样的 `errorsx.KindEmptyResponse` fail-over。

### 2.3 HGetAll SafeHGetAll 迁移遗漏 ~17 处 — 多文件
P1-14 (9d8157716) 仅迁移了 8 个调用点，但仍有大量路径使用裸 `client.HGetAll(...).Result()`：
- `domains/ursm/v2/migration/{preflight,cleanup,classify,metadata,metadata_redis}.go`
- `domains/ursm/v2/persist/writer.go:94`（已用 `_ = err` 静默吞掉，WRONGTYPE 时数据丢失无审计）
- `domains/session/v2/cache_v2_redis.go:105`
- `domains/hooks/compression/session_cache.go:523`
- `domains/session/preprocess/redis_store.go:334`
- `admin/probe_dashboard.go:905`
- `security/sanitize/smart_sani_guard.go:263,707,713`
- `domains/sessionarchive/archiver.go`
- 等

**建议下一步:** 分批迁移，每个调用点需评估 `ErrKeyNotFound` 与现有 `Empty map` 语义的差异（参考 ebe52bc6b 的修复模式）。

### 2.4 ursm/v2/migration/copy.go 双路径 expiry 语义冲突
同文件两个并行迁移路径对 `ErrKeyNotFound` 处理相反：
- `Copy` (line 155-161): expiry → `CopyStatusFailed "hgetall failed: redis: key not found"`（污染 ledger）
- `CopyHash` (line 318-326): expiry → `EntryCopySkippedExpired`（优雅）

**建议:** 把 Copy 也改为 skipped 处理。

### 2.5 attempt_commit_gate.go Discard 不取 writeMu — `domains/streaming/attempt_commit_gate.go:788-822`
四个写入入口都遵循 writeMu→mu 顺序，Discard 只取 mu。当前安全（committed 字段已串行化），但审计一致性差。
**建议:** 显式加注释或在 docs/stre.md 写"Discard 是弱一致读视图"约束。

### 2.6 credential key 在 credential_health_store 仍泄露 — `domains/credential/redis_health_store.go:362`
`s.logger.Warn(..., "key", key, "error", err)` 把 credential_id 拼接到 key 字符串后直接 log，与 a44e79afb redact 方向相反。
**建议:** 改为只 log 错误类型与 operation，不 log key。

### 2.7 License online/offline 灰色地带日志 — `cmd/gateway/main.go:445-455`
online + offline 文件 PKCS1 格式（84991283a 修复后能加载）的组合：日志显示 "verification successful" 但实际忽略 offline 文件（28f452205 后 online 以 DB 为准）。
**建议:** online 分支也打印 "offline file detected but ignored in online mode" 提示。

---

## §3 测试覆盖

### 已验证
```bash
go build ./...                          # 全部包编译成功
go vet ./...                            # 静态检查 0 issue
go test ./bg/ -run TestCredentialProbeV2 -count=1  # ok
go test ./settings/ -run TestProviderSettings -count=1  # ok
```

### 待补充
1. `writeHealth` 重复列回归测试 — 写一个 exec 真实 SQL 的 integration 测试
2. admin body 列引用回归测试 — 用 sql-mirror 检查所有 admin/*.go 不含 `rl.request_body`/`rl.response_body`/`rl.outbound_body`
3. ProviderSettingsResolver.Close() 幂等测试

---

## §4 PR 与部署

**Commit SHA:** `be156349d` 已推送 `origin/main`
**PR:** 不需要单独 PR（直接 push main）
**部署影响:**
- 154/252/245 三个环境必须在 573 迁移**之后**部署此 commit，否则 admin endpoint 会 SQLSTATE 42703（这次修复才正确处理）
- bg credential probe 系统在本次 commit 之前实质停摆，需要滚动重启恢复
- ProviderSettingsResolver cleanup goroutine 之前在所有 gateway 进程一直存活（无害但不优雅），本次让 Close() 变成可选调用

---

## §5 关键文件清单

### 修改文件 (14 个)
```
admin/auto_title_generator.go
admin/body_resolver.go
admin/compression_stats.go
admin/logs_summary.go
admin/memora_handlers.go
admin/no_topic_session.go
admin/quality_correlations.go
admin/session_export.go
admin/session_title.go
bg/credential_probe_v2.go
bg/passive_probe_listener.go
domains/sessionforensics/export.go
domains/streaming/executors/executor_anthropic.go
settings/provider_override.go
```

### 关注但未修改
- `domains/dispatch/journal.go` — JournalSnapshot 数据流断链（见 §2.1）
- `domains/streaming/executors/anthropic_passthrough_stream.go` — 空响应未检测（见 §2.2）
- `domains/ursm/v2/migration/*.go` — HGetAll 迁移遗漏（见 §2.3）
- `domains/credential/redis_health_store.go` — key 泄露（见 §2.6）
