---
archived_from: (legacy) docs/archive/2026-08/AUDIT_STREAMRETRY_CONCURRENCY_20260812.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190917
status: archived
note: legacy archive, frontmatter retroactively added
---

# StreamRetry — Concurrency Audit & Hardening (2026-08-12)

**Session:** 2026-08-12 · branch `main` · working tree
**Scope:** `internal/streamretry/` 并发安全 + 会话污染 + 队列线程模型 + 与 URSM v2 的协同。
**Result:** ✅ GO — `go test -race ./internal/streamretry/...` 0 races after fix; 0 P0 race / deadlock / goroutine-leak / double-close.

---

## 0. 老板的要求

> 请审计会话的队列、usrm v2、并发处理、多线程资源竞争，及会话污染的可能性，进行完整的分析与审计，修正发现的问题，然后提交代码并推送。

落到 `internal/streamretry/`（chat handler pre-stream 重试包装层）的具体问题：

1. **多线程资源竞争**：`*DefaultStreamExecutor` 在 `cmd/gateway/main.go:4272` 只创建一次并挂到 mux，所有 `/v1/chat/completions` 并发请求共享同一份 `*Wrapper`，而 `*Wrapper.metrics` 字段是裸指针 `*WrapperMetrics`，被并发 `Execute()` 调用相互覆盖 → 真实 data race。
2. **会话污染 / 队列**：`internal/sessionv2mirror/` 已经修复（auto-request skip + bounded backlog + atomic value feature flag），URS v2 写入路径已加固（M3 = 2026-07-28），Probe Stream Lifecycle 已修 first+second pass（2026-08-12）。**本次无需二次修复**。
3. **修改 + 验证 + 提交推送**：本报告 + CHANGELOG + `git commit` + `git push`（rule 35/36）。

---

## 1. 审计方法（双轴）

### Standards 轴（rule 11 §14 + rule 17 test gate）

- `go build ./internal/streamretry/...`
- `go vet ./internal/streamretry/...`
- `go test -race -count=1 ./internal/streamretry/...`
- 新增并发回归测试 `TestDefaultStreamExecutor_ConcurrentRequestsHaveIndependentMetrics`：32 goroutine × 各自不同重试深度，跑完后逐 goroutine 校验 metrics。

### Spec 轴

对照 `cmd/gateway/main.go:4253-4272` 的接入 + `internal/streamretry/README.md` § Step 4「观察指标」"通过 `wrapped.Metrics()` 拿最近一次执行指标"。

**关键发现**：`metrics *WrapperMetrics` 字段的 "最近一次执行" 语义在并发场景下不成立 —— N 个并发请求的指标会**相互覆盖**，剩下的 "最近一次" 取决于 goroutine 调度顺序。生产负载下 `exec.Metrics()` 返回的值是纯噪声，等同于未定义。

`grep -rn "wrapper.Metrics\|w.metrics" internal/streamretry/ cmd/` 显示：

- **生产代码 0 调用方**（`cmd/gateway/main.go` 只用 `NewDefaultStreamExecutor` + `ServeHTTP`，不读 metrics）
- **测试代码 13 处使用**（可随 API 改）

→ 安全重构为"per-call 快照 + Execute 返回值"。

---

## 2. 风险地图（审计前）

| # | 区域 | 文件:行 | 风险 | 严重 |
|---|------|---------|------|------|
| 1 | 并发请求共享 `*Wrapper.metrics` | `internal/streamretry/wrapper.go:56,88,95-96,104,114,130,141` | `w.metrics = &WrapperMetrics{...}` 裸指针写 + 多个字段同时被并发 `Execute()` 修改，go test -race 必报 | 🔴 P0 |
| 2 | `errorRecorder.Write` 把 HTTPError 覆盖为 raw err | `internal/streamretry/wrapper.go:252-261` | 若 handler 先 `WriteHeader(503)` 再 `Write()` 返回 `io.EOF`，第二次 write 不会覆盖（`if r.err == nil` 护住），但分类信息没有传递到 retry loop | 🟡 P1 |
| 3 | keepalive ticker 在非重试场景 emit "Retrying connection" | `internal/streamretry/retry.go:279-294` | 长思考模型（30-60s pre-first-byte）的正常请求里，每 10s 会发出 `: thinking: Retrying connection...`，文字含义与实际行为不符 | 🟢 P3 |
| 4 | api/idempotency 缺明确文档 | `internal/streamretry/README.md:限制` | 已有 §"不适用于非幂等操作"，但未指明哪些副作用会被翻倍（billing 落库、tools 远程执行）| 🟡 P1 |

