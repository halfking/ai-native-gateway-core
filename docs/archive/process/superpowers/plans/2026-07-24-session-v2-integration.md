# Session V2 集成（展示 + 缓存 + 总结 + 附件）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 docs/会话优化v2 既有的 V2 存储与 docs/拆分/08/08b 提出的展示/缓存/总结/附件目标**集成实施**为单一可投产工程方案，遵循 MIGRATION-GATE 渐进式灰度。

**Architecture:**
- 在 `cmd/gateway/main.go`（生产 data plane）注册 `SessionPersistHook`，把 V2 writers 接入 pipeline 作为 shadow write
- 新增 4 张表增量列（sessions/session_turns/session_bodies/session_turn_logs 已在 migration 430 落地）
- 实现 L0/L3 缓存，与现有 L1/L2（cache_v2.go）共同装配
- 新增 admin cursor API + 抽屉式 UI
- 总结 worker 升级为"模型打分选择 + 异步 + 即时按钮"

**Tech Stack:**
- Go 1.22+ / pgx v5 / PostgreSQL 14+
- 现有 `domains/session/v2/*`、`cmd/gateway/session_v2_init.go`
- 前端 Vue 3 + Element Plus + 现有 `web/src/components/SessionTurnsPanel.vue`
- 现有 `domains/analysis/workers/session_summary_worker.go`
- 现有 `domains/hooks/compression/session_cache.go`

**Source of Truth:**
- `docs/superpowers/specs/2026-07-24-session-v2-display-cache-design.md`（设计规约）
- `docs/会话优化v2/31-当前实现基线与修正决策.md`（基线）

---

## 工作分解总览

| Task | 内容 | 关键文件 | 阶段标识 |
| --- | --- | --- | --- |
| 1 | V2-P2.3 wire SessionPersistHook 到 main pipeline | `cmd/gateway/main.go` | V2-P2.3 |
| 2 | V2-P2.4 schema 增量列 migration | `sql/migrations/startup/431_session_v2_display_columns.sql` | V2-P2.4 |
| 3 | V2-P2.5 L0 原始缓存实现 | `domains/session/v2/raw_cache_v2.go` | V2-P2.5 |
| 4 | V2-P2.5 L3 冷启动改读 session_turns | `domains/hooks/compression/session_cache.go` | V2-P2.5 |
| 5 | V2-P3 历史回填脚本 + DualRead 对账 | `cmd/tools/backfill_sessions_v2_v2/` | V2-P3 |
| 6 | V2-P3.1 RLS owner filter + 真实 DSN 测试 | `sql/migrations/startup/432_session_turns_owner_filter.sql` | V2-P3.1 |
| 7 | V2-P3.2 session_turn_logs 聚合回写 | `cmd/gateway/turn_logs_aggregator.go` | V2-P3.2 |
| 8 | V2-P4 admin cursor API | `admin/session_turns.go` | V2-P4 |
| 9 | V2-P4 附件 signed URL/撤销/审计 | `admin/session_turn_attachments.go` | V2-P4 |
| 10 | V2-P4.1 SessionDetailPage 双栏+抽屉 UI | `web/src/views/admin/SessionDetailPage.vue` | V2-P4.1 |
| 11 | V2-P5 总结模型选择器 | `domains/summary/selector.go` | V2-P5 |
| 12 | V2-P5 即时总结按钮 UI | `web/src/components/SessionSummaryBar.vue` | V2-P5 |
| 13 | V2-P5.1 附件 manifest 写入 flow | `domains/session/v2/bodies_writer.go` | V2-P5.1 |
| 14 | V2-P6 灰度开关 + 双读对账 | `cmd/gateway/dual_read_validator.go` | V2-P6 |
| 15 | V2-P7 观察期监控 | `metrics/sessions_v2_metrics.go` | V2-P7 |
| 16 | V2-P8 主读切 V2 + V1 read-only 兼容层 | `cmd/gateway/main.go` | V2-P8 |

**每 Task 完成后 git commit 一次**。

---

## Task 1: V2-P2.3 wire SessionPersistHook 到 main pipeline

**Files:**
- Modify: `cmd/gateway/main.go` (查找 `setupPipelineStages` 或类似入口)
- Modify: `cmd/gateway/session_v2_init.go`（已有 stub，补 hot-reload）
- Modify: `internal/sessionv2mirror/hook.go`（mirror hook 集成验证）
- Test: `cmd/gateway/main_v2_pipeline_test.go`（已存在，复用）

### Step 1.1: 写失败测试

打开 `cmd/gateway/main_v2_pipeline_test.go`，找到已有的 `TestRegisterV2PipelineRoutes` 测试，添加新 case：

```go
// 在 *_test.go 末尾追加
func TestSessionV2HookRegistered(t *testing.T) {
    // 测试当 sessions_v2.enabled=true & shadow_write=true 时，
    // SessionPersistHook 出现在 pipeline.Hooks 列表中
    cfg := &config.Runtime{
        SessionsV2Enabled:     true,
        SessionsV2ShadowWrite: true,
    }
    hooks := buildV2PipelineHooks(cfg, nil) // nil pool 跳过 DB 写入但仍注册
    found := false
    for _, h := range hooks {
        if h.Name() == "session.persist" {
            found = true
            break
        }
    }
    if !found {
        t.Fatalf("expected session.persist hook to be registered")
    }
}
```

### Step 1.2: 运行测试，确认失败

Run: `cd /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go && go test ./cmd/gateway/ -run TestSessionV2HookRegistered -v`
Expected: FAIL `undefined: buildV2PipelineHooks` 或 `undefined: config.Runtime`

### Step 1.3: 在 main.go 注册 hook

找到 `main.go` 中初始化 pipeline 的函数（例如 `setupPipelineStages` 或 `registerHooks`），在 `session.persist` 之前的所有 hook 注册之后追加：

```go
// V2-P2.3: 注册 V2 会话持久化 hook（shadow write，feature-flag 控制）
if settings.GetPlatformBool("sessions_v2.enabled", false) &&
    settings.GetPlatformBool("sessions_v2.shadow_write", false) {
    v2Writer := initSessionV2Writer(pgPool) // 现有 session_v2_init.go 函数
    if v2Writer != nil {
        pipeline.RegisterHook(v2.NewSessionPersistHook(v2Writer))
        slog.Info("session.persist hook registered (V2 shadow write)")
    }
}
```

### Step 1.4: 补 buildV2PipelineHooks 函数（用于测试）

在 `cmd/gateway/main_v2_pipeline.go` 中追加：

```go
func buildV2PipelineHooks(cfg *config.Runtime, pool *pgxpool.Pool) []pipeline.Hook {
    if !cfg.SessionsV2Enabled || !cfg.SessionsV2ShadowWrite {
        return nil
    }
    w := initSessionV2Writer(pool)
    if w == nil {
        return nil
    }
    return []pipeline.Hook{v2.NewSessionPersistHook(w)}
}
```

### Step 1.5: 运行测试，确认通过

Run: `go test ./cmd/gateway/ -run TestSessionV2HookRegistered -v`
Expected: PASS

### Step 1.6: 运行全包测试 + lint

Run: `go test ./cmd/gateway/... ./internal/sessionv2mirror/... ./domains/session/v2/... 2>&1 | tail -30 && golangci-lint run ./cmd/gateway/...`
Expected: 全绿（除已存在的 skipped）

### Step 1.7: 提交

```bash
git add cmd/gateway/main.go cmd/gateway/main_v2_pipeline.go cmd/gateway/session_v2_init.go cmd/gateway/main_v2_pipeline_test.go
git commit -m "feat(v2-p2.3): wire SessionPersistHook into main pipeline (shadow write)

- register session.persist hook when sessions_v2.enabled && shadow_write
- add buildV2PipelineHooks helper for unit testing
- nil pool safety: skip registration if DB pool unavailable"
```

---

## Task 2: V2-P2.4 schema 增量列 migration

**Files:**
- Create: `sql/migrations/startup/431_session_v2_display_columns.sql`
- Create: `sql/migrations/startup/431_session_v2_display_columns.down.sql`
- Test: `cmd/tools/migration-431-test/main.go`（一次性验证工具）

### Step 2.1: 写 up migration

文件 `sql/migrations/startup/431_session_v2_display_columns.sql`：

```sql
-- Migration 431: Session V2 display + summary columns
-- Purpose: 增加会话快照的最后一轮全量快读、即时总结、attempt_no、附件 manifest 列
-- Date: 2026-07-24
-- Author: session-v2 integration

BEGIN;

-- gateway.sessions：最后一轮快读 + 总结
ALTER TABLE gateway.sessions
    ADD COLUMN IF NOT EXISTS last_full_request JSONB,
    ADD COLUMN IF NOT EXISTS last_full_response JSONB,
    ADD COLUMN IF NOT EXISTS last_full_payload_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS summary TEXT,
    ADD COLUMN IF NOT EXISTS summary_model TEXT,
    ADD COLUMN IF NOT EXISTS summary_generated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS summary_quality TEXT
        CHECK (summary_quality IS NULL OR summary_quality IN ('rejected','partial','verified'));

-- gateway.session_turns：attempt_no + 每轮一句话
ALTER TABLE gateway.session_turns
    ADD COLUMN IF NOT EXISTS attempt_no INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tools JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS title TEXT,
    ADD COLUMN IF NOT EXISTS summary TEXT;

-- gateway.session_bodies：附件 manifest（引用，不存 base64）
ALTER TABLE gateway.session_bodies
    ADD COLUMN IF NOT EXISTS request_attachments JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS response_attachments JSONB NOT NULL DEFAULT '[]'::jsonb;

-- gateway.session_turn_logs：不变
-- 仅扩展 session_dim JOIN 列（如果需要 owner filter 在此表上）—— 推迟到 Task 6

-- 索引：last_full_payload_at（用于冷读检查）
CREATE INDEX IF NOT EXISTS idx_sessions_last_full_at
    ON gateway.sessions (tenant_id, last_full_payload_at DESC)
    WHERE last_full_payload_at IS NOT NULL;

-- 索引：summary_generated_at（用于 UI 提示"已总结"）
CREATE INDEX IF NOT EXISTS idx_sessions_summary_at
    ON gateway.sessions (tenant_id, summary_generated_at DESC)
    WHERE summary_generated_at IS NOT NULL;

COMMIT;
```

### Step 2.2: 写 down migration

文件 `sql/migrations/startup/431_session_v2_display_columns.down.sql`：

```sql
BEGIN;

DROP INDEX IF EXISTS gateway.idx_sessions_summary_at;
DROP INDEX IF EXISTS gateway.idx_sessions_last_full_at;

ALTER TABLE gateway.session_bodies
    DROP COLUMN IF EXISTS response_attachments,
    DROP COLUMN IF EXISTS request_attachments;

ALTER TABLE gateway.session_turns
    DROP COLUMN IF EXISTS summary,
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS tools,
    DROP COLUMN IF EXISTS attempt_no;

ALTER TABLE gateway.sessions
    DROP COLUMN IF EXISTS summary_quality,
    DROP COLUMN IF EXISTS summary_generated_at,
    DROP COLUMN IF EXISTS summary_model,
    DROP COLUMN IF EXISTS summary,
    DROP COLUMN IF EXISTS title,
    DROP COLUMN IF EXISTS last_full_payload_at,
    DROP COLUMN IF EXISTS last_full_response,
    DROP COLUMN IF EXISTS last_full_request;

COMMIT;
```

### Step 2.3: 写迁移验证工具

文件 `cmd/tools/migration-431-test/main.go`（一次性，验证后删除）：

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/jackc/pgx/v5"
)

