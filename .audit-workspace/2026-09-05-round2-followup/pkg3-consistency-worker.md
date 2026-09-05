# pkg3 — storage Reconcile/Repair 补齐安全性 + 后台一致性 worker（B-#2 / G-#9 / B-#6）

- 日期：2026-09-05
- 分支/基线：main @ cab1bbf8c（工作区含其他并行子代理改动，本包改动限定 storage/、bg/、config/、cmd/gateway/storage_mode_init*.go）
- 审计依据：`.audit-workspace/2026-09-05-round2/axis-B-storage.md`（B-#2、B-#6）、`.audit-workspace/2026-09-05-round2/axis-G-fix-quality.md`（G-#9）
- 结论：三项全部修复，`go build ./...`、`go vet`（storage/bg/config/cmd/gateway）、`go test ./storage/... ./bg/... ./config/... ./cmd/gateway/...` 全绿（新增测试含 -race 复跑通过）。

## 一、三项修复的落点

### G-#9（P2）Reconcile 读-读窗口 → Repair 误删在途 body —— 双保险修复

写序是「body 先落盘、meta 后提交」（`cmd/gateway/lite_telemetry_sink.go:139-178`），`ReconcileTurnArtifacts` 先读 meta 再列 body，两次读取之间完成提交的合法在途 turn 会被误判为 OrphanBodies。修复全部落在删除路径上（`storage/consistency.go`）：

1. **删除前复检（double-confirm）**：`RepairTurnArtifacts` 签名改为
   `(ctx, report, bodies, turns, action, opts) (*RepairResult, error)`
   （storage/consistency.go:195）——新增 `turns TurnsStore` 参数与 `*RepairOptions`，返回新增 `RepairResult`。删除每个孤儿前重读一次 turn meta（`turnAbsentInMeta`，consistency.go:263），只删「两次快照都不在 meta 中」的 turn；复检发现 meta 已提交的记入 `RepairResult.SkippedInFlight`（consistency.go:219），body 合法保留。全仓调用方仅测试与新 worker，签名变更成本可控（无其他生产调用方，已 grep 确认）。
2. **mtime 宽限**：新增 `TurnFileStater` 接口（consistency.go:51）与
   `FileBodiesStore.TurnFileModTime`（storage/file/bodies_store.go:256，缺失时返回 `storage.ErrNotFound` 哨兵）；`RepairOptions.OrphanGrace`（consistency.go:95）控制宽限期——零值/nil → `DefaultOrphanBodyGrace = 10 分钟`（consistency.go:40，保守默认），负值禁用（仅测试）。mtime 距今不足宽限的孤儿跳过删除仅报告（`SkippedByGrace`，consistency.go:228）；mtime 查询失败同样保守跳过（宁漏删不误删，consistency.go:234）；文件已消失（ErrNotFound）无删自灭直接跳过。bodies 未实现 TurnFileStater 时宽限护栏不生效（降级语义有测试钉死，见下）。

调用方保护链条：worker 空闲阈值（第一道，枚举层面）→ 删除前 meta 复检（第二道）→ mtime 宽限（第三道）。

### B-#6（P3）缺 TurnFileDeleter 静默 no-op → 返回 error

`storage/consistency.go:245-249`：action=RepairDeleteOrphanBodies、复检后仍有待删孤儿、但 bodies 未实现 `TurnFileDeleter` 时返回 error（错误信息点名缺失能力与待删数量）。report-only、无孤儿场景不受影响（照旧 no-op 成功）；nil turns 时删除动作同样直接拒绝（consistency.go:204-206，无法复检即不许删）。

### B-#2（P2）原语零生产调用方 → 后台一致性 worker 接线

- **新 worker**：`bg/consistency_worker.go`（全文新增）
  - `ConsistencyWorker`（:51）：默认 `RepairReportOnly`（零值即安全）、interval 24h（:40）、idle threshold 10min、max sessions 500、首跑延迟 10min（:46）。
  - `Start(ctx)`（:141）：阻塞式、首跑等 initialDelay（避开启动争资源）后立即执行一轮，再按 ticker 周期执行；ctx.Done 优雅退出——生命周期照抄 BodiesTrimmer。
  - `RunOnce(ctx)`（:178）：`ListIdleSessions(now-idleThreshold, maxSessions)` 枚举「近期活跃且已空闲」会话（最近活跃优先、bounded），逐会话 `ReconcileTurnArtifacts`；consistent=false 的会话输出结构化 slog.Warn（tenant/session/missing_bodies/orphan_bodies/两侧 turn 数），轮末 slog.Info 汇总；单会话失败只告警继续、枚举失败返回 error。仅当配置显式开启删除时才调 `RepairDeleteOrphanBodies`（天然享受 G-#9 双保险），护栏跳过与删除结果均结构化输出。统计快照（lastSessionsChecked/lastInconsistent/lastOrphans/lastMissing/lastDeleted）供测试与监控。