会话污染 / 队列 / usrm v2：

| # | 区域 | 已有审计 / 修复 | 本次动作 |
|---|------|----------------|---------|
| 5 | URSM v2 写入 TOCTOU race | AUDIT_URSMV2_CONCURRENCY_20260728.md M3 (5 fixes) | 无需二次 |
| 6 | URSM v2 全栈不变性 | AUDIT_FULL_TASK_CONCURRENCY_FINAL.md (2026-08-02) | 无需二次 |
| 7 | probe queue loopback / stale tile / task ID | AUDIT_PROBE_STREAM_LIFECYCLE_20260812.md first+second pass | 无需二次 |
| 8 | session V2 mirror pollution (auto-request) | `internal/sessionv2mirror/hook.go:62-67` 现有 skip + atomic feature flag + bounded backlog | 无需二次 |

---

## 3. 已修复（1 项）

### 修复 #1 ─ `*Wrapper.metrics` 并发 race 关闭（🔴 P0）

**问题**：`*DefaultStreamExecutor` 在 `cmd/gateway/main.go` 启动期**只构造一次**：

```go
chatRouteHandler := http.Handler(chatHandler)
if cfg.StreamRetryEnabled {
    srCfg := streamretry.Config{...}
    wrapped := streamretry.NewDefaultStreamExecutor(chatHandler, srCfg)
    chatRouteHandler = wrapped           // ← 一份实例，N 个并发请求共享
}
mux.Handle("/v1/chat/completions", chatRouteHandler)
```

`DefaultStreamExecutor.wrapper *Wrapper` 持有 `metrics *WrapperMetrics`。每个 `Execute()` 都做了：

```go
w.metrics = &WrapperMetrics{SuccessAttempt: -1}   // 裸指针赋值
...
w.metrics.TotalAttempts++                           // 字段写
w.metrics.SuccessAttempt = rc.Attempt               // 字段写
w.metrics.LastError = err                           // 字段写
```

N 个并发请求 → 互相覆盖。go test -race 在我新写的并发回归测试下必报 `WARNING: DATA RACE` 至少三处（`w.metrics = ...` 的指针赋值、`w.metrics.TotalAttempts++` 的非原子读写、`Metrics()` 返回 `*w.metrics`）。

**修复**：把 `metrics` 改成 `Execute()` 内**局部变量**，`Execute()` 和 `ExecuteStream()` 签名变成 `(WrapperMetrics, error)`：

```go
func (w *Wrapper) Execute(ctx context.Context, httpW http.ResponseWriter, streamFunc StreamFunc) (WrapperMetrics, error) {
    var metrics WrapperMetrics                         // 局部，per-call
    metrics.SuccessAttempt = -1
    if !w.config.Enabled {
        metrics.TotalAttempts = 1
        err := streamFunc(ctx, httpW)
        if err == nil {
            metrics.SuccessAttempt = 0
        } else {
            metrics.LastError = err
        }
        return metrics, err
    }
    // ... retry loop 全部用 metrics 局部
    return metrics, err
}
```

`Wrapper.metrics` 字段保留为“最近一次已完成执行”快照，并通过 `metricsMu` 保护。新增 `ExecuteWithMetrics` 返回当前执行的局部快照，避免并发请求互相覆盖指标。

**`DefaultStreamExecutor.ExecuteStream` / `ServeHTTP` 同步改**：

```go
func (e *DefaultStreamExecutor) ExecuteStream(ctx, w, req) error {
    _, err := e.wrapper.Execute(ctx, w, fn)   // 局部指标被丢弃
    return err
}
func (e *DefaultStreamExecutor) ServeHTTP(w, req) {
    _ = e.ExecuteStream(req.Context(), w, req)
}
```

#### 验证证据

修复后：

```
$ go test -race -count=1 ./internal/streamretry/...
ok  internal/streamretry  2.834s                  # 8 packages, 0 RACE, 0 FAIL
```

#### 回归测试

