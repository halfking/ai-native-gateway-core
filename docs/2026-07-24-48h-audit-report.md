# 48 小时改动审计与总结报告 (2026-07-22 ~ 2026-07-24)

> 生成时间：2026-07-24 · 范围：`git log --since=48h` · 仓库：`llm-gateway-go`

## 0. TL;DR

- 48 小时内合并 **233** 次提交，触及约 1,230 个非 vendor 文件。
- 三个并行审计智能体（streaming/goal、systemmonitor/bg-probe、routing-v2/installer）
  共发现 **6 处生产代码缺陷 + 1 处安全护栏地雷 + 1 处功能语义失配**；installer
  test 跑测时追加发现 **1 处 goroutine 死锁**。
- 本轮已 **修复全部 6+1 处生产缺陷 + 1 处安全护栏**，并 gofmt 对齐 5 个文件。
- `go build`/`go vet` 两模块均通过；目标测试全部 PASS。

---

## 1. 量化统计

| 指标 | 数值 |
|------|------|
| 提交数 | **233** |
| 变更文件触碰总次数 | 3,238 |
| 去重变更文件（vendor/截图除外） | ~1,230 |
| 代码行新增（含 vendor） | ~704k |
| 代码行删除 | ~17k |
| Go 包数量 | 203 |
| 模块（go.mod）| 2（主 + installer） |

### 1.1 提交类型分布

| 类型 | 数量 |
|------|------|
| fix | 97 |
| feat | 37 |
| chore | 17 |
| docs | 16 |
| test | 6 |
| refactor | 6 |
| merge | 5 |

### 1.2 功能域分布（commit scope top 12）

`web`(43) · `launcher`(14) · `timeout`(11) · `licensing`(11) · `streaming`(8) · `dashboard`(7) · `telemetry`(6) · `systemmonitor`(5) · `goal`(5) · `i18n`(4) · `executor`(4) · `routing-v2`(3)

---

## 2. 主要功能变更

### 2.1 系统监测模块 Phase 1–3（bg/systemmonitor + admin）
- 指标收集器、Redis 队列、Lua 脚本、inflight 去重、SSE 推送、切流 Vue Dashboard。

### 2.2 Goal 重试契约
- 引入请求级重试策略、修正取消/计数持久化、可观测指标、压力/内存泄漏测试。

### 2.3 流式稳定性 / 超时治理
- `eof_without_done` 改进 + 单测、池化请求首字节超时、Minimax-M3 超时调参。

### 2.4 Routing-v2 Resolve 可视化
- 候选全展示 + 行级抽屉 + 行级管理；后端候选绑定 API（`admin/routing.go`）。

### 2.5 Dashboard 实时流多维过滤器
- `LiveRequestStreamV2.vue` 多维过滤 + 6 语言 i18n 补齐。

### 2.6 自动打包与分发
- upgrade-package builder + Cloudreve 设计 + maintain `/distribution/version-check` API。

### 2.7 节点探针 / 凭据恢复
- backoff ladder 24h → 6h、过期 CMB binding 重探、provider 587 format_conversion 修复。

### 2.8 双主题（light/dark）一致性
- data-lifecycle / pricing / model-pricing 等页面去除暗色硬编码。

---

## 3. 审计发现与修正（6 生产 + 1 安全 + 5 格式化）

### 🔴 #1 路由评分方向反转回归（生产 P0·方向 bug）
- **文件**：`domains/streaming/executors/router_scoring.go:34` `calculateLoadScore`
- **引入**：`60809965`（concurrency-aware latency_score + headroom bonus）
- **问题**：实现把 `latencyScore`/`headroom` 直接相加，但 P2C 在 `router.go:690`
  取 `min`（越低越优）。`latencyScore` 是 health（< 800ms → 1.0），
  与 lower-wins 语义反向 → 好凭据 composite 偏高、被惩罚。
- **影响**：生产路由偏向「慢/饱和」凭据，放大延迟与热点。
- **修正**：用 `(1 - x)` 转 penalty；新增 `latency_penalty`/`headroom_penalty`
  日志字段；保持 P2C min 语义不变。

### 🔴 #2 SystemMonitor `Claim(0, "")` 永远失败（生产 P0·队列永不消费）
- **文件**：`bg/systemmonitor/{monitor.go:248,redis_queue.go:120}` +
  `bg/systemmonitor/lua/claim.lua`
- **引入**：`bc0b0756` (Phase 1+2 上线)
- **问题**：
  - `fetchTask` 每 200ms tick 用 `(0, "")` 调用 `Claim`；
  - `Claim` 守卫 `credID>0 && rawModel!=""`，每次都返回
    `invalid claim args` 错误 → `markFallback()` → fallback/redis 反复抖动。
  - 即使去掉守卫，因为 KEYS[2]=`inflight:0:` 是全局单一 token，整个
    30s dedup 会折叠为「同一时刻只能有 1 个 in-flight 任务」。
