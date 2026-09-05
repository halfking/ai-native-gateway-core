# 审计报告 pkg3：存储一致性/并发（B-#2 / G-#9 / B-#6 修复）

- 项目：llm-gateway-go
- 审计对象：`git diff 5378324de..203021f33 -- storage/ bg/ config/ cmd/gateway/`（HEAD 203021f33）
- 审计日期：2026-09-05
- 审计轴：comprehensive-code-audit 五标准（重点：并发安全、业务闭环、状态机分支完备、数据兼容、调用方完备 + 测试有效性）
- 纪律：只读，未修改任何业务文件，未 commit

## 总评

**评级：A-（可发布）**。P0=0，P1=0，P2=1，P3=8。

三项修复（G-#9 双保险删除、B-#6 缺删除器报错、B-#2 周期对账 worker）语义正确、默认恒安全、生命周期闭环完整，测试真实钉住关键行为；`go build`、`go vet`、`go test -count=1`、`go test -race` 全绿。唯一的 P2 是 worker「单轮 500 上限 + updated_at DESC 排序」组合下，超出 500 的老会话被永久饿死（与注释"超出部分留待下一轮继续"的声明不符），削弱 B-#2"检出不一致"的核心目标。

---

## 一、并发安全（重点轴）

### 1.1 consistency.go 修复路径的复检→删除窗口

**结论：窗口存在但被 mtime 宽限兜住，纵深防御成立。**

- 复检（`turnAbsentInMeta`，storage/consistency.go:262-274）与删除（consistency.go:250-258）之间仍有一读-写窗口：meta 可在复检后、`DeleteTurnFile` 前提交，body 被误删。此窗口由第二道保险覆盖：只有 mtime 距今 ≥ 宽限期（默认 10min，consistency.go:40、103-112）的孤儿才进入 pending，而合法在途轮的 body mtime 必然新鲜（body→meta 间隔为单次 SQLite 写延迟）。要误删需同时满足「两次 meta 快照都缺席」+「文件已存在超过 10min 但 meta 仍未提交」——后者意味着写入方已停滞 10min 以上，该 body 实质就是孤儿。
- `now := time.Now()`（consistency.go:209）循环外单次取样：mtime 只会更老不会更新，`now.Sub(mt)` 单调偏大 → 只可能更多跳过宽限、不可能漏掉新鲜文件；时钟回拨（mt 在未来）时 `now.Sub(mt) < 0 < grace` → 跳过。方向均保守。
- mtime 查询失败 → `SkippedByGrace`（consistency.go:233-235），宁漏删不误删，符合需求 1；文件已消失（`ErrNotFound`）跳过（consistency.go:230-232）——见发现 F3（该孤儿不计入任何结果桶）。

**降级模式（bodies 未实现 TurnFileStater）风险评估**：仅剩复检一道保险（consistency.go:206-208）。但生产不可能降级——FileBodiesStore 在 storage/file/bodies_store.go:32-37 有四个接口（BodiesStore/BodiesLister/TurnFileDeleter/TurnFileStater）的编译期断言，且 lite 装配恒为 FileBodiesStore 惰性单例（storage/factory/factory.go:214-221；storage/memory 包只有 state_store.go，无 memory bodies 实现）。降级仅可能出现在测试 fake（consistency_repair_test.go:77-85 有意构造并文档化，:196-202 钉住该语义）。真实风险：可忽略。

### 1.2 bg/consistency_worker.go

**结论：生产路径（Start 串行调用 RunOnce）无竞态；导出 API 层面存在理论竞态，见 F4。**

- 统计字段 `lastSessionsChecked/lastInconsistent/lastOrphans/lastMissing/lastDeleted`（consistency_worker.go:43-48）无锁，仅在 RunOnce 尾部赋值（:240-244）。生产中只有 Start 循环串行调用 RunOnce；但 RunOnce 是导出方法且注释声称"供测试与监控读取"，与 Start 循环并发调用（或两次并发 RunOnce）即构成数据竞争。对比：测试内的 fakeIdleSessionLister 自己加了 mu（consistency_worker_test.go:26-31），worker 本体没有。协程泄漏：无——Start 的 initialDelay 用 `select { <-ctx.Done() / <-time.After }`（:157-162），ticker `defer tk.Stop()`（:169），无裸 go。
- ctx 中途取消：RunOnce 头部（:179-181）与每会话循环（:202-204）检查 `ctx.Err()`；ListIdleSessions 用 `QueryContext` 即时失败；ListTurns（文件 ReadDir）不接 ctx 但耗时极短且循环内下个会话即退出。取消后 RunOnce 返回 `context.Canceled`，Start 侧 `errors.Is(err, context.Canceled)` 过滤（:166、:174），统计字段保持上一轮旧值（可接受）。
- ctx 取消与数据安全：取消只会提前中止，不会跳过护栏——删除在复检+mtime 判定之后同步执行，无"取消时半删"状态。

