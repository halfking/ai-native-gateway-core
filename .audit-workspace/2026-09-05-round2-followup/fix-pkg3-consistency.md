# 修复报告 pkg3：存储一致性/并发（对应 audit-pkg3-consistency.md）

- 项目：llm-gateway-go（基线 HEAD 203021f33，main）
- 修复日期：2026-09-05
- 修复范围：`storage/`（consistency.go、interfaces.go、sqlite/schema.go、sqlite/session_store.go 及测试）、`bg/consistency_worker.go` 与其测试、`config/storage.go` 与 `config/storage_test.go`
- 纪律：未 git commit / git add
- 注释标识：2026-09-05 round2 复审

## 总览

| 发现 | 级别 | 状态 | 处置 |
|---|---|---|---|
| F1 空闲会话 >500 老会话永久饿死 | P2 | 已修 | OFFSET 轮转分页（跨轮全覆盖） |
| F2 listIdleSessionsSQL 无 updated_at 索引 | P3 | 已修 | 幂等补索引 |
| F3 已消失孤儿不计入结果桶 | P3 | 已修 | RepairResult.Vanished 桶 + worker 透出 |
| F4 统计字段无锁 + 无 getter | P3 | 已修 | sync.Mutex + LastRunStats() |
| F5 config env/YAML 优先级未测 | P3 | 已修 | 补两个用例 |
| F9 构造器不校验 nil 依赖 | P3 | 已修 | panic fail-fast（跟随包内惯例） |
| F6 ctx 取消路径测试 | P3 | 不修（审计判定记录即可） | 未动 |
| F7 delete_orphans *bool 化 | P3 | 不修（审计判定记录即可） | 未动 |
| F8 复检 O(n×m) | P3 | 不修（审计判定记录即可） | 未动 |

---

## F1（P2，必修）：空闲会话 >500 时老会话永久饿死 → OFFSET 轮转分页

**修法**（采用审计建议中的 offset 轮转，改动最小）：

- 接口 `storage/interfaces.go:64-73`：`IdleSessionLister.ListIdleSessions` 签名
  `(ctx, idleBefore, limit)` → `(ctx, idleBefore, limit, offset int)`；注释写明
  offset 供 worker 跨轮轮转分页、消除"恒取最新一页"盲区。
- SQLite 实现 `storage/sqlite/session_store.go:25`：`listIdleSessionsSQL` 追加
  `OFFSET ?`；实现 `:217-224` 增加 offset 参数（`offset < 0` 归零）。
- worker `bg/consistency_worker.go:96-105`：新增 `idleOffset` 字段（与统计字段
  同一把 `runMu` 保护）；`RunOnce` 在 `:248-250` 锁内读偏移 → `:253` 传入枚举 →
  `:313-318` 锁内推进：返回满一页前移 `offset += len(sessions)`，返回不足一页
  （已覆盖到最老端）回绕 0。
- 语义注释 `bg/consistency_worker.go:96-105`：重启回绕可接受（Reconcile 幂等
  只读，重复对账无害）；轮间新会话进入导致窗口漂移可接受——目标是跨轮全覆盖，
  不保证单轮精确游标。N 个空闲会话在 ceil(N/500) 轮内全部覆盖。
- 注释与实现对齐：`bg/consistency_worker.go:19-22`（包注释改为"按轮转偏移跨轮
  分页覆盖，老会话不会被永久饿死"）、`config/storage.go:76-78`（MaxSessionsPerRun
  注释同步改写，删除"留待下一轮按最近活跃优先继续"的失实表述）。

**接口变更清单（F1）**：
- `storage.IdleSessionLister.ListIdleSessions(ctx, time.Time, int, int)` —— 全仓
  grep 确认实现方仅 `SQLiteSessionStore`、调用方仅 `bg.ConsistencyWorker.RunOnce`
  与测试 fake，全部同步更新。

## F2（P3）：listIdleSessionsSQL 无 updated_at 索引

- `storage/sqlite/schema.go:34-36`：幂等补
  `CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions (updated_at);`，
  对齐文件内现有 `idx_sessions_tenant/idx_sessions_user` 声明风格（SchemaSQL 全
  IF NOT EXISTS，旧库重启自动补建）。升序单列索引足够：SQLite 可反向扫描满足
  `ORDER BY updated_at DESC`。

## F3（P3）：已消失孤儿不计入结果桶 → Vanished 桶

- `storage/consistency.go:124-127`：`RepairResult` 增
  `Vanished []int \`json:"vanished,omitempty"\``，注释说明「检查与删除之间文件
  已自行消失（如被外部清理），无须删除但计入报告供审计对账」。
- `storage/consistency.go:234-238`：mtime 查询 `ErrNotFound` 分支由裸 `continue`
  改为 `res.Vanished = append(...)` 后 continue（行为不变：仍不删，只补桶）。
- worker 透出 `bg/consistency_worker.go:263,300-307,323,337`：RunOnce 累计
  `vanished`、护栏日志增 `vanished` 键、统计快照增 `lastVanished`、完成日志增
  `vanished_orphans` 键。
- 测试更新：`storage/consistency_repair_test.go:196-201`（vanished 子测试断言
  `res.Vanished == [2]`）；新增 worker 级用例 `TestConsistencyWorker_RunOnce_
  VanishedOrphans`（bg/consistency_worker_test.go，经恒返回 ErrNotFound 的
  TurnFileStater fake 钉住 stats.Vanished=1、不删除）。

## F4（P3）：统计字段无锁 + 无 getter

- `bg/consistency_worker.go:82-86`：新增 `runMu sync.Mutex`（统计字段与
  idleOffset 同锁，注释写明加锁动机：生产 Start 串行本无竞态，加锁使导出的
  RunOnce 与监控读并发无数据竞争）。
