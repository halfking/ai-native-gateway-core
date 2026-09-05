# D-2#6 修复包 2：7 张遗留 hot 表 promote 函数原子化重装（迁移 659）

日期：2026-09-05 ｜ 分支 main @ cab1bbf8c ｜ 子代理：pkg2-promote-659

## 产出文件

- `sql/migrations/startup/659_legacy_promote_atomic_cte.sql`（up，7 个 `CREATE OR REPLACE FUNCTION` + 运行时自检 DO 块 + schema_migrations 登记）
- `sql/migrations/startup/659_legacy_promote_atomic_cte.down.sql`（down，逐字还原 659 前的旧函数体）
- `sql/migrations/startup/migration_659_test.go`（契约测试，package startup，`TestMigration659LegacyPromoteAtomicContract`）

未触碰任何禁改目录；未 git commit/add。

## 每张表的事实核对（以迁移链最终状态 + 真实库为准）

签名/默认值全部保持不变：`(p_retention interval DEFAULT '7 days'::interval, p_batch_size integer DEFAULT 5000) RETURNS bigint`（bg/admin 调用方均显式传参，仅影响 psql 直调）。

| # | 表 | promote 体来源（链上最终状态） | ts/谓词列 | 批次键（DELETE join） | 父表唯一约束 | ON CONFLICT 决策 |
|---|---|---|---|---|---|---|
| 1 | usage_ledger | 399 重装（344 原体去 ON CONFLICT） | `ts` | `(request_id, ts)` | `UNIQUE (request_id, ts)` | **不加**（2026_08 叶子为 columnar） |
| 2 | request_wal | 345 占位体（"无 ts"→恒返回 0） | `created_at` | `(request_id, created_at)` | `PK (request_id, created_at)` | **不加**（2026_08 叶子为 columnar） |
| 3 | routing_decision_log | 399 重装（346 原体去 ON CONFLICT） | `ts` | `(request_id, ts)` | **无任何约束** | 纯 INSERT |
| 4 | credential_model_index | 399 重装（347 原体去 ON CONFLICT） | `updated_at`（分区键 `bucket`） | `(bucket, credential_id, raw_model)` | **无任何约束** | 纯 INSERT |
| 5 | tool_usage_stats | 348 原体 | `usage_date`（分区键 `created_at`） | `(tool_id, tenant_id, usage_date)` | `PK (id, created_at)` + `UNIQUE (tool_id, tenant_id, usage_date, created_at)` | 不加（全 heap，但为一致性与未来 columnar 分区安全统一省略） |
| 6 | credit_ledger | 349 原体 | `created_at` | `id` | `PK (id, created_at)` | 不加（同上） |
| 7 | request_logs_bodies | **528**（353/455 之后 528 已改为 TTL 两阶段；D-2#6 原文据 353 快照判其仍为 temp+EXCEPTION，实际 528 的 promote 阶段已是单语句 CTE，但缺守卫/预 ensure/SKIP LOCKED 且用 `RETURNING *`——本次补齐并保留其 TTL 语义） | `ts` | `request_id`（hot 上 UNIQUE） | **无任何约束** | 纯 INSERT |

**列清单来源**：live 容器 `llm-gateway-pg`（127.0.0.1:5432, kx-citus-pg17）`information_schema.columns` 按 ordinal order 提取，并与迁移链交叉核对（344/032/333/335/349/353/445/2026-07-13-multimodal 等）。全部 7 对表 hot/parent 列序逐一确认。

**ON CONFLICT 决策依据**（与 656 有意分歧，文件头已注释）：
- 399 已论证 Citus columnar 不支持 speculative insertion（`ON CONFLICT` 即报错），5/7 张表存在 columnar 叶子（routing 全部、usage/request_wal/credential 的历史月份、bodies 2026_08/09，live `pg_am` 实查）。
- routing_decision_log / credential_model_index / request_logs_bodies 父表无任何 PK/唯一约束，`ON CONFLICT` 无 arbiter 可言。
- 并发双 promote 由批次行锁（持至提交）+ `SKIP LOCKED` 天然去重；若真出现对约束父表的键冲突，现在会**报错上抛且 hot 行保留**（原子 CTE），而非旧体的"吞错+行已删"。
- 602/624 两个同类先例也用纯 INSERT + 错误上抛。

**ensure 函数**（本迁移只调用、不重装，签名 live 实查）：
`ensure_usage_ledger_partition(target_month timestamptz)`(330)、`ensure_request_wal_partition(target_ts timestamptz)`(305)、`ensure_routing_decision_log_partition / ensure_credential_model_index_partition(target_month timestamptz)`(319)、`ensure_tool_usage_stats_partition / ensure_credit_ledger_partition(target_month timestamptz)`(475)、`ensure_request_logs_bodies_partition(target_ts timestamptz)`(562)。预 ensure 循环迭代**分区键列**的月份（credential 按 `bucket`、tool_usage_stats 按 `created_at`，谓词列与路由列不同），与批次 CTE 同谓词限定，LIMIT 12（656 同款）。`now()` 全程使用（事务内稳定，保证预 ensure 与 CTE 谓词零漂移；旧体也用 now()）。

## request_wal 语义激活说明（唯一行为变更点）