### 1.3 FileBodiesStore.TurnFileModTime 与写路径竞态

**结论：无实际竞态。**

- 写路径非"队列后台写"：`FileBodiesStore.Write` → `AsyncFileWriter.Write` **阻塞至完成**（storage/file/async_writer.go:97-103 "入队一个写入任务并阻塞等待其完成"），采用临时文件+rename 原子写（async_writer.go:6-7）。因此：(a) `PersistRequestLog` 中 body 落盘（lite_telemetry_sink.go:165）返回时刻 mtime 已定，meta 提交（:168）在其后——mtime 与 meta 提交次序有保障；(b) `TurnFileModTime`（bodies_store.go:256-273）经 `os.Stat` 只能看到 rename 后的完整文件，永远见不到半写状态。
- `DeleteTurnFile`（os.Remove）与 `TurnFileModTime`（os.Stat）并发：两个独立 syscall，最坏交错是 stat 成功→remove 成功（删除目标正是孤儿，安全）或 stat ENOENT→保守跳过。无竞态危害。

### 1.4 ListIdleSessions 与 session 写入并发

**结论：安全，且残留竞态被双保险兜住。**

- SQLite 默认 PRAGMA `journal_mode=WAL`（storage/sqlite/schema.go:87）：读不阻塞写。worker 读 sessions 快照与 sink 的 `ensureSession` Get→Update（lite_telemetry_sink.go:185-201）并发无锁冲突。
- 真正的语义竞态：会话在枚举后、Reconcile 前被新请求触碰（updated_at 刷新 + 新 turn 写入）→ 会话进入对账而新 turn 处于在途窗口。此时新 turn 若被误报孤儿，由复检（meta 已提交）或 mtime 宽限（文件新鲜）双保险保住——与 consistency.go:26-27 "纵深防御"注释一致。这正是三层数（空闲阈值 10min + 复检 + mtime 宽限 10min）设计的意义。
- SQL 为参数化查询（session_store.go:223），无注入面。

---

## 二、业务闭环

**结论：闭环成立；一处覆盖面缺口见 F1（P2）。**

- 生命周期：`initStorageMode`（cmd/gateway/storage_mode_init.go:162-183）→ `trimmerWG.Add(1)` 先于 `go worker.Start(trimmerCtx)` → `Shutdown` 先 `trimmerCancel()` 再 `waitTrimmers(3s)`（:228-253，shutdownOnce 幂等）。Start 各等待点均响应 ctx，测试钉住"首跑延迟内 Shutdown 即退"（storage_mode_init_test.go:313）。
- 枚举失败：RunOnce 返回 error → Start 记 Warn 后**继续 ticker 循环**（consistency_worker.go:172-175）——worker 不死亡，下一周期重试。
- 单会话失败：`continue`（:206-209），不拖垮整轮（测试 :228-235 钉住）。
- Shutdown 3s 界：ctx 取消后 RunOnce 的每会话检查使退出在毫秒级；500 会话 × 每会话一次即时失败的 QueryContext 远小于 3s。超时（waitTrimmers :251-252）只告警不阻塞进程退出，进程退出即回收协程——无卡死风险。**评价：够用。**
- F1（P2）：单轮上限 500 + `ORDER BY updated_at DESC LIMIT ?`（session_store.go:22，无游标/分页）→ 表内空闲会话 >500 时，每轮都选中**同样的最新 500 个**，更老会话（崩溃孤儿的所在）永久进不了对账窗；lite 模式 sessions 表无保留期清理任务（仅 API DeleteSession，grep 证实无 prune worker），流量持续时老会话位次只退不进。consistency_worker.go:25 与 config/storage.go:77 注释"超出部分留待下一轮继续"与实际行为不符。缓解因素：BodiesTrimmer 30 天保留期最终会整目录清掉老 body，磁盘不会无限增长——缺口在于**检出/报告**（B-#2 的第一目标）对老会话永久失效。

