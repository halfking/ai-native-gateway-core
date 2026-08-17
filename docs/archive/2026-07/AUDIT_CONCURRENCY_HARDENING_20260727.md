---
archived_from: (legacy) docs/archive/2026-07/AUDIT_CONCURRENCY_HARDENING_20260727.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# Concurrency Hardening — Audit Report

**Session:** 2026-07-27 · branch `fix/concurrency-hardening-20260727`
**Scope:** 全仓多线程代码审计：资源竞争 / 死锁 / 锁状态 / 互锁干扰，改锁、提效、增安全。
**Result:** ✅ GO — 77 files, build clean, `go vet ./...` clean, `go test -race` 61 packages / 0 races.

## Audit method (双轴)

1. **Standards 轴** — `go vet ./...` + `go build ./...` + `gofmt -l` + 5 路并行源码审计（streaming/transformation、credential、routing/pool/cache、session/hooks、bg/internal/eventbus），分类：data race / lock-held-across-I/O / 锁序 / 重入自死锁 / 漏 Unlock / mutex 按值拷贝 / RWMutex 误用 / WaitGroup 误用 / goroutine 泄漏 / channel 误用。
2. **Spec 轴** — 每条修复对齐原始缺陷，不越界、不改公开签名（唯一例外 `metrics.Global` 从变量改函数，已确认包外零调用）。

## P0 fixed (production-path races / deadlocks)

| Defect | Fix |
|---|---|
| `domains/streaming/executors/executor.go` startAsyncRetry 把客户端 `ResponseWriter` + 6 个 `On*` 回调随 `*params` 整体拷进 detached goroutine，handler 写完 202 后 runAsyncRetry 仍向死掉的 writer 写 → 响应串包 / superfluous WriteHeader | `bgParams.W=nil` + `SuppressSuccessWrite=true` + 清空所有回调；新增 `responseSink()` 丢弃 writer，downstream 流式/非流式写改用它（upstream body 仍完整读入 Capture） |
| `domains/streaming/executors/ttfb_tracker.go` Get 返回 map 内 `*TTFBStats` 指针，与 Record 原地修改形成竞争 | 返回结构体副本，共享指针不逃逸锁 |
| `domains/credentialstate/cache.go` + `manager.go`：`sync.Map` 里的共享 `*State` 被直接交出，Update* 在请求路径无锁原地改，且 `go setToRedis` 把同一个 live `*State` 丢给 goroutine 做 json.Marshal | `getFromMemCache` 返回副本；Update* 用 per-key mutex 串行化 read-modify-write；所有 setToRedis goroutine 拿独立 snapshot |
| `domains/credential/scoring.go` SnapshotScore 持 RLock 却写 `b.scores`（map 写，fatal） | 改为只读路径，miss 时合成默认值，不触碰 map |
| `cache/semantic/memory.go` Lookup 持 RLock 却 `c.s.Lookups++`（计数丢失 + -race） | 计数器全改 atomic（对齐 cache/kv/memory.go） |
| `domains/hooks/observability/types.go` Counter/Histogram 字段在 RLock 外被每请求并发改 | Counter.Value 改 atomic；Histogram 加 per-metric mutex，快照深拷贝 Counts |
| `domains/dbdegradation/generic_recovery.go` execute 写 task 字段无锁，admin handler 同时 json.Marshal 同一 `*RecoveryTask` | 写全部加 `task.mu`；Status() 返回字段级 snapshot，live 指针不再外逃 |
| `domains/dbdegradation/ttl_manager.go` stopCh/doneCh 一次性创建，Enter→Exit→Enter 第二个 runExtendLoop `defer close(doneCh)` panic | 每次启动循环重建一对 channel；`stopLoopLocked()` 单一 close 点 |

## P1 fixed (latent races / deadlock risks / goroutine leaks)

