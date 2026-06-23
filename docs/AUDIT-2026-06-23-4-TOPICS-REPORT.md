# llm-gateway-go 4 主题深度审计报告

**审计日期**：2026-06-23  
**审计模式**：Plan Mode（只读）  
**审计范围**：services/llm-gateway-go/（HEAD `1c96a587`，working tree 有 1 个无关 dirty 文件）

---

## 一、目标 1：并发与槽位管理

### 1.1 当前状态（事实陈述）

**核心组件**：
- `credentialfpslot/slot.go`（833 行）：Manager + 3 个 Lua 脚本（acquire/release/forceUnpin）+ memory fallback
- `credentialfpslot/reclaim.go`（244 行）：后台回收 goroutine
- `routing/sticky.go`（180 行）：进程内 StickyCache + DB sticky_sessions 持久化

**TTL 常量**（slot.go:19-22）：
```go
slotTTLSeconds    = 1800  // 30 min
sessionPinTTLSeconds = 86400  // 24 h
```

**Reclaim 接入状态**（cmd/gateway/main.go）：
- 创建 Manager（L221-224）：✅
- 启动 reclaimLoopStart：**❌ 零调用**

**Redis key 命名空间**（slot.go:162-168）：
```
llmgw:cred_fp_slot:{cred}:{slot}      # slot TTL
llmgw:sess_cred_fp:{holder}:{cred}    # pin (24h)
```
**注意**：不含 tenantID。

**StickyKey 设计**（sticky.go:156-180）：
```go
BuildClientStickyKey(tenant, app, key, profile, model)
// 格式：{tenant}:{app}:{key}:{profile}:{model}
// 排除：session_id 和 fpSeed → 同 client 多次会话 = 1 个 stickyKey
```

### 1.2 改进方向（具体可执行）

| 优先级 | 改动 | 文件 + 行号 |
|--------|------|-------------|
| **P0** | 启用 reclaim goroutine | `cmd/gateway/main.go` L221 之后加 `fpSlots.reclaimLoopStart(ctx, defaultReclaimConfig())` |
| **P0** | 修文档/注释错位（24h vs 15min 颠倒） | slot.go:99, slot.go:209, 040 migration:25, slot_fingerprint_reuse_test.go:52 |
| **P0** | hasPin memory 模式加过期检查 | slot.go:497-500 加 `if pin.exp.Before(now) { delete; return false }` |
| **P1** | 把 tenantID 注入 fp_slot key | slot.go:162-168 改 `{tenant}:{cred}:{slot}` 格式；executor.go:703 用 `params.TenantID` 替代 `"default"` |
| **P1** | holder fallback 用 client identity hash（非 X-Request-Id） | executor.go:631-633 |
| **P2** | 合并 acquireRedis 的 2 次 round-trip | slot.go:505+509 合并到一个 Lua |
| **P3** | releaseSlotScript 返回 false 时升级 slog.Warn | slot.go:275-277 |
| **P3** | 删除/接入 IdentityPool | executor.go:582-587 |

---

## 二、目标 2：Anthropic↔OpenAI tools 转换（opus-4 vs sonnet-4）

### 2.1 当前状态（事实陈述）

**核心代码**：
- `internal/ir/stream.go` `ParseAnthropicStreamEvent` (L244-417)
- `relay/anthropic_to_openai_stream.go` `StreamAnthropicSSEToOpenAI` (L47-462)
- `relay/anthropic_to_chat.go` `ConvertAnthropicResponseToChat` (L17-164)

**已知 opus-4 vs sonnet-4 差异**：
1. Extended Thinking 频率 + 长度
2. thinking block 强制要求 `signature` 字段往返
3. thinking + tool_use 混合顺序
4. max_tokens 截断 input_json 的概率

**关键发现**：**`thinking` 块的 `signature` 字段在 IR 层完全丢弃**
- parse_anthropic.go:311 只存 Thinking.Thinking 字符串
- types.go ThinkingBlock 无 Signature 字段
- stream.go content_block_start 只处理 tool_use（line 313 if），跳过 thinking
- anthropic_to_chat.go:30 解析 Signature 但**全程未使用**

**Fixture 缺失**：
- 12 个流式 tool_use 测试 fixture 全部是 text-then-tool_use 或纯 tool_use
- **无任何** thinking + tool_use 混合 fixture

### 2.2 改进方向

| 优先级 | 改动 | 文件 + 行号 |
|--------|------|-------------|
| **P0** | IR 层加 Signature 字段（ThinkingBlock、ResponseContentBlock、StreamChunk） | types.go:221-223, parse_anthropic.go:311-318, response.go:138-143, stream.go:284-317 |
| **P0** | 流式 content_block_start 处理 thinking 类型 | stream.go:284-317 |
| **P0** | content_block_delta 加 `signature_delta` 分支 | stream.go:318-348 |
| **P1** | 新增 thinking + tool_use 混合 fixture | 新建 `relay/anthropic_to_openai_stream_thinking_tool_test.go`，用 claude-opus-4-8 作 model |
| **P1** | 新增 max_tokens 截断 input_json fixture | 模拟 stop_reason=max_tokens + 不完整 JSON |
| **P2** | 去除同一份 SSE data 3 次解析 | anthropic_to_openai_stream.go:265, 305-308, 312-329 选其一 |
| **P2** | IR StreamChunk Index 语义化 | stream.go:303, 342 文档化为"上游 content_block index" |
| **P3** | default 分支对 unknown delta 优先入 reasoning | anthropic_to_openai_stream.go:363-379 |