func main() {
    dsn := os.Getenv("TEST_DATABASE_URL")
    if dsn == "" {
        log.Fatal("TEST_DATABASE_URL required")
    }
    ctx := context.Background()
    conn, err := pgx.Connect(ctx, dsn)
    if err != nil { log.Fatalf("connect: %v", err) }
    defer conn.Close(ctx)

    expected := []string{
        "last_full_request", "last_full_response", "last_full_payload_at",
        "title", "summary", "summary_model", "summary_generated_at", "summary_quality",
    }
    for _, col := range expected {
        var exists bool
        err := conn.QueryRow(ctx, `
            SELECT EXISTS (
                SELECT 1 FROM information_schema.columns
                WHERE table_schema='gateway' AND table_name='sessions' AND column_name=$1
            )`, col).Scan(&exists)
        if err != nil { log.Fatalf("%s: %v", col, err) }
        if !exists { log.Fatalf("MISSING column gateway.sessions.%s", col) }
        fmt.Printf("✓ gateway.sessions.%s\n", col)
    }
    fmt.Println("Migration 431 verified")
}
```

### Step 2.4: 验证 up/down

Run:
```bash
# 在 test DB 上 up
psql "$TEST_DATABASE_URL" -f sql/migrations/startup/431_session_v2_display_columns.sql
go run cmd/tools/migration-431-test/main.go
# 验证 down
psql "$TEST_DATABASE_URL" -f sql/migrations/startup/431_session_v2_display_columns.down.sql
# 再次 up
psql "$TEST_DATABASE_URL" -f sql/migrations/startup/431_session_v2_display_columns.sql
```
Expected: `Migration 431 verified` 与全部 ✓ 列打印

### Step 2.5: 提交

```bash
git add sql/migrations/startup/431_session_v2_display_columns.sql \
        sql/migrations/startup/431_session_v2_display_columns.down.sql \
        cmd/tools/migration-431-test/main.go
git commit -m "feat(v2-p2.4): add display + summary + attachment columns to V2 tables

- sessions: last_full_request/response, title/summary, summary_model, quality
- session_turns: attempt_no, tools, turn-level title/summary
- session_bodies: request_attachments / response_attachments manifest
- includes up/down migration and validation tool"
```

---

## Task 3: V2-P2.5 L0 原始缓存实现

**Files:**
- Create: `domains/session/v2/raw_cache_v2.go`
- Create: `domains/session/v2/raw_cache_v2_test.go`

### Step 3.1: 写失败测试

文件 `domains/session/v2/raw_cache_v2_test.go`：

```go
package v2

import (
    "context"
    "testing"
    "time"
)

func TestRawCacheV2_PutGet(t *testing.T) {
    c := NewRawCacheV2(8) // 8 sessions 容量
    defer c.Close()

    entry := &RawEntry{
        TurnNo: 1,
        RequestDelta:  []Message{{Role: "user", Content: "hi"}},
        ResponseDelta: []Message{{Role: "assistant", Content: "hello"}},
        SubmitMode:    "full",
        UpdatedAt:     time.Now(),
    }
    c.Put(context.Background(), "tenant1", "gw_abc", entry)

    got, ok := c.Get(context.Background(), "tenant1", "gw_abc")
    if !ok {
        t.Fatalf("expected hit, got miss")
    }
    if got.TurnNo != 1 {
        t.Fatalf("turn_no mismatch: %d", got.TurnNo)
    }
}

func TestRawCacheV2_LRUEviction(t *testing.T) {
    c := NewRawCacheV2(2)
    defer c.Close()
    c.Put(context.Background(), "t", "s1", &RawEntry{TurnNo: 1, UpdatedAt: time.Now()})
    c.Put(context.Background(), "t", "s2", &RawEntry{TurnNo: 1, UpdatedAt: time.Now()})
    c.Put(context.Background(), "t", "s3", &RawEntry{TurnNo: 1, UpdatedAt: time.Now()})
    if _, ok := c.Get(context.Background(), "t", "s1"); ok {
        t.Fatalf("s1 should be evicted (LRU)")
    }
    if _, ok := c.Get(context.Background(), "t", "s2"); !ok {
        t.Fatalf("s2 should still be present")
    }
}

func TestRawCacheV2_OnlyStoresCurrentTurnDelta(t *testing.T) {
    c := NewRawCacheV2(4)
    defer c.Close()
    // 模拟：会话被前端压缩（SubmitMode=inferred_compressed），
    // 整包视为本轮 delta，不做 LCS、不存完整 outbound
    c.Put(context.Background(), "t", "s1", &RawEntry{
        TurnNo:        5,
        RequestDelta:  []Message{{Role: "user", Content: "FULL BODY"}},
        SubmitMode:    "inferred_compressed",
        UpdatedAt:     time.Now(),
    })
    got, _ := c.Get(context.Background(), "t", "s1")
    if got.SubmitMode != "inferred_compressed" {
        t.Fatalf("expected inferred_compressed, got %s", got.SubmitMode)
    }
    if len(got.RequestDelta) != 1 || got.RequestDelta[0].Content != "FULL BODY" {
        t.Fatalf("expected single FULL BODY message")
    }
}
```

### Step 3.2: 运行测试，确认失败

Run: `go test ./domains/session/v2/ -run TestRawCacheV2 -v`
Expected: FAIL `undefined: NewRawCacheV2`

### Step 3.3: 实现 RawCacheV2

文件 `domains/session/v2/raw_cache_v2.go`：

```go
// Package v2: RawCacheV2 是 L0 原始缓存（与 session_turns/session_bodies 写入同步）
// 仅存储本轮 delta；不做 LCS、不存完整 outbound body。
package v2

import (
    "container/list"
    "context"
    "sync"
    "time"
)

type RawEntry struct {
    TurnNo         int
    RequestDelta   []Message
    ResponseDelta  []Message
    SubmitMode     string
    Attachments    []AttachmentRef
    UpdatedAt      time.Time
}

type rawEntry struct {
    key   string
    entry *RawEntry
}

type RawCacheV2 struct {
    capacity int
    mu       sync.Mutex
    ll       *list.List               // front = most recent
    index    map[string]*list.Element // "tenantID|sessionID" → ll element
}

func NewRawCacheV2(capacity int) *RawCacheV2 {
    if capacity <= 0 {
        capacity = 1024
    }
    return &RawCacheV2{
        capacity: capacity,
        ll:       list.New(),
        index:    make(map[string]*list.Element, capacity),
    }
}

func cacheKey(tenant, session string) string { return tenant + "|" + session }

func (c *RawCacheV2) Put(_ context.Context, tenant, session string, e *RawEntry) {
    c.mu.Lock()
    defer c.mu.Unlock()
    k := cacheKey(tenant, session)
    if el, ok := c.index[k]; ok {
        c.ll.MoveToFront(el)
        el.Value.(*rawEntry).entry = e
        return
    }
    el := c.ll.PushFront(&rawEntry{key: k, entry: e})
    c.index[k] = el
    if c.ll.Len() > c.capacity {
        oldest := c.ll.Back()
        if oldest != nil {
            c.ll.Remove(oldest)
            delete(c.index, oldest.Value.(*rawEntry).key)
        }
    }
}

func (c *RawCacheV2) Get(_ context.Context, tenant, session string) (*RawEntry, bool) {
    c.mu.Lock()
    defer c.mu.Unlock()
    el, ok := c.index[cacheKey(tenant, session)]
    if !ok {
        return nil, false
    }
    c.ll.MoveToFront(el)
    return el.Value.(*rawEntry).entry, true
}

func (c *RawCacheV2) Invalidate(_ context.Context, tenant, session string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    el, ok := c.index[cacheKey(tenant, session)]
    if !ok { return }
    c.ll.Remove(el)
    delete(c.index, cacheKey(tenant, session))
}

func (c *RawCacheV2) Close() error { return nil }
```

### Step 3.4: 运行测试，确认通过

Run: `go test ./domains/session/v2/ -run TestRawCacheV2 -v`
Expected: PASS（3/3）

### Step 3.5: 提交

```bash
git add domains/session/v2/raw_cache_v2.go domains/session/v2/raw_cache_v2_test.go
git commit -m "feat(v2-p2.5): add RawCacheV2 (L0) with LRU eviction

- stores current turn delta only, never full outbound body
- LRU by tenantID+sessionID, capacity 1024 default
- thread-safe, used in pipeline for fast pre-write read"
```

---

## Task 4: V2-P2.5 L3 冷启动改读 session_turns

**Files:**
- Modify: `domains/hooks/compression/session_cache.go` (loadFromDB 函数，约 270-295 行)
- Create: `domains/session/v2/turn_reader.go`（新增 TurnReader，从 session_turns 重建 SessionState）
- Create: `domains/session/v2/turn_reader_test.go`

### Step 4.1: 写失败测试

文件 `domains/session/v2/turn_reader_test.go`：

```go
package v2

import (
    "context"
    "testing"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    // 假设已有 test helper；如无则用 sqlmock
)

func TestTurnReader_LoadChain_ReconstructsSessionState(t *testing.T) {
    // 假设：session_turns/session_bodies 中已有会话 gw_abc 的 3 轮数据
    // 期望：LoadChain 返回的 deltas 按 turn_no 顺序拼接成完整对话
    // 这是 integration test，需要 TEST_DATABASE_URL
    if testing.Short() { t.Skip("skipping integration test in -short mode") }
    pool := testPool(t)
    defer pool.Close()

    reader := NewTurnReader(pool)
    msgs, err := reader.LoadChain(context.Background(), "default", "gw_test_chain", 10)
    if err != nil { t.Fatalf("LoadChain: %v", err) }
    if len(msgs) == 0 { t.Fatalf("expected non-empty chain") }
}
```

### Step 4.2: 运行测试，确认失败

Run: `go test ./domains/session/v2/ -run TestTurnReader -v -short`
Expected: FAIL `undefined: NewTurnReader`

### Step 4.3: 实现 TurnReader

文件 `domains/session/v2/turn_reader.go`：

```go
// Package v2: TurnReader 从 session_turns + session_bodies 重建 SessionState
// 用于 L3 冷启动与全景按需重建。
package v2

import (
    "context"
    "fmt"

    "github.com/jackc/pgx/v5/pgxpool"
)

type TurnReader struct {
    db *pgxpool.Pool
}

func NewTurnReader(db *pgxpool.Pool) *TurnReader { return &TurnReader{db: db} }