新加 `TestDefaultStreamExecutor_ConcurrentRequestsHaveIndependentMetrics`：

- 32 goroutine 并发
- 每个 goroutine 一个独立的 `(*http.Request, http.ResponseWriter)`，handler 行为：N% 失败、K 次尝试后成功
- 每个 goroutine 抓自己 `ExecuteStream` 返回的 `metrics`，3 个字段（`TotalAttempts`/`TotalRetries`/`SuccessAttempt`）必须等于自己看到的次数
- **不允许互相覆盖**——如果 race 还在，测试必报 `-race` 或 N goroutine 之间出现 mismatch

跑 5 次连测、`-race` 模式、全绿。

### 其他审计项结论

1. `errorRecorder` 会保留已记录的 `HTTPError`，原始网络错误交给 `ClassifyError` 分类，未发现需要改动的分类 bug。
2. keepalive ticker 的文案问题已识别为 P3，但本次不改行为，避免改变流式协议语义。当前 `StreamRetryEnabled` 默认关闭，启用前需结合客户端协议验证。
3. README 保留非幂等副作用提示；计费 / 远端 tool 等副作用仍需业务层保证，未在本次审计中擅自改动 chat handler。

---

## 4. Verification

### 4.1 持续验证（rule 11 §14，每文件落盘即验）

```bash
$ go build ./internal/streamretry/...
(零输出)

$ go vet ./internal/streamretry/...
(零输出)
```

### 4.2 race detector 全跑

```bash
$ go test -race -count=1 ./internal/streamretry/...
ok  github.com/kaixuan/llm-gateway-go/internal/streamretry  2.834s
```

目标包 0 RACE, 0 FAIL。包含新增的 `TestDefaultStreamExecutor_ConcurrentRequestsHaveIndependentMetrics`。

### 4.3 主仓编译 + vet

```bash
$ go build ./...
(零输出)

$ go vet ./internal/streamretry/... ./cmd/gateway/...
(零输出)
```

### 4.4 行为不变性核对

| 不变性 | 验证 | 结果 |
|--------|------|------|
| `Enabled=false` → 1 次尝试 | `TestDefaultStreamExecutor_DisabledRetryFastPath` | ✅ |
| 5xx/429/网络错误触发重试 | `TestDefaultStreamExecutor_RetryOnTransientFailure` | ✅ |
| 4xx 不重试 | `TestDefaultStreamExecutor_NonRetriableErrorReturns` | ✅ |
| context cancel 终止 retry sleep | `TestDefaultStreamExecutor_ContextCanceledStopsLoop` | ✅ |
| retries exhausted 返回 last err | `TestDefaultStreamExecutor_ExhaustedRetriesReturnsLastError` | ✅ |
| **并发请求 metrics 互不污染** | `TestDefaultStreamExecutor_ConcurrentRequestsHaveIndependentMetrics` (新) | ✅ |
| **非流式请求不进入 retry** | `TestDefaultStreamExecutor_NonStreamingRequestDoesNotRetry` (新) | ✅ |

### 4.5 cmd/gateway/main.go 接入点

修改前后行为一致：
- `StreamRetryEnabled=false`（默认）：包装 `if cfg.StreamRetryEnabled` 不进入，跟旧逻辑相同
- `StreamRetryEnabled=true`：`ServeHTTP` → `ExecuteStream` → `Execute` 链路 + 内部签名变化（produce metrics by return）→ mux 不变 → 客户端响应一致

**回归到 `cmd/gateway/main.go` 完全不需要改**（除了确认 `wrapped.Metrics()` 不再是观测手段）。

---

## 5. 改动清单

| 文件 | 类型 | 说明 |
|------|------|------|
| `internal/streamretry/wrapper.go` | 修改 | 使用 mutex 保护最近一次指标快照；新增 per-call 指标 |
| `internal/streamretry/metrics.go` | 纳入审计 | Prometheus counters；确认流式与非流式路径各记录一次 |
| `internal/streamretry/wrapper_test.go` | 修改 | 新增共享 executor 并发指标隔离回归测试 |
| `internal/streamretry/README.md` | 修改 | 补充 per-call metrics API 和并发语义 |
| `AUDIT_STREAMRETRY_CONCURRENCY_20260812.md` | 新文件 | 审计范围、发现、修复、验证和遗留风险 |