---

## 三、状态机/分支完备

**结论：分支矩阵完备，唯一"静默吞掉"见 F3。**

RepairTurnArtifacts（storage/consistency.go:195-260）对关键输入的分支矩阵逐项核验：

| 输入组合 | 行为 | 证据 | 测试 |
|---|---|---|---|
| report==nil / action≠delete / 无孤儿 | 返回空 RepairResult, nil | :196-198 | consistency_repair_test.go:230-234 |
| turns==nil + delete | error（拒绝） | :199-201 | :236-240 |
| 孤儿复检命中 meta | SkippedInFlight | :218-220 | :123-144 |
| mtime 新鲜 | SkippedByGrace | :227-229 | :159-170、lite_test :316-330 |
| mtime ErrNotFound | 跳过（无桶） | :230-232 | :184-194 |
| mtime 其他错误 | SkippedByGrace | :233-235 | （未直接测，代码路径同上分支） |
| 待删但无 TurnFileDeleter | **error**（B-#6） | :244-249 | :208-223 |
| 删除中途出错 | 返回部分 res + error | :253-255 | （未测中途失败） |
| opts==nil / 零值 | 默认 10min 宽限 | :103-108 | lite_test 默认宽限用例 |
| opts 负值 | 禁用宽限（测试用） | :108-110 | 多处 OrphanGrace:-1 |

三桶互斥完备性：每个孤儿恰好落入 Deleted / SkippedInFlight / SkippedByGrace 之一，**除了** mtime=ErrNotFound 的"已消失"孤儿（落入零桶，consistency.go:230-232）与复检/删除中途出错时剩余未处置孤儿（随 error 返回，桶外）。前者为刻意的第四种处置但未建模进 RepairResult，见 F3。

---

## 四、数据兼容

**结论：通过。**

- 旧 YAML 无 `consistency` 段：`LiteConsistencyConfig` 为值类型内嵌字段，yaml.Unmarshal 缺段即零值，`ApplyLiteDefaults` 兜底（config/storage.go:205-217），不报错（TestLiteConsistencyDefaults）。
- interval_hours=0/负、idle_threshold_min=0、max_sessions_per_run=0：ApplyLiteDefaults 补 24/10/500；worker 侧 With* 再兜一层（`d>0` 才生效，consistency_worker.go:98-117，测试 :132-136 钉住零值穿透）。无除零/ticker panic 面：interval 最终恒 ≥1h（applyPositiveIntEnv 只接受正整数，config.go:703-712）或 worker 默认 24h。
- Enabled *bool 三态：未配置(nil)→默认 true；YAML false 保留（:205-208 + TestLoadStorageConfigYAMLConsistency）；env 覆盖仅在 nil 时写入（applyOptionalBoolEnv，:386-400）。env 优先级与包内既定惯例一致——本包 applyEnvOverrides 的既定语义就是"env 只填充 YAML 未提供的零值字段，显式配置不被覆盖"（config/storage.go:266-269 头注释），并非"env 压过 YAML"。新代码遵守了该惯例。
- 注意点：`Enabled` 用指针使 YAML 显式 false 可表达，而 `DeleteOrphanBodies` 为普通 bool——YAML 显式 `delete_orphans: false` 与未配置不可区分，env `LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS=true` 会置位覆盖（:356-364）；env=false 无法中和 YAML true。类型限制所致，见 F7。

---

## 五、数据溯源/调用方完备

**结论：通过。**

- `RepairTurnArtifacts` 签名变更（+turns,+opts → +*RepairResult）后生产调用方全仓 grep 仅 1 处：bg/consistency_worker.go:223，已适配；`ReconcileTurnArtifacts` 生产调用方仅 worker:199。无遗漏（含测试均已更新，编译通过佐证）。
- 编译期断言齐备：FileBodiesStore 四接口（bodies_store.go:32-37）、SQLiteSessionStore 实现 IdleSessionLister（session_store.go:35-38）。factory 的既有断言块（factory.go:39-45）未加新接口断言——非必须（lite 装配处有运行时类型断言降级保护），见 F8 顺带建议。
- memory 包无 bodies store 实现，worker 装配路径不可能要求其实现 Stater。
- TurnFileStater/TurnFileDeleter/BodiesLister 均为消费者驱动的小接口，定义在消费者侧 storage 包，方向正确。

---

## 六、测试有效性

