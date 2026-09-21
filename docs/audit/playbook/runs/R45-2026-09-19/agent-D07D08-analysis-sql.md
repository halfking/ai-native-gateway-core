# D07+D08（hot columnar 读面 + 供应商错误）子代理报告（窗口：51d6147c7..de7efd453）

对象：commit 875897a3c（R44 分析 SQL 四缺陷重写）后的 `scripts/analysis/credential_health_check.sql` 与 `scripts/analysis/credential_usage_analysis.sql`。本轮独立实跑真库（只读 SELECT），不沿用 R44 文档记载。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | **P1** | 裸 `rl.request_type = 'main'` 在 `request_logs_with_current_month` 上漏掉 93%–99.5% 流量，且残存读面是**探测流量**，导致 R44 声称修好的 F3（词表计数恒 0）在新读面上原样复发 | `scripts/analysis/credential_health_check.sql:62`；`scripts/analysis/credential_usage_analysis.sql:85,115,138` | 改为 `COALESCE(rl.request_type,'main')='main'`（生产读端既定惯例） |

**F1 触发路径与真库证据（决定性）**：
- 视图三段结构（`pg_get_viewdef` 实查）：`session_turns_hot ∪ session_turns ∪ (request_logs hot∪母表 WHERE NOT EXISTS session_turns族)`。前两段的 `request_type` 投影为 `NULL::text`（session_turns 表无该列）。
- 真库（活跃库，数据新鲜度 2 秒）：母表 request_logs 24h 的 **8969 行 request_id 100% 存在于 session_turns**（被第三段 NOT EXISTS 全部排除）；视图 24h 共 10314 行，其中 9592 行 request_type IS NULL。
- 后果量化：24h 窗裸 `'main'`+credential 非空 = **41 行**，COALESCE 口径 = **8912 行**（漏 99.5%）；7d = 74871 vs 213533（漏 65%）。
- 生产正确惯例在位：`admin/session_online.go:415`、`admin/session_turns_unified.go:118`、`admin/session_turns_tree.go:286` 均用 `COALESCE(request_type,'main')`。
- 残存 41 行的成色：7d 裸 main 读面 error_kind 分布 = NULL 21280 / probe_direct_rate_limited 19851 / probe_direct_endpoint_build 16483 / probe_direct_network_error 4797 / probe_direct_http_503 4661 / probe_direct_auth_failed 3421 …——**没有任何一个 errorsx 业务词**。因此 §1 的四个 FILTER（health_check.sql:45-48）恒 0、§1 输出 13 行全是 request_count=1~15 的小样本 `error_rate=100%` 假 critical；usage_analysis 实跑 top_errors 全是 probe_direct_* 词、top_models 第一名全是空模型名、total_cost_usd 全空。R44 F1"修盲区"实为引入更大盲区。

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 2 | P2 | top_models 预聚合不排除空串 `outbound_model`，真库实跑输出污染严重 | `scripts/analysis/credential_usage_analysis.sql:114`（`IS NOT NULL` 不拦 `''`）、:121（STRING_AGG 产出 `" (1318)"` 型条目） | `AND NULLIF(rl.outbound_model,'') IS NOT NULL` |

真库证据：7d 窗 rn≤3 内空串组合 24 个；usage_analysis 实跑前 10 个凭据的 top_models **第一位全部是空名条目**（` (1318)`、` (2433)`、` (4550)` …）。

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 3 | P3 | §2 `dominant_stage` 未做空串→'unknown' 映射，与凭据详情页读端约定不一致 | `scripts/analysis/credential_health_check.sql:189`（`MODE() WITHIN GROUP (ORDER BY se.stage)`）vs `admin/vendor_credential_error_handlers.go:232`（`COALESCE(NULLIF(stage,''),'unknown')`） | MODE 内包 `COALESCE(NULLIF(se.stage,''),'unknown')` |