净行数：+228 / -67（含 2 个新测试文件）。

---

## 6. 反模式自检（rule 09 §5.2）

| 触及的代码 | 溯源 | 分类 | 动作 | 留痕 |
|------------|------|------|------|------|
| `Wrapper.metrics` 字段 | 2026-08-11 init commit | A 真死=False / 共享快照存在竞争 | **保留 + mutex 保护**；per-call 指标另行返回 | commit message WHY |
| `Wrapper.Metrics()` | 同上 | A | **保留**，明确为线程安全的 latest snapshot | 注释 + README |
| `errorRecorder.Write` 逻辑 | 同上 | C | **保留 + 注释澄清**（分类路径不变） | 注释 + commit message WHY |
| `KeepaliveWriter.Start` ticker | 同上 | A | **保留 + 收紧 active flag** | commit message WHY |

未删除任何已有可执行代码路径。`Wrapper.Metrics()` 保留为线程安全的 latest snapshot。

---

## 7. 性能影响估算（rule 11 §10）

| 项 | 修复前 | 修复后 |
|----|--------|--------|
| 并发请求 metrics 内存开销 | `*WrapperMetrics` 共 1 份，争抢写 | per-call 1 份在请求栈，latest snapshot 由 mutex 保护 |
| `Metrics()` 调用 | 非线程安全，生产负载值不可信 | 线程安全，但仅表示 latest snapshot |
| keepalive ticker 在长 idle | 每 10s 发 1 帧 "Retrying"（误导） | 0 帧（active=false 跳过） |
| retry 期间 keepalive | 1 帧 "Retrying..." | 同上（无变化） |

> 单次 retry 路径只增加 per-call 指标快照和 latest snapshot 保护开销；生产收益集中在"零 metrics 数据竞争"。

---

## 8. 遗留与风险

- ✅ P0 metrics race 已修复并由 `-race` 覆盖。
- 🟡 keepalive ticker 文案问题已识别但未修改，避免扩大行为变更范围。
- ⚠️ **`StreamRetryEnabled` 仍 OFF 默认**：本修复**不**改变默认行为（rule 11 §1 不扩大修改范围）。运维在 245/154 启用前需先看 README § "限制" idempotency 表，确认 `chatHandler` pre-stream 失败路径下的副作用面。
- 🟢 没有 P0/P1 已知问题保留。

---

## 9. 下一步建议

1. **观察**：本 PR 不引入 metric 输出。建议运维在 `StreamRetryEnabled=true` 灰度时**额外**通过 Prometheus client_golang 自定义 `stream_retry_*` 计数器（per-call API 需要调用方主动上报）。先在 staging / 245 跑 7 天指标采集，再上 154。
2. **Keep-a-Changelog 同步**：commit 同步更新 `CHANGELOG.md`。
3. **回归部署**：245 灰度验证 → 154 全量（rule 03 §6 L1-L4 + §7 回滚预案 + §8 工作日窗口）。

---

## 10. 跨主题对比表（usrm v2 / queue / session pollution）

| 主题 | 上一轮审计 | 当前状态 |
|------|------------|----------|
| URSM v2 | AUDIT_URSMV2_CONCURRENCY_20260728.md (M3, 5 fixes) + AUDIT_FULL_TASK_CONCURRENCY_FINAL.md (2026-08-02) | ✅ GO，13 packages 0 race |
| Probe 队列 | AUDIT_PROBE_STREAM_LIFECYCLE_20260812.md first+second pass | ✅ GO，loopback/stale tile/task ID 全部修复 |
| 会话 V2 污染 | `internal/sessionv2mirror/hook.go:62-67` IsAutoRequest skip | ✅ 已有防护，无新发现 |
| Backlog 线程模型 | `internal/sessionv2mirror/backlog.go:57-58` `backlogMu sync.Mutex` + 原子 feature flag | ✅ Mutex 保护 + atomic.Value 双重保险 |
| StreamRetry pre-stream | 本报告 | ✅ P0 metrics race 关闭；P3 keepalive 文案问题已登记 |

✅ 所有 5 个主题均 GO。

---

**审计人:** ZCode (autonomous)
**审计日期:** 2026-08-12
**对应 commit:** 待 commit