- **影响**：Redis-backed FIFO 队列完全失效；多实例部署时 instance A 提交的任务
  永远不会被 instance B 处理；监控 dashboard queue_size 单调增长。
- **修正**：
  1. Lua 脚本内从 task.credential_id / task.raw_model 构造 inflight key，
     KEYS 改为只有 queue；任务字段缺失则 LPOP 丢弃避免队头卡死。
  2. `Claim(ctx)` 不再接受 credID/rawModel；删除守卫。
  3. `bg/systemmonitor/monitor.go:fetchTask` 改为 `sm.queue.Claim(ctx)`。
- **回归补充**：`scripts` 字段改用 `atomic.Pointer[LoadedScripts]` 规避
  Submit 失败重载与 worker 读路径之间的数据竞争（agent #2 第二个发现）。

### 🔴 #3 GoalRetryPolicy.Enabled 被忽略（生产 P0·重试风暴）
- **文件**：`domains/streaming/handler.go:2320` (loop start) +
  `domains/streaming/goal_retry_policy.go` (新增 `EffectiveMaxRetries`)
- **引入**：`8254fecc` + `187504a5`
- **问题**：租户可在 `goal.retry_on_error=false` 关闭重试，但
  handler 只把 `Enabled` 写日志、循环仍按 `MaxRetries` 跑满指数退避。
- **影响**：客户刻意关闭的故障 → 反而放大对失败上游的压力。
- **修正**：
  - `GoalRetryPolicy.EffectiveMaxRetries()` 在一处收敛不变量
    （`!Enabled ⇒ 0`）。
  - handler 通过该 helper 取值；不再直接读 `MaxRetries`。
  - 新增 `TestEffectiveMaxRetriesHonorsDisabledFlag` 回归测试。
  - 注意：`Normalize()` 不统一处理 Enabled，因为零值 false 会误伤未写
    Enabled 的合法策略（在 helper 注释中说明）。

### 🟡 #4 TestLatencyScore_SaturationCurve 断言方向相反（测试同步缺失）
- **文件**：`domains/streaming/executors/router_scoring_test.go:78`
- **问题**：测试表 (`1000→0.5 / 2000→0.67 / 5000→0.83`) 来自旧
  `score = latency/(latency+k)` 惩罚曲线；实现已改为 health-table。
- **修正**：重写为分段健康度值断言，并用人类可读子用例名替代
  `string(rune(...))` 乱码。

### 🟡 #5 TestRedisHealthStore_ReadPerformance 不稳定（环境噪声）
- **文件**：`domains/credential/redis_health_store_test.go:206`
- **问题**：`avgLatency < 1µs` 在含 `fmt.Sprintf` 热循环 + 共享 CPU 下不可重复。
- **修正**：`testing.Short()` 跳过；预生成 keys 仅度量 Get；阈值放宽到 5µs。

### 🟠 #6 internal/release admin 路由认证护栏地雷（latent→fail-close）
- **文件**：`internal/release/handler.go` (NewHandler 签名)
- **引入**：`7a371bc7`（distribution Phase 1-3）
- **问题**：原代码注释 `// admin.Use(middleware.RequireAdmin()) // 添加认证中间件`，
  一旦未来有人挂载 `/admin/releases` 即裸奔。
- **修正**：`NewHandler(service, gin.HandlerFunc)` 必传 adminAuthMW；nil
  则不挂载任何管理端点（fail-close）。

### 🟠 #7 RecordDownload 吞掉 file_id parse 错误（同步修复）
- **文件**：`internal/release/handler.go:RecordDownload`
- **修正**：解析错误 / `<=0` → 400 + 明确错误，避免静默累加到错误记录。

### 🟠 #8 Upgrader 按 platform/arch 选 artifact（同步修复）
- **文件**：`installer/internal/upgrader/client.go:CheckUpdateDistribution`
- **问题**：请求带了 `platform`/`arch`，但客户端无脑取 `TargetArtifacts[0]`。
  多平台 release 时可能把 amd64 二进制推到 arm64 节点。
- **修正**：按 `Platform==platform && Arch==arch` 选择；无匹配返回显式错误。

### 🟢 #9 installer containsString 不可达代码（vet 失败）
- **文件**：`installer/internal/enrollment/register_test.go:159`
- **问题**：函数 return 之后还跟了一大段 substring 循环 + json 反序列化副作用；
  `go vet` 报 unreachable code，installer 模块 `go vet ./...` exit 1。
- **修正**：`return strings.Contains(s, substr)`，导入 strings。

### 🟢 #10 gofmt 对齐（5 文件机械性修复）
- `cmd/gateway/migrate_test.go`
- `domains/streaming/goal_retry_integration_test.go`
- `domains/streaming/goal_retry_stress_test.go`
- `internal/collector/metrics.go`
- `internal/collector/reporter_multi.go`

仅空白对齐，无逻辑变更。

---

## 4. 已审查且无 bug（agent 已确认）