真库证据：24h supplier_errors stage 空串 1206/1333 = **90.5%**；§2 实跑 dominant_stage 列全为空串。

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 4 | P3 | §2 只呈现四类分组（rate_limit/auth 族/quota 族/upstream_down），其余错误无 other 计数，分项与 error_count 的缺口不可见 | `scripts/analysis/credential_health_check.sql:185-188` | 加 `COUNT(*) - 四类合计 AS other_count` 或头注说明口径 |

真库证据：24h distinct error_type = 15 种；credential 37 行 error_count=158 而 auth 7 + quota 1，150 个错误不在任何列。

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 5 | P3 | rpm/tpm 利用率除法缺 limit>0 护栏：`rpm_limit=0` 行会使 numeric 除零 22012 炸掉整条查询 | `scripts/analysis/credential_usage_analysis.sql:224,230` | `NULLIF(cs.rpm_limit,0)` / `NULLIF(cs.tpm_limit,0)` |

真库证据：当前 0 值行数 = 0（暂不可触发），但 credentials CHECK 约束全集中**无** rpm_limit/tpm_limit>0 约束，DB 层不禁止 0。其余除法核对健康（error_rate_pct/success_rate_pct/error_pct 均有 NULLIF 或恒>0）。

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 6 | P3 | 头注释宣称与真实约束漂移（不影响行为） | `scripts/analysis/credential_health_check.sql:12`（宣称"quota_state 词汇 = CHECK 约束"，实际 pg_constraint 中 quota_state **无任何 CHECK**）；:96（'unknown' 非存储词汇，仅读端 COALESCE 值）；:114（'suspended' 作 quota_state 值在写入面不存在，属死词汇，宽 IN 无漏报风险） | 注释改为"quota_state 词汇 = 代码写入面"；'balance_exhausted' 为真实词汇可保留 |

## 二、核实为健康的面

- **读面载体与 hot 覆盖（结构层面）**：两 SQL 读面均为 `request_logs_with_current_month`；视图链实查 hot 段在位；母表/hot 双行由 NOT EXISTS + LATERAL LIMIT 1 防双计。（但见 F1：`request_type='main'` 过滤使该读面整体失真。）
- **error_kind 词表对齐**：SQL 引用的 8 个词逐一存在于 `errorsx/classify.go`；虚构词已绝迹。R44 F3 的词表替换本身正确——失效纯粹因 F1 的读面。
- **credentials 状态词汇**：SQL 全部为判据式引用，与 CHECK 约束实查一致；真库 distinct 值均在集合内。旧 F4 缺陷未复发。
- **no_recent_traffic 语义**：health:120 实现正确（实跑 credential 11 被正确打标）；possibly_stalled 彻底移除。
- **top_models 预聚合正确性（结构层面）**：一次 GROUP BY + ROW_NUMBER，credential 2 人工验证排序一致；相关子查询未回潮。（空串污染见 F2。）
- **statement_timeout 护栏**：health:16 SET '5min' → :201 RESET；usage:16 SET '10min' → :247 RESET。两文件真库实跑全程通过（usage 全量 16.6s）。
- **§2 聚合口径与 NULLIF**：按 credential_id 维度聚合 + supplier_errors_unified 同一事实源；§2 自身无除法。
- **join 无放大**：providers.id、credentials.id 重复行检查均为空。
- **实跑无硬失败**：两文件 psql -f 直跑均成功返回，42703 类错误未复发。

## 三、未覆盖项与原因

- **F1 修复的最终口径需业务定夺** —— COALESCE 修正后探测流量（probe_direct_*）仍占错误大头（7d probe_direct_rate_limited 43831 vs provider_error 4100），业务/探测分离口径需主代理/业务确认。
- **EXPLAIN ANALYZE 计划级分析** —— 实跑 16.6s 在 10min 护栏内、无需逐节点优化。
- **§2 与详情页的 tenant 维度差异**未展开 —— 判定为设计差异而非缺陷。
- **迁移纪律#3 印证**：R44 文档记载"真库实跑验证"属实（无硬失败），但结果语义失真未被其自跑发现——本轮以独立实跑为准推翻其"读面已修复"结论。
