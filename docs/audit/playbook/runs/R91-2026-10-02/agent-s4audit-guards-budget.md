# R91 域 D 报告：s4-audit 三提交守卫复审 + budget 超时 + 观测面

HEAD=ad34b2bc5，全部引用来自本次实际读取；go build/vet 通过，相关测试亲跑通过。审计日 2026-10-02。

## 一、发现（候选，待主代理复核）

**D1（守卫质量·中）710 反向校验存在解析盲区：迁移实际补位 34 列，测试只认得 30 列。**
`TestSessionArmNullPaddedColumnsMatchMigration`（admin/request_logs_stop_write_classification_test.go:748-784）确实读迁移本体（:750-751），机制为真。但其正则 `NULL::[A-Za-z ]+ AS ([a-z_]+)`（:756）只匹配纯类型名，迁移 session 臂实际有 **34** 个 `NULL::` 补位（:195-:305），其中 4 个带参数化/数组类型被漏掉：`NULL::numeric(4,3) AS confidence_num`(:256)、`NULL::text[] AS quality_flags`(:267)、`NULL::numeric(3,2) AS quality_score`(:269)、`NULL::text[] AS test_col`(:278)。实测同正则捕获恰 30 列、与 `sessionArmNullPaddedColumns` 完全一致所以门绿——即「权威来源对账」在静默接受一个不完整集合。§9.14 提交信息称「30 列 NULL 补位」，实为 34。后果：若某读点用这 4 列之一做谓词，会被 `sourceFamilyOf` 归入 `familyView`，其正确的 `silently_empty` 判定会被 familyView 禁令拒绝——恰是 §9.14 声称要防止的失败模式。当前为潜伏缺陷（抽查 admin/credential_monitor_heatmap.go:329 读 710 视图但只用 COALESCE 后的模型名，未见活跃误判）。
- 【主代理处置 2026-10-02】已修：正则放宽至 `[A-Za-z0-9_(),\[\]]` + 表补 4 列（30→34）；变异 M3 红/还原绿。见轮文档 §二#5。

**D2（分类准确性·中）§9.14 三处改判之一（bg/candidate_failure_monitor.go）的机制论据失实。**
提交信息称「:267 GROUP BY provider_id，新流量全归 NULL 组」。实读：:267 的 GROUP BY 在 **candidate_failure_logs_with_current_month** 上（bg/candidate_failure_monitor.go:265-267），该视图按迁移 392 = candidate_failure_logs_hot ∪ candidate_failure_logs，**纯 v1、无 session 臂、无 NULL 补位**；其写入方 domains/streaming/executors/candidate_failure_logger.go **不受 S4 门控**（domains/streaming/ 全树零处 RequestLogsWriteEnabled）。停写后 candidate_failure 行照常带真 provider_id 流入，「新流量归 NULL 组」不成立。该文件 request_logs 侧读点（:206 活性探针 max(ts)、:334-341 按 credential_id 分组——credential_id 不在补位表）经 session 臂照常工作。batch1 原判 unaffected 大概率是对的；分类器把它机械标进 null-padded 族（因 provider_id 字样出现在文件里）本属已知保守近似（:911 自注「5 个被标记文件里 3 真 2 假」），正确出口是具名论证表，而非带错误机制的改判。另两处改判核实成立：admin/model_status.go:268-269/:296-297（client_model IS NOT NULL，710 补位列谓词）→ silently_empty 对；bg/stats_minute_rollup_retire.go:27-32/:106-109 → 保护失效方向「过度清理」成立。
- 【主代理处置 2026-10-02】已订正：判定改回 unaffected + nullPaddedUnaffectedJustification 具名论证入表；分类门 5 测试全绿。见轮文档 §二#6。

**D3（可用性·低）budget 检查重查询无超时，发现成立，定级恰当（条件性挂起而非 503）。**
现行代码两处：domains/authentication/verifier.go:785（budget 查询）与 :798-804（重查询 DISTINCT ON SUM），函数 `checkBudgetDB`(:780-821) 全程无 WithTimeout。调用方仅两处——domains/streaming/handler.go:2386 与 domains/streaming/embeddings.go:399，均传 `r.Context()`（无 deadline；WriteTimeout=0）⇒ DB 变慢时预算前置检查挂起最长分钟级，无 503、无指标。既定 5s 形态：admin/keys.go:958-959。加超时正确层级：**函数内**（一处覆盖两条查询与两个入口）；超时 err 落入 CheckBudget default 分支 ⇒ 计 `gateway_budget_checks_total{outcome="error"}` ⇒ 两个 handler 现成 fail-closed 503 语义。
- 【主代理处置 2026-10-02】已修：函数内 5s WithTimeout；钉测 WillDelayFor(10s) 断言 DeadlineExceeded，变异 M2 红/还原绿。见轮文档 §二#4。

