# Session State Management — Architecture & Design Document

> **作者**: ACC Team
> **日期**: 2026-07-06
> **状态**: 已实施 Phase 1-5
> **仓库**: `services/llm-gateway-go`

---

## 1. 目标概述

扩展现有 Session 管理能力，提供：

| 能力 | 说明 |
|------|------|
| 会话生命周期 | active → stopped → recovered → expired |
| 实时统计 | 总轮次、token、费用、当前凭据统计 |
| 凭据轮换审计 | 每次凭据切换的完整记录（持续轮次、时间、token、费用） |
| 停止后保留 | 配置可调（默认 30 分钟），支持恢复 |
| DB 持久化 | 凭据轮换批量异步写 DB，会话停止时写快照 |
| 凭据/会话联动 | 与 Fingerprint Slot、Concurrency Slot 管理集成 |

---

## 2. 架构图

```
┌──────────────────────────────────────────────────────────────┐
│                    Request Flow                              │
└──────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        ▼                     ▼                     ▼
  Session.Create()    Reload Routing        Hook:OnCredentialSelected
        │                     │                     │
        ▼                     ▼                     ▼
  ┌──────────────────────────────────────────────────────────┐
  │                   Redis (Real-time SSOT)                   │
  │  ┌──────────────────┐ ┌──────────────────┐ ┌────────────┐ │
  │  │ session:{id}     │ │ session:{id}:    │ │ session:   │ │
  │  │ (Hash, 7d TTL)   │ │  cred_rotations  │ │  stopped:  │ │
  │  │ + extended       │ │  (List, 100 max) │ │  {tenant}  │ │
  │  │   fields         │ │                  │ │  (Set)     │ │
  │  └──────────────────┘ └──────────────────┘ └────────────┘ │
  │  ┌──────────────────┐ ┌──────────────────┐                 │
  │  │ session:apiKey:  │ │ session_pref:    │                 │
  │  │  {id}:active     │ │  {id}            │                 │
  │  │  (Set)           │ │  (String)        │                 │
  │  └──────────────────┘ └──────────────────┘                 │
  └──────────────────────────────────────────────────────────┘
        │                     │                     │
        ▼                     ▼                     ▼
  ┌──────────────────────────────────────────────────────────┐
  │                  Batch DB Writer (Async)                   │
  │   ┌──────────────────────────────────────────────────┐    │
  │   │  Ring Buffer  ──►  batchSize=10  ──►  INSERT    │    │
  │   │  (in-memory)   ──►  flushInterval=60s ──► DB    │    │
  │   └──────────────────────────────────────────────────┘    │
  └──────────────────────────────────────────────────────────┘
                              │
                              ▼
  ┌──────────────────────────────────────────────────────────┐
  │                  PostgreSQL (Audit)                       │
  │  ┌─────────────────────────┐ ┌─────────────────────────┐  │
  │  │ session_state_snapshots │ │ session_credential_     │  │
  │  │ (会话停止时写入)        │ │  rotations              │  │
  │  │                         │ │ (轮次批量/Flush 写入)   │  │
  │  └─────────────────────────┘ └─────────────────────────┘  │
  └──────────────────────────────────────────────────────────┘
                              │
                              ▼
  ┌──────────────────────────────────────────────────────────┐
  │                  Cleanup Worker                           │
  │   scanInterval=5min                                         │
  │   stoppedTTL=30min (configurable via Settings)             │
  └──────────────────────────────────────────────────────────┘
```

---

## 3. Redis 数据结构

### 3.1 Session Hash — `session:{sessionID}` (TTL 7d)

**已有字段**（保持不变）：
```
api_key_id, tenant_id, session_key, task_id, namespace
created_at, last_active, expires_at
devices(JSON), provider_cache_info(JSON)
```

**新增字段**：
| Field | Type | 说明 |
|-------|------|------|
| `status` | string | active / stopped / recovered |
| `stopped_at` | RFC3339 | 停止时间 |
| `stop_reason` | string | user_close / admin_stop / idle |
| `recovered_at` | RFC3339 | 恢复时间 |
| `client_ip` | string | 客户端 IP |
| `client_fp` | string(16) | 客户端指纹 hash（前 16 字符） |
| `current_credential_id` | int | 当前凭据 ID |
| `current_model` | string | 当前模型 |
| `current_provider` | string | 当前供应商 |
| `total_turns` | int | 总会话轮次 |
| `first_request_at` | RFC3339 | 首次请求时间 |
| `last_request_at` | RFC3339 | 最后请求时间 |
| `total_prompt_tokens` | int64 | 累计输入 token |
| `total_completion_tokens` | int64 | 累计输出 token |
| `total_cost_usd_cents` | int64 | 累计费用（万分之一美元） |
| `current_cred_turns` | int | 当前凭据轮次 |
| `current_cred_start_at` | RFC3339 | 当前凭据开始时间 |
| `current_cred_start_turn` | int | 当前凭据开始时的总轮次 |
| `title` | string(200) | 会话标题 |
| `annotation` | string(500) | 用户标注 |
| `tags` | string | 逗号分隔标签 |
| `fp_slot_index` | int | 当前指纹 slot 索引 |
| `fp_slot_credential_id` | int | Slot 对应的凭据 |

