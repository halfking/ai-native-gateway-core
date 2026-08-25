# Database Change And Real Verification Standard

状态：强制
适用：所有数据库 DDL、表/分区/视图变更、ORM 字段变更、SQL 写入路径变更和部署修复

## 1. 核心原则

数据库结构不是实现细节，而是跨服务运行契约。任何结构变更必须满足：

```text
影响分析 → 全量读写检索 → 迁移与回滚 → 本地结构验证
→ 测试环境真实变更 → 真实写入/更新/读取 → 逐项对账
→ 目标版本确认 → 提交 → 推送
```

未完成任一步骤，禁止提交和推送。

## 2. 变更前门禁

变更前必须形成清单，至少包含：

- 目标数据库、schema、表、父表、hot 表、默认分区和所有月分区。
- 现有字段名、类型、NULL 约束、默认值、索引、唯一约束和 RLS 策略。
- 所有 INSERT、UPDATE、DELETE、SELECT、JOIN、Scan、JSON/API 映射和后台任务引用。
- 所有派生视图、函数、触发器、物化视图、缓存和导出接口。
- 数据规模、锁风险、预计执行时间、业务高峰影响和磁盘影响。
- 回滚 SQL、回滚条件、备份位置、恢复验证方法。
- 兼容窗口：旧代码和新代码是否需要同时运行。

禁止以“其他表有这个字段”“字段看起来应该存在”代替 schema probe。

## 3. 分区结构同步

分区表变更必须逐层对账：

| 层 | 必须一致 |
|---|---|
| 父表 | 字段名、顺序、类型、NULL/默认约束 |
| `_default`/hot 表 | 写入字段、类型和唯一键 |
| 月度分区 | 字段类型和写入契约 |
| `*_with_current_month` 视图 | SELECT 列、顺序、类型 |
| 代码 | INSERT/UPDATE/SELECT/Scan 参数顺序 |

正文拆表时，主表只保存元数据，正文表保存正文；两表必须以 `request_id` 和明确的时间键关联。正文表不得凭类比增加租户字段，字段是否存在必须以实际 schema 为准。

## 4. 变更后逐项验证

必须在测试环境执行真实验证，不得只跑 mock：

1. 查询 `information_schema.columns`，逐表对账字段、类型和 NULL 约束。
2. 查询 `pg_inherits`、`pg_class.relam`，确认父表、hot 表和月分区状态。
3. 真实 INSERT 一条包含租户、用户、客户和正文的记录。
4. 真实 UPDATE 同一 `request_id`，验证终态字段和正文补写。
5. 从列表视图读取 `tenant_id/end_user_id/customer_id`。
6. 从正文视图读取 `request_body/response_body/outbound_body`。
7. 通过真实 HTTP API 验证列表、详情、正文、附件、摘要和会话接口。
8. 验证成功后回滚测试数据，并确认回滚没有残留。
9. 记录每个接口的 HTTP 状态、耗时、响应结构、服务版本和 `x-request-id`。

## 5. 查询性能门禁

查询优化不能只增加 context timeout。必须保留：

- `EXPLAIN (ANALYZE, BUFFERS)` 结果和时间范围。
- count、aggregate、分页、详情、body join 的单独耗时。
- 是否发生全表扫描、重复 lateral 查询、无索引排序或 N+1 查询。
- 优化前后执行计划和 P95/P99 对比。
- 超时日志必须包含 query stage、时间范围、页大小和底层错误，不得只返回 `query failed`。

## 6. 提交与推送门禁

提交前必须确认：

- 代码、schema、migration、down migration、视图和测试在同一变更集内。
- 当前运行版本的 commit、版本号、二进制哈希和目标服务一致。
- 245/测试环境真实验证通过，失败不得晋级 154。
- 工作区、staged、untracked、删除文件已完整审计。
- 没有并发部署锁、未完成部署或其他 agent 覆盖目标版本。
- commit message 写明根因和验证结果。

推送后必须再次用真实 API 验证；只要列表或详情任一返回 4xx/5xx，任务状态只能是未完成。

## 7. 禁止事项

- 禁止未经影响分析直接 ALTER/DROP/RENAME 表、列、索引或视图。
- 禁止只改数据库，不同步代码和 schema SSOT。
- 禁止只改代码，不验证目标数据库真实结构。
- 禁止把人工线上 SQL 当成最终修复而不补 migration。
- 禁止以健康检查 200 代替业务 API 真实验证。
- 禁止在并发部署锁未清理或版本未确认时继续晋级。
