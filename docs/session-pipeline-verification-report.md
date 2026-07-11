# 会话管理全链路分析与验证报告

> 生成时间: 2026-07-11
> 数据源: 生产环境 252 (115.29.212.252) + 代码仓库 HEAD
> 审计范围: 三级会话缓存管道 (原始→压缩→审计)

---

## 1. 现状概览

### 1.1 生产数据扫描 (252)

| 数据表 | 记录数 | 核心状态 |
|--------|--------|----------|
| Redis `session:sc:*` (L2) | 99 keys | **活跃** — 会话状态正确追踪 (mc, te, loh) |
| Redis DB0 | 152,167 keys | 全量缓存 |
| `handoff_logs` | 1,547 | **活跃** — 自动交接，LLM 摘要 |
| `request_logs_hot` | 16,537 | 活跃 — 请求日志写入 |
| `request_logs_bodies_2026_07` | 1,651 | 活跃 — 请求/响应体 |
| `approval_queue` | 49 | **全部 timeout** — 无人工审批 |
| `session_summaries` | **0** | **空表** — 无摘要写入 |
| `session_audit_records` | **0** | **空表** — 无审计记录 |
| `sticky_sessions` | **0** | **空表** — 无粘性路由 |
| `request_logs.outbound_msg_count` | **NULL** | 未写入 |
| `request_logs.compression_meta` | **NULL** | 未写入 |

### 1.2 Redis L2 Cache 采样

取典型会话 `gw_cc374ec3...`:

```
v=1, loh=<sha256>, lcat=0, mc=10, te=36848, smm="", rcat=0
```

✅ 基础状态追踪正常 (mc, te, loh)
❌ `lcat=0` — 从未压缩
❌ `smm=""` — 无摘要标记
❌ 无 v4/v5/v6 字段 (无 cut marker、无审计数据、无优化标记)

### 1.3 手写交接数据

| 指标 | 值 |
|------|-----|
| 总交接数 | 1,547 |
| 触发条件 | `absolute_threshold:180000` (180K tokens) |
| 触发模式 | 全部 `auto` |
| 平均 tokens | 299,520 |
| 平均消息数 | 951 |
| 摘要引擎 | 全部 `llm` |
| 平均耗时 | 7ms |

---

## 2. 三级缓存管道验证

### 2.1 原始会话 (Original / L3 PostgreSQL)

**预期行为**: 请求日志含完整 `gw_session_id`、`request_body`、`outbound_body`、`compression_meta` 等

**实际状态**:
- ✅ `request_logs_hot` 正确记录请求基本信息 (tokens, latency, success)
- ✅ `request_logs_bodies_*` 正确存储 body 数据 (16,537 hot + 1,651 月度)
- ❌ `outbound_msg_count`, `outbound_token_est`, `compression_meta`, `quality_score` **全部为 NULL**
- ❌ `session_summaries` 表为空 — 无聚合摘要写入

**结论**: 原始数据存在但关键字段未填充

### 2.2 压缩会话 (Compressed / Redis L2)

**预期行为**: Redis `session:sc:*` 含完整 `loh`, `lcat>0`, `smm`, v4/v5/v6 字段

**实际状态**:
- ✅ 99 个活跃会话键存在
- ✅ `loh` (last outbound hash) 正确
- ✅ `mc` (message count) 正确
- ✅ `te` (token estimate) 正确
- ❌ `lcat` 全部为 0 — 从未触发压缩
- ❌ `smm` 全部为空 — 无摘要标记
- ❌ 无 `fsh`, `hcm`, `cm_*` — 无 cut marker
- ❌ 无 `aud_*`, `sec_sc`, `app_st` — 无审计状态

**结论**: 压缩管道未到达 Redis — `CompressionHook` 未触发或条件不满足

### 2.3 审计会话 (Audited / DB + L2)

**预期行为**: `session_audit_records` 有记录，Redis `aud_*` 字段有值

**实际状态**:
- ❌ `session_audit_records` 表为 **0 行**
- ❌ `approval_queue` 全部 timeout — 无有效审批
- ❌ Redis 无 `aud_*` 字段

**结论**: 审计管道完全未工作

---

## 3. 核心差距分析

### 差距 P0: 压缩管道未到达生产

| 组件 | 所在文件 | 预期 | 实际 |
|------|----------|------|------|
| `SessionCache.Set()` | `session_cache.go:262` | 写入 Redis L2 | ✅ Redis 有键 |
| `SessionCompressor.Prepare()` | `session_compressor.go:20` | 执行 LCS diff | ? 未验证 |
| `CompressionHook` | `hook.go:46` | 标记 `lcat>0` | ❌ 所有 `lcat=0` |
| `CacheSaveHook` | `cache/hook.go:113` | 写 `compression_meta` | ❌ 全为 NULL |
| DB writer | `session_db_writer.go:186` | 写 `outbound_msg_count` | ❌ 全为 NULL |

**根因假设**: 要么 `CompressionHook` 未被 Pipeline 注册，要么 `MetaKeyNeedsCompression` 条件始终不满足。

### 差距 P1: Session Summary 管道中断

| 组件 | 预期 | 实际 |
|------|------|------|
| `Summarizer.GenerateSummary()` | 写 `session_summaries` | ❌ 表空 |
| `handoff_logs.summary_text` | 由交接触发 | ✅ LLM 摘要正常 |
| `session_summaries.summary` | 由 Summarizer 写入 | ❌ 未连接 |

