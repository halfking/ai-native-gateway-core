# 2026-09-07 request-logs 列表 42P01：canonical 视图被带外删除 + 本地部署轨道 CGO 断路

## 现象

`http://localhost:8782/request-logs` 列表查询报错：

```
query failed: ERROR: relation "request_logs_with_current_month" does not exist (SQLSTATE 42P01)
```

## 诊断

数据库状态（共享 llm-gateway-pg / llm_gateway 库）：

- `pg_views` 中 `request_logs_with_current_month` **不存在**；
- 但 577/610 包装链的两个中间视图仍在：
  `…_without_customer_id`（577 时代）、`…_without_request_class_due_at`（610 时代）；
- `schema_migrations` 显示 610 于 2026-08-27 成功应用（视图当时存在）。

结论：**表在、视图没了，且没有任何 migration 或启动逻辑会重建它**。写路径
（telemetry → `request_logs_hot`）不受影响，只有读路径挂。

根因（推断，PG 未记录 DDL）：610 之后某次带外手工操作重放了 341 式迁移——
其开头的 `DROP VIEW IF EXISTS request_logs_with_current_month` 在 autocommit
下先落库，随后的 `CREATE VIEW … SELECT * FROM request_logs_hot UNION ALL
SELECT * FROM request_logs` 因 hot/parent 列数不匹配（603 后 hot 多约 45 列）
而失败——DROP 已提交、CREATE 未执行，正好留下"包装视图在、canonical 消失"的
现场。341 文件本身没有 BEGIN/COMMIT 包裹，这是它重放会留下半成功状态的原因。

## 修复（commit bbf617d03 内，编号 680）

1. **`sql/migrations/startup/680_request_logs_current_month_view_bootstrap.sql`**
   纯增量、单事务、分阶段自愈：canonical 在 → 直接 RETURN（健康库零 DDL）；
   缺哪层补哪层（610 层 → 577 层 → 基础 108 列 UNION）。基础列取
   **hot∩parent 交集**（排除 customer_id/request_class/due_at），HOT_ONLY 列
   永远进不了 UNION——从机制上杜绝 341 式 `SELECT *` 重放的失败模式。
2. **`db.ensureRequestLogsCurrentMonthView`**（接入 `applyMigrationsOnce`）：
   同一套逻辑的二进制内自愈，每次启动幂等执行，视图被删后重启即恢复。
3. **`apply-db-revision-sequence.sh`** 追加 680，升级库部署轨道同步修复。

## 连带修复（同一部署轨道暴露）

- **669 非幂等**：裸 `ADD CONSTRAINT chk_auto_confidence_range` 在重放时
  42710，且文件无事务包裹，半成功后 marker 永不写入，**阻塞整条修复序列**。
  已改为 DO 块守卫。
- **670 非幂等**：多处裸 `CREATE INDEX`，半应用过的库重放必 42P07。已全部
  `IF NOT EXISTS` 化。
- **deploy-local.sh CGO 断路**：上游引入 onnxruntime_go/go-sqlite3（CGO-only）
  后 `CGO_ENABLED=0` 构建必然失败。`build_backend` 增加
  golang:1.27-alpine 容器内 CGO 回退构建（musl 产物兼容 alpine:3.22 运行时）。

## 验证

- `/api/logs`（/request-logs 页数据源）200：aggregate + items 正常，
  `request_class` 过滤（新增列路径）200，网关日志 0 条 42P01；
- 视图 111 列（108 基础 + customer_id + request_class + due_at）、22 万行可查；
- 680 up 双跑幂等 + down 只删 canonical；Go 往返测试在 scratch 库验证
  冷启动全链重建 / HOT_ONLY 列隔离 / 幂等二跑（`LLM_GATEWAY_TEST_PG_DSN` 门控）。

## 经验教训

1. **"DROP+CREATE"式迁移必须单事务包裹**：autocommit 下的 DROP 先落库、
   CREATE 后失败 = 视图消失且 marker 不写。任何会被人工/工具重放的 SQL 都要
   按幂等+事务双标准写（规则 49 §9.2 view freeze 的本意即此）。
2. **关键读路径视图要有启动自愈**：表在视图丢是共享库多产品环境的常态事故，
   migration 记录"已应用"不等于对象健在。ensure 模式（probe → 零 DDL 快路径）
   是最低成本的兜底。
3. **共享 PG 上判断"删了什么"先看包装链**：`pg_views` 里只剩 `_without_*`
   中间态视图时，直接对照最后重建该视图的 migration 定义补齐顶层即可，
   不必全链重建。
4. **基础 UNION 视图取列交集而非 `SELECT *`**：hot 表独立演进（HOT_ONLY 列）
   后，`SELECT *` UNION 是列数不匹配的定时炸弹。
5. **本地部署三重竞争**：多 checkout 并发部署共享 8782/共享 bin 目录时，
   版本 SSOT 按 checkout 各自递增必然撞号；部署后必须核对 /healthz 的
   git_sha/build_seq 字面量。CGO 引入后还要核对构建模式（静态 vs musl）。