---

## 三、目标 3：凭据+模型状态管理与回测

### 3.1 当前状态（事实陈述）

**3 层状态机**：

```
Layer A: credentials.availability_state  (PG 共享)
   ready ↔ cooling / rate_limited / unreachable
   auth_failed → 永久 / suspended → 永久
   quota_periodic_exhausted → quota_recover_at 到期

Layer B: credentials.circuit_state  (in-memory, **每节点独立**)
   closed ↔ open ↔ half_open
   → quarantined (永久, 仅 admin Reset)

Layer C: credential_model_bindings.available  (PG 共享，路由 source of truth)
   TRUE ↔ FALSE
   FALSE → unavailable_at 到期恢复
```

**6 个时间维度并存**：
| 维度 | 实现 | 窗口 | 阈值 |
|------|------|------|------|
| W1 Redis 滑窗 | recorder.go LPUSH+LTRIM | 100 条 / 2h | 记录 |
| W2 Checker | checker.go | 1h | 80% + min 5 样本 |
| W3 Tuner | tuner.go OnError | per-call | Concurrent 削 10% |
| W4 Circuit | breaker.go consecutive | 同类连续 | auto=3, exp=2 |
| W5 Model Probe | model_probe.go 共识 | consecutive | 3 次成功/失败 |
| W6 SQL recent_success_rate | db.go 035 migration | 50 sample × 3h | < 0.3 (TEMPORARY) |
| W7 Auto-Cool | candidate_failure_monitor.go | 5min | 80% + 20+ 样本 |

**端到端延迟**：
- 同节点：circuit (μs) + cmb 写 (10ms) + invalidate 候选 (μs) = < 30s
- 跨节点 71↔184：**0~30s**（candCache TTL）
- circuit.Manager **每节点独立**（无 Redis 同步）

**回测机制**：
- `credential_recovery.go` 60s 周期：5 个串行 UPDATE 恢复 availability_state / quota / circuit / consecutive / health
- `health_auto_recover.go` 1min 周期：恢复 cmb + model_offers
- `credential_probe_v2.go` 1h 真实探测（GET /v1/models + mini chat "hi"）+ 5min fast-reprobe（auth_failed/unreachable）
- `model_probe.go` 10min 共识探测（只探非 routable），backoff 1m/5m/15m/60m

### 3.2 改进方向

| 优先级 | 改动 | 文件 + 行号 |
|--------|------|-------------|
| **P0** | 移除 dead `writeModelLevelFailure`（旧版 2026-06-22 cross-model polluter） | writer.go:296-379，所有调用改 `writeModelLevelFailureOnly` |
| **P0** | credential_recovery.recover() 也清 cmb 原因（防"cmb=TRUE 但 availability=cooling"假阴） | credential_recovery.go:52-192 |
| **P0** | recent_success_rate 阈值从 0.3 恢复 0.5（注释为 TEMPORARY） | provider/client.go:680 + 291 migration |
| **P1** | 跨节点状态同步：Redis pub/sub broadcast InvalidateAllCandidateCache | 新建 `bg/credential_cache_pubsub.go` |
| **P1** | circuit.Manager 同步到 Redis | circuit/breaker.go Manager 加 Redis write/read |
| **P1** | KindNetwork 加入 checker 失败率（当前被排除） | checker.go:90 |
| **P2** | model_probe_state 加 tenant_id 列 | bg/model_probe.go:320 + 新 migration |
| **P2** | credential_recovery.recover() 5 个 UPDATE 并行化 | credential_recovery.go:52-192 |
| **P3** | candidate pool 大小 metric（minimax-m3 事件暴露痛点） | 新建 Prometheus metric |

---

## 四、目标 4：路由决策记录 + 请求/错误/统计/可用性

### 4.1 当前状态（事实陈述）

**4 层决策 trace**：

```
L1 audit.Event (in-memory, 17 字段, 含 DecisionTrace any)
  ↓
L2 Trace (PlannedCandidates / BlockedCandidates / Chosen / FailureReason)
  ↓
L3 routing_decision_log 表 (PG, JSONB decision_trace)
  ↓
L4 request_logs 行 (failure_detail_code / failure_stage / error_kind)
```

**19 个 ErrorKind 端到端处理**：
- 9 种 retryable（transient/timeout/network/rate_limit/upstream_down/concurrent/stream_timeout...）
- 5 种 non-retryable fatal（auth/auth_revoked/quota 系列）
- 5 种 IsClientBug（model_not_found/tool_call_id_mismatch/unsupported_feature/canceled/model_not_found）
- 1 种特殊（context_length → trim+retry）