- `pool/pool.go` evictIdle / evictOldestLocked / CloseAll：锁内 Close()（含 wg.Wait + 5s 探针）→ 全部改为「锁内 unlink，锁外 Close」。
- `ratelimit/redis_sliding.go`：`l.shaOnce = sync.Once{}` 重置竞争 + waitForRecovery 无 stop / 可重入 → 显式 loaded 标志（l.mu）+ recoveryStop channel + 单实例 recovering 守卫。
- `circuit/breaker.go` callHalfOpen：halfOpenMu 跨真实上游调用 → CAS 抢探测名额后释放锁再调 fn()。
- `safety/filter.go`：RLock 跨 wg.Wait + CPU 扫描 → 快照 matcher 后解锁再匹配；`len(cf.rules)` 读入 RLock。
- `autoroute/embedding_classifier.go`：process mutex 跨 BeginTx→FOR UPDATE→Commit → 删除（行级 FOR UPDATE 已串行化）。
- `domains/credentialstate/popularity_tracker.go`：refresh 换 map 无锁 → RWMutex；构造函数建 Ticker 永不 Stop → 挪到 Start/Stop。
- `domains/credential/{health,limiter,flusher}.go` + `batch_writer.go`：lost-update RMW、bare `close()` double-Stop panic、Stop-without-Start 死等 → 全部加 sync.Once / started 守卫。
- `domains/credential/bandit.go` GetScore 返回 raw `*BanditScore` → 返回值副本；Sample 热路径独占写锁 → RLock fast-path + miss 升级重检。
- `bg/*`（concurrency_peak_collector / stats_minute_rollup / tuning_store_refresher / concurrency_auto_scaleup / call_history_aggregator / health_auto_recover / active_probe_worker / node_probe / systemmonitor）：Stop-without-Start 死等、unguarded close、probeState 字段锁外读 → 统一 sync.Once + started 守卫；字段拷进局部。
- `eventbus/memory_bus.go`：per-handler goroutine 不被 wg 跟踪 + 无界 → 每事件单 goroutine 按注册顺序串行，wg.Add 在父侧。
- `telemetry/dashboard_events.go`：Stop hang + double-close + 每 flushSize 跨界 `go flush()` → sync.Once / started 守卫 + 容量 1 信号 channel。
- `internal/logging/logging.go` Reconfigure 原地改 lumberjack 字段，与其后台 mill goroutine 竞争 → 改为 ReInit 同款整体替换（保留 File/LocalTime）。
- `internal/trace/snapshot.go`：Unlock→赋值→Lock（defer Unlock 已挂起）→ 在持锁内直接赋值。
- `domains/hooks/{config_manager,registry}.go`：watchLoop 读字段 vs Stop 重建 channel 竞争 / ReloadConfig 持 RLock 调回调（回调再取写锁自死锁）→ stopCh 快照入参 / 回调列表拷出后解锁再调。
- `domains/sessionstate/state_machine.go`：Transition 持写锁跑用户回调 + 把受锁 metadata 直接交出 → 锁内匹配+快照 → 解锁跑回调 → 重锁提交（并发态变更则 abort，语义保持）。
- `domains/hooks/observability/telemetry/{client,request_logger}.go`：worker 已启动后无锁写 onPersisted/onEmitted/fallback → hookMu / rl.mu 保护读写两侧。
- `domains/session/session.go` SetDBWriter/SetFileWriter + `sessionforensics/service.go` SetSummarizer：热路径无锁字段写 → atomic.Pointer。
- `domains/session/session_db_writer.go`：锁内 `go flush()` 无界 → 容量 1 信号 channel，锁外投递。
- `domains/feishubot/dedup.go`：gcLoop 无 stop + Configure 每次新建 Deduper 泄漏 → stopCh + sync.Once Close，Configure 替换前 Close 旧的。
- `domains/hooks/audit/audit.go`：HasThinking/ThinkingBlocksN 流 goroutine 无锁写、SummaryAsMap 锁内读 → 新增 MarkThinkingBlock / SetHasThinking / ThinkingSummary 带锁 setter；streaming/transformation 全部调用点已迁移。
- `domains/hooks/security/hook.go`：每请求取写锁 reload 配置 → atomic.Pointer + 5s TTL 异步刷新，热路径无锁 load。

## P2 fixed (efficiency / lock-primitive)

- `domains/streaming/{executors/sticky.go,response_format_adapter.go}`：ticker/loop 无 stop → Close()+sync.Once。
- `autoroute/feature_flags.go` `globalFeatureFlags` 裸指针 → atomic.Pointer。
- `autoroute/index.go` `idx.pool` 锁外读 → RLock 快照（对齐 get48hFallback）。
- `autoroute/decision.go` + `decision_v2.go`：Get→HitCount++→Put 丢失更新 → 新增 `SessionIntentCache.IncrementHit` 单写锁 RMW。
- `domains/routing/sticky.go` SetDB 锁外写 + 异步 dbSet 读 → SetDB 加锁 + `dbPoolSnapshot()`，签名扩参传 pool，锁不跨 DB I/O。
- `metrics/interface.go` `Global` 裸变量 → atomic.Pointer（API：变量改 `Global()` 函数，全仓零外部调用）。
- `discovery/{discovery,alias_sync}.go` + `internal/quality/scheduler.go`：Stop 后 Start 复用已关闭 stopCh → Start 内重建。
- `internal/logging/anomaly_reporter.go`：每异常一个 goroutine + 请求 ctx → 单 drain goroutine（对齐 lockfree 实现）。
- `bg/systemmonitor/monitor.go` IsFallback 全量 Mutex 读 bool → RWMutex。

## Skipped (out-of-scope or non-existent)

- `domains/transformation/lockfree_circuit_breaker.go`：grep 确认生产代码未使用（仅测试）→ 不改。
- `domains/credential/redis_identity.go` 的 `verifyConsistency` per-read goroutine：该符号在当前分支不存在（仅存在于另一 worktree）。

## Verification

```
gofmt -l <all changed>      → clean
go build ./...              → 0 errors
go vet ./...                → 0 findings
go test -race -count=1 \
  <61 changed packages>     → 61 ok, 0 FAIL, 0 DATA RACE
```

## Out-of-scope rollback during audit

`domains/streaming/executors/{router_scoring.go,router_scoring_test.go}` 被某 fix-agent 误改（回滚了既有的 latency-score 语义），非并发缺陷、非本次任务范围 → `git checkout main --` 还原。

---

**Reviewer:** ZCode (autonomous, session 2026-07-27)
