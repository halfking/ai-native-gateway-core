# D08 供应商错误链与凭据服务质量 子代理报告（窗口：48h = 643735a28^..HEAD；重点（未经审计增量）= b75c91900..HEAD）

所有路径相对仓库根 `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2`。按 conventions.md §3 证据纪律，以下发现均为**候选线索**，须主代理亲读复核后方可登记。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 | 建议处置 |
|---|---|---|---|---|---|
| 1 | P3 | **D08 域文档 R42 回注指向已被合并淘汰的常量名与测试名**。9c99f3c40（R42 子代理侧）引入 `bg/balance_manual_guard.go` 的 `manualBalanceGuardSQL` + `TestManualBalanceGuardPredicateLockstep`，与 6dbf55973（另一并行会话）的 `bg/balance_manual_protection.go` `ManualBalanceProtectionPredicate` 撞车；merge（59f770cf1/b214efb31）取后者存活，`balance_manual_guard.go` 在 HEAD 已不存在。回注文字（含"五处消费"的文件归属表述）已漂移；本质不变量（3+2 五处消费、守卫测试锁计数）在 HEAD 仍成立 | docs/audit/playbook/domains/D08-provider-errors.md:54；对照 bg/balance_manual_protection.go:31、bg/balance_manual_protection_test.go:70（`TestManualBalancePredicateWriteTimeCoverage`）、`git ls-tree HEAD bg/`（无 balance_manual_guard.go） | 下轮回注更正时按现名改写：谓词 = `ManualBalanceProtectionPredicate`，守卫测试 = `TestBalanceManualPredicateWriteTimeCoverage`。避免下轮代理按文档找错文件 |
| 2 | P3 | **refresh-balance 失败路径仍 bump `balance_last_checked_at=NOW()`**（R42 D08 遗留#3，修复轮未动）。后台两条失败路径已按"只写 balance_error 不动 checked_at"收口，唯独 operator 手动 ⟳ 这一侧没有：manual 行探测持续失败时反复点击 ⟳ 会把 24h 保护窗无限顺延，自动探测被永久挂起 | admin/provider_credential_balance.go:107-112（`SET balance_error=$1, balance_last_checked_at=NOW()`，无 manual 谓词、不区分值变化）；对照 bg/balance_floor_guard.go:1005-1020、bg/credential_probe_v2.go:597-615（两处均注释"deliberately NOT bumped"） | 操作员在凭据抽屉对 manual 行点 ⟳ 且厂商端点持续故障 → 每次点击刷新 checked_at → `ManualBalanceProtectionPredicate` 永真 → floor guard/probe_v2 永远跳过该行 | 失败分支只写 balance_error；或 bump 前加 `AND NOT (balance_source='manual')` 式豁免。配钉桩测试 |
| 3 | P3 | **⟳ 按钮仍无余额能力门控**（R42 D08 遗留#6，未修）。按钮对全部凭据渲染；对无余额 API 的厂商（zhipu/minimax 套餐型/Anthropic 等）点击必得 400。R42 的"⟳静默失败"修复只解决了失败**可见性**（400 现在内联显示 balanceUnsupported，ProvidersView.vue:546-548），必败请求本身仍在 | web/src/views/ProvidersView.vue:1384-1390（button 无 capability 条件）；providercap 能力表 internal/providercap/capability.go | 操作员对不支持厂商点 ⟳ → 400 → UI 内联报错（不再静默，但每次都是一次必败往返） | 按 `providercap` 能力描述符禁用/隐藏按钮或 tooltip 预声明 |
| 4 | P3 | **FetchBalanceUSD 丢弃 HTTP 状态码与响应体，balance_error 信息量低**（R42 D08 遗留#4，未修）。401（key 失效）/402（欠费）/5xx 在 UI 均显示同一句 "balance probe failed (network/HTTP/parse)"，凭据服务质量面无法区分"配置错"与"厂商故障" | internal/providercap/capability.go:182-212（非 200 一律 `return 0, false`）；消费侧 admin/provider_credential_balance.go:106、bg/balance_floor_guard.go:1010 | 任何非 200 探测 → 同一模糊错误戳落 balance_error | 给 FetchBalanceUSD 增加错误详情返回（status code 足矣），三处调用方透传进 balance_error |
| 5 | P3 | **docs/balance-query-optimization.md §2.4 伪代码与实现漂移**（R42 D08 遗留#7，未复核未修，继承登记） | docs/balance-query-optimization.md §2.4 vs admin/provider_credential_balance.go（R42 报告已列：ctx 时长/列名/分支缺项） | 阅读文档照抄实现会得到错误行为 | §2.4 加"设计稿、以实码为准"标注或更新为实码 |
| 6 | P3 | **credential_model_index_hot rollup 的 DELETE+INSERT 非同一事务**（预存在设计，非窗口回归；726 事故期间因 hot 表恰为 0 行未实损）。若 INSERT 在 DELETE 成功后再失败（如唯一索引再次漂移），该 bucket 既有 rollup 行被删且当 tick 无补，auto 路由快照退化 | bg/auto_index_refresher.go:221-229（两次独立 `r.db.Exec`，无 tx 包裹）；事故记录 docs/changelogs/2026-09-18-autoroute-rollup-42p10.md | DELETE 成功 → INSERT 42P10（如 718 类索引漂移复现）→ hot 表该 bucket 空 | 可顺手并入后续轮：把 deleteSQL+insertSQL 包进单事务；至少在 runbook 记录该残余面 |