**结论：新测试真实钉住核心行为；存在 4 个未测关键路径（F5/F6）。**

已钉住（有效）：复检序列竞态（可控序列 fake 精确复现"两次快照之间 meta 提交"，fake+真实 SQLite 双层：consistency_repair_test.go:123-144、consistency_lite_test.go:262-298）；mtime 宽限含真实文件 Chtimes 回拨（lite_test:303-345）；B-#6 error 语义与 nil turns（repair_test:208-241）；默认恒安全三处独立钉住（config/worker/init 各一）；worker Start 首跑+周期+优雅退出（worker_test:237-270，含 ctx 取消 2s 超时断言）；init 装配三态 + Shutdown 即退（init_test:261-313）；ListIdleSessions 排序/跨租户/limit/空切片（session_store_test.go:277-345）；config env-only、YAML 显式 false、非法值不采纳（storage_test.go:441-563）。

未测关键路径：
1. env 与 YAML 同是的优先级组合：YAML `enabled: true` + env `CHECK_ENABLED=false`（应 YAML 胜出）、env 非法值 → 默认 true。
2. RunOnce 中途 ctx 取消返回 ctx.Err() 路径（Start 测试只取消空闲循环）。
3. Repair 删除中途单文件失败返回部分 res+error。
4. worker 级 delete 模式的 fake 未实现 TurnFileStater——mtime 宽限与 worker 的组合只在 storage 层验证（可接受，層次分工合理）。

---

## 七、发现清单

### P2

**F1（P2，业务闭环/覆盖面）：空闲会话 >500 时老会话被永久饿死，"留待下一轮继续"的注释声明与实现不符**
- 证据：bg/consistency_worker.go:184（`ListIdleSessions(ctx, idleBefore, w.maxSessions)`）、storage/sqlite/session_store.go:22（`ORDER BY updated_at DESC LIMIT ?`，无游标/OFFSET）、bg/consistency_worker.go:25 与 config/storage.go:77（"超出部分留待下一轮继续/按最近活跃优先继续"）；lite sessions 表无保留期清理（全仓仅有 API 层 DeleteSession，无 prune worker）。
- 影响：空闲会话总数 >500 时，每 24h 轮恒选同一批最新 500 会话，更老会话（恰是崩溃孤儿所在）永久不被对账——B-#2"生产中永不检出不一致"的核心缺口在中等规模部署即复现。磁盘有 BodiesTrimmer 30 天保留兜底，损失的是检出/报告能力与 delete 模式的提前回收。
- 建议：(a) 每轮记录上次推进位（last updated_at 游标）做轮转分页；或 (b) 排序改 `updated_at ASC`（最老优先，孤儿最集中的方向）保 500 上限；或 (c) 上限改按批循环直至取尽（每批 500、有界总预算）。至少先修正注释避免误导运维。

### P3

**F2（P3，性能）：`listIdleSessionsSQL` 无 updated_at 索引，每轮全表扫描+排序**
- 证据：storage/sqlite/schema.go:22-33（sessions 仅 tenant/created_at 与 user/created_at 索引）、session_store.go:22。
- 影响：lite sessions 表无清理（见 F1）持续增长时，每 24h 一次的全表扫描成本随之线性增长。
- 建议：`CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions (updated_at DESC);`（SchemaSQL 幂等，自动补建）。

**F3（P3，可观测性/结果桶不完备）：已消失孤儿（mtime→ErrNotFound）不计入 RepairResult 三桶中的任何一个，也无日志**
- 证据：storage/consistency.go:230-232（`continue` 无桶）；bg/consistency_worker.go:225-232 只打印 SkippedInFlight/SkippedByGrace。
- 影响：行为安全（不误删），代码有注释、测试有覆盖（repair_test:184-194），但删除轮的结果聚合中这些 turn"凭空消失"，审计对账时无法解释差异。
- 建议：RepairResult 增加 `Vanished []int`（或并入 SkippedByGrace 并在 JSON 注释区分），worker 日志透出。

**F4（P3，并发 API 约束）：ConsistencyWorker 统计字段无锁，导出的 RunOnce 与 Start 循环并发调用构成数据竞争；统计字段无 getter**
- 证据：bg/consistency_worker.go:43-48（字段）、:179（导出 RunOnce）、:240-244（尾部赋值）；注释 :47 "供测试与监控读取" 但无任何导出访问器（包外不可读，注释承诺落空）。
- 影响：生产串行路径安全（-race 绿）；但外部测试/工具经导出 API 并发触发即 race，且监控根本读不到统计。
- 建议：加 `sync.Mutex` 或改 atomic 计数并提供 `LastRunStats()` 快照方法；或把 RunOnce 改非导出、暴露受锁保护的 TriggerOnce。

