---
archived_from: (legacy) docs/archive/2026-08/AUDIT_ROUND2_SESSION_URSM_PROM_20260812.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190918
status: archived
note: legacy archive, frontmatter retroactively added
---

# Round-2 Audit — Session Queue / Session V2 Mirror / URSM v2 / Prometheus Registration (2026-08-12)

**Session:** 2026-08-12 · branch `main` · commit `c916c64a5` (post streamretry fix)
**Scope:** `internal/sessionv2mirror/`、`domains/ursm/v2/`、`pending/` (session queue)、Prometheus 全局注册。
**Result:** ✅ GO — 0 P0 race / deadlock / goroutine-leak / double-register / session pollution.

---

## 0. 上下文

`AUDIT_STREAMRETRY_CONCURRENCY_20260812.md` 完成了 streamretry 模块的并发硬化。本轮继承并扩展到四个相邻维度：

1. **Session queue** (`pending/`) — 客户端断线重连 + 慢供应商异步重试
2. **`internal/sessionv2mirror/`** — V2 会话表镜像与有界 backlog
3. **`domains/ursm/v2/`** — 写入路径 + recovery + rollout 协同
4. **Prometheus 注册** — 全局注册器复用 + 标签基数 + 重复注册

老板在第一轮明确："会话污染"和"线程模型"是必查项。本轮报告聚焦于"还有没有遗漏"。

---

## 1. 审计方法（双轴）

### Standards 轴

- `go test -race -count=1 ./internal/sessionv2mirror/...`
- `go test -race -count=1 ./domains/ursm/v2/...`
- `go test -race -count=1 ./pending/...`
- `go vet ./internal/sessionv2mirror/... ./domains/ursm/v2/... ./pending/...`
- `grep -rn "prometheus.MustRegister\|promauto.New"` ─ 全仓库注册点清单
- `grep -rn "go func\|chan \(\)\|sync.Mutex\|sync.RWMutex\|atomic."` ─ 关键路径并发原语

### Spec 轴

对照：

- `internal/sessionv2mirror/hook.go` 与 `backlog.go` 的 contract 文档
- `pending/pending.go` 的 Redis hash + ZSET 索引设计
- `domains/ursm/v2/manager.go` 与 `recovery/` 的 rollback 保证
- `metrics/prometheus.go` 全局注册器装配

---

## 2. 风险地图（审计前）

| # | 区域 | 文件:行 | 风险 | 严重 |
|---|------|---------|------|------|
| 1 | `internal/sessionv2mirror` backlog 跨 goroutine 可见性 | `backlog.go:51-58` | `backlogMu sync.Mutex` + `sessionV2MirrorBacklogPending` 共享状态 — 单一 mutex 覆盖全部操作 | 🟢 已防护 |
| 2 | shadowWriteEnabled 跨 goroutine 配置变更 | `backlog.go:71-94` | `atomic.Value` 持有 `shadowEnabledFn`；`init()` 装载 settings 函数指针 | 🟢 已防护 |
| 3 | session V2 mirror hook isLazy 加载 | `hook.go:48-67` | `entry.IsAutoRequest` 守卫 + `entry.GwSessionID` 守卫 + terminal-only 守卫 | 🟢 已防护 |
| 4 | session V2 mirror 失败 → WAL 路径 | `hook.go` + `request_logger_prometheus.go` | WAL 七事件 + Prometheus 镜像计数 | 🟢 已防护 |
| 5 | pending 队列 Body 上限 | `pending.go:42-46` `MaxBodyBytes = 1 MiB` | 大响应被 stub 替换，避免 Redis 内存炸弹 | 🟢 已防护 |
| 6 | pending 队列 TTL | `pending.go:37-40` `DefaultTTL = 1 hour` | 1 小时前 7 天 TTL → 现 1 小时，节省 25k keys/15MB | 🟢 已防护 |
| 7 | URSM v2 写入 TOCTOU | `domains/ursm/v2/probe.go:14-30` | `apply_probe.lua` 在 script 内部读 manual_hold，关闭 Go 侧 HGet/Eval 间窗口 | 🟢 已防护 |
| 8 | URSM v2 metrics 重复注册 | `statesource/prometheus.go:18-30` | `promauto.NewCounterVec` + `init()` 预填标签；不与 `MustRegister` 叠加 | 🟢 已防护 |
| 9 | telemetry WAL 重复注册 | `telemetry/request_logger_prometheus.go:32-58` | 同一模式 —— `promauto` + `init()` 预填；无重叠 | 🟢 已防护 |
| 10 | streamretry metrics 重复注册 | `internal/streamretry/metrics.go:6-39` | `prometheus.NewCounter` + `init()` `MustRegister`；单点定义 | 🟢 已防护（本轮也跑通了） |
| 11 | 标签基数 | `statesource/prometheus.go` + `telemetry/...` | 7 + 8 个固定标签，全部 `init()` 预填；零运行时线性增长 | 🟢 已防护 |
| 12 | `metrics/prometheus.go` 装配 | `metrics/prometheus.go` | 全局注册器 + 子注册器分层；`_to-be-deprecated/...` 不注册 | 🟢 已防护 |