## 二、核实为健康的面

**R42 修复本体（重点问题 1）**
- **TOCTOU 谓词五处消费齐全且计数被锁死**：`ManualBalanceProtectionPredicate` 消费点实测 = floor guard Pass A 候选 SELECT（bg/balance_floor_guard.go:863）+ 成功 UPDATE（:1027）+ 失败戳 UPDATE（:1013）共 3 处；probe_v2 成功 UPDATE（bg/credential_probe_v2.go:588）+ 失败戳（:609）共 2 处。grep 复核计数与 `TestManualBalancePredicateWriteTimeCoverage` 断言（3/2）逐字一致；两文件均无内联 `'24 hours'` 手抄副本；全仓 grep 无第 6 处消费、无第 5 个 balance_usd 写者（写者清点：admin PATCH、admin refresh-balance、floor guard、probe_v2，与 bg/balance_manual_protection.go:16-27 契约注释一一对应；refresh-balance 为文档化的"刻意非消费者"）。
- **balanceProbeFailStamp 两处都接**：bg/balance_floor_guard.go:1013-1019 与 bg/credential_probe_v2.go:603-615 失败路径均写 balance_error 且都不动 checked_at（保 #12a 15min/1h 失败重试节奏）、都带 manual 谓词；错误文本不含厂商响应体（`balanceProbeFailStamp()` bg/balance_manual_protection.go:38-40）。
- **manual 戳条件化（IS DISTINCT FROM）正确**：admin/provider_credential.go:632-639 三个 CASE 均比较旧值（UPDATE SET 右端见 pre-image），值不变时 source/checked_at/error 三列全部原样保留；`TestUpdateCredentialStampsManualOnlyOnValueChange`（admin/provider_credential_balance_test.go:57-78）源码级钉住。
- **⟳ 静默失败修复本体成立**：非 404 的 DB 错误改 500（admin/provider_credential_balance.go:66-78，`pgx.ErrNoRows` 才 404）；厂商探测失败加 slog.Warn（:102-105）；前端 catch 分支写 `c.balance_error` 行字段（ProvidersView.vue:541-550）。
- **DecideFailover case 合并等价**：新合并 case（errorsx/failover_policy.go:112-124）与被删的独立 KindClientBug case 字段逐一相同（ScopeModel / EnqueueProbe=false / FrontendWait=0 / ReasonCode=model_or_request_not_eligible），`TestClassifyErrorWithBody_MiniMaxBaseRespEnvelope400StaysClientBugFamily`（errorsx/classify_minimax_test.go:44-58）把 base_resp 2013 信封先落 `tool_call_id_mismatch` 的标签分叉也钉住——两 kind 在 IsClientBug（errorsx/classify.go:1452-1458）、terminalActionKind（errorsx/action_policy.go:241-257）、attempt_outcome 终态表（domains/streaming/attempt_outcome.go:189-201）行为完全一致，无行为分叉。

**thinking 重写失败面（重点问题 2）**
- `translateThinking`（internal/paramreg/translate.go:46-68）三种失败形态均为**原样透传**而非静默篡改或丢字段：非对象/坏 JSON 透传（注释明言"上游再校验，比网关主动猜更安全"）、`json.Marshal` 失败透传、非 enabled 值透传；恒 `ok=true`，不会走 ActionDrop 丢字段。dst≠MiniMax 全透传。enabled→adaptive+strip budget_tokens 是文档化的有意语义改写（MiniMax M3 无 budget 概念），与 P5 reasonnorm 出向路径（internal/ir/serialize_openai.go:243-286 minimax 最小对象）语义一致且有双向守卫（thinkingRendered 后 restoreExtensions 不覆盖已存在键，internal/ir/extensions_restore.go:73-76）。
- TargetProvider 接线完备：出向 OpenAI 序列化全部 4 个分支（executor_chat.go:1751/1828/1871 + inline_validation.go:41）与 Anthropic 出向（executor_anthropic.go:556）均 stamp `cand.CatalogCode`，transport 层二次还原按 TargetProvider→UpstreamCatalogCode→协议回退解析（domains/transformation/ir_converter.go:537-544）；`executor_target_provider_wiring_test.go` 逐分支钉住。未 stamp 的 handler_gemini 为文档化豁免（序列化早于路由，executor 后续带 candidate 重序列化——gemini-generate dispatch 未实现，出向必经已 stamp 的 executeOpenAI 分支，executor_dispatch.go:822-828）。

