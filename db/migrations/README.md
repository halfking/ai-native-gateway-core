# ⚠️ db/migrations/ — 冻结的历史通道，禁止新增与手工执行

本目录是**旧仓库时代的迁移残留**（014 + 352-365），不是活通道：

- **没有执行器接线**：网关启动链（db/db.go Go ensure）与
  `scripts/apply-db-revision-sequence.sh`（startup 编号序列）都不读本目录。
- **与正典通道撞号异文件**：`353_goal_loop_detection` ≠ 正典
  `sql/migrations/startup/353_request_logs_bodies_hot_independence`；
  `354_handoff_schema_fix` ≠ 正典 `354_credential_model_index_hot_independence`；
  `365_goal_client_signal` 是正典 647 的旧编号。撞号意味着"按编号重放"会
  把**另一套 DDL** 打到活库上。
- **唯一消费者**：`scripts/test-phase3-integration.sh`（集成测试脚手架）
  直接 apply `014_create_releases_tables.sql`。该文件与 releases 表的
  正典定义（startup/379/388 一族）并存时靠幂等性兜底，属测试专用。

## 纪律（R37 审计登记）

1. **新的 schema 变更一律走 `sql/migrations/startup/` 编号通道**（并按
   五点同步纪律登记 installer），不要落到这里。
2. **不要手工对真实库执行本目录任何文件**——撞号异文件会覆盖/冲突正典
   通道的同号变更。
3. 本目录仅作历史追溯保留；删除需先清理 test-phase3-integration.sh 的
   唯一引用。
