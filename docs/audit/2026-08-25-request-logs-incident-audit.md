# Request Logs Incident Audit

日期：2026-08-25
范围：245、154、252、`request_logs` 写入与 `/api/logs` 查询链路

## 1. 结论

本次故障不是单一问题，而是数据库结构、运行代码和部署状态未保持同一契约造成的连续故障：

1. migration 573 删除了 `request_logs` 正文列，但旧运行代码仍向主表写入，导致请求记录事务失败。
2. 正文表的运行 SQL 又写入不存在的 `request_logs_bodies_hot.tenant_id`，导致新请求继续失败。
3. `request_logs_hot.customer_id` 为 `text`，父表和月分区为 `bigint`，存在结构漂移。
4. `request_logs_with_current_month` 视图缺少 `customer_id`，导致元数据无法通过列表查询契约完整返回。
5. `/api/logs` 直接实测返回 HTTP 500，耗时约 5000ms；原始详情接口 `omit_body=1` 实测返回 HTTP 200。
6. 245 曾被并发部署切换到未包含修复的旧版本，导致已修复代码没有成为实际运行代码。

## 2. 证据

### 2.1 真实接口

```text
GET /api/logs?from=2026-08-23T20:40:31.321Z&to=2026-08-24T20:40:31.321Z&page=1&page_size=50
HTTP 500
request_id=884ece05deb74601396bd05da54e84de
duration=5000ms

GET /api/logs/839f3673c4b01ffb65f6299cb87d2d42?omit_body=1
HTTP 200
```

### 2.2 245 运行日志

```text
column "request_body" of relation "request_logs_hot" does not exist
column "tenant_id" of relation "request_logs_bodies_hot" does not exist
SQLSTATE 42703
```

### 2.3 252 数据库现场

```text
request_logs_hot.customer_id                 bigint
request_logs.customer_id                    bigint
request_logs_2026_08.customer_id             bigint
request_logs_2026_09.customer_id             bigint
request_logs_with_current_month.customer_id  bigint
request_logs_bodies_hot.request_body         jsonb
request_logs_bodies_hot.response_body        jsonb
request_logs_bodies_hot.outbound_body        jsonb
```

事务测试已证明 metadata 与 body 可以在同一事务中写入并读取；该测试使用 `ROLLBACK`，不污染业务数据。

## 3. 主要失误

1. 先改数据库结构，再追查所有读写方，违反 schema truth first。
2. 只验证了局部单元测试，没有先对真实运行版本做 API 写入和查询闭环。
3. 将“代码已提交”误当成“目标服务器正在运行该代码”。
4. 发现字段错误后多次依赖推测，没有立即用实际请求、`x-request-id` 和服务日志闭环。
5. `/api/logs` 只返回模糊的 `query failed`，缺少查询阶段和底层错误，延长了定位时间。
6. 245/154 部署锁和并发部署状态没有在晋级前清点，造成运行版本被覆盖。

## 4. 遗留风险

1. 245 当前是否已运行包含全部正文表和列表查询修复的最终提交，必须通过版本、二进制哈希、日志和 API 实测共同确认。
2. `/api/logs` 当前的 30 秒预算只能降低超时概率，不能替代执行计划和查询结构优化。
3. 252 上手工补的 `tenant_id` 必须补入正式 migration、down migration 和 schema SSOT，禁止只改运行库。