## 二、核实为健康的面

1. **§9.12 全部声称与 diff 一致**：纯函数 discovery/discovery.go:1091-1096；护栏调用 :1110 位于两条写入之前（UPDATE model_offers :1150-1151、UPDATE cmb :1188-1189），全仓 `auto_discovery_expired` 仅此两处写入、唯一调用方 :252——无漏护第二路径。复用 settings.RequestLogsWriteEnabled()（key_request_logs_write_enabled.go:27-28），Global 未初始化回落 true。护栏三条断言真实存在（stale_expiry_gate_test.go：调用存在 :108-112、分支含 return :100-104、位置在 UPDATE 前 :117-120；Init/Cond 双扫 :125-147）。cmb 分支声称准确：ENABLE_CMB_EXPIRE opt-in，S4 下被同一 early return 一并拦住（保守方向）。
2. **§9.13 数字与机制全对账**：batch1=29 条，6+29=35=105-70 进度算术闭合；第 6 档 silently_degraded_content；bodiesUnaffectedJustification 默认拒绝+具名豁免，§9.13 时点恰 2 条，db/db.go 系 §9.15 增补——与各自提交一致。两条豁免理由有实质且与代码吻合（telemetry.go:391/:454 读点在写门内；db/db.go:7197/:7231 为 pg_class relname 名单非 FROM/JOIN）。
3. **§9.13 族争议裁决：分类器是对的**。admin/usage_trend_series.go 归 base 族成立——`request_logs_with_current_month_without_customer_id` 在迁移 680 就是 hot ∪ base（无 session 臂），710 只换 canonical 体不动包装链。
4. **§9.14 健康部分**：familyViewNullPadded=33 文件（测试实跑输出 33 ✓）；null-padded 族「unaffected 须具名论证」门与两条具名（路径字面量/投影+COALESCE 兜底）成立；M8 变异声称与 :765-783 双向集合比对代码一致。
5. **§9.15 核心发现成立且定级恰当**：usageCreditSQL bg/ledger_reconciliation.go:261-291 FULL OUTER JOIN :283，usage 腿 request_logs_hot、credit 腿 credit_ledger_hot(:271-278)，LIMIT=ledgerFindingsCap=200(:60)；insertFinding 写 maas_reconciliation_findings(:334-339)、插入错误仅 Warn(:340-343)。credit_ledger_hot 写入方 maas/service.go:682 无 S4 门控；范围声明 settings/key_request_logs_write_enabled.go:3-4 明写「request_logs wide family」。**开关默认 true（不停写）**(:28)，故为翻转门后的条件性风险，登记口径（silently_frozen + Note 误报洪水）恰当。
6. **§9.15 数字闭合**：batch3=23 条，70→47，实测进度 58/105、47 未评估，`-tags s4audit` 硬门实跑为红——门红是登记缺口的事实而非门坏。
7. **观测面接线（供 compliance lane 扩容「一并观测」）**：
   - `output_gate_decisions_total`：metrics/output_gate_metrics.go:23-26，labels [gate,action,path]；枚举 gate=output_compliance(:40)、action=block/redact/observe/allow(:42-45)、path=body/stream_lane/stream_comment/stream_event(:47-50)；预热 init :56-73。接线：interceptor.go:219/:223(block)/:228(redact)/:244(observe)/:254(allow)；stream_compliance.go:197(stream_lane)/:375(stream_comment)/:428(stream_event)。伴随面 `output_gate_fail_closed_errors_total`(:32-35)。**扩容注意**：新增 gate/path/action 必须同步枚举常量与 init 预热循环，否则首 Inc 前无样本。
   - `gateway_budget_checks_total`：metrics/budget_metrics.go:25-26；五 outcome :33-37；预热 :44-47；接线全在 verifier.go:763(skipped_snapshot)/:769(ok)/:771(exceeded)/:775(error)/:808(degraded_no_ledger)。

## 三、未覆盖项与原因

1. **真库量测类声称不可本地复核**：session_turns.model=client_model（1138/1138）、视图 7,221 行/36.55% session 臂、fingerprint 24h 0 行——本审计无 DB 连接，仅对 710/680/392 迁移源码做结构性核对（710 session 臂 34 列补位已从迁移本体逐行确认）。
2. **TestRequestLogsViewSessionArmPinIsCurrent 未定位到**：classification_test.go:689 注释称视图白名单「由真库门钉住」，但该测试名在 admin/ 下 grep 无定义（仅注释命中）；白名单 requestLogsViewsWithSessionArm(:710-712) 是否真有活体门未证实。
3. 47 个未评估文件未逐个审（超出本轮范围，硬门自身在跟踪）。
4. §9.13 与子代理的 6 文件族争议未重演原争议过程，仅对 6 个抽样文件做 sourceFamilyOf 与真实 SQL 比对——5 对 1 错（见 D2）。
