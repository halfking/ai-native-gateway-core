# D07（hot+columnar，横切 D08/D10）凭据分析 SQL 审计子代理报告（窗口：468a1ce82..HEAD）

> R44 轮原文落盘（子代理只读报告，主代理已逐条亲读复核并在真库复现，处置见轮文档）。
> 审计对象：scripts/analysis/credential_health_check.sql（cc013e68d）、scripts/analysis/credential_usage_analysis.sql（5815184f2）。目标：人工 psql 直跑"打开就能跑对、数字可信"。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| F1 | **P1** | **两个 SQL 全部只查分区母表 `request_logs`，不查 hot 表——8h 数据盲区，数字系统性不可信**。写路径只写 request_logs_hot（telemetry/client.go:1146），hot 由 promote 迁母表且保留 8h（01-schema.sql:3777；bg/partition_manager.go:44）。正确读面 = `request_logs_with_current_month`（hot UNION ALL 母表，448:98-101）。后果：(a) 24h 窗实际只覆盖 [now-24h, now-8h]；(b) `possibly_stalled`（>1h）对每行恒真 → 100% 误报；(c) 最近 8h 内才有流量的凭据（刚恢复/刚出事）完全消失；(d) 7d 总量/成功率/成本全少计 | credential_health_check.sql:37-43；credential_usage_analysis.sql:82-88、72-79 | FROM 改视图，或手工 UNION ALL |
| F2 | **P1**（硬失败 42703） | **`providers.name` 列不存在，一执行就报 column does not exist**。providers 全列只有 code/display_name（01-schema.sql:12186-12265）；生产查询用 `COALESCE(p.display_name,'unknown')`（admin/analytics.go:238） | credential_health_check.sql:13、40；credential_usage_analysis.sql:14、85 | `p.name` → `p.display_name` |
| F3 | **P1** | **error_kind 枚举字面量 3/4 是虚构值，auth/quota/不可用告警全失效**。真实分类法 errorsx/classify.go:15-35（auth/auth_revoked/quota/quota_periodic/quota_balance/quota_permanent/upstream_down/rate_limit…）；生产先例 model_routing_diagnostic.go:94。`quota_exceeded`/`invalid_auth`/`service_unavailable` 代码库 0 命中；severity 的 auth 分支恒假——大面积 401 也不判 critical | credential_health_check.sql:26-29、94 | 改真实分类法 |
| F4 | P2 | **凭据状态机词汇错配：一半标签永不触发、一个标签 100% 误报**。真实词汇（CHECK 01-schema.sql:7039-7044）：availability_state ∈ {ready,cooling,rate_limited,auth_failed,unreachable,suspended}、quota_state 运行时值 {ok,balance_exhausted,permanently_exhausted,periodic_exhausted,suspended,unknown}。`availability_state NOT IN ('available','online')` → 每条活跃凭据被打 'unavailable'；`quota_state IN ('exhausted','depleted','suspended')` → 三种真实耗尽态全漏 | credential_health_check.sql:84-87、94 | 按 CHECK 词汇重写 |
| F5 | P2 | **top_models 相关子查询按行重复全窗口扫描**。母表 request_logs 无 credential_id 索引（credential 复合索引只在 hot 表），月分区千万行 × N 凭据 × 3 遍 = I/O 放大 | credential_usage_analysis.sql:66-80、106-120 | 一次性 GROUP BY + 窗口函数取 Top3 |
| F6 | P3 | top_models 子查询漏 `request_type='main'` 过滤（外层有），口径不一致 | credential_usage_analysis.sql:72-78 vs 88 | 子查询补过滤 |
| F7 | P3 | 无 statement_timeout 护栏；多遍大窗口聚合生产直跑有风险 | 两文件头部 | 文件头补护栏与盲区说明 |

## 二、核实为健康的面（含核对过的表→列清单）

- **request_logs（母表）**：ts/credential_id/provider_id/outbound_model/prompt_tokens/completion_tokens/total_tokens/cost_usd numeric(14,8)/latency_ms/success/error_kind/stream_*/client_timeout ✓；`request_type` 不在快照 CREATE TABLE，由 510:33-38 双侧补齐（真实库存在，合法；R36 已登记的快照漂移）。
- **request_logs_hot**：与母表共享核心分析列 ✓；写唯一入口 ✓；有 idx_request_logs_hot_credential_model_ts ✓。
- **credentials**：id/provider_id/label/status/lifecycle_status/availability_state/quota_state/concurrency_limit/fp_slot_limit/rpm_limit/tpm_limit/balance_usd/plan_type ✓；is_free_tier 由 db_omnifree.go:345-346 ensure + 075 兜底（真实库存在，合法）。
- **providers**：id/code/display_name ✓；name ✗（→F2）。
- **request_logs_with_current_month 视图**：= hot UNION ALL 母表（448:98-101），修复 F1 的现成读面。
- **promote/分区机制**：写只在 hot ✓、母表按月 RANGE（columnar 只在 request_logs_archive）✓、promote 单 CTE 原子幂等 ✓、时区钉扎 SET LOCAL 'Asia/Shanghai' ✓。
- **D08 关联**：真实错误表族 = supplier_errors（hot→8h promote→90d TTL；V371/703）；两个 SQL 没引用不存在的错误表名（无 P1 触发），但完全没读 supplier_errors——凭据维度错误聚合视角缺失，建议修复版 join supplier_errors（含 _hot）。
- **时区钉扎**：无 date_trunc/DATE()/日期字面量，全 NOW()-INTERVAL 纯区间算术，473/687 族陷阱不适用 ✓（残留：BETWEEN 双闭区间边界行双计，量级可忽略）。
- **数值口径**：除法全有 NULLIF 保护；cost/token 单位正确；LEFT JOIN 均 PK 1:1 无放大 ✓。
- **可用性**：纯 SELECT 无 psql 元命令依赖 ✓。

## 三、未覆盖项与原因

- 真库 EXPLAIN/实跑验证（无生产连接，静态推导）——建议主代理真库复现 F2 报错与 F1 行数差（主代理注：已复现，24h 窗母表比视图少 1860 行/13.6%，providers.name=0 列确认）。
- 01-schema.sql 快照漂移（request_type/is_free_tier 缺失于快照）超出窗口，登记佐证 R36。
- usage_analysis rpm/tpm 利用率分母假设（运营口径选择）；credential_model_peak_1m 可作更准峰值口径。