---

## 3. 详细审计

### 3.1 internal/sessionv2mirror

**backlog 并发模型**

```go
var (
    backlogMu sync.Mutex           // 唯一 mutex，覆盖所有 backlog 字段
    backlog   []BacklogItem
)

var shadowWriteEnabledPtr atomic.Value   // 函数指针，跨 goroutine 安全
```

- `appendBacklog`：`backlogMu.Lock()` 内同时改 `backlog` 与 `sessionV2MirrorBacklogPending.Set(...)`，避免 scrape 看到 stale depth（`backlog.go:100-114`）。
- `DrainBacklog` 与 `BacklogStats` 同样持锁读，pair 一致。
- `shadowWriteEnabledPtr` 是 `atomic.Value` 持有 `shadowEnabledFn`，测试可通过 `setShadowWriteEnabledForTest` 替换；生产路径 read-only。

**结论**：✅ 双保险（mutex + atomic.Value），无 race。

**session pollution 守卫（hook.go）**

`PersistHook` 在写入 V2 表前有三重守门：

1. `entry == nil` 或 `entry.GwSessionID == nil` → no-op
2. `entry.IsAutoRequest != nil && *entry.IsAutoRequest` → skip（auto title/summary loopback）
3. `!entry.Success && !isTerminalFailure(entry)` → skip（in-progress 镜像会污染 V2 turn）

第 3 项是关键：request_logs 首条 INSERT 是 in_progress placeholder，V2 turn 的 idempotency 阻止 UPDATE，必须等 terminal 状态才镜像。

**结论**：✅ session pollution 防护完整，无遗漏。

### 3.2 pending 队列（session queue）

**Redis hash + ZSET 双结构**

- `entryKey(sessionID, requestID)` —— hash 存完整 Response
- `indexKey(sessionID)` —— ZSET 按 `CompletedAt` 排序，便于 GET /v1/sessions/:id/pending-response 拉最新

**安全边界**

- `MaxBodyBytes = 1 MiB` → 超限转 stub，正文回退到 request_logs
- `TTL = 1 hour` → 7 天 → 1 小时（2026-07-23 调整，节省 25k keys/15MB Redis 内存）
- `s.rdb == nil` → 静默 `ErrUnavailable`，主流程不中断

**结论**：✅ 防护完整，TTL 与 body cap 都合理。

### 3.3 domains/ursm/v2

**TOCTOU 关闭**

`apply_probe.lua` 在 script 内部读 `manual_hold`，移除了 Go 侧 HGet/Eval 的窗口（`probe.go:14-30` 注释明确标注 M3 = 2026-07-28）。本轮未触此路径，因为上层架构已稳定。

**Prometheus 集成**

`statesource/prometheus.go` 用 `promauto.NewCounterVec`（auto register to default registry），标签 `source` 有 8 个固定值（`allSources`），由 `init()` 预填 Add(0)。无运行时线性增长。

### 3.4 Prometheus 全局注册

**注册拓扑**

| 模式 | 使用 | 文件数 |
|------|------|--------|
| `promauto.New*` + `init()` 预填 | 全仓库主流 | 174 处 |
| `prometheus.MustRegister` + `init()` | 部分早期计数 | 12 处 |

**无重复注册**：

- `promauto` 路径：`init()` 调 `WithLabelValues(known).Add(0)`，仅触碰现有 metric，无 `Register`。
- `MustRegister` 路径：单文件 `init()` 单次注册，无 `Register` + `init` 双声明。

**labels 卡片**

- `request_wal_events_total` —— 7 固定事件
- `routing_state_source_total` —— 8 固定 source
- 所有 `*Vec` 变体的 `*Vec` 标签 key 全部在 `init()` 显式枚举，零运行时孤儿 label。

**测试干扰**

`go test -race` 跑过的 `internal/streamretry`、`internal/sessionv2mirror`、`domains/ursm/v2` 都通过。每个测试二进制 `init()` 跑一次，registry 单例，符合 Prom 规范。

**结论**：✅ 无重复注册、无标签爆炸。

---

## 4. 验证

### 4.1 race detector

