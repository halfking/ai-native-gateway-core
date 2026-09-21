# 2026-09-12 request_logs 视图补 raw_model_name 列（迁移 700）——drift scanner 42703 根修

## 概要

`integrity_fingerprint_drift`（7 天指纹漂移检测器）自读面切到 `request_logs_with_current_month`（83bf582dd，近期窗口读面教义）起，每周期报 `column "raw_model_name" does not exist (SQLSTATE 42703)`——视图包装链的基础交集在 migration 485（父表加 raw_model_name）之前冻结，链上从未重建，该列从未出现在视图上。696 只补了 system_fingerprint，没有覆盖 scanner 同时需要的 raw_model_name。本迁移（700）按 696 同款 lateral 形状把列补进 canonical 视图。

## 根因链与影响面（已实查双库）

- 基础包装 `…_without_customer_id` 停在 pre-485 冻结交集（108 列）；577/610/696 的 lateral 只在链顶追加列，从不重建基础层；680 引导 / db.go 自愈只救"canonical 缺失"，不救"canonical 陈旧"（canonical 存在即 RETURN）。
- **本机 2089 部署**：scanner 每小时 + 启动即 WARN（active 故障）。
- **生产 252 PG**（2026-09-12 只读取证）：视图 112 列、raw_model_name=0、fp=1，与本机同构——生产当前二进制（2086/2087）尚未携带切视图的 scanner，属**潜伏缺陷**，下一个携带 83bf582dd 的生产二进制上线即在生产线复现。本修复必须在下次生产发布前进通道。

## 修复面

- **迁移 700**（`sql/migrations/startup/700_request_logs_view_raw_model_name.sql`）：696 同款双形状幂等——冻结链重建 canonical（同一 hot-first lateral 同时携带 696 的 system_fingerprint 与 raw_model_name，不回退 696）；动态重建链（680/db.go 自愈，交集自带列）守卫 no-op。纯 CREATE OR REPLACE，无 DROP，down 无。
- **db.go 自愈镜像**（`db/request_logs_view_schema.go`）：696/700 两列均改为按"基础包装缺列 && hot 侧实有列"双探针条件追加；lateral 列引用按底层表实况裁剪——顺带修掉一个预存缺陷：696 之后 live round-trip 测试对真库本就失败（scratch 表无 fp 列，自愈 DDL 42703；CI 离线跳过故未被发现）。
- **installer 接线四点**：embeddata 字节一致副本 + go:embed var + embeddedSQLFiles + dbinit StartupFiles + byte-equality map（1f17dc2ac 同款，本轮补 700）。
- **通道**：apply-db-revision-sequence.sh +700。编号注记（撞号改判）：本迁移按当时双账本核查取 699（07:52 实查 698/699 均空闲），提交后与并行线撞号——该线 2ad8d64ad（698 promote 时区钉扎）/ 531ea1a86（699 supplier_errors ensure 钉扎）先行入 origin，本迁移**重编号 699→700**；重编号前复核生产双账本 698-702 均 0 命中（schema_migrations 与 sequences 均无 698/699/700/701；两线均尚未应用生产，撞号发生在仓库存号层）。教训与 694 跨项目撞号同源：双账本核查只能证明"应用层空闲"，**并行线的仓库存号要在 fetch 后以 `git show origin/main:sql/migrations/startup/` 复核**。
- scanner（bg/）零改动：读面守卫（recent_surface_reads_test）继续钉在视图上。

## 本机 DB 修复（数据操作口径：先备份、单事务、事务内复核）

- 备份：三视图定义 + promote 函数体 + 账本行落盘 `envs/local/backup-llmgateway-viewfix-20260912-075826.sql`。
- 单事务应用 696（守卫预期 no-op：fp 已由 Go 镜像补过）+ 697（promote 函数对齐，补上本机缺的写路径携带）+ 700 + 双账本登记（schema_migrations 补 696/697/700——本机 sequences 账本原有 696/697 标记而 schema_migrations 无行的预存错位一并归齐）。
- 事务内复核：视图 113 列、raw/fp 各恰 1；scanner 形状 SELECT 正常解析；promote 函数 fp 出现 3 次（RETURNING/INSERT/SELECT 三清单）且 695 final-success 自愈保留（is_final_success ×6）；包装链 3 视图完好；样例行经视图可读。COMMIT 成功。

## 验证

- live round-trip（真库 scratch）：动态链形状（fp/raw 经交集继承，canonical=交集+3）与冻结链重放（陈旧包装 + canonical 缺失 → 条件 lateral 补 fp/raw，scanner 形状 SELECT 出数）双场景 PASS；幂等二次调用 no-op。
- 文本守卫 `TestMigration700ViewRawModelName`（含通道钉）+ 既有 695/696/697 守卫全绿；installer 全套（含 TestStartupFilesAreAllEmbedded 字节等价）绿；`go build ./...`、`go vet ./db/`、`./db/ ./sql/migrations/... ./bg/ ./deploy/grafana/ ./deploy/prometheus/...` 全绿。
- 运行中 2089 二进制零重启受益：视图列补齐后，下个整点扫描周期 42703 即停（启动即扫一次 + 每小时）。

## 遗留

- **生产 252 未应用 700**（与 696/697 同通道，等下次生产发布窗口；在 700 进生产前，携带 83bf582dd 的二进制不可上 245/154，否则 scanner 在生产必炸）。
- 视图基础包装仍为 pre-485 冻结交集（缺 48 列），靠 577/610/696/700 lateral 逐次打补丁；如再遇"scanner/读者需要某基础列"同类缺陷，考虑一次性基础层重建迁移（680 形态的"陈旧也重建"变体）。