**F5（P3，测试缺口）：config env/YAML 优先级组合未测**
- 证据：config/storage_test.go:441-563 仅覆盖 env-only 与 YAML-显式-false；未测 YAML true + env false（applyOptionalBoolEnv 应保持 YAML true，config/storage.go:387）与 env 非法值（应保持 nil → 默认 true）。
- 建议：补两个 t.Setenv 组合用例，钉死"YAML 显式配置胜过 env 填充"的包惯例。

**F6（P3，测试缺口）：RunOnce ctx 中途取消路径、Repair 删除中途失败的部分结果路径未测**
- 证据：bg/consistency_worker_test.go 全文无取消中途的 RunOnce；consistency_repair_test.go 无 DeleteTurnFile 出错用例（storage/consistency.go:253-255 返回部分 res+error 的语义未钉）。
- 建议：fake deleter 注入第 N 个删除失败，断言 res.Deleted 为前 N-1 个且 error 非 nil。

**F7（P3，加固）：`delete_orphans` 非指针 bool 导致 YAML 显式 false 无法被表达，env=true 恒可置位**
- 证据：config/storage.go:73-75（bool 类型）、:356-364（parseBoolEnv 真值即置位，覆盖 YAML 零值/false）。运维显式写 `delete_orphans: false` 防delete，但部署环境继承的 `LLM_GATEWAY_CONSISTENCY_DELETE_ORPHANS=true` 仍会打开删除模式。
- 影响：危险开关的"显式关闭"语义缺失（与 Enabled 的指针三态不对称）。
- 建议：`DeleteOrphanBodies *bool` 三态化，env 仅在 nil 时写入，与 Enabled 同惯例。

**F8（P3，性能/小）：Repair 复检对每个孤儿全量重读一次 GetTurnsMeta，O(孤儿数 × 会话轮数)**
- 证据：storage/consistency.go:212-216（循环内 turnAbsentInMeta）+ :262-274（每次全查）；SQLiteTurnsStore.GetTurnsMeta 每次全量拉取该会话全部行（turns_store.go:64-91）。
- 影响：崩溃会话常见"几十轮全孤儿"，一次 repair 触发几十次全量查询；24h 频率下可容忍。
- 建议：复检快照取一次后做集内判定（每删一个前可再增量确认单个 turn），或提供按 turnNo 的 EXISTS 查询。

**F9（P3，健壮性/小）：NewConsistencyWorker 不校验 nil 依赖**
- 证据：bg/consistency_worker.go:72-81（直接存字段）；nil sessions/turns/bodies 时 RunOnce 在 :184/:199 处 nil 接口调用 panic。当前唯一装配点保证非 nil（storage_mode_init.go:164-165 工厂非 nil 返回）。
- 建议：构造时校验并 panic-fast 或返回 error，把装配错误从运行期提前到启动期。

---

## 八、验证记录

- `go build ./...`：通过
- `go vet ./storage/... ./bg ./config ./cmd/gateway`：无输出（通过）
- `go test ./storage/... ./bg/... ./config/... ./cmd/gateway/... -count=1`：12 个包全 ok
- `go test -race ./storage ./storage/file ./storage/sqlite ./bg ./config ./cmd/gateway -count=1`：全 ok，无竞态报告

## 九、正面亮点

- 三层防御（空闲阈值 → 复检 → mtime 宽限）方向判断准确：读-读窗口危险方向的推导（consistency.go:18-27）与写序（lite_telemetry_sink.go:139-178）完全对上，mtime 宽限恰好覆盖 body→meta 提交延迟。
- 恒安全默认贯彻到三层：config 零值兜底、worker With* 二次兜底、RepairOptions 零值取保守宽限。
- 注释-代码-测试三者一致性好：序列 fake 精确建模 TOCTOU，真实 SQLite/文件系统端到端用例（Chtimes 回拨）补齐 fake 无法覆盖的 mtime 语义。
- 装配降级路径（非 SQLite session store 时禁用并告警，storage_mode_init.go:178-182）有明确日志与启动参数透出（:196-200）。