- `bg/consistency_worker.go:52-66`：新增快照值类型 `ConsistencyRunStats`
  （SessionsChecked/Inconsistent/Orphans/Missing/Deleted/Vanished）。
- `bg/consistency_worker.go:187-199`：新增 `LastRunStats() ConsistencyRunStats`
  返回锁内值拷贝，注释「供监控读取；ctx 中途取消的轮次不更新」落到实处。
- RunOnce 尾部 `:313-325` 轮转推进 + 统计写入合并为单次临界区。
- 测试更新：`bg/consistency_worker_test.go` 原直接读 `w.lastSessionsChecked` 等
  的 5 处断言全部改走 `w.LastRunStats()`（ReportOnly/DeleteMode/KeepsDoubleConfirm/
  PerSessionErrorContinues 及轮转用例）。

## F9（P3）：构造器 nil 依赖 fail-fast

- 惯例考证：bg 包绝大多数 New* 不校验入参；**唯一**处理非法入参的先例是
  `NewGoalRunActionScheduler`（bg/goalrun_action_scheduler.go:186-196）——panic。
  故跟随 panic 惯例，**签名不变**（保持 `*ConsistencyWorker` 返回），
  `cmd/gateway/storage_mode_init.go` 装配点无需改动。
- `bg/consistency_worker.go:112-121`：`NewConsistencyWorker` 对 nil 的
  sessions/turns/bodies 逐一 `panic("bg: consistency worker requires non-nil ...")`，
  把装配错误从运行期 nil 接口调用提前到启动期。
- 新增测试 `TestNewConsistencyWorker_NilDependenciesPanic`（PanicsWithValue 钉住
  三条消息）。

## F5（P3）：config 测试补两用例

- `config/storage_test.go:566-591` `TestConsistencyYAMLExplicitBeatsEnv`：
  YAML `consistency_check_enabled: true` + env
  `LLM_GATEWAY_CONSISTENCY_CHECK_ENABLED=false` → 断言 YAML 胜出（Enabled 显式
  true；applyOptionalBoolEnv 只填 nil 指针，config/storage.go:399-401）。
- `config/storage_test.go:594-611` `TestConsistencyEnvInvalidBoolFallsBackToDefault`：
  env `notabool` → 不报错、指针保持 nil → `ApplyLiteDefaults` 后为默认 true。

---

## 测试更新清单

| 文件 | 变更 |
|---|---|
| bg/consistency_worker_test.go | fakeIdleSessionLister 增 offset 参数与 gotOffset 快照；failingIdleLister 签名同步；统计断言改走 LastRunStats()；新增 fakePagingLister、fakeWorkerStatableBodies 与 3 个新用例（RotatesAcrossPages / VanishedOrphans / NilDependenciesPanic） |
| storage/consistency_repair_test.go | vanished 子测试断言 res.Vanished == [2] |
| storage/sqlite/session_store_test.go | ListIdleSessions 调用补 offset 实参；新增 offset=1 分页与 offset 越界返回空切片（非 nil）断言 |
| config/storage_test.go | 新增 TestConsistencyYAMLExplicitBeatsEnv、TestConsistencyEnvInvalidBoolFallsBackToDefault |

轮转用例钉住的关键序列（5 会话、单轮 2）：offsets `[0, 2, 4, 0]`，每轮
SessionsChecked `[2, 2, 1, 2]`，前三轮不重不漏覆盖全部 5 个会话。

## 验证输出（全绿）

```
$ go build ./... && go vet ./storage/... ./bg/... ./config/... ./cmd/gateway/...
BUILD+VET OK

$ go test ./storage/... ./bg/... ./config/... ./cmd/gateway/... -count=1
ok  github.com/kaixuan/llm-gateway-go/storage            0.802s
ok  github.com/kaixuan/llm-gateway-go/storage/factory    0.978s
ok  github.com/kaixuan/llm-gateway-go/storage/file       9.098s
ok  github.com/kaixuan/llm-gateway-go/storage/memory     1.919s
ok  github.com/kaixuan/llm-gateway-go/storage/sqlite     5.280s
ok  github.com/kaixuan/llm-gateway-go/bg                 5.989s
ok  github.com/kaixuan/llm-gateway-go/bg/freequotacleanup 2.178s
ok  github.com/kaixuan/llm-gateway-go/bg/freequotareset   2.977s
ok  github.com/kaixuan/llm-gateway-go/bg/systemmonitor    3.575s
ok  github.com/kaixuan/llm-gateway-go/config             2.920s
ok  github.com/kaixuan/llm-gateway-go/cmd/gateway        4.371s
ok  github.com/kaixuan/llm-gateway-go/cmd/gateway/webhooks 2.195s

$ go test -race ./storage ./storage/sqlite ./bg ./config -count=1
ok  github.com/kaixuan/llm-gateway-go/storage        1.584s
ok  github.com/kaixuan/llm-gateway-go/storage/sqlite 1.927s
ok  github.com/kaixuan/llm-gateway-go/bg             5.037s
ok  github.com/kaixuan/llm-gateway-go/config         2.168s
```

新增/受影响用例 -count=1 -v 定点复跑：全部 PASS（含 RotatesAcrossPages /
VanishedOrphans / NilDependenciesPanic / TestSessionListIdleSessions /
TestRepairGraceAndDegradedStater 四子测试 / TestConsistencyYAMLExplicitBeatsEnv /
TestConsistencyEnvInvalidBoolFallsBackToDefault）。

## 备注

- `git status` 中 `internal/ir/*.go` 的改动属并行修复代理（audit-pkg1-ir）范围，
  本任务未触碰；本任务改动仅限上表所列 10 个文件 + 本报告。
- 未 commit / 未 add。