- `node_probe.go` / `credential_recovery.go` / `credential_selfcheck.go`：
  `Submit/cycle/runOne` 加锁正确；`wakeTimers` 在 Stop 清理；
  `pickDueAtomically` SKIP LOCKED + defer Rollback OK。
- `admin/systemmonitor_handlers.go` + `systemmonitor_stream_sse.go`：
  SSE hub mutex 正确；`removeClient` 幂等；`fanOut` 拷贝切片在锁内、
  发送在锁外，无数据竞争。
- `admin/routing.go`：`handleRoutingCandidateBindingUpdate` 用 `SuperAdminMiddleware`
  校验；SQL 参数化（`$1..$4`）；h+nil db 在测试场景下被早期输入校验守卫保护。
- `scripts/{build-upgrade-package,publish-download-release,upgrade-instance}.sh`：
  全部 `set -euo pipefail`，无 eval、无未引用变量、无明文密码。
- `187504a5` 的重试取消：`break` 仅退出 select，下一次循环顶端的
  `retryCtx.Err()` 检查接住；单次 Execute 调用，无遗漏。
- `8254fecc` 重试计数：increment 受 `attempt>0` 与 ctx 取消双重保护；
  持久化 fail-open 且 `gwSessionID` 非空才写。

### 🟠 #11 Checker.Stop 死锁（test-discovered, fail-safe 修复）
- **文件**：`installer/internal/launcher/checker/checker.go`
- **问题**：`Checker.loop` 仅监听 `<-ctx.Done()`，但 `Stop()` 仅做 `wg.Wait()`
  且测试普遍写法 `defer c.Stop(); defer cancel()`。defer LIFO → Stop 先于
  cancel → wg.Wait 永久阻塞。`TestCheckNowTriggersImmediate` 在 `-count=1 ./...`
  下 timeout 60s 失败。
- **修复**：增加内部 `stop chan struct{}`；`Stop()` 关闭 `stop` 并 wg.Wait；
  `loop` 同时 select `ctx.Done()` 与 `stop`。`stopOnce` 确保 Stop 与 Start
  顺序不敏感。
- **影响范围**：仅 `internal/launcher/checker`；不破坏既有用法（无 ctx 也可退出）。

### 已知但未在本轮修改（建议后续）

1. **telemetry body-null 覆盖**（pre-existing）：`domains/hooks/observability/telemetry/client.go:upsertRequestLogBodies`
   在 body 为 nil 时绑定字符串 `"null"` → JSONB null → COALESCE 失效。后续应改为
   `NULLIF($2::jsonb, 'null'::jsonb)` 或直接传 SQL NULL。
2. **bg/asset_health_probe.go:probeOneTenant 无限循环**：使用 `offset += batchSize`
   但未实际分页传递；>1000 行 tenant 会卡死。48h 内只新增 doc 注释。需后续修复。
3. **plugin-runtime TestExecCommand_*: 含 go build 子进程的微基准**：在负载下
   会偶发失败；建议迁出 -short run。

---

## 5. 验证结果

| 验证项 | 结果 |
|--------|------|
| `go build ./...`（主模块） | ✅ |
| `go build ./...`（installer） | ✅ |
| `go vet ./...` × 2 | ✅ 0 告警 |
| `go test -short -count=1` 目标包（streaming / streaming/executors / credential） | ✅ 全绿 |
| 受影响包重跑（installer 全模块） | ✅ 全绿 |
| `gofmt -l` 全仓 | ✅ 0 |

---

## 6. 后续建议

1. `calculateLoadScore` 当前用 `(1-x)` 手工转 penalty，建议下一阶段按
   `docs/design/2026-07-20-latency-aware-routing.md` §2.2 的 tier-plane + SWRR
   统一选择语义为「higher=better」，消除「健康度需手工取反」的易错点。
2. `domains/credential` 异步 save goroutine 在 miniredis / Redis 关闭后仍打
   WARN 日志（非失败但噪声大），可在 `client.Close()` 后停止 worker。
3. 性能基准（ReadPerformance 等）应迁出 `-short` 测试包，改用 `testing.B`
   Benchmark + 显式 `-benchtime=1s`，避免 CI 误报。
4. CI 增加 `gofmt -l` 与 `go vet ./...` 两步硬门禁，避免后续 48h 风格漂移。

---

## 附录 A：审计智能体

| ID | Scope | 提交数 | 已应用 |
|----|------|------|--------|
| #1 streaming/goal retry | domains/streaming, cmd/gateway, domains/hooks/goal | 8 | #3, #4 |
| #2 systemmonitor + bg probe | bg/, admin/systemmonitor_*, cmd/gateway/system_monitor_adapter | 8 | #2 |
| #3 routing-v2 + installer | admin/routing, installer/, scripts/ | 6 | #6, #7, #8, #9 |

智能体报告原文归档在 `memory-bank/audit-agents-2026-07-24/`。
