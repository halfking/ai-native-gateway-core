# D04/D11 子代理报告（窗口：67f78247c..294e0f65d；负责改动面：93f530901 settle worker 合成轮过滤）

> 原文存档（主代理已逐条亲读复核，处置见轮文档）。子代理：Explore，2026-09-17。

审计对象：`git show 93f530901`（bg/auto_route_settle_worker.go + bg/auto_route_settle_worker_integration_test.go）。已核实两文件自 93f530901 起至工作树零改动，以下 file:line 均为当前工作树行号，可直接复核。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| F1 | P3 | 合成轮判定双实现存在理论性微漂移：Go 侧 `IsSyntheticActor` 先 `strings.TrimSpace` 再匹配，SQL 侧 `SQLExcludeSyntheticActors` 的 `NOT LIKE 'goal-%'/NOT IN(...)` 不 trim。若 DB 中 origin_actor 带首尾空白，同一行会被 settle worker Go 过滤（abandon）却仍被同一次 sweep 的 baselines/LATERAL SQL 计入——两口径对同一行不一致。触发路径：origin_actor 写入带空白 → settle 主查询按 Go 判定 abandon 该行，但 loadTaskBaselines 的 p95/p75 仍含该行。当前实际风险极低：入口 `middleware/origin_mw.go:277` 对 `X-LLM-Origin-Actor` 先 TrimSpace 再落库，内部回环 actor 为程序内常量无空白 | autoroute/shadow_actors.go:35（TrimSpace）vs :53-54（SQL 谓词不 trim） | 低优先：在 shadow_actors.go 两函数互引注释声明"写入口已 trim"这一前置条件，或将 SQL 谓词改 `BTRIM(...)`；勿单独改一侧造成反向漂移 |
| F2 | P3（观察项，非本提交引入） | settle 输入仍有一条未过滤合成轮的统计路径：`session_summaries` join 的 `request_count/error_count/health_score` 未过 `SQLExcludeSyntheticActors`，而 session_summaries 聚合写侧也未见合成轮过滤。触发路径：真实会话含 goal- 影子轮 → `ss.request_count` 被合成轮抬高 → `ShouldAttributeSession` 的 0.8 门槛更难达到（方向保守：少归因而非污染奖励）；一旦归因成立，`routingOnlyHealth` 的 errRate 分母含合成轮，奖励值轻微偏移。R37 定义的过滤面清单本就不含 session_summaries，且 mr LATERAL 测试（fixture 显式给 request_count=2）把这一非对称钉为契约——故登记为口径边界而非缺陷 | bg/auto_route_settle_worker.go:394-395（ss join）、:502-504（归因门槛）、:520-540（routingOnlyHealth）；fixture bg/auto_route_settle_worker_integration_test.go:129-131 | 留档为 R37 口径边界的明示项；若未来要收敛，须连 session_summaries 写侧一起改并同步改两个集成测试的期望值 |
| F3 | P3（既有债，非本提交引入；因检查要点 4 登记备查） | `writeReward`/`abandon` 不检查 `RowsAffected`：Redis 关闭双实例同时 sweep 时，后写实例对已 settle 行的 UPDATE 命中 0 行仍会 `settled++/abandoned++` 并递增 `llmgw_autoroute_settled_total`（真实轮路径）——日志与指标可虚高，DB 状态不重复。sweep 注释已声明该降级为接受行为（"counters counted once per instance, not correctness"）。93f530901 的合成轮 abandon 路径刻意**不**递增 prometheus 计数器，与 R37 口径一致，未恶化此项 | bg/auto_route_settle_worker.go:453-455（合成轮 abandon 不增计数器）、:464-467（真实轮计数器）、:556-570/:577-582（UPDATE 均带 `settled_at IS NULL` 守卫但不查 RowsAffected）、:221-231（双实例降级声明） | 既有接受债，无需动；如后续要收，统一改为按 commandTag.RowsAffected()>0 才计数 |

## 二、核实为健康的面