**Latency 测量**：
- end-to-end LatencyMs：整个请求
- stream_first_chunk_ms（TTFB）
- stream_chunk_count
- upstream_latency_ms（per-attempt）：executor_chat.go:354-366
- health_latency_ms（probe）：仅 v2 探测
- **per-attempt latency in candidate_failure_logs**：❌ 缺失（executor.go:1038 注释 `// latency_ms: per-attempt latency isn't tracked here`）

**request_id 生命周期**：
- middleware/requestid_mw.go:23-28 → X-Request-Id header
- ✅ request_logs / candidate_failure_logs / routing_decision_log 三表共享
- ❌ health_tracker.go Redis callhist entry 用本地 `req_%d_%d`（不可关联回 request_logs）

**错误聚合 SQL 函数**：
- recent_success_rate(cred, model, sample_n=50, window_h=3) — 291 migration
- model_probe_backoff(consensus_failures) — 038/039 migrations
- 5 dashboard 端点：listCandidateFailures / getCandidateFailuresByCredential / getCandidateFailureStats / listRecentAlerts / HandleCredentialSuccessRates

### 4.2 改进方向

| 优先级 | 改动 | 文件 + 行号 |
|--------|------|-------------|
| **P0** | candidate_failure_logs 加 per-attempt latency | executor.go:1038（注释位置） + candidate_failure_logger.go + 加 migration 加列 |
| **P0** | health_tracker callhist entry 用真 request_id | executor.go:804, 1049 |
| **P1** | recent_success_rate window 改 24h（覆盖更长历史） | 291 migration + provider/client.go:651 |
| **P1** | vendor response body 脱敏（cap 200 char + redact PII） | executor_chat.go:471 等位置的 slog.Warn |
| **P2** | tenant_id 维度加入 dashboard 聚合 | admin/candidate_failure_handlers.go:218-234 + 新增 tenant filter |
| **P2** | 错误按 (provider, model, error_kind, time_bucket) pre-aggregated material view | 新建 migration + DB view |
| **P2** | cross-tenant 数据隔离 R40 audit 补全（所有状态字段加 tenant_id） | 见目标 3 改进 |
| **P3** | request_logs.request_body 强制脱敏（OAuth token, API key, PII） | telemetry/client.go:382-390 sanitizeRequestLogEntry |
| **P3** | 流式 first-byte timeout chunk-level observability | stream.go:176 + audit StreamCapture |

---

## 五、跨主题的关联发现

| 发现 | 关联主题 | 跨主题影响 |
|------|----------|-----------|
| **circuit.Manager 不跨节点同步** | T1+T3 | 71 熔断开 → 184 还认为健康 → 跨节点 0~30s 内仍会尝试 → 失败累积 → cmb 写入 → 触发 candidate_failure_monitor auto-cool |
| **recent_success_rate 阈值 0.3 是 TEMPORARY** | T3+T4 | 资源泄漏修复后必须恢复 0.5，否则 degraded credential 永远恢复 |
| **Thinking signature 丢失** | T2 | 多轮 round-trip 必然 400（Anthropic 要求带 signature），但 OpenAI client 不会触发此路径 |
| **fp_slot key 不含 tenantID** | T1+T3+R40 | 多租户下 credential 跨租户撞 pin |

---

## 六、立即可执行的 4 主题优先级矩阵

| 主题 | P0 必做 | P1 重要 | P2 中期 | P3 长期 |
|------|---------|---------|---------|---------|
| **T1 并发/槽位** | 启用 reclaim、修 TTL 注释、hasPin 过期 | tenantID 注入、identity fallback | 合并 Redis round-trip | releaseSlotScript warn |
| **T2 tools 转换** | IR 加 Signature、流式 thinking_start/delta | thinking+tool_use fixture、max_tokens fixture | 去重 SSE 解析、Index 语义化 | unknown delta 优先级 |
| **T3 状态管理** | 删 dead code、恢复阈值、修假阴 | Redis pub/sub、KindNetwork 进 checker | tenant_id 补全、UPDATE 并行化 | pool size metric |
| **T4 路由/请求** | per-attempt latency、真 request_id | window 24h、body 脱敏 | tenant filter、material view | first-byte observability |

**P0 总数**：11 个改动  
**P1 总数**：10 个改动  
**P2 总数**：8 个改动  
**P3 总数**：4 个改动

---

## 七、推荐实施顺序（基于风险×价值）

1. **第一批（P0，单 PR 可合）**：
   - T1: 启用 reclaim + 修注释（commit `fix(credentialfpslot): enable reclaim + fix TTL comments`）
   - T2: Signature 字段全套（commit `feat(ir): add thinking signature field for opus-4 round-trip`）
   - T3: 删 dead code + 恢复阈值（commit `refactor(credentialstate): drop legacy writeModelLevelFailure + restore 0.5 threshold`）
   - T4: per-attempt latency + 真 request_id（commit `fix(routing): record per-attempt latency + use real request_id in health tracker`）

2. **第二批（P1）**：4 个独立 PR，按主题逐一合
3. **第三批（P2+P3）**：合并到下一个 sprint