**Stats 更新**：使用 Lua 脚本原子更新（避免多字段并发冲突）。

### 3.2 凭据轮换历史 — `session:{sessionID}:cred_rotations` (List, TTL 7d)

每条记录（JSON）：
```json
{
  "credential_id": 42,
  "model": "gpt-4o",
  "provider": "openai",
  "started_at": "2026-07-06T10:00:00Z",
  "ended_at": null,
  "turns": 5,
  "prompt_tokens": 1500,
  "completion_tokens": 800,
  "cost_usd_cents": 234,
  "switch_reason": "rotate",
  "fp_slot_index": 0
}
```

**switch_reason 枚举**：
- `initial` — 会话首次
- `sticky` — 粘性路由命中
- `rotate` — 凭据轮换
- `fallback` — 主凭据降级
- `model_switch` — 模型切换
- `manual` — 管理员手动
- `slot_exhaust` — Slot 耗尽
- `probe_fail` — 探测失败

**Length Cap**：100 条（Settings `session.cred_history_max`），超出后 LTRIM

### 3.3 会话索引

```
session:apiKey:{apiKeyID}:active  → Set of sessionIDs (现有)
session:stopped:{tenantID}        → Set of sessionIDs (新增, TTL=24h)
```

---

## 4. 数据库 Schema

### 4.1 `session_state_snapshots`

```sql
CREATE TABLE public.session_state_snapshots (
    id                      bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id              text         NOT NULL UNIQUE,
    tenant_id               text         NOT NULL,
    api_key_id              bigint       NOT NULL,
    task_id                 text,
    status                  text         NOT NULL DEFAULT 'active',
    created_at              timestamptz  NOT NULL,
    first_request_at        timestamptz,
    last_request_at         timestamptz,
    stopped_at              timestamptz,
    stop_reason             text,
    recovered_at            timestamptz,
    final_credential_id     bigint,
    final_model             text,
    final_provider          text,
    total_turns             integer      NOT NULL DEFAULT 0,
    total_duration_sec      integer      NOT NULL DEFAULT 0,
    total_prompt_tokens     bigint       NOT NULL DEFAULT 0,
    total_completion_tokens bigint       NOT NULL DEFAULT 0,
    total_cost_usd          numeric(14,8) NOT NULL DEFAULT 0,
    title                   text,
    summary                 text,
    annotation              text,
    tags                    text[]       NOT NULL DEFAULT '{}',
    fp_slot_index           integer,
    raw_snapshot            jsonb,
    created_at_db           timestamptz  NOT NULL DEFAULT now()
);
```

**触发时机**：会话 stop 时、或定期 flush

### 4.2 `session_credential_rotations`

```sql
CREATE TABLE public.session_credential_rotations (
    id                  bigint       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id          text         NOT NULL,
    tenant_id           text         NOT NULL,
    seq                 integer      NOT NULL,
    credential_id       bigint       NOT NULL,
    credential_label    text,
    model               text,
    provider            text,
    started_at          timestamptz  NOT NULL,
    ended_at            timestamptz,
    turns               integer      NOT NULL DEFAULT 0,
    duration_sec        integer      NOT NULL DEFAULT 0,
    prompt_tokens       bigint       NOT NULL DEFAULT 0,
    completion_tokens   bigint       NOT NULL DEFAULT 0,
    cost_usd            numeric(14,8) NOT NULL DEFAULT 0,
    switch_reason       text         NOT NULL DEFAULT 'initial',
    fp_slot_index       integer,
    created_at          timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);
```

**写入时机**：
- 批量缓冲：每个 session 累计 10 条 或 60 秒时 flush
- 强制 flush：会话 stop 时立即 flush

---

## 5. Settings 配置项

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `session.stopped_ttl_minutes` | int | 30 | 会话停止后保留时间 |
| `session.cred_history_max` | int | 100 | 凭据历史保留条数 |
| `session.db_batch_size` | int | 10 | 批量写 DB 阈值 |
| `session.db_flush_interval_sec` | int | 60 | 批量写 DB 间隔 |

**位置**：`settings/spec_session.go::SessionSpecs()`

---