- **同一合成轮定义，无第二套口径（检查要点 1）**：settle worker 三处过滤全部直接引用 autoroute 包单一事实源——Go 侧 `autoroute.IsSyntheticActor`（bg/auto_route_settle_worker.go:449），SQL 侧 `autoroute.SQLExcludeSyntheticActors`（:301 baselines、:403 mr LATERAL）。定义 = 内部回环三 actor（auto-title-generator/auto-summary-generator/session-summary，shadow_actors.go:26-30）+ `goal-` 前缀（:22），与 R37 域文档 §4 记载逐字一致；全仓 7 个消费点（optimizer_bridge.go:197 choke、outcome_feedback.go:240 终态、streaming/handler.go:6183 tuning 门、admin/work_types.go:276、settle 三处）共享同一定义，无字面量复写。
- **settle 内全部统计 SQL 覆盖（检查要点 2）**：文件内共 3 条查询——loadTaskBaselines（:294-303，SQL 谓词）、settleBatch 主查询（:377-405，LEFT JOIN 后 Go 过滤）、mr LATERAL（:396-404，SQL 谓词）。无第 4 条统计路径；唯一未过滤输入是 F2 的 session_summaries（已登记）。
- **过滤位置正确、pending 时序无提前统计（检查要点 3）**：origin_actor 与 success/latency/cost 在同一条 `request_logs_hot` INSERT 原子落库（domains/hooks/observability/telemetry/client.go:1146 起列清单含 origin_stage/origin_actor；PK request_id + ON CONFLICT DO NOTHING 保证 LEFT JOIN 至多一行，无行翻倍），不存在"先按真实轮统计、后变合成轮"的中间态。过滤放在 `p.success == nil` 分支之后（:433-443 abandon 路径在前、:449 合成过滤在后），rl 未落库的行（originActor=nil）照旧走 4h abandon——提交说明的"保留 abandon 路径"与代码一致。且决策时 selection 行写入侧（telemetry/selection_writer.go:227-320，35 列无 actor、无合成过滤）仍会为影子轮写 selection 行，故 settle 侧过滤确是 reward 面的承重闸，补丁落点正确。
- **并发与幂等（检查要点 4）**：leader 选举门 `acquireSweepDistLock("auto_route_settle")`（:225-231，R31 模式）；批内无跨行事务但每行 UPDATE 带 `(id, partition_date) AND settled_at IS NULL` 双守卫（writeReward :566-567、abandon :580），同一轮被两次 settle 在 DB 层不可能（第二次为 0 行 no-op）；selection 侧 `uq_ars_request(request_id, partition_date)` ON CONFLICT DO NOTHING 防重复样本（selection_writer.go:303-306）；批中途崩溃未处理行仍 `settled_at IS NULL` 下轮重扫，无丢行。合成轮 abandon 走 reward=NULL，下游 affinity rollup `WHERE s.reward IS NOT NULL`（bg/auto_route_affinity_worker.go:268）使其永不回灌训练面——闭环封死。500 批上限无饥饿：合成行当轮即 abandon 出 `idx_ars_hot_unsettled` 部分索引，不挤压后续批次。
- **集成测试真锁行为（检查要点 5）**：非 smoke——真 PG 容器 + 真 worker 方法（`loadTaskBaselines`+`settleBatch`），断言精确到行为：settled=1/abandoned=2、真实行 reward∈(0,1]、两合成行 `settled_at IS NOT NULL AND reward IS NULL`（bg/auto_route_settle_worker_integration_test.go:210-246）；任一断言可分别捕获"合成轮被奖励""合成轮滞留 unsettled 索引"两类回归。既有两个守卫（baselines :68-105 经 p95 数值敏感断言、mr LATERAL :107-169 经 reward_source='request' 归因契约断言）均为行为级。Minor：'session-summary'/'auto-summary-generator' 两 actor 未在该级测试覆盖（由 autoroute/shadow_actors_test.go 单测兜住）；合成轮"不增 prometheus 计数器"未钉桩。
- **D04 不变量未回退**：hot 表分区不变量保持（settleAbandonAfter 4h < 8h retention，:53-60 注释与 abandon 路径一致）；sweep 双层 panic 守卫（:172-176、:206-210）与 Start/Stop 幂等（:145-167）均为既有健康面，本提交未触碰。

## 三、未覆盖项与原因

- **未实跑 `-tags=integration` 三轮测试** —— 只读审计环境无 Docker/PG 容器执行条件；仅做了代码级核对（测试 schema 列与 worker SQL 投影逐列对得上，包括 :380 新增 `rl.origin_actor` 投影与 :418 scan 位置）。
- **session_summaries 写侧是否含合成轮未深挖到底** —— 写入点分散（internal/summarystore/store.go:181、domains/sessionsummary 等），不在 93f530901 改动面内，属 D10/会话域；F2 仅按 settle 消费侧登记为线索。
- **生产库真实索引/约束（uq_ars_request、idx_ars_hot_unsettled）未对照活库验证** —— 依据 db/db.go:6142 与 selection_writer 注释，未真库查询（R38 教训：活库断言须以真库为准，此处明示未做）。
- **窗口内其余提交（mDNS、RLS 719、probe 等）不在本子代理改动面** —— 按 conventions §1 未读。