- **session 枚举**：`storage/interfaces.go:65` 新增窄接口 `IdleSessionLister`（不侵入既有 `SessionStore`）；`storage/sqlite/session_store.go:215` 新增 `ListIdleSessions(ctx, idleBefore, limit)`（`updated_at < ?`，`ORDER BY updated_at DESC LIMIT ?`，复用 scanSession；:37 编译期断言）。
- **装配**：`cmd/gateway/storage_mode_init.go:162-182`——lite 启动时与两个 trimmer 并列挂入 trimmerCtx/trimmerWG（:172 起，`rt.trimmerWG.Add(1)`）；session store 不支持 IdleSessionLister 时降级关闭并告警；`storageRuntime` 新增 `consistencyWorker` 快照字段（测试断言/观测用）；启动日志补 consistency 四项（:196-199）。

## 二、新配置项与默认值（config/storage.go）

`LiteStorageConfig.Consistency LiteConsistencyConfig`（yaml 段 `lite_storage.consistency`，config/storage.go:64-88、:103），默认值在 `ApplyLiteDefaults`（:205-218）补齐，env 走 `applyEnvOverrides`（:313-347，`applyOptionalBoolEnv`/`parseBoolEnv` 辅助 :368-392）：

| 配置项 | YAML 键 | env 变量 | 默认值 | 说明 |
|---|---|---|---|---|
| Enabled | `consistency_check_enabled` | `LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED` | **true** | `*bool` 区分「未配置→true」与 YAML/env 显式 false（ApplyLiteDefaults 不翻转显式 false） |
| IntervalHours | `interval_hours` | `LLM_GATEWAY_CONSISTENCY_INTERVAL_HOURS` | 24 | 对账周期（小时），env 仅接受正整数 |
| IdleThresholdMin | `idle_threshold_min` | `LLM_GATEWAY_CONSISTENCY_IDLE_THRESHOLD_MIN` | 10 | 空闲阈值（分钟），把在途写入挡在对账窗外 |
| DeleteOrphanBodies | `delete_orphans` | `LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS` | **false（report-only）** | 仅真值置位；零值即安全默认，无兜底 |
| MaxSessionsPerRun | `max_sessions_per_run` | `LLM_GATEWAY_CONSISTENCY_MAX_SESSIONS_PER_RUN` | 500 | 单轮会话数上限（bounded） |

首跑延迟 10 分钟为 worker 内建常量（`defaultConsistencyInitialDelay`），未暴露配置（可用 WithInitialDelay 覆盖，供测试）。默认语义恒安全：默认开启对账但只报告不删数据；删除需显式 `delete_orphans: true` 且仍受复检+宽限保护。

## 三、测试清单

storage 包（真实 SQLite + 文件系统，`storage/consistency_lite_test.go`）：
- `TestLiteRepairDoubleConfirmSkipsInFlightBody`：在途轮（body 落盘、meta 未提交）与真孤儿并存 → Reconcile 均判孤儿 → 在途轮 meta 在两次快照之间提交 → Repair 复检跳过在途轮（SkippedInFlight、body 内容逐字节完好）、真孤儿被删；复检后整体 Consistent。
- `TestLiteRepairGraceSkipsFreshOrphanThenAgedDeleted`：默认宽限（nil opts → 10min）下新鲜孤儿只报告不删；`TurnFileModTime` 契约（存在→mtime、缺失→ErrNotFound）；mtime 回拨 2× 宽限后同一孤儿被正常删除并恢复一致。
- 既有 4 用例适配新签名（含 delete 用例显式 `OrphanGrace:-1` 禁宽限，聚焦孤儿身份）。

storage 包（fake 可控序列，`storage/consistency_repair_test.go` 新文件）：
- `TestRepairDoubleConfirmSequenceRace`：`sequenceTurns` 让 GetTurnsMeta 第一次返回空（判孤儿）、第二次含该 turn（复检命中）→ 不删合法 body、双快照皆无的照删。
- `TestRepairGraceAndDegradedStater`（4 子用例）：宽限内跳过 / 超宽限删除 / 文件已消失 no-op / 有删除器无 stater 时宽限降级但复检仍保护。
- `TestRepairDeleteWithoutDeleterReturnsError`（B-#6）：缺 TurnFileDeleter + 待删孤儿 → error（点名 TurnFileDeleter）；report-only no-op 成功；无孤儿不报错；nil turns 拒绝删除。