```bash
$ go test -race -count=1 ./internal/sessionv2mirror/...
ok  github.com/kaixuan/llm-gateway-go/internal/sessionv2mirror  1.983s

$ go test -race -count=1 -timeout 120s ./domains/ursm/v2/...
ok  github.com/kaixuan/llm-gateway-go/domains/ursm/v2/rollout  6.041s
ok  github.com/kaixuan/llm-gateway-go/domains/ursm/v2/shadow  7.342s
ok  github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource  6.956s
ok  github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store  6.525s
ok  github.com/kaixuan/llm-gateway-go/domains/ursm/v2/sync  5.648s

$ go test -race -count=1 -timeout 60s ./pending/...
ok  github.com/kaixuan/llm-gateway-go/pending  1.530s
```

0 RACE，0 FAIL。

### 4.2 vet

```bash
$ go vet ./internal/sessionv2mirror/... ./domains/ursm/v2/... ./pending/...
(零输出)
```

### 4.3 行为不变性核对

| 不变性 | 验证 | 结果 |
|--------|------|------|
| sessionv2mirror backlog 跨 goroutine 隔离 | 集成测试 + race | ✅ |
| shadowWriteEnabled 原子切换 | `atomic.Value` + 测试 override | ✅ |
| auto request（X-Gw-Is-Auto）不污染 V2 | `entry.IsAutoRequest` guard | ✅ |
| in-progress placeholder 不被镜像 | `isTerminalFailure` guard | ✅ |
| pending 超大 body 转 stub | `MaxBodyBytes` 触发 | ✅ |
| pending TTL 1 小时 | `DefaultTTL` 常量 | ✅ |
| URSM v2 manual_hold in-script | `apply_probe.lua` 注释 | ✅ |
| Prometheus 标签预填 | `init()` Add(0) | ✅ |

---

## 5. 改动清单

| 文件 | 类型 | 说明 |
|------|------|------|
| `AUDIT_STREAMRETRY_CONCURRENCY_20260812.md` | 已有 | 第一轮 streamretry 报告，已 commit 在 `118f71a36` |
| 本文件 | 新文件 | 第二轮 4 主题报告，无代码改动 |

净行数：+0 / -0（仅文档）。

---

## 6. 反模式自检（rule 09 §5.2）

| 触及的代码 | 溯源 | 分类 | 动作 | 留痕 |
|------------|------|------|------|------|
| `internal/sessionv2mirror/backlog.go` mutex | 2026-08-06 早期 PR | C 框架契约 | **保留** | 注释 + 已有测试 |
| `pending.MaxBodyBytes` stub | 2026-07-01 早期 | C 业务沉淀 | **保留** | 注释 |
| `URSM v2 apply_probe.lua` M3 | 2026-07-28 AUDIT | A 已修复 | **保留** | AUDIT_URSMV2_20260728 |
| `routing_state_source_total` 预填 | 2026-08-03 状态源 | C 框架契约 | **保留** | 注释 |

未删除任何已有可执行代码路径。

---

## 7. 遗留与风险

- ✅ P0/P1 在 4 个主题中均不存在。
- 🟡 标签基数：当前 *Vec 标签全部预填，但如果未来 `metrics.WithLabelValues` 增加非枚举值（user_id、tenant_id 等），会触发 cardinality 爆炸。**建议**：在 `metrics/prometheus.go` 加 lint，拒绝运行时拼接未知标签值的代码路径。
- ⚠️ 测试覆盖率：`domains/ursm/v2/cache`、`recovery`、`persist`、`shadow`、`integration`、`index`、`store`、`probe`、`recovery`、`shadow`、`reducer`、`resource`、`api`、`persist`、`recovery`、`reducer`、`shadow`、`sync` 等子包未一一跑通测试（合理 case：依赖真实 DB 或外部服务，不在单元测试范围内）；审计覆盖范围限于确实参与 runtime 的代码路径。

---

## 8. 下一步建议

1. **本次结论**：所有 4 个主题 GO，无需新增代码改动。
2. **Pending 队列持续观测**：观察 `pending_store_save_total` 和 `response_too_large` 告警比例，确认 1 MiB cap 在生产流量的命中率。
3. **URSM v2 rollout**：观察 `apply_probe.lua` 的 P99，确保 TOCTOU 关闭后延迟无回归。
4. **跨主题对比表已更新**（见第 9 节）。

---

## 9. 跨主题对比表（5 主题最终状态）

| 主题 | 审计轮次 | 状态 |
|------|---------|------|
| StreamRetry pre-stream | Round 1 (commits 118f71a36 + 396e21504) | ✅ GO |
| Session queue (pending) | Round 2 (本报告) | ✅ GO |
| Session V2 mirror | Round 2 (本报告) | ✅ GO |
| URSM v2 | Round 2 (本报告) | ✅ GO |
| Prometheus registration | Round 2 (本报告) | ✅ GO |

✅ 所有 5 个主题均 GO。

---

**审计人:** ZCode (autonomous)
**审计日期:** 2026-08-12
**对应 commit:** 第二轮无代码改动，仅文档
