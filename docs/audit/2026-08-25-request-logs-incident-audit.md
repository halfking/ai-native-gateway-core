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

## 5. 根因定案（同日 14:20–16:00 补充调查）

前述 §1 各项是表层症状。用唯一 request_id 实弹复现后定位到两个独立根因，二者叠加才构成"请求日志全部消失"的完整链路：

### 5.1 根因 A：promote 函数 DELETE 先于 INSERT，schema 漂移触发静默丢数据（历史数据全灭）

`promote_request_logs_hot_to_partition`（migration 341 安装）的实现顺序是：

1. 临时表抓一批冷行 →
2. **先 DELETE FROM request_logs_hot** →
3. 后在 EXCEPTION 子块内 `INSERT INTO request_logs SELECT * FROM 批次`。

`SELECT *` 按位置映射，而 hot 与分区父表早已漂移（hot 独有 caller_id / session_correlation_id / status_code；父表独有 effective_timeout_seconds 等 14 列）。每批 INSERT 必然 42601 失败，EXCEPTION 吞掉错误，**已执行的 DELETE 照常提交**——每批 5000 行从此蒸发，且 WARNING 文案谎称 "rows preserved in hot table"。

实弹复现（2026-08-25，request_id `repro-fix154-loss-test-20260825`）：

```text
WARNING: promote_request_logs_hot_to_partition: INSERT failed (column "customer_id" is of type
bigint but expression is of type text), rows preserved in hot table
fn_result=0  after_hot=0  after_parent=0   ← 行被删且未插入，永久丢失
```

数据库铁证：`request_logs_hot` 累计 insert 540,664 / delete 529,005 / live 0；`request_logs_2026_08` 累计 insert 0。7 月 19 日之后所有超过 hot 窗口的元数据都被删光。同架构的 `request_logs_bodies_hot_to_partition`（已是原子 CTE + 显式列清单）从未丢数据，月分区 30 万+ 行健在，反证根因在函数实现而非架构。

**修复**：migration `602_request_logs_promote_atomic.sql` 重写为单条数据修改 CTE（batch → DELETE RETURNING → INSERT，显式 135 列清单，跳过锁），同一语句原子提交，错误上抛给 partition_manager 记日志。排除 5 个类型分叉且无写入方的死列（protocol_conversion / ir_extensions / sanitizer_mutations / content_safety_score / dlp_violations）。已在 252 应用并实弹验证：fn_result=1、hot=0、parent=1、view=1。

### 5.2 根因 B：INSERT 列清单 / VALUES / Go 参数三重错位（当前写入全灭）

`insertRequestLog` 的列清单加入 token_band、customer_id 后，VALUES 的 `$N` 与 Go 参数没有同步对齐，存在两处独立错位：

1. Go 参数 `entry.CustomerID` 位于 $83（紧跟 OriginActor），而列清单 customer_id 是第 101 列——$83 起 18 列全部错一格（customer_id 数据落进 routing_attempts……）。
2. VALUES 中段 `$61~$71` 的 cast 标注整体早一格：`$66::text[]`（quality_flags 位置）实际绑定 `jsonOrNull(OutboundMsgHashes)`，其值 `"null"` 或 `[{"index":0,"sha256":...}]` 被按 text[] 解析。

每个 telemetry INSERT 事务死于 SQLSTATE 22P02 `malformed array literal`，fallback 也只写本地。154 实测（14:54）：真实 minimax-m3 请求 200 成功，`request_logs_hot` 近 10 分钟 0 行，journalctl 连续出现 `telemetry request db persist failed; fallback written ... malformed array literal: "null"`。写入断流时刻（bodies_hot max ts 05:23）与当日 build 1747/1748 上线吻合。

**修复**：VALUES cast 归位 + `entry.CustomerID` 参数移至末尾；新增 `insert_placeholder_alignment_test.go`，静态解析 client.go 源码，对 INSERT 与 UPDATE 分别校验「列数=表达式数=参数数+1、占位符连续且唯一、列↔参数语义匹配、cast 与列类型一致」，变异测试确认能捕获两种错位形态。

### 5.3 教训

- `SELECT *` + 分区父表 = 漂移地雷；promote 必须显式列清单 + 单语句原子搬移。
- 100+ 参数的手写 SQL 与参数表必须由测试守护对齐，人工数数不可靠。
- `RAISE WARNING ... rows preserved` 这类"自我安慰式"日志必须与实际行为一致，否则误导排查方向。
