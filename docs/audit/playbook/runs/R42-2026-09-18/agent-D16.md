# D16 流程/数据/反馈闭环 子代理报告(窗口:0a015af51^..b75c91900)

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P0 候选**(全新安装通道硬失败;缺口早于本窗口,但本窗口 722/723 登记在同一条链上延续并加重) | **installer 全新安装必崩于 657(42P01)**:embeddata/01-schema.sql 完全不含 durable 家族表,唯一创建者 516 不在 installer 任何同步点,而 StartupFiles 在 657 处无条件 ALTER TABLE durable_llm_tasks。触发路径:llm-gw-installer 全新装 → InitSchema 先应用 01-schema(无 durable 表)→ 到 657 → psql ON_ERROR_STOP=1 --single-transaction 报 relation does not exist → 安装中止。同一条 embeddedSQLFiles 也喂 copySQLBackup→db/init,docker initdb 字母序同样先跑 657,同崩 | installer/internal/dbinit/runner.go:99(657 登记)、:228-234(ON_ERROR_STOP);embeddata/01-schema.sql 无 durable 表(grep 证实);sql/migrations/startup/657:33(无条件 ALTER);516 零登记 | 把 516(及 520)补进 installer 五点同步并插 StartupFiles 正确位次(515 与 521 之间) |
| 2 | P1 候选(与 #1 同根,独立成立) | **即便 657 通过,722/723 在 installer 链上也各自 42P01**:722 的 checkpoint_payload ALTER 与 durable_pending_outbox FK 引用缺失表;723 对 candidate_failure_logs_columnar_old——该表在 sql/migrations 全目录无任何创建者(仅 fixes 引用)。两文件已在本窗口登记进 StartupFiles | sql/migrations/startup/722:49-50,66;723:25;installer/internal/dbinit/runner.go:170-171 | 与 #1 一并处置;723 登记前应核对依赖表在三条基线的存在性——存量新库走 revision-sequence 也会崩 |
| 3 | P2 候选(注释/断言漂移) | revision-sequence 中 722 的注释"全新安装零变化"只在 canonical 链成立,对 installer 链为假 | scripts/apply-db-revision-sequence.sh:452-455;runner.go:167-170 | 修 #1 后自然成立;否则改措辞 |
| 4 | P3(反馈闭环部分闭合,commit 已自declared 半边) | balance_error 只有手动 ⟳ 按钮路径会写;两条后台探测路径失败时均不写:floor guard 失败仅 fail-open(无日志),probe_v2 失败分支未改——"厂商余额端点故障把失败原因展示到 UI"只覆盖操作员主动刷新场景 | bg/balance_floor_guard.go:999-1006;bg/credential_probe_v2.go:575-589 | 登记债或补齐:后台失败统一写 balance_error(注意 #12a 退避语义) |
| 5 | P3(数据闭环小缺陷) | 520 建的 durable_task_settlement_intents 在 installer 全新装库中缺失且无人报错:Go 侧消费者经接口访问该表,installer 链缺 520 → 全新装库上 durable worker 首次结算写入即运行时失败 | sql/migrations/startup/520(创建者,installer 零登记);domains/streaming/durable_recovery_worker.go:34-38,579;durable/pg_integration_test.go:129 | 并入 #1 的 516/520 补登记 |
| 6 | P3(parity map 覆盖盲区说明) | 五点同步测试族对 <704 的迁移免检:显式 num < 704 continue——这正是 516/520 漏登记零测试报警的原因 | installer/cmd/llm-gw-installer/stats_migrations_test.go:334-376 | 若修 #1,可为"durable 族基表在 installer 库存在"加 schema 级契约测试 |

## 二、核实为健康的面

- **720/721/722/723 五点同步完整性(canonical 逐字节)**:9 个文件 cmp 全 OK;go:embed var、embeddedSQLFiles map、StartupFiles、parity map、revision-sequence 登记五点齐全。实测 installer 双包测试全绿。
- **登记顺序 720<721<722<723** 三处均升序;session_turns_hot_bootstrap 保持队尾且有形锁测试。
- **撞号残留清零**:723 直接以 723 号入账,无 "721" 残留引用,无陈旧共引。
- **余额刷新反馈闭环端到端闭合**:UI ⟳ → refreshCredentialBalance API → 路由 case 分发 → handler 三分支 → providercap GET-only → DB UPDATE → listCredentials 暴露 → i18n → UI 渲染。数据三问:来源/去处/清理全部闭环。
- **24h 手工保护窗两处谓词一致**(SELECT 侧),PATCH 手工输入写 source+checked_at。
- **ON_ERROR_STOP 语义**:每文件独立 --single-transaction,失败即中止,fail-fast 无半套 schema 静默成功。

## 三、未覆盖项与原因

真机全新装复现 657 崩溃(只读不能起 docker;强推断,建议主代理实跑定性);hostedtask 回调闭环(窗口零改动);mirror_outbox 重放运行期行为(需真库);720/723 存量真库实跑(轮文档记载为转述级证据)。