storage/sqlite 包：
- `TestSessionListIdleSessions`（session_store_test.go 末尾）：阈值过滤、跨租户、updated_at 倒序、limit 有界、limit<=0 兜底、空集。

bg 包（`bg/consistency_worker_test.go` 新文件，fake 全离线）：
- `TestConsistencyWorkerDefaults`：默认 report-only + 24h/10m/500/10m；With* 覆盖与零值防御。
- `TestConsistencyWorker_RunOnce_ReportOnly`：不删任何数据，Missing/Orphan/Inconsistent 统计正确。
- `TestConsistencyWorker_RunOnce_DeleteMode`：delete 模式删除复检确认的真孤儿。
- `TestConsistencyWorker_RunOnce_KeepsDoubleConfirm`：序列 fake 证明 worker 驱动的 Repair 保留复检护栏（在途轮不删）。
- `TestConsistencyWorker_RunOnce_BoundedAndIdleCutoff`：maxSessions 传参生效、idleBefore ≈ now-阈值。
- `TestConsistencyWorker_RunOnce_EnumerationError` / `_PerSessionErrorContinues`：枚举失败报错；单会话失败不拖垮整轮。
- `TestConsistencyWorker_Start_LoopAndGracefulExit`：首跑延迟 + 周期 + ctx 优雅退出（fake 带 mutex，-race 干净）。

cmd/gateway 包：
- `TestInitStorageModeConsistencyWorkerWiring`（storage_mode_init_test.go 末尾）：默认装配（report-only）、显式关闭不装配、delete_orphans 开启时策略与三项 knob 正确注入；三种形态 Shutdown 均幂等优雅退出。

## 四、验证输出结论

```
go build ./...                                          → OK
go vet ./storage/... ./bg/... ./config/... ./cmd/gateway/... → OK
go test ./storage/... ./bg/... -count=1
  → ok storage / storage/factory / storage/file / storage/memory / storage/sqlite
    ok bg / bg/freequotacleanup / bg/freequotareset / bg/systemmonitor（全绿）
go test ./config/... → ok
go test ./cmd/gateway/... -count=1
  → ok cmd/gateway / cmd/gateway/webhooks（全绿，无预存红，无需基线回溯）
新增测试 -race 复跑：storage（9 用例）与 bg（8 用例）全部 PASS
gofmt：本包全部改动文件格式干净（bg 下 gofmt -l 列出的其余文件为预存问题、非本包改动）
```

## 五、遗留 / 取舍

- **B-#3 明确不在本包**：`retention.request_logs_days` 有配置无执行者、sessions/session_turns 元数据无保留清理、`nextTurnNo` 只看 body 侧轮号——均属 B-#3 范畴，本包未动（本轮 Reconcile 对「bodies 过期但 meta 仍在」的会话会报 MissingBodies 噪音，与审计 B-#3 描述一致，待其修复后自然消解）。
- **admin 端点未暴露**：审计建议的备选「暴露为 admin 端点」未做（本包文件权限不含 admin/），周期 worker 已覆盖「生产检出」主诉求；如需手动触发可后续复用 `ConsistencyWorker.RunOnce`。
- **RepairTurnArtifacts 签名变更为破坏性**：选了改签名而非新增函数（全仓调用方只有测试与新 worker，语义最清晰）；旧签名 `RepairTurnArtifacts(ctx, report, bodies, action)` 无编译期兼容层。
- **宽限护栏依赖 TurnFileStater**：第三方 bodies store 未实现该接口时仅剩复检一道保险（有测试文档化该降级）；FileBodiesStore 已实现全部三个缝（Lister/Deleter/Stater，编译期断言）。
- **Enabled 用 `*bool`**：为区分「未配置→默认 true」与 YAML 显式 false，偏离包内纯值类型风格；env `..._CHECK_ENABLED=false` 可显式关闭（`applyOptionalBoolEnv` 不覆盖 YAML 显式值）。
- **worker 首跑延迟未暴露配置**：固定 10min（与审计「如 10min」建议一致），避免配置面膨胀；周期/阈值/上限/删除开关均已可配。
- **请求侧在途写与 trimmer 无互斥**：空闲阈值 + 复检 + 宽限为纯时序防护（纵深三层），未引入跨介质锁（lite 单机写路径为异步队列，锁收益低、复杂度高）。