**根因假设**: `session_summaries` 表的写入路径与 handoff 路径解耦 — handoff 写 `handoff_logs.summary_text` 但不写 `session_summaries`。

### 差距 P2: 审批队列全部超时

- 49 条审批记录全部 `status=timeout`
- 人工审批流程缺失或超时时间过短
- `expires_at` 已过期导致自动标记 timeout

### 差距 P3: request_logs 压缩字段未填充

所有 request_logs 变体表都有 `compression_meta`, `compression_reason`, `compression_strategy`, `outbound_msg_count`, `outbound_token_est` 字段但全部为 NULL。

### 差距 P4: 三级缓存间反向传播缺失

- L2→L3 写入仅在 `SessionCache.GetOrLoad()` 的 cold-start 路径触发 (L3→L2 backfill)
- L2 更新后无反向写入 L3 的机制

---

## 4. 典型案例

### 案例 A: handoff 会话链 (12 个连续会话)

```
gw_86750cd0 → gw_aa5226e8 → gw_6526e539 → ... → gw_20a6c6df
(581K tokens, 966 msgs) → (565K) → ... → (547K, 933 msgs)
```

- **特征**: 连续手写交接，LLM 摘要生成正常
- **问题**: 摘要仅存 `handoff_logs.summary_text`，**未同步**到 `session_summaries`、**未写入**Redis 压缩标记

### 案例 B: 超时审批会话 (批准队列 49 条)

```
gw_4dd827c0: score=10, detect=need_approval, status=timeout
gw_e5acfef7: score=10, detect=need_approval, status=timeout
```

- **特征**: 高评分风险检测触发审批，但最终无人处理
- **问题**: 审批流程 lack 自动降级/兜底机制

### 案例 C: Redis 缓存会话 (99 活跃键)

```
gw_d0813a00: mc=63, te=35361, lcat=0
gw_2386d4ca: mc=31, te=19076, lcat=0
gw_518b534d: mc=31, te=19152, lcat=0
```

- **特征**: 多轮会话状态正确追踪，但从未压缩
- **问题**: 压缩条件 (`MetaKeyNeedsCompression`) 不满足

---

## 5. 改进方案

### 5.1 P0 修复: 压缩管道接入生产

**目标**: 让 `CompressionHook` 在生产 Pipeline 中执行

```
1. 验证 buildPipeline() 中 CompressionHook 注册
   → /cmd/gateway-v2/main.go:172
2. 确保 MetaKeyNeedsCompression 条件被触发
   → 在消息数 > threshold 时自动标记
3. 验证 CacheSaveHook 写 compression_meta 到 request_logs
4. E2E 测试: 发送多轮消息 → 验证 lcat>0, smm 非空
```

### 5.2 P1 修复: Session Summary 全链路

**目标**: handoff 摘要同时写入 `session_summaries` 和 Redis

```
1. 在 handoff 完成后调用 Summarizer
2. 写 session_summaries 表 (当前为空)
3. 写 Redis smm 字段
4. 验证: SELECT * FROM session_summaries LIMIT 5 (非空)
```

### 5.3 P2 修复: 审批自动降级

**目标**: 审批超时后自动降级而非永久 timeout

```
1. 在 ApprovalManager 中添加 auto-escalation 逻辑
2. 超时后自动转 approved (安全模式) 或 rejected (严格模式)
3. 配置化: 按 tenant 设置 auto_approve_timeout_seconds
```

### 5.4 P3 修复: 三级缓存反写

**目标**: Redis L2 → DB L3 的压缩数据同步

```
1. 在 CacheSaveHook 完成时写 outbound_msg_count, outbound_token_est
2. 在 CompressionHook 完成时写 compression_meta, compression_strategy
3. 在 SessionAuditHook 完成时写 session_audit_records
```

### 5.5 端到端验证清单

```
[ ] 1. 创建多轮会话 → 验证 Redis L2 有 mc>1
[ ] 2. 超出压缩阈值 → 验证 lcat>0, smm 非空
[ ] 3. 查询 request_logs → 验证 outbound_msg_count > 0
[ ] 4. 触发手写交接 → 验证 handoff_logs + session_summaries 均有摘要
[ ] 5. 触发审计 → 验证 session_audit_records 有记录
[ ] 6. 触发审批 → 验证 approval_queue 有记录且非 timeout
[ ] 7. Redis 过期 → 验证 L3 可以冷启动恢复
[ ] 8. 三级一致性 → 验证 L1 = L2 = L3 的 mc, te, loh
```

---

## 6. 环境说明

| 资源 | 地址 | 凭证 |
|------|------|------|
| 服务器 252 | `115.29.212.252:25022` | root |
| PostgreSQL | `172.16.2.210:5432/llm_gateway` | llm_gateway |
| Redis | 同一台 (pms-redis) | 密码: Veritrans9900 |
| 二进制 | `/opt/llm-gateway-go/llm-gateway-go` | systemd |
| 数据库迁移 | `deploy-to-252.sh` 自动执行 | 当前 356_handoff_enhanced.sql |

---

**下一步行动**:
1. 本地部署 (local-deploy-test) 搭建完整测试环境
2. 执行端到端验证清单
3. 修复 P0 差距后验证 Redis lcat > 0
4. session.pin 关键发现到 MCP sessionstore