## 6. API 端点

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/admin/sessions` | admin | 列出会话 |
| GET | `/api/admin/sessions/{id}` | admin | 会话详情 |
| GET | `/api/admin/sessions/{id}/cred-rotations` | admin | 凭据轮换历史 |
| POST | `/api/admin/sessions/{id}/stop` | super_admin | 停止会话 |
| POST | `/api/admin/sessions/{id}/recover` | super_admin | 恢复会话 |
| PUT | `/api/admin/sessions/{id}/annotation` | admin | 更新标注 |
| PUT | `/api/admin/sessions/{id}/tags` | admin | 更新标签 |

---

## 7. 核心流程

### 7.1 请求完成时（凭据轮换检测）

```
请求到达 → 路由选择 credential_id
   ↓
[Hook: OnCredentialSelected]
   ↓
读取 session_pref:{id} → old_cred_id
   ↓
if old_cred_id != new_cred_id && old_cred_id > 0:
   ├─ EndCredRotation(old entry)
   └─ StartCredRotation(new entry) — LPUSH
   ↓
处理请求
   ↓
请求完成 → TouchUsage(Lua script):
   ├─ HINCRBY total_turns
   ├─ HINCRBY total_prompt_tokens
   ├─ HINCRBY total_cost_usd_cents
   └─ HSET last_request_at, current_model
   ↓
DBWriter.Enqueue (async, buffered)
```

### 7.2 会话停止流程

```
POST /api/admin/sessions/{id}/stop?reason=admin_stop
   ↓
StopSession(ctx, id, reason):
   ├─ EndCredRotation (finalize last entry)
   ├─ Pipeline:
   │   ├─ HSET status=stopped
   │   ├─ SADD session:stopped:{tenant}
   │   ├─ SREM session:apiKey:{id}:active
   │   └─ EXPIRE session:stopped:{tenant} 24h
   ├─ DBWriter.FlushSession (强制写入)
   └─ WriteSnapshot (写入 session_state_snapshots)
   ↓
Audit log
```

### 7.3 会话恢复流程

```
POST /api/admin/sessions/{id}/recover
   ↓
RecoverSession(ctx, id):
   ├─ 检查存在性 + status=stopped
   ├─ Pipeline:
   │   ├─ HSET status=recovered
   │   ├─ SREM session:stopped:{tenant}
   │   └─ SADD session:apiKey:{id}:active
   └─ 返回成功
```

### 7.4 清理流程

```
每 5 分钟扫描一次:
   ├─ SCAN session:stopped:*
   ├─ SMEMBERS 每个 stopped set
   └─ 对每个 sessionID:
       ├─ HGET stopped_at
       ├─ if stopped_at < now - TTL:
       │   ├─ DEL session:{id}
       │   └─ DEL session:{id}:cred_rotations
       └─ SREM from all session:stopped:* sets
```

---

## 8. 已实施文件清单

### 8.1 新增文件

| 文件 | 说明 |
|------|------|
| `sql/migrations/domain/331_session_state_and_rotations.sql` | 数据库表迁移 |
| `domains/session/session_state.go` | Session Hash 扩展 + Lua 原子更新 |
| `domains/session/session_db_writer.go` | 批量异步 DB Writer |
| `domains/session/session_cleanup.go` | 清理过期 stopped session |
| `admin/session_state_handlers.go` | Admin API handlers |

### 8.2 修改文件

| 文件 | 修改内容 |
|------|----------|
| `settings/spec_session.go` | 新增 4 个 Settings |
| `domains/session/session.go` | 新增 `GetRedisClient()` 方法 |
| `admin/handler.go` | 新增 session manager/DB writer/cleanup worker 字段 + setter 方法 |
| `admin/handler.go` | 注册 8 个新路由 |

---

## 9. 待完成

| 项目 | 优先级 | 状态 |
|------|--------|------|
| 在 main.go 完成依赖注入 | High | 待完成 |
| 凭据轮换 relay hook | High | 设计完成 |
| 前端会话管理页面 | Medium | 待开发 |
| 集成测试 | Medium | 待完成 |
| 性能压测 | Low | 待完成 |

---

## 10. 风险与缓解

| 风险 | 缓解 |
|------|------|
| Redis 内存膨胀 | 删除过期 stopped session（Worker）+ 精简字段 |
| DB 写入突发 | 批量缓冲 + 失败重试 |
| 凭据 race condition | Lua 脚本原子更新 |
| 停止时间精确性 | 时间戳比较使用 cutoff |
| 跨租户数据泄露 | API 层 tenant_id 校验 |

---

## 11. 监控指标

- `session_active_count` — Gauge
- `session_stopped_count` — Gauge
- `session_creations_total` — Counter
- `session_stops_total` — Counter
- `session_recoveries_total` — Counter
- `session_cred_rotations_total` — Counter
- `session_db_writer_flush_duration_seconds` — Histogram
- `session_cleanup_cleaned_total` — Counter