345 占位体注释称"request_wal 没有 ts 字段"，但 `032_request_wal.sql` 起 `created_at` 就是分区键+PK 组成部分（live 表有 17 列含 created_at）。占位导致 request_wal_hot 无界增长（live 实测 52,874 行，最老 2026-09-03），而 bg 调度每小时照常调用。659 按 656 模板激活真实 promote；批次上限 + SKIP LOCKED + 原子性使首日回灌为有界渐进（每小时 ≤ batch_size 行）。

## down 决策

**建了 down**。依据：602（同类 promote 原子化迁移的最直接先例）有 `.down.sql` 且其内容就是逐字还原旧体 + 顶部 WARNING（624/628/615/626/656 均有 down；仅 534/657/399 无）。`659_...down.sql` 从迁移链逐字还原 7 个旧体（399 的 4 个 temp+handler 体、345 占位体、348/349 带 ON CONFLICT 的 temp 体、528 的 TTL 体），头部 WARNING 明示会重新引入丢失窗口，仅限紧急回滚。测试断言 down 含 7 个 CREATE OR REPLACE 且确为旧体（`_promote_hot_batch` / handler 标记）。

## 真实 PG 重放与行为验证（容器 llm-gateway-pg，全部事务内 ROLLBACK，零残留）

up 重放：7 × CREATE FUNCTION + 自检 DO（NOTICE verified）+ INSERT schema_migrations + COMMIT 全通过；**重复执行 3 次幂等**。重放后 `pg_get_functiondef` 确认 7 函数为原子体（无 handler/temp/星号投影，含 SKIP LOCKED 批扫）。

逐表行为验证（测试行前缀 `659test-`，2020 老时间戳保证被小批次优先选中；p_retention=1s, batch=10）：

| 表 | moved | 父表测试行 | hot 残留 | 判定 |
|---|---|---|---|---|
| usage_ledger | 10（2 测试+8 最老真实行，事务内一并搬迁后回滚） | 2 | 0 | PASS |
| request_wal | 10 | 2 | 0 | PASS（占位体 → 真实搬迁首证） |
| routing_decision_log | 10 | 2 | 0 | PASS（迁入经 enforce_columnar 事件触发器转为 **columnar** 的新分区，纯 INSERT 成功，直接验证免 ON CONFLICT 设计） |
| credential_model_index | 10 | 2 | 0 | PASS |
| tool_usage_stats | 2 | 2 | 0 | PASS |
| credit_ledger | 2 | 2 | 0 | PASS |
| request_logs_bodies 阶段1（TTL 过期） | processed=1 | 0 | 0 | PASS（过期行删除且不进父表，528 语义保留） |
| request_logs_bodies 阶段2（promote） | processed=10 | 1 | 0 | PASS |

回滚干净度复检：`659test%` 行 0、`%_2020_01` 分区 0；仅 id 序列因 DEFAULT nextval 前跳 2 个值（credit/tool，非事务性、无害）。未污染共享库、未动容器其他对象。无跳过项——7 张表在本地库全部存在。

## 测试与构建

- `go test ./sql/migrations/startup/ -run 'TestMigration659' -v`：PASS（断言：7 函数重装、签名/DEFAULT 不变、7 × SKIP LOCKED 批扫、无 EXCEPTION WHEN/RAISE WARNING、无 temp 表/SELECT *、7 × 参数守卫、7 × ensure 调用、7 × 显式列 INSERT、promote 体无 ON CONFLICT（仅 schema_migrations upsert 1 处豁免）、528 TTL 阶段保留、down 还原旧体）。
- `go test ./sql/migrations/startup/`（全包）：ok。
- `go build ./...`：通过。

## 遗留与移交

1. **installer embed 注册未做（本包权限禁止碰 installer/）**：`installer/cmd/llm-gw-installer/main.go` 需新增 `659_legacy_promote_atomic_cte.sql` 的 go:embed + 两个 map 条目（参照 :250-251/992/1131 的 656 三处写法）；`stats_migrations_test.go` 的 expected map 增 `659` 条目；embeddata/startup/ 拷贝 up 文件（本项目惯例 down 不入 installer 资产）。留主代理/部署会话完成（即审计 D-2#3 同款缺口，勿让 659 复现）。
2. **数据保真度（另行立项，非本包范围）**：`usage_ledger_hot` 的 reasoning/image/audio/video/provider_tokens 5 列（2026-07-13 仅加在 hot）与 `request_logs_bodies_hot.tenant_id` 无父表对应列，promote 时无法携带（旧体下这些行是整批丢失；视图本就不暴露这些列）。要让多模态 token 计数进入历史归档，需单独的父表 ADD COLUMN 迁移。
3. **2026-07-13 起 usage_ledger promote 实际全军覆没**（hot 24 列 vs 父 19 列 → 每批 42601 → 吞错 + 行已删）。659 修复后建议运维核对 7 月中旬以来 usage_ledger 账单缺口是否有可从 hot 存活行/备份回补的数据（659 部署后 hot 中现存行即可正常搬迁）。
4. D-2#6 之外的关联项未动：D-2#2（V371 supplier_errors）、D-2#8（602 DEFAULT 7d→8h）、D-2#11（admin 手动 promote 无 advisory lock）。