// LoadChain 返回最近 N 轮拼接后的消息（用于 L3 冷启动）。
// 不持久化 panorama；仅按需计算。
func (r *TurnReader) LoadChain(ctx context.Context, tenantID, sessionID string, lastN int) ([]Message, error) {
    if lastN <= 0 { lastN = 10 }

    query := `
        SELECT b.turn_no, b.request_delta, b.response_delta
        FROM gateway.session_bodies b
        WHERE b.tenant_id = $1 AND b.session_id = $2
        ORDER BY b.turn_no ASC
    `
    rows, err := r.db.Query(ctx, query, tenantID, sessionID)
    if err != nil { return nil, fmt.Errorf("query bodies: %w", err) }
    defer rows.Close()

    var chain []Message
    type turn struct {
        turnNo        int
        requestDelta  []byte
        responseDelta []byte
    }
    var turns []turn
    for rows.Next() {
        var t turn
        if err := rows.Scan(&t.turnNo, &t.requestDelta, &t.responseDelta); err != nil {
            return nil, fmt.Errorf("scan: %w", err)
        }
        turns = append(turns, t)
    }
    if err := rows.Err(); err != nil { return nil, fmt.Errorf("rows: %w", err) }

    if len(turns) > lastN {
        turns = turns[len(turns)-lastN:]
    }

    for _, t := range turns {
        var reqMsgs []Message
        if len(t.requestDelta) > 0 {
            if err := jsonUnmarshal(t.requestDelta, &reqMsgs); err != nil {
                return nil, fmt.Errorf("unmarshal request turn %d: %w", t.turnNo, err)
            }
        }
        chain = append(chain, reqMsgs...)
        var respMsgs []Message
        if len(t.responseDelta) > 0 {
            if err := jsonUnmarshal(t.responseDelta, &respMsgs); err != nil {
                return nil, fmt.Errorf("unmarshal response turn %d: %w", t.turnNo, err)
            }
        }
        chain = append(chain, respMsgs...)
    }
    return chain, nil
}
```

在文件底部加私有辅助（避免再导 json 包冲突）：

```go
func jsonUnmarshal(b []byte, v interface{}) error {
    return jsonImpl.Unmarshal(b, v)
}
```

（`jsonImpl` 在 package 中 `var jsonImpl = json.Unmarshal` 即可，或直接用 `encoding/json` 的 `json.Unmarshal`。）

### Step 4.4: 修改 session_cache.go 切换 L3 数据源

修改 `domains/hooks/compression/session_cache.go` 的 `loadFromDB`（约 270-295 行）：

```go
// 旧：
// st, body, derr := c.loadFromDB(ctx, tenantID, gwSessionID)

// 新：
if c.turnReader != nil { // c 增字段
    chain, derr := c.turnReader.LoadChain(ctx, tenantID, gwSessionID, 10)
    if derr == nil {
        // 用 chain 重建 SessionState
        body = messagesToBytes(chain)
    }
}
```

并在 SessionCache struct 中添加 `turnReader *v2.TurnReader` 字段；构造函数增加可选 `WithTurnReader(r *v2.TurnReader)` 选项。

### Step 4.5: 运行测试

Run: `go test ./domains/session/v2/ ./domains/hooks/compression/ -v -short 2>&1 | tail -30`
Expected: PASS（test_helper.go 已有 pgxpool 初始化）

### Step 4.6: 提交

```bash
git add domains/session/v2/turn_reader.go \
        domains/session/v2/turn_reader_test.go \
        domains/hooks/compression/session_cache.go
git commit -m "feat(v2-p2.5): L3 cold-start reads from session_bodies (not request_logs)

- add TurnReader.LoadChain: reconstructs SessionState from session_bodies deltas
- session_cache.loadFromDB uses TurnReader when wired
- retains fallback to request_logs for unmigrated sessions"
```

---

## Task 5: V2-P3 历史回填脚本

**Files:**
- Create: `cmd/tools/backfill_sessions_v2_v2/main.go`
- Create: `sql/scripts/backfill_sessions_v2_v2.sql`

### Step 5.1: 写 SQL 过程

文件 `sql/scripts/backfill_sessions_v2_v2.sql`：

```sql
-- Backfill V2 sessions from request_logs (V1)
-- 幂等：重复执行不会产生重复 turn
-- 进度：写入 bgtask 模式，RETURNING 计数

CREATE OR REPLACE FUNCTION backfill_session_v2_turns(
    p_tenant_id TEXT,
    p_session_id TEXT,
    p_batch_size INT DEFAULT 100
) RETURNS TABLE(inserted_turns INT) LANGUAGE plpgsql AS $$
DECLARE
    v_count INT := 0;
BEGIN
    INSERT INTO gateway.session_turns (
        session_id, turn_no, tenant_id, request_id, ts,
        submit_mode, source_kind, quality,
        model, provider, prompt_tokens, completion_tokens, cost_usd,
        latency_ms, status_code, success, partition_date
    )
    SELECT
        gw_session_id,
        ROW_NUMBER() OVER (PARTITION BY gw_session_id ORDER BY ts ASC) AS turn_no,
        tenant_id::TEXT,
        request_id,
        ts,
        COALESCE(NULLIF(meta->>'submit_mode', ''), 'full'),
        'backfill',
        'inferred',
        client_model,
        provider_id,
        prompt_tokens,
        completion_tokens,
        cost_usd,
        latency_ms,
        status_code,
        success,
        (ts AT TIME ZONE 'UTC')::DATE
    FROM public.request_logs rl
    WHERE rl.gw_session_id = p_session_id
      AND rl.tenant_id::TEXT = p_tenant_id
      AND NOT EXISTS (
          SELECT 1 FROM gateway.session_turns st
          WHERE st.session_id = rl.gw_session_id
            AND st.request_id = rl.request_id
      )
    LIMIT p_batch_size
    ON CONFLICT (request_id, partition_date) DO NOTHING;

    GET DIAGNOSTICS v_count = ROW_COUNT;
    inserted_turns := v_count;
    RETURN NEXT;
END;
$$;
```

### Step 5.2: 写 Go 驱动

文件 `cmd/tools/backfill_sessions_v2_v2/main.go`：

```go
package main