**错误落账与两去向（重点问题 3）**
- **落账无 kind 过滤遗漏**：`persistSupplierError` 对所有 kind 无过滤（domains/streaming/executors/supplier_error_logger.go:58-114），client bug 照进 supplier_errors_hot→unified；窗口内新增/改动错误路径（MiniMax thinking 400 入向 400、bridge bad_request_error 中流、RLS bypass 修复）全部汇入 forwardForDispatch 唯一 funnel：LogFailure/LogFailureWithKind（executor_dispatch.go:869-914）→ candidate_failure_logs_hot + supplier_errors_hot 双写（candidate_failure_logger.go:164-181）。
- **think 回传/信封终态两去向齐全**：committed 流（bytesSent=true）走 `: thinking:` SSE 注释侧带（executor.go:3263-3289，消息只含 reason+chunk 计数，不泄漏内部细节）；未提交流 Resumable 透明 failover；bridge 中流错误信封只发 errType 不转发 relay 诊断 blob（anthropic_bridge.go:1513-1527）；无备援终态由 kind 化信封承担（bad_request_error→KindClientBug 终态，anthropic_bridge.go:593-603）。free/paid 熔断画像入口 RecordFailureWithBillingMode 双点在位（executor_nodehealth.go:161、:254），breaker 指数曲线/cycle≥5 告警/RecordSuccess 重置未被窗口触碰（domains/credential/breaker.go:442-455、:556）；client bug 双侧跳过（breaker.go:370、credentialstate/manager.go:282）——MiniMax thinking 400 不再引发 cooling↔probe 打摆，故障面收敛为对客户端的终态信封+落账。
- **分类守卫在位**：modelNotFoundWrappedRe（errorsx/classify.go:300）与 isGenericWebBody（:314，消费点 ：940/:1349）未被窗口触碰。

**candidate_failure_logger 与 726 交互（重点问题 4）**
- RLS bypass 修复（candidate_failure_logger.go:160-176 复用 supplier_error_logger.go:120-138 的 `execWithRLSBypass`，事务级 `set_config('app.bypass_rls','true',true)`，连接池不残留提权）封死"42501 被 warn 吞掉=失败账本静默丢失"——这是**堵漏**方向：窗口修复使错误数据更多落账而非更少。
- 与 726 无数据丢失交互：42P10 事故只影响 credential_model_index_hot（auto 路由决策快照），错误账本两表（candidate_failure_logs_hot / supplier_errors_hot）不在其 rollup 链上；错误聚合器 provider_error_aggregator 为 watermark+单事务+失败即整体回滚（bg/provider_error_aggregator.go:277-403），源行不丢、watermark 不进。残余面即上文发现 #6（DELETE+INSERT 非原子，预存在）。

**验证门**：`go build`（errorsx/paramreg/executors/bg/admin 六包）通过；`go test ./errorsx/ ./internal/paramreg/ -count=1` 全绿；bg 包 manual-guard 测试、executors 包 TargetProvider 接线测试 -count=1 全绿。

## 三、未覆盖项与原因

- **quality_scores_7d 口径与 ErrorDetailTab 呈现一致性（§3.3 后半）**：窗口内零改动（b75c91900..HEAD 未触碰该链路），本轮未从头重推导；沿用 R30/R42 基线记载。
- **supplier_errors_hot→8h promote→90d TTL 链路（§3.1 迁移侧）**：窗口内唯一迁移是 726（索引），703/719 promote/TTL 未触碰，未重放迁移；D07 边界。
- **bg/node_probe.go / probe_service.go 窗口内改动**（68 行）：属 D09（节点探测）边界，本域未审。
- **真库/真凭据端到端**：726 在 154/245 共享库的实际生效、MiniMax thinking 400 的 live 400→信封行为、⟳ 对真实厂商的探测，均需真机与凭据，未验证（726 契约测试 C1-C4 为本机静态验证）。
- **admin refresh-balance 的 DB 集成测试缺口**：仍只有纯函数 truncate 测试 + 源码断言测试，无实库三分支测试（R42 交接已知遗留，继承登记）。
- **handler_gemini 内部序列化路径逐行验证**：依据 c6de4699d 注释与 executor_dispatch 分支结构推断"executor 会带 candidate 重序列化"，未逐行走读 handler_gemini 全文。

**主代理复核结论（R43）**：#1 成立 → 域文档已按现名回注；#2 成立 → 已修（失败只写 balance_error）+ 源码钉桩；#3/#4/#5 登记（R42 遗留继承）；#6 登记遗留。健康面采信。#6 复核补充：bg/auto_index_refresher 的 DELETE+INSERT 现已有唯一索引保障（726），非原子面仅在索引再度漂移时显形，登记为运维观察项。
