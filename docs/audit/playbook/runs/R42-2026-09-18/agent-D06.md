# D06 双重存储架构 子代理报告（窗口：0a015af51^..b75c91900,即 2026-09-17 05:00 → 2026-09-18 05:02）

本轮域内改动:`durable/rls.go`(新增)、`durable/store*.go`、`durable/pending_outbox.go`、`durable/settlement_outbox.go` 及各 `_test.go`、`sql/migrations/startup/722_*`(+down)、installer embeddata 722 副本 + runner 登记、`db/rls_policy_census_test.go`。以下均为线索,待主代理亲读复核。

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1 候选**(窗口延续既有断口) | **installer 全新安装通道缺 durable 基表,窗口把 722 注册进该断序列**。安装器 StartupFiles 无 `516_durable_llm_tasks.sql`/`520_durable_task_settlement_intents.sql`(embeddata 全目录 grep 零命中),`01-schema.sql` 也无 durable 表;但序列含 `657`(裸 `ALTER TABLE durable_llm_tasks`,无存在性护栏)与本窗口新增的 `722`(`ALTER TABLE durable_llm_tasks` + `durable_pending_outbox` FK 引用该表)。runner 逐文件 `psql --single-transaction ON_ERROR_STOP=1` → 全新安装首先在 657 处 42P01 崩,即使修掉 657,722 同样崩 → `InitSchema` 报错,installer main.go:938 整个安装中止。**722 头注释"全新安装(516 已完整建齐)跑本迁移为零变化"仅对迁移通道为真,对安装器通道为假** | `installer/internal/dbinit/runner.go:99,170`;`sql/migrations/startup/722_durable_family_schema_convergence.sql:50,66`;`sql/migrations/startup/657_durable_llm_tasks_decision_history.sql:20`;`installer/internal/dbinit/runner.go:207-213,232-234`;`installer/cmd/llm-gw-installer/main.go:938-940`;守卫:`installer/cmd/llm-gw-installer/stats_migrations_test.go:341-376` | 把 516/520 纳入安装器交付(或 657/722 前置护栏跳过);同时修正 722 头注释 |
| 2 | **P2 候选**(RLS 横向不变量漏网读点,Phase 2 降权后生效) | `pending/pg_source.go` 的 `Get`/`GetLatest` 以 pool autocommit 直读 `durable_llm_tasks`,**无 GUC、无显式事务**——durable/rls.go 通道之外唯一漏网的 durable 族 SQL 读点。降权后无 GUC → 行不可见 → Redis 投影丢失时回源静默 not-found | `pending/pg_source.go:60-64,76-80`;装配:`cmd/gateway/main.go:2823`;policy 形状:`sql/migrations/startup/722_...sql:83-85` | Phase 2 前给 PGSource 包显式事务+旁路,并加入 rls.go 的路径清单 |
| 3 | P3 | `durable/rls.go` 头注释称 durable_task_settlement_intents 也是双 policy 形状;实际 520 是单条合并 policy `durable_task_settlement_access`。注释漂移 | `durable/rls.go:3-5`;`sql/migrations/startup/520_...sql:46-56` | 顺带修注释 |
| 4 | P3(预存在) | `ProjectPendingOutbox` 在 `FOR UPDATE OF o` 行锁+开事务期间做 Redis 网络调用,`markProjectionFailed` 固定 1 分钟重试无退避 | `durable/pending_outbox.go:48-58,144,179` | 登记为债 |
| 5 | P3(预存在) | lite/无 DB 下 `RequestSurvivalDurableEnabled=true` 时 durable 块整体静默跳过,无 API 层显式信号 | `cmd/gateway/main.go:2782,2812-2815` | 低优;/healthz 可补 durable 状态位 |

## 二、核实为健康的面

- **722 收敛体与 durable/* 代码逐列一致**:`durable_llm_task_events` 10 列与全部 6 处 INSERT 对齐;`durable_pending_outbox` 8 列与 upsert/SELECT/markProjectionFailed 对齐;`checkpoint_payload JSONB` 与 CheckpointCommitState 的 `$6::jsonb` 对齐;taskColumns 25 列均在 516 最终形态内。722 保留的三个历史列代码 grep 零引用——无 516 式中间形态残留。
- **durable/rls.go GUC 通道全覆盖**:worker 17 条路径全部 Begin+GUC 或 execWithBypassTx/queryWithBypassTx;包内 `s.db` 直连 SQL 零命中;streaming worker 15 个 store 调用全落覆盖面内;pgxmock 钉桩 34 处引用,PASS。
- **lite/sqlite 双模**:lite 下 durable store/worker/PG 回源全部不装配;新增 durable 写路径属 PG 专属,lite 下结构性禁用——无新破口。
- **分区不变量**:722 全文零分区 DDL;durable 族非分区表,与 hot/columnar 拓扑无交集。
- **双通道一致性**:embeddata 722 up/down 与 sql/migrations 逐字节一致;五点齐。
- **census 守卫**:三重守卫,722 六条 policy 与 516 逐字同形,722 不会回退 720 的词汇统一。

## 三、未覆盖项与原因

- 722 真库实跑复核(需 TEST_DB_URL);installer 全新安装端到端复现(需 docker compose 全新装);`durable/pg_integration_test.go`(-tags integration)未跑;D06 清单#3/#6 超出窗口改动面。