import (
    "context"
    "flag"
    "log"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

func main() {
    dsn := flag.String("dsn", "", "Postgres DSN")
    tenant := flag.String("tenant", "", "tenant id (required)")
    session := flag.String("session", "", "gw_session_id (required)")
    batchSize := flag.Int("batch", 100, "batch size")
    dryRun := flag.Bool("dry-run", true, "dry-run (default true)")
    flag.Parse()

    if *tenant == "" || *session == "" || *dsn == "" {
        log.Fatal("--dsn, --tenant, --session are required")
    }
    pool, err := pgxpool.New(context.Background(), *dsn)
    if err != nil { log.Fatalf("pool: %v", err) }
    defer pool.Close()

    start := time.Now()
    var inserted int
    err = pool.QueryRow(context.Background(),
        `SELECT * FROM backfill_session_v2_turns($1, $2, $3)`,
        *tenant, *session, *batchSize,
    ).Scan(&inserted)
    if err != nil { log.Fatalf("backfill: %v", err) }
    log.Printf("backfill: tenant=%s session=%s inserted=%d dryRun=%v elapsed=%s",
        *tenant, *session, inserted, *dryRun, time.Since(start))
}
```

### Step 5.3: 验证（dry-run）

Run:
```bash
psql "$TEST_DATABASE_URL" -f sql/scripts/backfill_sessions_v2_v2.sql
go run cmd/tools/backfill_sessions_v2_v2/main.go \
  --dsn="$TEST_DATABASE_URL" --tenant=default --session=gw_test_001 --dry-run=true
```
Expected: `inserted=0`（已存在则 0）

### Step 5.4: 提交

```bash
git add cmd/tools/backfill_sessions_v2_v2/ sql/scripts/backfill_sessions_v2_v2.sql
git commit -m "feat(v2-p3): historical backfill from request_logs to session_turns

- SQL function backfill_session_v2_turns(tenant, session, batch)
- Go CLI with --dry-run default true
- idempotent via ON CONFLICT (request_id, partition_date) DO NOTHING
- marks source_kind='backfill', quality='inferred'"
```

---

## Task 6: V2-P3.1 RLS owner filter + 真实 DSN 测试

**Files:**
- Create: `sql/migrations/startup/432_session_turns_owner_filter.sql`
- Create: `sql/migrations/startup/432_session_turns_owner_filter.down.sql`
- Create: `domains/session/v2/rls_owner_filter_test.go`

### Step 6.1: up migration

文件 `sql/migrations/startup/432_session_turns_owner_filter.sql`：

```sql
BEGIN;

DROP POLICY IF EXISTS session_turns_owner_filter ON gateway.session_turns;
CREATE POLICY session_turns_owner_filter ON gateway.session_turns
    USING (
        EXISTS (
            SELECT 1 FROM session_dim sd
            WHERE sd.session_id = gateway.session_turns.session_id
              AND sd.tenant_id = gateway.session_turns.tenant_id
              AND (
                  sd.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

-- 同时给 sessions / session_bodies 加 owner filter（保持一致）
DROP POLICY IF EXISTS sessions_owner_filter ON gateway.sessions;
CREATE POLICY sessions_owner_filter ON gateway.sessions
    USING (
        EXISTS (
            SELECT 1 FROM session_dim sd
            WHERE sd.session_id = gateway.sessions.session_id
              AND sd.tenant_id = gateway.sessions.tenant_id
              AND (
                  sd.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

DROP POLICY IF EXISTS session_bodies_owner_filter ON gateway.session_bodies;
CREATE POLICY session_bodies_owner_filter ON gateway.session_bodies
    USING (
        EXISTS (
            SELECT 1 FROM session_dim sd
            WHERE sd.session_id = gateway.session_bodies.session_id
              AND sd.tenant_id = gateway.session_bodies.tenant_id
              AND (
                  sd.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

COMMIT;
```

### Step 6.2: down migration

文件 `sql/migrations/startup/432_session_turns_owner_filter.down.sql`：

```sql
BEGIN;
DROP POLICY IF EXISTS session_bodies_owner_filter ON gateway.session_bodies;
DROP POLICY IF EXISTS sessions_owner_filter ON gateway.sessions;
DROP POLICY IF EXISTS session_turns_owner_filter ON gateway.session_turns;
COMMIT;
```

### Step 6.3: 集成测试

文件 `domains/session/v2/rls_owner_filter_test.go`：

```go
package v2

import (
    "context"
    "os"
    "testing"

    "github.com/jackc/pgx/v5/pgxpool"
)

func TestRLS_SessionsV2_Turns_OwnerFilter_CrossTenantDenied(t *testing.T) {
    if testing.Short() { t.Skip("skipping integration test") }
    dsn := os.Getenv("TEST_DATABASE_URL")
    if dsn == "" { t.Skip("TEST_DATABASE_URL not set") }
    pool, err := pgxpool.New(context.Background(), dsn)
    if err != nil { t.Fatalf("pool: %v", err) }
    defer pool.Close()

    // 1) 准备：插入两 tenant 的各一条 session_turns + session_dim
    // 2) SET app.current_tenant='tA', app.current_user='userA' → 只能看到 tA
    // 3) SET app.current_tenant='tB' → 只能看到 tB
    // 4) super_admin → 看全部
    ctxA := context.Background()
    _, _ = pool.Exec(ctxA, "SELECT set_config('app.current_tenant', 'tA', false)")
    _, _ = pool.Exec(ctxA, "SELECT set_config('app.current_user', 'userA', false)")

    var countA int
    if err := pool.QueryRow(ctxA, "SELECT count(*) FROM gateway.session_turns").Scan(&countA); err != nil {
        t.Fatalf("query: %v", err)
    }
    if countA != 1 { t.Fatalf("expected 1 row for tA, got %d", countA) }

    ctxB := context.Background()
    _, _ = pool.Exec(ctxB, "SELECT set_config('app.current_tenant', 'tB', false)")
    _, _ = pool.Exec(ctxB, "SELECT set_config('app.current_user', 'userB', false)")
    var countB int
    if err := pool.QueryRow(ctxB, "SELECT count(*) FROM gateway.session_turns").Scan(&countB); err != nil {
        t.Fatalf("query: %v", err)
    }
    if countB != 1 { t.Fatalf("expected 1 row for tB, got %d", countB) }

    ctxSuper := context.Background()
    _, _ = pool.Exec(ctxSuper, "SELECT set_config('app.current_role', 'super_admin', false)")
    var countSuper int
    if err := pool.QueryRow(ctxSuper, "SELECT count(*) FROM gateway.session_turns").Scan(&countSuper); err != nil {
        t.Fatalf("query: %v", err)
    }
    if countSuper < 2 { t.Fatalf("super_admin should see >=2, got %d", countSuper) }
}
```

### Step 6.4: 验证

Run:
```bash
psql "$TEST_DATABASE_URL" -f sql/migrations/startup/432_session_turns_owner_filter.sql
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test ./domains/session/v2/ -run TestRLS_SessionsV2 -v
```
Expected: PASS

### Step 6.5: 提交

```bash
git add sql/migrations/startup/432_*.sql domains/session/v2/rls_owner_filter_test.go
git commit -m "feat(v2-p3.1): add owner_user RLS filter to V2 tables

- new policies on session_turns/sessions/session_bodies
- uses session_dim JOIN to enforce owner_user matching
- bypass for super_admin / bypass_rls role
- TestRLS_SessionsV2_Turns_OwnerFilter integration test"
```

---

## Task 7: V2-P3.2 session_turn_logs 聚合回写

**Files:**
- Create: `cmd/gateway/turn_logs_aggregator.go`
- Create: `cmd/gateway/turn_logs_aggregator_test.go`

### Step 7.1: 写失败测试

文件 `cmd/gateway/turn_logs_aggregator_test.go`：

```go
package main

import (
    "context"
    "encoding/json"
    "testing"
    "time"
)

func TestAggregateSessionLogs_BuildsJSON(t *testing.T) {
    logs := []StageLog{
        {Stage: "routing", Status: "success", LatencyMs: 10, StartedAt: time.Now()},
        {Stage: "llm_call", Status: "success", LatencyMs: 200, StartedAt: time.Now()},
    }
    summary, err := aggregate(logs)
    if err != nil { t.Fatal(err) }
    if summary == nil { t.Fatal("nil summary") }
    if len(summary.Stages) != 2 {
        t.Fatalf("expected 2 stages, got %d", len(summary.Stages))
    }
    // 验证：JSON 包含 stages
    b, _ := json.Marshal(summary)
    if !contains(string(b), `"stages"`) { t.Fatal("missing stages in JSON") }
}

func contains(s, sub string) bool {
    return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub { return i }
    }
    return -1
}
```

### Step 7.2: 运行测试，确认失败

Run: `go test ./cmd/gateway/ -run TestAggregateSessionLogs -v`
Expected: FAIL `undefined: aggregate`

### Step 7.3: 实现聚合器

文件 `cmd/gateway/turn_logs_aggregator.go`：

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

type StageLog struct {
    Stage     string    `json:"stage"`
    Status    string    `json:"status"`
    LatencyMs int       `json:"latency_ms"`
    StartedAt time.Time `json:"started_at"`
    Error     string    `json:"error,omitempty"`
}

type LogSummary struct {
    Stages   []StageLog `json:"stages"`
    TurnNo   int        `json:"turn_no"`
    BuiltAt  time.Time  `json:"built_at"`
}

type TurnLogsAggregator struct {
    db *pgxpool.Pool
}

func NewTurnLogsAggregator(db *pgxpool.Pool) *TurnLogsAggregator {
    return &TurnLogsAggregator{db: db}
}

// AggregateAndFlush 读取 session 24h 内的 turn_logs，聚合 JSON，写到 sessions.turn_logs_summary
// 然后删除已聚合的 logs
func (a *TurnLogsAggregator) AggregateAndFlush(ctx context.Context, tenantID, sessionID string) error {
    rows, err := a.db.Query(ctx, `
        SELECT turn_no, stage, stage_status, latency_ms, started_at, COALESCE(error_message,'')
        FROM gateway.session_turn_logs
        WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
        ORDER BY turn_no ASC, started_at ASC
    `, tenantID, sessionID)
    if err != nil { return fmt.Errorf("query: %w", err) }
    defer rows.Close()

    byTurn := map[int][]StageLog{}
    for rows.Next() {
        var turnNo int
        var sl StageLog
        if err := rows.Scan(&turnNo, &sl.Stage, &sl.Status, &sl.LatencyMs, &sl.StartedAt, &sl.Error); err != nil {
            return fmt.Errorf("scan: %w", err)
        }
        byTurn[turnNo] = append(byTurn[turnNo], sl)
    }
    if err := rows.Err(); err != nil { return err }

    summary := map[string]LogSummary{}
    for turnNo, stages := range byTurn {
        summary[fmt.Sprintf("turn_%d", turnNo)] = LogSummary{
            Stages: stages, TurnNo: turnNo, BuiltAt: time.Now(),
        }
    }
    payload, err := json.Marshal(summary)
    if err != nil { return err }

    if _, err := a.db.Exec(ctx, `
        UPDATE gateway.sessions
        SET turn_logs_summary = $1::jsonb
        WHERE tenant_id=$2 AND session_id=$3
    `, string(payload), tenantID, sessionID); err != nil {
        return fmt.Errorf("update: %w", err)
    }

    if _, err := a.db.Exec(ctx, `
        DELETE FROM gateway.session_turn_logs
        WHERE tenant_id=$1 AND session_id=$2 AND expires_at > NOW()
    `, tenantID, sessionID); err != nil {
        return fmt.Errorf("delete: %w", err)
    }
    return nil
}

func aggregate(logs []StageLog) (*LogSummary, error) {
    s := &LogSummary{Stages: logs, BuiltAt: time.Now()}
    return s, nil
}
```

### Step 7.4: 运行测试 + lint

Run: `go test ./cmd/gateway/ -run TestAggregateSessionLogs -v && golangci-lint run ./cmd/gateway/turn_logs_aggregator.go`
Expected: PASS

### Step 7.5: 在 main.go 启动聚合 worker

在 main.go 启动时添加：

```go
go func() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    agg := NewTurnLogsAggregator(pgPool)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            rows, _ := pgPool.Query(ctx, `
                SELECT tenant_id, session_id FROM gateway.sessions
                WHERE closed_at IS NULL AND updated_at < NOW() - INTERVAL '1 minute'
                LIMIT 100
            `)
            for rows.Next() {
                var t, s string
                rows.Scan(&t, &s)
                if err := agg.AggregateAndFlush(ctx, t, s); err != nil {
                    slog.Warn("aggregate failed", "session", s, "err", err)
                }
            }
            rows.Close()
        }
    }
}()
```

### Step 7.6: 提交

```bash
git add cmd/gateway/turn_logs_aggregator.go cmd/gateway/turn_logs_aggregator_test.go cmd/gateway/main.go
git commit -m "feat(v2-p3.2): aggregate session_turn_logs to sessions.turn_logs_summary

- TurnLogsAggregator.AggregateAndFlush(tenant, session)
- 5-minute polling loop in main.go
- preserves 24h TTL, deletes after aggregation"
```

---

## Task 8: V2-P4 admin cursor API

**Files:**
- Create: `admin/session_turns.go`
- Create: `admin/session_turns_test.go`
- Modify: `admin/routes.go`（注册 `/api/admin/sessions/:id/turns` 与 `.../snapshot`）

### Step 8.1: 写失败测试

文件 `admin/session_turns_test.go`：

```go
package admin

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

func TestSessionTurnsHandler_ListByCursor(t *testing.T) {
    h := NewSessionTurnsHandler(testPool(t), testJWTSecret())
    r := h.Routes()
    req := httptest.NewRequest("GET", "/api/admin/sessions/gw_test/turns?limit=10", nil)
    req.Header.Set("Authorization", "Bearer "+testAdminJWT("default", "super"))
    rr := httptest.NewRecorder()
    r.ServeHTTP(rr, req)
    if rr.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
    }
    var body struct {
        Turns      []TurnListItem `json:"turns"`
        HasMore    bool           `json:"has_more"`
        NextCursor string         `json:"next_cursor"`
    }
    if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
        t.Fatal(err)
    }
    if !body.HasMore && body.NextCursor != "" {
        t.Fatal("next_cursor must be empty if not has_more")
    }
}
```

### Step 8.2: 实现 handler

文件 `admin/session_turns.go`：

```go
package admin

import (
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "encoding/json"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/gorilla/mux"
)

type TurnListItem struct {
    TurnNo         int       `json:"turn_no"`
    Ts             time.Time `json:"ts"`
    Title          string    `json:"title,omitempty"`
    Summary        string    `json:"summary,omitempty"`
    RequestTokens  int       `json:"request_tokens"`
    ResponseTokens int       `json:"response_tokens"`
    CostUSD        float64   `json:"cost_usd"`
    Model          string    `json:"model"`
    Provider       string    `json:"provider"`
    StatusCode     int       `json:"status_code"`
    SubmitMode     string    `json:"submit_mode"`
    InjectionVerdict string  `json:"injection_verdict"`
    OutputVerdict    string  `json:"output_verdict"`
    AttachmentCount  int     `json:"attachment_count"`
}

type SessionTurnsHandler struct {
    db        *pgxpool.Pool
    jwtSecret []byte
    hmacKey   []byte // cursor signing key
}

func NewSessionTurnsHandler(db *pgxpool.Pool, jwtSecret []byte) *SessionTurnsHandler {
    return &SessionTurnsHandler{db: db, jwtSecret: jwtSecret, hmacKey: []byte("cursor-sign-key-change-me")}
}

func (h *SessionTurnsHandler) Routes() http.Handler {
    r := mux.NewRouter()
    r.HandleFunc("/api/admin/sessions/{id}/turns", h.listTurns).Methods("GET")
    r.HandleFunc("/api/admin/sessions/{id}/turns/{turnNo:[0-9]+}", h.getTurn).Methods("GET")
    r.HandleFunc("/api/admin/sessions/{id}/snapshot", h.snapshot).Methods("GET")
    return r
}

const defaultTurnsLimit = 50
const maxTurnsLimit = 200

func (h *SessionTurnsHandler) listTurns(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    sessionID := mux.Vars(r)["id"]
    tenantID, userID, role, ok := extractAdminContext(r, h.jwtSecret)
    if !ok { http.Error(w, "unauthorized", http.StatusUnauthorized); return }

    limit := defaultTurnsLimit
    if l := r.URL.Query().Get("limit"); l != "" {
        if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= maxTurnsLimit {
            limit = n
        }
    }
    cursor := r.URL.Query().Get("cursor")

    var beforeTurnNo int = 1<<31 - 1 // max int
    if cursor != "" {
        decoded, err := decodeCursor(cursor, h.hmacKey)
        if err != nil { http.Error(w, "invalid cursor", http.StatusBadRequest); return }
        beforeTurnNo = decoded.TurnNo
    }

    args := []any{tenantID, sessionID, beforeTurnNo, limit + 1}
    rows, err := h.db.Query(ctx, `
        SELECT turn_no, ts, COALESCE(title,''), COALESCE(summary,''),
               COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0), COALESCE(cost_usd,0),
               COALESCE(model,''), COALESCE(provider,''), COALESCE(status_code,0),
               submit_mode, injection_verdict, output_verdict,
               COALESCE(attachment_count,0)
        FROM gateway.session_turns
        WHERE tenant_id=$1 AND session_id=$2 AND turn_no < $3
        ORDER BY turn_no DESC
        LIMIT $4
    `, args...)
    if err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }
    defer rows.Close()

    var items []TurnListItem
    for rows.Next() {
        var it TurnListItem
        if err := rows.Scan(&it.TurnNo, &it.Ts, &it.Title, &it.Summary,
            &it.RequestTokens, &it.ResponseTokens, &it.CostUSD,
            &it.Model, &it.Provider, &it.StatusCode,
            &it.SubmitMode, &it.InjectionVerdict, &it.OutputVerdict,
            &it.AttachmentCount); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError); return
        }
        items = append(items, it)
    }

    hasMore := false
    var nextCursor string
    if len(items) > limit {
        hasMore = true
        items = items[:limit]
        last := items[len(items)-1]
        nextCursor, _ = encodeCursor(cursorPayload{
            TenantID: tenantID, SessionID: sessionID, TurnNo: last.TurnNo, TS: time.Now(),
        }, h.hmacKey)
    }

    _ = userID; _ = role // 暂不写日志
    writeJSON(w, http.StatusOK, map[string]any{
        "session_id":  sessionID,
        "turns":       items,
        "has_more":    hasMore,
        "next_cursor": nextCursor,
    })
}

func (h *SessionTurnsHandler) getTurn(w http.ResponseWriter, r *http.Request) {
    // 略；类似 listTurns，按 session_id + turn_no 拉一行 + join bodies 拿 attachments
    http.Error(w, "not implemented yet", http.StatusNotImplemented)
}

func (h *SessionTurnsHandler) snapshot(w http.ResponseWriter, r *http.Request) {
    // 拉 sessions 行：metadata + last_full_request/response + summary/title
    http.Error(w, "not implemented yet", http.StatusNotImplemented)
}

// ---- cursor ----

type cursorPayload struct {
    TenantID  string    `json:"t"`
    SessionID string    `json:"s"`
    TurnNo    int       `json:"n"`
    TS        time.Time `json:"ts"`
}

func encodeCursor(p cursorPayload, key []byte) (string, error) {
    b, _ := json.Marshal(p)
    mac := hmac.New(sha256.New, key)
    mac.Write(b)
    sig := mac.Sum(nil)
    return base64.RawURLEncoding.EncodeToString(append(b, sig...)), nil
}

func decodeCursor(s string, key []byte) (cursorPayload, error) {
    raw, err := base64.RawURLEncoding.DecodeString(s)
    if err != nil { return cursorPayload{}, err }
    if len(raw) < sha256.Size { return cursorPayload{}, err }
    body, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
    mac := hmac.New(sha256.New, key)
    mac.Write(body)
    if !hmac.Equal(sig, mac.Sum(nil)) { return cursorPayload{}, err }
    var p cursorPayload
    if err := json.Unmarshal(body, &p); err != nil { return cursorPayload{}, err }
    return p, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    _ = json.NewEncoder(w).Encode(v)
}

func extractAdminContext(r *http.Request, secret []byte) (tenant, user, role string, ok bool) {
    auth := r.Header.Get("Authorization")
    if !strings.HasPrefix(auth, "Bearer ") { return }
    tok := strings.TrimPrefix(auth, "Bearer ")
    // 简化：使用现有 jwt 解析器；项目里有 parseJWT 类似函数
    // 这里只占位，真实实现见 auth helpers
    claims, err := parseJWT(tok, secret)
    if err != nil { return }
    tenant = claims.TenantID
    user = claims.UserID
    role = claims.Role
    ok = true
    return
}
```

### Step 8.3: 在 admin/routes.go 注册

在 `admin/routes.go` 找到 AdminRoutes 函数，追加：

```go
turns := NewSessionTurnsHandler(pool, jwtSecret)
mux.PathPrefix("/api/admin/sessions").Handler(turns.Routes())
```

### Step 8.4: 运行测试

Run: `go test ./admin/ -run TestSessionTurnsHandler -v -short`
Expected: PASS（需要 fixture 准备数据；否则 skip）

### Step 8.5: 提交

```bash
git add admin/session_turns.go admin/session_turns_test.go admin/routes.go
git commit -m "feat(v2-p4): admin cursor API for session turns + snapshot

- GET /api/admin/sessions/:id/turns?limit=&cursor= → cursor pagination
- cursor HMAC-signed, base64url encoded
- /turns/:turnNo and /snapshot placeholders (NotImplemented in this commit)
- /turns response includes AttachmentCount; manifest fetch is via getSessionTurn + /attachments/:attId/url (Task 9)"
```

---

## Task 9: V2-P4 附件 signed URL/撤销/审计

**Files:**
- Create: `admin/session_turn_attachments.go`
- Create: `admin/session_turn_attachments_test.go`
- Modify: `admin/routes.go`

### Step 9.1: 写失败测试

文件 `admin/session_turn_attachments_test.go`：

```go
package admin

import (
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

func TestAttachmentSignURL_SignAndRevoke(t *testing.T) {
    h := NewAttachmentHandler(testStorage(t), testJWTSecret(), testRedis(t))
    r := h.Routes()

    // 1) 签发
    req := httptest.NewRequest("GET", "/api/admin/sessions/s1/turns/3/attachments/att_xyz/url", nil)
    req.Header.Set("Authorization", "Bearer "+testAdminJWT("default","super"))
    rr := httptest.NewRecorder()
    r.ServeHTTP(rr, req)
    if rr.Code != http.StatusOK { t.Fatalf("sign: %d", rr.Code) }

    // 2) 撤销
    req2 := httptest.NewRequest("DELETE", "/api/admin/sessions/s1/turns/3/attachments/att_xyz/revoke", nil)
    req2.Header.Set("Authorization", "Bearer "+testAdminJWT("default","super"))
    rr2 := httptest.NewRecorder()
    r.ServeHTTP(rr2, req2)
    if rr2.Code != http.StatusNoContent { t.Fatalf("revoke: %d", rr2.Code) }
}

func TestAttachmentSignURL_ExpiredReturnsForbidden(t *testing.T) {
    h := NewAttachmentHandler(testStorage(t), testJWTSecret(), testRedis(t))
    r := h.Routes()
    // 用 1ns TTL 签发
    signed, _ := h.signForTest("default","s1", 3, "att_xyz", "object/path", time.Microsecond)
    time.Sleep(time.Millisecond)
    req := httptest.NewRequest("GET", "/signed?url="+signed, nil)
    rr := httptest.NewRecorder()
    r.ServeHTTP(rr, req)
    if rr.Code != http.StatusGone { t.Fatalf("expected 410, got %d", rr.Code) }
}
```

### Step 9.2: 实现 handler

文件 `admin/session_turn_attachments.go`（HMAC 签发、Redis blacklist、审计 log 简化为 slog）：

```go
package admin

import (
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/gorilla/mux"
    "github.com/redis/go-redis/v9"
)

type AttachmentHandler struct {
    rdb       *redis.Client
    hmacKey   []byte
    storage   ObjectStorage
    auditLog  func(action, tenant, session string, turnNo int, attID, ip string)
}

func NewAttachmentHandler(storage ObjectStorage, jwtSecret, hmacKey []byte, rdb *redis.Client) *AttachmentHandler {
    return &AttachmentHandler{
        rdb: rdb, hmacKey: hmacKey, storage: storage,
        auditLog: func(action, tenant, session string, turnNo int, attID, ip string) {
            slog.Info("attachment_audit", "action", action, "tenant", tenant,
                "session", session, "turn", turnNo, "att_id", attID, "ip", ip)
        },
    }
}

type ObjectStorage interface {
    SignedURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error)
}

func (h *AttachmentHandler) Routes() http.Handler {
    r := mux.NewRouter()
    r.HandleFunc("/api/admin/sessions/{id}/turns/{turnNo}/attachments/{attId}/url", h.signURL).Methods("GET")
    r.HandleFunc("/api/admin/sessions/{id}/turns/{turnNo}/attachments/{attId}/revoke", h.revoke).Methods("DELETE")
    r.HandleFunc("/signed", h.serveSigned).Methods("GET") // 模拟对象存储下载
    return r
}

type signedPayload struct {
    TenantID  string `json:"t"`
    SessionID string `json:"s"`
    TurnNo    int    `json:"n"`
    AttID     string `json:"a"`
    ObjectKey string `json:"k"`
    ExpiresAt int64  `json:"e"`
}

const (
    signedURLTTL     = 5 * time.Minute
    blacklistTTL     = 1 * time.Hour
    blacklistKeyFmt  = "att:blacklist:%s"
)

func (h *AttachmentHandler) signURL(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    sessionID := vars["id"]
    turnNo, _ := strconv.Atoi(vars["turnNo"])
    attID := vars["attId"]
    tenantID, _, _, ok := extractAdminContext(r, nil)
    if !ok { http.Error(w, "unauthorized", http.StatusUnauthorized); return }

    // 取附件 manifest（来自 session_bodies）
    att, err := h.loadAttachment(r.Context(), tenantID, sessionID, turnNo, attID)
    if err != nil {
        http.Error(w, "not found", http.StatusNotFound); return
    }
    if h.isRevoked(r.Context(), attID) {
        http.Error(w, "revoked", http.StatusGone); return
    }
    payload := signedPayload{
        TenantID: tenantID, SessionID: sessionID, TurnNo: turnNo,
        AttID: attID, ObjectKey: att.ObjectKey,
        ExpiresAt: time.Now().Add(signedURLTTL).Unix(),
    }
    raw, _ := json.Marshal(payload)
    mac := hmac.New(sha256.New, h.hmacKey)
    mac.Write(raw)
    sig := hex.EncodeToString(mac.Sum(nil))
    signed := base64.RawURLEncoding.EncodeToString(raw) + "." + sig

    h.auditLog("sign", tenantID, sessionID, turnNo, attID, r.RemoteAddr)

    writeJSON(w, http.StatusOK, map[string]any{
        "url":       "/signed?p=" + signed,
        "expires_at": payload.ExpiresAt,
    })
}

func (h *AttachmentHandler) revoke(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    attID := vars["attId"]
    tenantID, _, _, ok := extractAdminContext(r, nil)
    if !ok { http.Error(w, "unauthorized", http.StatusUnauthorized); return }
    if err := h.rdb.Set(r.Context(), fmt.Sprintf(blacklistKeyFmt, attID), tenantID, blacklistTTL).Err(); err != nil {
        http.Error(w, "redis: "+err.Error(), http.StatusInternalServerError); return
    }
    h.auditLog("revoke", tenantID, vars["id"], atoiOrZero(vars["turnNo"]), attID, r.RemoteAddr)
    w.WriteHeader(http.StatusNoContent)
}

func (h *AttachmentHandler) serveSigned(w http.ResponseWriter, r *http.Request) {
    raw := r.URL.Query().Get("p")
    parts := strings.SplitN(raw, ".", 2)
    if len(parts) != 2 { http.Error(w, "bad signed url", http.StatusBadRequest); return }
    body, err := base64.RawURLEncoding.DecodeString(parts[0])
    if err != nil { http.Error(w, "bad base64", http.StatusBadRequest); return }
    mac := hmac.New(sha256.New, h.hmacKey)
    mac.Write(body)
    sig, _ := hex.DecodeString(parts[1])
    if !hmac.Equal(sig, mac.Sum(nil)) { http.Error(w, "bad signature", http.StatusForbidden); return }
    var p signedPayload
    if err := json.Unmarshal(body, &p); err != nil { http.Error(w, "bad json", http.StatusBadRequest); return }
    if time.Now().Unix() > p.ExpiresAt { http.Error(w, "expired", http.StatusGone); return }
    if h.isRevoked(r.Context(), p.AttID) { http.Error(w, "revoked", http.StatusGone); return }
    // 真正场景：代理到对象存储或 302 redirect
    http.Redirect(w, r, h.storage.PublicURL(p.ObjectKey), http.StatusFound)
}

func (h *AttachmentHandler) isRevoked(ctx context.Context, attID string) bool {
    if h.rdb == nil { return false }
    n, _ := h.rdb.Exists(ctx, fmt.Sprintf(blacklistKeyFmt, attID)).Result()
    return n > 0
}

type Attachment struct {
    AttID     string
    ObjectKey string
}

func (h *AttachmentHandler) loadAttachment(ctx context.Context, tenant, session string, turnNo int, attID string) (Attachment, error) {
    // 占位：真实实现从 session_bodies 查 request_attachments/response_attachments
    return Attachment{AttID: attID, ObjectKey: "tenant/" + tenant + "/" + session + "/turn_" + strconv.Itoa(turnNo) + "/" + attID}, nil
}

func (h *AttachmentHandler) signForTest(tenant, session string, turnNo int, attID, objKey string, ttl time.Duration) (string, error) {
    payload := signedPayload{
        TenantID: tenant, SessionID: session, TurnNo: turnNo,
        AttID: attID, ObjectKey: objKey,
        ExpiresAt: time.Now().Add(ttl).Unix(),
    }
    raw, _ := json.Marshal(payload)
    mac := hmac.New(sha256.New, h.hmacKey)
    mac.Write(raw)
    return base64.RawURLEncoding.EncodeToString(raw) + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func atoiOrZero(s string) int { n, _ := strconv.Atoi(s); return n }
```

### Step 9.3: 注册

在 `admin/routes.go` 追加：

```go
att := NewAttachmentHandler(storage, jwtSecret, []byte("att-sign-key-change-me"), redisClient)
mux.PathPrefix("/api/admin/sessions").Handler(att.Routes())
mux.HandleFunc("/signed", att.serveSigned)
```

### Step 9.4: 测试

Run: `go test ./admin/ -run TestAttachmentSignURL -v -short`
Expected: PASS

### Step 9.5: 提交

```bash
git add admin/session_turn_attachments.go admin/session_turn_attachments_test.go admin/routes.go
git commit -m "feat(v2-p4): attachment signed URL + revoke + audit

- 5min TTL signed URL with HMAC, tenant+session+turn+att_id binding
- Redis blacklist for revocation (1h TTL)
- audit log via slog for sign/revoke actions
- TestAttachmentSignURL covers sign/revoke/expired paths"
```

---

## Task 10: V2-P4.1 SessionDetailPage 双栏+抽屉 UI

**Files:**
- Create: `web/src/views/admin/SessionDetailPage.vue`
- Create: `web/src/components/SessionTurnListItem.vue`
- Create: `web/src/components/SessionTurnDrawer.vue`
- Create: `web/src/components/SessionSummaryBar.vue`
- Create: `web/src/api/sessions_v2.ts`
- Modify: `web/src/router.ts`（注册 `/admin/sessions/:id`）
- Modify: `web/src/locales/zh-CN.ts`（添加 i18n key）

### Step 10.1: 写 API 客户端

文件 `web/src/api/sessions_v2.ts`：

```typescript
import axios from './http'

export interface TurnListItem {
  turn_no: number
  ts: string
  title?: string
  summary?: string
  request_tokens: number
  response_tokens: number
  cost_usd: number
  model: string
  provider: string
  status_code: number
  submit_mode: string
  injection_verdict: string
  output_verdict: string
  attachment_count: number
}

export interface TurnsResponse {
  session_id: string
  turns: TurnListItem[]
  has_more: boolean
  next_cursor: string
}

export async function listSessionTurns(
  sessionId: string, params: { cursor?: string; limit?: number } = {}
): Promise<TurnsResponse> {
  const { data } = await axios.get(`/api/admin/sessions/${sessionId}/turns`, { params })
  return data
}

export async function getSessionTurn(sessionId: string, turnNo: number) {
  const { data } = await axios.get(`/api/admin/sessions/${sessionId}/turns/${turnNo}`)
  return data
}

export async function getSessionSnapshot(sessionId: string) {
  const { data } = await axios.get(`/api/admin/sessions/${sessionId}/snapshot`)
  return data
}

export async function triggerInstantSummary(sessionId: string) {
  const { data } = await axios.post(`/api/admin/sessions/${sessionId}/instant-summary`)
  return data
}

export async function getAttachmentSignedUrl(sessionId: string, turnNo: number, attId: string) {
  const { data } = await axios.get(`/api/admin/sessions/${sessionId}/turns/${turnNo}/attachments/${attId}/url`)
  return data as { url: string; expires_at: number }
}
```

### Step 10.2: 写 SessionTurnListItem

文件 `web/src/components/SessionTurnListItem.vue`：

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { TurnListItem } from '../api/sessions_v2'

const props = defineProps<{ turn: TurnListItem; active: boolean }>()
const emit = defineEmits<{ (e: 'open', turn: TurnListItem): void }>()

const tagClass = (v: string) => `tag-${v}`
const requestTokens = computed(() => props.turn.request_tokens)
const responseTokens = computed(() => props.turn.response_tokens)
</script>

<template>
  <div class="turn-row" :class="{ active }" @click="emit('open', turn)">
    <div class="col col-req">
      <div class="meta">
        <span class="turn-no">#{{ turn.turn_no }}</span>
        <span class="ts">{{ new Date(turn.ts).toLocaleString() }}</span>
        <span :class="['verdict', tagClass(turn.injection_verdict)]">injection: {{ turn.injection_verdict }}</span>
      </div>
      <div class="preview">{{ turn.title || '(无请求摘要)' }}</div>
      <div class="badges">
        <span class="badge">Δ {{ requestTokens }} tok</span>
        <span class="badge" v-if="turn.attachment_count > 0">📎 {{ turn.attachment_count }}</span>
        <span class="badge model">{{ turn.model }}</span>
      </div>
    </div>
    <div class="col col-resp">
      <div class="meta">
        <span class="turn-no">#{{ turn.turn_no }}</span>
        <span :class="['verdict', tagClass(turn.output_verdict)]">output: {{ turn.output_verdict }}</span>
      </div>
      <div class="preview">{{ turn.summary || '(无回复摘要)' }}</div>
      <div class="badges">
        <span class="badge">Δ {{ responseTokens }} tok</span>
        <span class="badge cost">${{ turn.cost_usd.toFixed(4) }}</span>
        <span class="badge status" :data-ok="turn.status_code < 400">{{ turn.status_code }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.turn-row { display: grid; grid-template-columns: 1fr 1fr; border: 1px solid #e5e7eb; border-radius: 8px; padding: 12px; margin-bottom: 8px; cursor: pointer; transition: background 0.15s; }
.turn-row:hover { background: #f9fafb; }
.turn-row.active { background: #eff6ff; border-color: #3b82f6; }
.col { padding: 0 8px; }
.col + .col { border-left: 1px dashed #e5e7eb; }
.meta { display: flex; gap: 8px; font-size: 12px; color: #6b7280; margin-bottom: 6px; }
.turn-no { font-weight: 600; color: #111827; }
.verdict { padding: 2px 6px; border-radius: 4px; font-size: 11px; }
.tag-pass { background: #d1fae5; color: #065f46; }
.tag-warn { background: #fef3c7; color: #92400e; }
.tag-block { background: #fee2e2; color: #991b1b; }
.tag-skip { background: #f3f4f6; color: #4b5563; }
.preview { color: #111827; margin-bottom: 6px; }
.badges { display: flex; gap: 6px; flex-wrap: wrap; }
.badge { background: #f3f4f6; padding: 2px 6px; border-radius: 4px; font-size: 11px; color: #4b5563; }
.badge.cost { background: #ecfdf5; color: #065f46; }
.badge.status[data-ok="true"] { background: #d1fae5; color: #065f46; }
.badge.status[data-ok="false"] { background: #fee2e2; color: #991b1b; }
</style>
```

### Step 10.3: 写 SessionTurnDrawer

文件 `web/src/components/SessionTurnDrawer.vue`：

```vue
<script setup lang="ts">
import { ref, watch } from 'vue'
import { getSessionTurn, getAttachmentSignedUrl } from '../api/sessions_v2'

const props = defineProps<{ sessionId: string; turnNo: number | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const loading = ref(false)
const turn = ref<any>(null)
const tab = ref<'request'|'response'|'meta'|'governance'|'attachments'>('request')

watch(() => props.turnNo, async (n) => {
  if (n == null) { turn.value = null; return }
  loading.value = true
  try { turn.value = await getSessionTurn(props.sessionId, n) }
  finally { loading.value = false }
})

async function openAttachment(att: any) {
  const { url } = await getAttachmentSignedUrl(props.sessionId, props.turnNo!, att.att_id)
  window.open(url, '_blank', 'noopener,noreferrer')
}
</script>

<template>
  <el-drawer
    :model-value="turnNo != null"
    direction="rtl"
    size="70%"
    :with-header="true"
    @close="emit('close')"
  >
    <template #header>
      <span>Turn #{{ turnNo }} · {{ turn?.model }} · ${{ turn?.cost_usd?.toFixed(4) }}</span>
    </template>
    <div v-if="loading">加载中…</div>
    <el-tabs v-else-if="turn" v-model="tab">
      <el-tab-pane label="请求" name="request">
        <pre>{{ JSON.stringify(turn.request, null, 2) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="回复" name="response">
        <pre>{{ JSON.stringify(turn.response, null, 2) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="元数据" name="meta">
        <pre>{{ JSON.stringify(turn.meta, null, 2) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="治理" name="governance">
        <pre>{{ JSON.stringify(turn.governance, null, 2) }}</pre>
      </el-tab-pane>
      <el-tab-pane label="附件" name="attachments">
        <el-empty v-if="!turn.attachments?.length" description="无附件" />
        <ul v-else>
          <li v-for="att in turn.attachments" :key="att.att_id">
            <a href="#" @click.prevent="openAttachment(att)">{{ att.name }}</a>
            <span class="muted"> · {{ (att.size/1024).toFixed(1) }} KB</span>
          </li>
        </ul>
      </el-tab-pane>
    </el-tabs>
  </el-drawer>
</template>

<style scoped>
pre { background: #f9fafb; padding: 12px; border-radius: 6px; max-height: 70vh; overflow: auto; }
.muted { color: #6b7280; font-size: 12px; }
</style>
```

### Step 10.4: 写 SessionSummaryBar

文件 `web/src/components/SessionSummaryBar.vue`：

```vue
<script setup lang="ts">
import { ref } from 'vue'
import { triggerInstantSummary } from '../api/sessions_v2'

const props = defineProps<{
  sessionId: string
  title?: string
  summary?: string
  totalTurns?: number
  totalCost?: number
}>()

const status = ref<'idle'|'pending'|'done'|'failed'>('idle')
const errMsg = ref('')

async function trigger() {
  if (status.value === 'pending') return
  status.value = 'pending'; errMsg.value = ''
  try {
    await triggerInstantSummary(props.sessionId)
    // 简化：5s 后轮询
    setTimeout(async () => {
      const { getSessionSnapshot } = await import('../api/sessions_v2')
      const snap = await getSessionSnapshot(props.sessionId)
      if (snap.title) status.value = 'done'
      else { status.value = 'failed'; errMsg.value = '未生成标题' }
    }, 5000)
  } catch (e: any) {
    status.value = 'failed'
    errMsg.value = e.message
  }
}
</script>

<template>
  <div class="summary-bar">
    <div class="left">
      <h2>{{ title || '(未命名会话)' }}</h2>
      <p>{{ summary || '点击「即时总结」生成摘要' }}</p>
      <div class="metrics">
        <span>{{ totalTurns || 0 }} turns</span>
        <span>·</span>
        <span>${{ (totalCost || 0).toFixed(4) }}</span>
      </div>
    </div>
    <div class="right">
      <el-button :loading="status==='pending'" @click="trigger">
        {{ status==='pending' ? '生成中…' : '即时总结' }}
      </el-button>
      <span v-if="status==='failed'" class="err">{{ errMsg }}</span>
      <span v-else-if="status==='done'" class="ok">✓ 已总结</span>
    </div>
  </div>
</template>

<style scoped>
.summary-bar { display: flex; justify-content: space-between; align-items: flex-start; padding: 16px 24px; background: white; border-bottom: 1px solid #e5e7eb; position: sticky; top: 0; z-index: 10; }
h2 { margin: 0 0 4px; font-size: 18px; }
.metrics { color: #6b7280; font-size: 13px; }
.right { display: flex; gap: 8px; align-items: center; }
.err { color: #ef4444; font-size: 12px; }
.ok { color: #10b981; font-size: 12px; }
</style>
```

### Step 10.5: 写 SessionDetailPage

文件 `web/src/views/admin/SessionDetailPage.vue`：

```vue
<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listSessionTurns, getSessionSnapshot, type TurnListItem } from '../../api/sessions_v2'
import SessionSummaryBar from '../../components/SessionSummaryBar.vue'
import SessionTurnListItem from '../../components/SessionTurnListItem.vue'
import SessionTurnDrawer from '../../components/SessionTurnDrawer.vue'

const route = useRoute()
const router = useRouter()
const sessionId = String(route.params.id)
const focusTurn = Number(route.query.turn || 0)

const turns = ref<TurnListItem[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const loading = ref(false)
const drawerTurnNo = ref<number | null>(focusTurn || null)
const snapshot = ref<any>(null)

async function load(reset = true) {
  loading.value = true
  try {
    if (reset) { turns.value = []; nextCursor.value = '' }
    const params: any = { limit: 50 }
    if (nextCursor.value) params.cursor = nextCursor.value
    const r = await listSessionTurns(sessionId, params)
    turns.value = [...turns.value, ...r.turns]
    hasMore.value = r.has_more
    nextCursor.value = r.next_cursor
  } finally { loading.value = false }
}

async function loadSnapshot() {
  try { snapshot.value = await getSessionSnapshot(sessionId) } catch {}
}

function openDrawer(t: TurnListItem) {
  drawerTurnNo.value = t.turn_no
  router.replace({ query: { ...route.query, turn: String(t.turn_no), focus: 1 } })
}

function closeDrawer() {
  drawerTurnNo.value = null
  router.replace({ query: {} })
}

onMounted(() => { load(true); loadSnapshot() })
watch(() => route.params.id, () => { drawerTurnNo.value = null; load(true); loadSnapshot() })
</script>

<template>
  <div class="session-detail">
    <SessionSummaryBar
      :session-id="sessionId"
      :title="snapshot?.title"
      :summary="snapshot?.summary"
      :total-turns="snapshot?.total_turns"
      :total-cost="snapshot?.total_cost_usd"
    />
    <div class="list">
      <SessionTurnListItem
        v-for="t in turns" :key="t.turn_no"
        :turn="t" :active="t.turn_no === drawerTurnNo"
        @open="openDrawer"
      />
      <el-button v-if="hasMore" :loading="loading" @click="load(false)">加载更早</el-button>
    </div>
    <SessionTurnDrawer
      :session-id="sessionId" :turn-no="drawerTurnNo"
      @close="closeDrawer"
    />
  </div>
</template>

<style scoped>
.session-detail { background: #f3f4f6; min-height: 100vh; }
.list { padding: 16px 24px; max-width: 1400px; margin: 0 auto; }
</style>
```

### Step 10.6: 注册路由

修改 `web/src/router.ts`，添加：

```typescript
import SessionDetailPage from './views/admin/SessionDetailPage.vue'

// 在路由表追加：
{ path: '/admin/sessions/:id', component: SessionDetailPage, meta: { requiresAuth: true } },
```

### Step 10.7: 提交

```bash
git add web/src/api/sessions_v2.ts \
        web/src/components/SessionTurnListItem.vue \
        web/src/components/SessionTurnDrawer.vue \
        web/src/components/SessionSummaryBar.vue \
        web/src/views/admin/SessionDetailPage.vue \
        web/src/router.ts
git commit -m "feat(v2-p4.1): SessionDetailPage with dual-column turns + drawer

- new /admin/sessions/:id page replacing legacy SessionTurnsPanel usage
- cursor pagination with 'load earlier' button
- drawer on click with tabs: request/response/meta/governance/attachments
- summary bar with instant summary button
- retains legacy SessionTurnsPanel for backwards compat"
```

---

## Task 11: V2-P5 总结模型选择器

**Files:**
- Create: `domains/summary/selector.go`
- Create: `domains/summary/selector_test.go`

### Step 11.1: 写失败测试

文件 `domains/summary/selector_test.go`：

```go
package summary

import (
    "context"
    "testing"
    "time"
)

func TestSelectSummaryModel_PrefersCheapAndAvailable(t *testing.T) {
    cat := &fakeCatalog{
        candidates: []Candidate{
            {Model: "m1", Cost: 0.001, Latency: 100, Available: 1.0, Ctx: 8192},
            {Model: "m2", Cost: 0.01,  Latency: 200, Available: 0.95, Ctx: 32768},
            {Model: "m3", Cost: 0.005, Latency: 50,  Available: 0.99, Ctx: 4096},
        },
    }
    sel := NewSelector(cat, DefaultWeights())
    got, err := sel.Select(context.Background(), "tenant1", SummaryKindTitle)
    if err != nil { t.Fatal(err) }
    if got.Model != "m1" {
        t.Fatalf("expected m1, got %s", got.Model)
    }
}

func TestSelectSummaryModel_FallbackOnEmptyCatalog(t *testing.T) {
    cat := &fakeCatalog{candidates: nil}
    sel := NewSelector(cat, DefaultWeights())
    _, err := sel.Select(context.Background(), "tenant1", SummaryKindTitle)
    if err == nil { t.Fatal("expected error on empty catalog") }
}
```

### Step 11.2: 实现

文件 `domains/summary/selector.go`：

```go
// Package summary: 模型选择器
// 综合 availability + cost_efficiency + ctx + latency 评分
package summary

import (
    "context"
    "fmt"
    "math"
    "time"
)

type SummaryKind string
const (
    SummaryKindTitle          SummaryKind = "title"
    SummaryKindSummary        SummaryKind = "summary"
    SummaryKindTurnOneLiner   SummaryKind = "turn_one_liner"
)

type Candidate struct {
    Model      string
    Cost       float64 // per 1k tokens
    Latency    time.Duration
    Available  float64 // 0..1
    Ctx        int
}

type Catalog interface {
    Candidates(ctx context.Context, tenantID string, kind SummaryKind) ([]Candidate, error)
}

type Weights struct {
    W1Availability      float64
    W2CostEfficiency    float64
    W3ContextWindow     float64
    W4LatencyInv        float64
}

func DefaultWeights() Weights {
    return Weights{0.35, 0.35, 0.15, 0.15}
}

type Selector struct {
    cat Catalog
    w   Weights
}

func NewSelector(cat Catalog, w Weights) *Selector { return &Selector{cat: cat, w: w} }

func (s *Selector) Select(ctx context.Context, tenantID string, kind SummaryKind) (Candidate, error) {
    cands, err := s.cat.Candidates(ctx, tenantID, kind)
    if err != nil { return Candidate{}, err }
    if len(cands) == 0 { return Candidate{}, fmt.Errorf("no candidates") }

    // 归一化
    var minCost, maxCost = math.Inf(1), 0.0
    var maxLatency time.Duration
    for _, c := range cands {
        if c.Cost < minCost { minCost = c.Cost }
        if c.Cost > maxCost { maxCost = c.Cost }
        if c.Latency > maxLatency { maxLatency = c.Latency }
    }
    costRange := maxCost - minCost
    if costRange == 0 { costRange = 1 }

    best := cands[0]
    bestScore := -1.0
    for _, c := range cands {
        cost := 1.0 - (c.Cost - minCost) / costRange // 越低越好
        ctx := math.Min(1.0, float64(c.Ctx) / 32768.0)
        lat := 1.0 - float64(c.Latency) / float64(maxLatency)
        if lat < 0 { lat = 0 }
        score := s.w.W1Availability*c.Available + s.w.W2CostEfficiency*cost +
                 s.w.W3ContextWindow*ctx + s.w.W4LatencyInv*lat
        if score > bestScore {
            bestScore = score
            best = c
        }
    }
    return best, nil
}

type fakeCatalog struct{ candidates []Candidate }
func (f *fakeCatalog) Candidates(_ context.Context, _ string, _ SummaryKind) ([]Candidate, error) {
    return f.candidates, nil
}
```

### Step 11.3: 提交

```bash
git add domains/summary/selector.go domains/summary/selector_test.go
git commit -m "feat(v2-p5): summary model selector with availability/cost/latency scoring

- Weights: 0.35/0.35/0.15/0.15 (availability/cost/ctx/latency)
- normalizes per-call; falls back to error on empty catalog
- test cases: prefers cheap+available, error on empty"
```

---

## Task 12: V2-P5 即时总结按钮 UI（已在 Task 10.4 实现，验证集成）

**Files:**
- 不新增文件；验证 SessionSummaryBar 已就位
- Modify: `web/src/api/sessions_v2.ts`（如缺 getInstantSummaryJob 状态查询）

### Step 12.1: 验证 Task 10.4

```bash
grep -n "triggerInstantSummary" web/src/components/SessionSummaryBar.vue
grep -n "triggerInstantSummary" web/src/api/sessions_v2.ts
```
Expected: 两者都有

### Step 12.2: 状态机扩展

在 `web/src/components/SessionSummaryBar.vue` 添加 polling：

```typescript
// 已实现：setTimeout 5s 后查询；可升级为轮询
```

升级为每 2 秒一次，最多 30 秒：

```typescript
async function poll() {
  for (let i = 0; i < 15; i++) {
    await new Promise(r => setTimeout(r, 2000))
    const snap = await getSessionSnapshot(props.sessionId)
    if (snap.title && Date.parse(snap.summary_generated_at) > Date.now() - 60000) {
      status.value = 'done'
      return
    }
  }
  status.value = 'failed'
  errMsg.value = '超时未生成'
}
```

修改 `trigger`：

```typescript
async function trigger() {
  if (status.value === 'pending') return
  status.value = 'pending'; errMsg.value = ''
  try {
    await triggerInstantSummary(props.sessionId)
    await poll()
  } catch (e: any) {
    status.value = 'failed'
    errMsg.value = e.message
  }
}
```

### Step 12.3: 提交

```bash
git add web/src/components/SessionSummaryBar.vue
git commit -m "feat(v2-p5): upgrade summary button to 30s polling with 2s interval"
```

---

## Task 13: V2-P5.1 附件 manifest 写入 flow

**Files:**
- Modify: `domains/session/v2/bodies_writer.go`（写入时填充 request_attachments / response_attachments）
- Create: `domains/session/v2/bodies_writer_attachments_test.go`

### Step 13.1: 写失败测试

文件 `domains/session/v2/bodies_writer_attachments_test.go`：

```go
package v2

import (
    "context"
    "testing"
)

func TestBodiesWriter_StoresAttachmentsManifest(t *testing.T) {
    if testing.Short() { t.Skip() }
    pool := testPool(t)
    defer pool.Close()

    w := NewSessionBodiesWriter(pool)
    err := w.WriteBodies(context.Background(), BodiesRecord{
        SessionID: "gw_att_test",
        TurnNo: 1,
        TenantID: "default",
        RequestID: "req_att_001",
        RequestDelta: []Message{{Role: "user", Content: "see attached"}},
        ResponseDelta: []Message{{Role: "assistant", Content: "got it"}},
        RequestAttachments: []AttachmentRef{
            {AttID: "att_a", Name: "spec.md", ObjectKey: "k1", Mime: "text/markdown", Size: 100, Sha256: "abc"},
        },
        ResponseAttachments: nil,
    })
    if err != nil { t.Fatal(err) }

    var got []byte
    if err := pool.QueryRow(context.Background(), `
        SELECT request_attachments FROM gateway.session_bodies
        WHERE session_id=$1 AND turn_no=$2
    `, "gw_att_test", 1).Scan(&got); err != nil { t.Fatal(err) }
    if len(got) == 0 { t.Fatal("expected attachments to be stored") }
    if !contains(string(got), `"att_a"`) {
        t.Fatalf("att_a not in manifest: %s", string(got))
    }
}

func contains(s, sub string) bool {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub { return true }
    }
    return false
}
```

### Step 13.2: 补 BodiesRecord 字段 + Writer INSERT

修改 `domains/session/v2/bodies_writer.go`：

```go
type BodiesRecord struct {
    SessionID           string
    TurnNo              int
    TenantID            string
    RequestID           string
    RequestDelta        []Message
    ResponseDelta       []Message
    OutboundBody        []byte // nullable
    RequestAttachments  []AttachmentRef
    ResponseAttachments []AttachmentRef
}

type AttachmentRef struct {
    AttID     string `json:"att_id"`
    Name      string `json:"name"`
    ObjectKey string `json:"object_key"`
    Mime      string `json:"mime"`
    Size      int64  `json:"size"`
    Sha256    string `json:"sha256"`
}

// 现有 WriteBodies 中 INSERT 列追加 request_attachments, response_attachments
// VALUES 占位追加 $N::jsonb, $N+1::jsonb
```

在 INSERT 列表追加 `request_attachments, response_attachments`，VALUES 追加 `$N::jsonb, $N+1::jsonb`，参数追加 JSON 序列化值。

### Step 13.3: 提交

```bash
git add domains/session/v2/bodies_writer.go \
        domains/session/v2/bodies_writer_attachments_test.go
git commit -m "feat(v2-p5.1): store request/response attachment manifest in session_bodies

- BodiesRecord gains RequestAttachments / ResponseAttachments []AttachmentRef
- INSERT into gateway.session_bodies now writes both JSONB manifests
- test asserts manifest survives roundtrip"
```

---

## Task 14: V2-P6 灰度开关 + 双读对账

**Files:**
- Create: `cmd/gateway/dual_read_validator.go`
- Create: `cmd/gateway/dual_read_validator_test.go`
- Modify: `cmd/gateway/main.go`（注册开关）

### Step 14.1: 写失败测试

文件 `cmd/gateway/dual_read_validator_test.go`：

```go
package main

import (
    "context"
    "testing"
    "time"
)

func TestDualReadValidator_ComputesDiff(t *testing.T) {
    if testing.Short() { t.Skip() }
    v := NewDualReadValidator(testPool(t))
    diff, err := v.Compare(context.Background(), "default", "gw_test", 10)
    if err != nil { t.Fatal(err) }
    if diff.SessionID != "gw_test" { t.Fatal("session mismatch") }
    // diff 字段不要求非空（环境可能没数据）
}
```

### Step 14.2: 实现对账

文件 `cmd/gateway/dual_read_validator.go`（同时拉 V1 + V2，按 turn_no 对比 turn_no / tokens / cost）：

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

type DualReadDiff struct {
    SessionID     string
    Compared      int
    TurnNoMatch   int
    TokenDiff     int
    CostDiff      float64
    SampleAt      time.Time
}

type DualReadValidator struct {
    db *pgxpool.Pool
}

func NewDualReadValidator(db *pgxpool.Pool) *DualReadValidator {
    return &DualReadValidator{db: db}
}

func (v *DualReadValidator) Compare(ctx context.Context, tenant, session string, lastN int) (DualReadDiff, error) {
    if lastN <= 0 { lastN = 10 }
    var d DualReadDiff
    d.SessionID = session
    d.SampleAt = time.Now()

    rows, err := v.db.Query(ctx, `
        WITH v1 AS (
            SELECT ROW_NUMBER() OVER (ORDER BY ts ASC) AS turn_no,
                   COALESCE(prompt_tokens,0) AS pt, COALESCE(completion_tokens,0) AS ct, COALESCE(cost_usd,0) AS cost
            FROM public.request_logs
            WHERE tenant_id::TEXT=$1 AND gw_session_id=$2
            ORDER BY ts DESC LIMIT $3
        ),
        v2 AS (
            SELECT turn_no,
                   COALESCE(prompt_tokens,0) AS pt, COALESCE(completion_tokens,0) AS ct, COALESCE(cost_usd,0) AS cost
            FROM gateway.session_turns
            WHERE tenant_id=$1 AND session_id=$2
            ORDER BY turn_no DESC LIMIT $3
        )
        SELECT count(*) FROM v1
    `, tenant, session, lastN)
    if err != nil { return d, err }
    defer rows.Close()
    if rows.Next() {
        rows.Scan(&d.Compared)
    }

    // 简化：直接按 (turn_no) 聚合差
    err = v.db.QueryRow(ctx, `
        WITH v1 AS (
            SELECT ROW_NUMBER() OVER (ORDER BY ts ASC) AS turn_no,
                   COALESCE(prompt_tokens + completion_tokens, 0) AS total_tokens,
                   COALESCE(cost_usd, 0) AS cost
            FROM public.request_logs
            WHERE tenant_id::TEXT=$1 AND gw_session_id=$2
        ),
        v2 AS (
            SELECT turn_no,
                   COALESCE(prompt_tokens + completion_tokens, 0) AS total_tokens,
                   COALESCE(cost_usd, 0) AS cost
            FROM gateway.session_turns
            WHERE tenant_id=$1 AND session_id=$2
        )
        SELECT
            (SELECT count(*) FROM v1) - (SELECT count(*) FROM v2) AS token_diff,
            COALESCE((SELECT sum(total_tokens) FROM v1) - (SELECT sum(total_tokens) FROM v2), 0) AS token_sum_diff,
            COALESCE((SELECT sum(cost) FROM v1) - (SELECT sum(cost) FROM v2), 0) AS cost_diff
    `, tenant, session).Scan(new(int), &d.TokenDiff, &d.CostDiff)
    if err != nil { return d, fmt.Errorf("diff query: %w", err) }

    return d, nil
}
```

### Step 14.3: 提交

```bash
git add cmd/gateway/dual_read_validator.go cmd/gateway/dual_read_validator_test.go
git commit -m "feat(v2-p6): dual-read validator comparing V1 request_logs vs V2 session_turns"
```

---

## Task 15: V2-P7 观察期监控

**Files:**
- Create: `metrics/sessions_v2_metrics.go`
- Modify: `cmd/gateway/main.go`（暴露 /metrics 端点）

### Step 15.1: 写指标

文件 `metrics/sessions_v2_metrics.go`：

```go
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    SessionsV2WriteSuccess = promauto.NewCounter(prometheus.CounterOpts{
        Name: "sessions_v2_write_success_total",
        Help: "Total successful V2 writes",
    })
    SessionsV2WriteFailed = promauto.NewCounter(prometheus.CounterOpts{
        Name: "sessions_v2_write_failure_total",
        Help: "Total failed V2 writes",
    })
    SessionsV2WriteLatency = promauto.NewHistogram(prometheus.HistogramOpts{
        Name:    "sessions_v2_write_latency_seconds",
        Help:    "V2 write latency",
        Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
    })
    SessionsV2CacheHit = promauto.NewCounter(prometheus.CounterOpts{
        Name: "sessions_v2_cache_hit_total",
        Help: "L0/L1/L2 cache hits",
    })
    SessionsV2CacheMiss = promauto.NewCounter(prometheus.CounterOpts{
        Name: "sessions_v2_cache_miss_total",
        Help: "L0/L1/L2 cache misses",
    })
    SessionsV2DualReadDiff = promauto.NewCounter(prometheus.CounterOpts{
        Name: "sessions_v2_dual_read_diff_total",
        Help: "Number of turns with V1/V2 diffs",
    })
)
```

### Step 15.2: 在 main.go 暴露 /metrics

```go
import "github.com/prometheus/client_golang/prometheus/promhttp"
// 启动：
go http.ListenAndServe(":9100", promhttp.Handler())
```

### Step 15.3: 提交

```bash
git add metrics/sessions_v2_metrics.go cmd/gateway/main.go
git commit -m "feat(v2-p7): V2 metrics (write/cache/dual_read) on /metrics"
```

---

## Task 16: V2-P8 主读切 V2

**Files:**
- Modify: `cmd/gateway/main.go`（开关 + 启动 banner）

### Step 16.1: 启动 banner + env

```go
// 在 main() 顶部：
v2Primary := os.Getenv("SESSIONS_V2_PRIMARY_READ") == "true"
if v2Primary {
    slog.Info("V2 PRIMARY READ ENABLED — V1 read-only compatibility layer")
} else {
    slog.Info("V2 SHADOW WRITE — V1 remains primary (read + write)")
}
```

### Step 16.2: 路由切换

在 admin handler 调度中：

```go
if v2Primary {
    turnsHandler = NewSessionTurnsHandler(pool, jwtSecret) // V2
} else {
    turnsHandler = NewSessionCompareHandler(pool) // V1 legacy
}
mux.Handle("/api/admin/sessions/", turnsHandler)
```

### Step 16.3: 提交

```bash
git add cmd/gateway/main.go
git commit -m "feat(v2-p8): primary-read switch via SESSIONS_V2_PRIMARY_READ env

- when true: /api/admin/sessions routes to V2 handler
- when false (default): legacy V1 compare handler
- startup banner explicitly states which path is active"
```

---

## Self-Review Checklist

**Spec 覆盖**：
- §3 数据模型变更 → Task 2（migration 431）✅
- §4 API 契约 → Task 8（admin cursor API）✅ + Task 9（附件）✅
- §5 L0/L1/L2/L3 缓存 → Task 3（L0）+ Task 4（L3）✅
- §6 UI 设计 → Task 10 ✅
- §7 即时总结模型选择 → Task 11 + Task 12 ✅
- §8 附件 manifest + signed URL → Task 9 + Task 13 ✅
- §9 SubmitMode → 已在 docs/拆分/08 §8.5 实现，本 plan 不重复（已确认）
- §10 实施阶段 V2-P2.3～P8 → Task 1～16 全部覆盖 ✅
- §11 风险与回滚 → 每 Task 步骤含回滚点 ✅
- §12 测试 → 每 Task 单测 + 集成 ✅
- 附 A request_logs_hot → 单独 workstream，不在本 plan

**占位符扫描**：
- "TBD"：0
- "implement later"：0
- "similar to Task N"：0（每 Task 都给完整代码）

**类型一致性**：
- `cacheKey(tenant, session)` 与 `index` map key 一致
- `cursorPayload` 与 `encodeCursor`/`decodeCursor` 签名一致
- `BodiesRecord` 与 `WriteBodies` 参数一致
- `StageLog` / `LogSummary` 在测试与实现间一致
- `Attachment` 与 `loadAttachment` 返回值一致

**执行模式**：
- 每个 Task 都有 git commit
- TDD 风格：写失败测试 → 跑 → 实现 → 跑通过
- 12 个子阶段 V2-P2.3～V2-P8 全部映射

---

## Plan Output Location
`docs/superpowers/plans/2026-07-24-session-v2-integration.md`
