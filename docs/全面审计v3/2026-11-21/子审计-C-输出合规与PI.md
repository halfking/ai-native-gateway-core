# 子审计 C —— 输出合规同族收口 + PI 智能体接入 + error-playbook（窗口 c4b5d00cb..c6bf29aea 提交簇 3）

- 审计对象提交簇：`02eb1e44a`（断流截断误判根修）/ `c0ca39cc6`（失败终帧同族收口）/ `6851e072c`（输出守卫接入 PI 智能体）/ `3643a6408`（error-playbook + 迁移 995）。
- 方法：只读取证；不采信提交信息原文，逐条以代码与实跑对拍；断言给 file:line；三态判定（已实现/部分/未实现）；无法本地验证的生产侧断言标「**待主控活体复核**」。
- 环境实测基线：go1.27.4 darwin/arm64（`export PATH="$HOME/sdk/go/bin:$PATH"`）。HEAD=c6bf29aea。
- **工作树披露**：共享工作树在审计窗口内被并行会话持续改写（实测 `git status` 从 2 项脏涨到 20+ 项：admin/analytics_handlers.go、domains/analytics/cost.go、d13_apply_error_split_test.go 等）⇒ **全部测试结论以隔离 worktree（`git worktree add --detach /tmp/sa-c-audit-head c6bf29aea`）为准**；共享树上的早跑读数只作过程记录不作证据。审计全程零代码改动。

## 一、判定总表

| # | 审计问题 | 三态判定 | 一句话结论 |
| --- | --- | --- | --- |
| 1 | 断流截断误判根修（02eb1e44a） | **已实现** | 豁免点在 releaseCheckedFrames 且**与 lane 名/载体类型解耦**（只看 issues+terminalSeen）⇒ 四份载体枚举的新类型（tool_search_call/mcp_approval_request）在截断场景**结构性全覆盖**；钉测 6 用例全绿 |
| 2 | 失败终帧同族收口（c0ca39cc6） | **已实现** | streamTerminalKind 三态化、failed 不置位；**非流式 processBody 是同族第三处候选**（无失败/截断语义，P3 登记） |
| 3 | 输出守卫接入 PI（6851e072c） | **已实现（带 1 个 P2 红）** | 接入点/归因闭环/apply 类型闸恒 view-only 全部核实；但该提交引入 **HEAD 真 red：TestD13ApplyBusinessFailureReturns422**，与其提交信息「admin 全量套件 exit 0」矛盾 |
| 4 | error-playbook + 迁移 995（3643a6408） | **已实现** | 八面登记逐一核实齐备（含 sha256 实测与 db-changelog 逐字一致）；down.sql 成对；未知错误闭环函数链齐备；默认关 fail-closed |
| 5 | tool_json_invalid 判定点普查 | **已收口（流式）** | 判定产生点唯一（output_sensitive.go:211）；流式豁免单点生效；非流式无豁免＝同族第三处（并入 #2 的 P3） |
| 6 | 测试实跑 | **部分绿** | 5 快包 + streaming 全量（非 race）全绿；**admin 全量 2 红（HEAD 实测，1 个属本簇、1 个跨簇）** |
| 7 | 竞态/并发 | **无数据竞争** | outputcompliance/errorplaybook/promptinjection -race 绿；streaming -race **零 DATA RACE 告警**（唯一失败＝TestLargeIngressMemoryScaling，race 仪表拖慢 ingress 超 503 预算的量具形态，窗口前既有，见 §八） |

## 二、Q1 断流截断根修（02eb1e44a）——已实现

**根因核对（与提交信息一致，另有生产证据佐证）**：截断豁免位于 `domains/hooks/outputcompliance/stream_compliance.go:702`——`result.Blocked && !state.terminalSeen && issuesAllToolJSONInvalid(result.Issues)` ⇒ 不产 PolicyBlockError、`continue` 放行、WARN 留痕（:704-712）+ allow 指标（:703）。`terminalSeen` 由 processStreamChunk 见终帧置位（:457-462 前身；本笔为 `isStreamTerminalFrame` 布尔版）。`issuesAllToolJSONInvalid`（:828-840）要求 issues 逐条 Subtype==tool_json_invalid 且非空 ⇒ 豁免面收窄成立：内容规则命中/脱敏改写不受豁免。

**关键问题：是否覆盖全部载体枚举（195 号教训的四份拷贝）——答：结构性覆盖，理由是豁免判据与载体解耦**：
- 豁免在 `releaseCheckedFrames` 的 lane 循环里（:653-714），判据是「issues 全为 tool_json_invalid + 未正常收尾」，**不检查 lane 名与载体类型**；任何载体只要走 `__tool_json` 标签（`protocol_text.go:153-157` 按 lane 后缀判定）就会被同一把尺量、同一把尺豁免。
- 四份枚举逐一核对：
  1. **入向** `security/sanitize/input_protocols.go:319`（`tool_search_call, mcp_approval_request` 收进 `.arguments`，195 修复 e0625c6a5）——入向消费客户端完整 JSON，无截断语义，不适用本修；
  2. **出向** `security/sanitize/native_restore.go:211/219`——restore 按 marker 工作，不判 json.Valid，不适用；
  3. **输出合规·非流 switch** `domains/hooks/outputcompliance/protocol_text.go:309-317`（addOutputItem，注释明写「195 号：输出侧这份第三份枚举漏了它们」）；done 快照镜像走同一函数（:459→addOutputItem）；
  4. **输出合规·流式 switch** `protocol_text.go:497-500`（`response.output_item.added` 与 done 同口径镜像，注释明写「否则出现 done 才拦的半帧缝」）。
  - 两处输出侧 lane 名均带 `.arguments` 且 `responses.` 前缀 ⇒ `isJSONToolArgumentLane`（stream_compliance.go:814-820，:818 分支）命中 ⇒ 标签 `__tool_json`（protocol_text.go:154-156）⇒ 判定与豁免都吃得到。**tool_search_call/mcp_approval_request 在截断场景与 function_call 同待遇，无漏点。**
- 另一处历史坑位已核对不复存在：增量片段在 `inspectSSEFrame` 一律降级为 `__tool_fragment`（:1047-1051），CheckField 对片段不判 JSON（output_sensitive.go:204-207）⇒ 降级逐帧路径（:422-443）不可能产出截断 tool_json_invalid。

**钉测**：truncated_tool_lane_interruption_test.go 三契约（断流放行/完整收尾照拦/断流+内容命中照拦）隔离 worktree 实跑 PASS（见 §八）。

## 三、Q2 失败终帧同族收口（c0ca39cc6）——已实现

- `isStreamTerminalFrame` 重构为 `streamTerminalKind`（stream_compliance.go:936-980）：`done`（[DONE]/message_stop/response.completed）/ `failed`（response.failed/response.incomplete）/ `""`；布尔包装 `isStreamTerminalFrame`（:936-938）保持既有调用方零改动（grep 实测仅 stream_compliance.go 内部消费）。
- 置位改 `state.terminalSeen = kind != "failed"`（:462），失败终帧照常经终帧分支转发（:463-481 prior+terminal 合并落线）⇒ 客户端看到真实失败原因。钉测 2 条（failed/incomplete 释放+转发、response.completed 照拦）隔离 worktree PASS。
- **同族第三处普查结论：非流式 `processBody` 是残余候选**。`interceptor.go:402-449`（transformVisibleJSON stream=false + transform 回调 :430 blocked）对 tool_json_invalid **无任何失败/截断语义**——非流式 Anthropic `stop_reason=max_tokens` 或 Responses `status=incomplete` 且参数截断时，同样会把真实原因顶成 output_policy_blocked。**无生产证据、本簇未登记** ⇒ **P3 登记（是否立项待主控裁决）**；与两笔根修的合同自洽（「上游发完了的参数非法」在非流式=整包已收，判定面更接近 rv-2089 原合同，故非必改）。
- **Gemini 核查（无第三处）**：Gemini 客户端端点把请求改写成合成 `/v1/chat/completions` 委托 ChatHandler（handler_gemini.go:383-394, 571），geminiStreamWriter 是**底层** writer，输出合规拦截发生在其上游的 chat 形态帧上（handler.go:5389 流式包装）⇒ Gemini 流见到的终帧是 chat `[DONE]`，streamTerminalKind 判据适用，无协议盲区。
- **Anthropic 原生错误帧核查（行为正确）**：Anthropic 流失败用 `event: error`（网关自产同形状 messages.go:1958-1961），该类型**不在** streamTerminalKind 名单 ⇒ 不置位 ⇒ 终止后 finish() 的 FlushStreamPending（handler.go:256-283，writeErr==nil 时恒调）走豁免放行——语义正确（失败流=截断族）；error 帧本身的 error.message/detail lane 是普通文本 lane（protocol_text.go:265-273），不会被误判 tool_json_invalid。

**P3｜terminalSeen 不随 clear() 复位**：`clear()`（stream_compliance.go:907-911）只清 frames/lanes/bytes；终帧 inline release 后 terminalSeen 残留为 true（done）或 false（failed）。正常协议终帧后无后续帧，无实际影响，且 :460-461 注释自述「置位只对本次 release 生效」；但这是**隐性单次语义**，「终帧后异常续流」形态无钉测。

## 四、Q3 输出守卫接入 PI 智能体（6851e072c）——已实现（带 P2 红）

**接入点**：统一出口 `registerPolicyBlockReview` 顶部 `maybeEnqueueOutputGuardPIAnalysis(in)`（policy_block_feedback.go:153→:228-255），仅格式类守卫触发（:229 `isFormatGuard`→`errorsx.IsFormatGuardRule`，errorsx/output_policy.go:41-48 三规则闭集）；内容类不投（注释 :151-152）。装配：`cmd/gateway/main.go:7816-7837` SetOutputGuardPISink → `piCollector.CollectAsync(CollectedError{ErrorType: output_guard_blocked, Priority:5, InputText: Evidence, ErrorDetails: rule/field/digest/client_type/degraded})`。**7 个调用点全覆盖**（handler.go:328 流式终帧/351 降级/5514 非流式 403/6669 非流式兜底；messages.go:996/1757；responses.go:984），与主档「8 个协议路径」表述略有出入（7 个 call site；Gemini 经 chat 路径复用）——覆盖面本身成立。sink 非阻塞契约（:217）+ panic recover（:238-242）+ context.WithoutCancel。

**归因链路闭环（格式类拦截→PI 分析→建议落库）**：入队（sink→CollectAsync，24h error_hash 去重）→ PI worker 5min 轮 → `getOutputGuardBlockedTemplate`（error_analyzer.go:548-579：归因四分类 upstream_interrupted/upstream_format_defect/gateway_guard_misjudge/redaction_side_effect + suggested_fix 分客户端侧/运维侧 + rule_adjustment 必须为 null :570）→ error_analysis_results → /admin/pi-analysis 详情（PIAnalysisView.vue:879 formatErrorType 映射 + 8 语种 `errorTypeOutputGuardBlocked` 齐，zh-TW 繁体区分实测）。**运行时链路（211 开 PI 开关后活体）未验证 ⇒ 待主控活体复核**（作者主档 L1 已自登记）。

**apply 恒 view-only（核实无生产写路径）**：类型闸在 `admin/pi_analysis_fix_apply.go:369-384`——apply 先查 `error_analysis_queue.error_type`（:370-372），==output_guard_blocked ⇒ 422「view-only」（:379-383），位置在置信度门（:399）与事务 Begin（:407）**之前** ⇒ 无任何写路径可达。ApplyFix 是全仓唯一 apply 入口（grep `piApplyActions` 仅此一处调用 :414）；白名单动作（disable_rule/adjust_severity/update_pattern）指向 prompt_injection_rules 表，与输出守卫合成规则（errorsx.GuardRule*）是两张表，双重不可达。钉测 TestApplyFix_OutputGuardBlockedIsViewOnly（0.95 置信度+合法动作形态仍 422）隔离 worktree PASS。

**P2｜本提交引入 HEAD 真 red：TestD13ApplyBusinessFailureReturns422（admin/d13_apply_error_split_test.go:60）**：
- 失败形态（隔离 worktree c6bf29aea 实测）：`got 500 body=query queue item failed`——类型闸新增的 `SELECT error_type FROM error_analysis_queue WHERE id = $1`（fix_apply.go:370-372）在 sqlmock 期望链（d13 测试首期望=Query rule_adjustment）之外 ⇒ handler 500。
- 归因：d13_apply_error_split_test.go 最后一次改动是 014410dc3（窗口前），6851e072c 只补了 pi_analysis_fix_apply_test.go 的 4 个用例 mock（diff 实证），**漏了 d13 文件里这个同样驱动 ApplyFix 的用例** ⇒ 与提交信息「既有 4 用例补类型闸 mock 后全绿；**admin 全量套件 exit 0**」矛盾（「4 用例」的计数面没扫全仓）。
- 佐证：共享工作树上已出现一笔**未提交**的 d13 测试修复（补 gate mock）——并行会话在途，尚未落仓；HEAD 上该红真实存在。
- 修法建议（供主控）：按共享树在途形态把 gate mock 补进 d13NewHandler（一 expect，3 行）。

## 五、Q4 error-playbook + 迁移 995（3643a6408）——已实现

**八面登记逐一核实**（faces → file:line）：
1. **canonical**：sql/migrations/startup/995_error_playbook.sql（201 行）+ down.sql（成对，DROP 两表+函数+清台账，down.sql:8-12）；
2. **embeddata 副本 ×2**：installer/cmd/llm-gw-installer/embeddata/startup/995_error_playbook.{sql,down.sql}，diff 实测与 canonical **逐字节一致**（UP/DOWN 双 IDENTICAL）；
3. **installer main.go**：go:embed ×2（main.go:1863-1867）+ map ×2（:2506-2507）；
4. **dbinit runner**：installer/internal/dbinit/runner.go:1991-1997（StartupFiles 登记）；
5. **tsv**：sql/schema/installed_startup_migrations.tsv:404（index 386）；
6. **docker 通道**：scripts/apply-db-revision-sequence.sh 显式序列清单（:2808-2813）+ 产物探针 ledger_artifact_probe（:3627-3632，两表+唯一索引+seed≥5）；
7. **db-changelog**：docs/db-changelog.md:11131（row 995，状态 pending deploy，**sha256 实测 `7d6ddc1d…70ef` 与该行逐字一致**）；
8. **期望索引台账三份**：db-object-expect.tsv:945-949 / db-object-gap-ledger.tsv:946-950（retired/complete）/ installer-reachability.tsv:949-953（reachable-and-legitimately-retired）——由仓内生成器重生成的形态（5 索引+1 唯一索引齐）。
- 另有 sqlguard 投递门 migration_995_delivery_test.go 实跑 ok（§八）。

**SQL 要点抽查**：plan_kind CHECK 闭集 script/api/prompt（:73）；auto_execute 默认 FALSE fail-closed；表达式唯一索引 `uq_error_playbook_type_pattern (error_type, COALESCE(match_pattern,''))`；种子 5 条单 INSERT 全 prompt 型（:149-188）＋SET（会话级）超时守卫（:67-69，994 实测先例的 docker 通道适配）。

**执行路径闭环（谁写谁读）**：
- **写**：worker `SQLSource.FetchWindow`（worker.go:156-219，candidate_failure_logs_hot 按 error_kind 聚合窗口，watermark 增量，ErrNotProvisioned 跳过）→ `Worker.runOnce`（:69-141，cfg.Enabled=false 直接返回 ⇒ **默认关**，settings/error_playbook_specs.go:17-22 `error_playbook.enabled` Default:false）→ `Engine.HandleEvent`（engine.go:96-176）。
- **已知错误标准执行**：PickEntry（正文模式>类型级）→ RecordHit → 未开自动执行 ⇒ awaiting_approval 落 executions（:112-121）；冷却期 skip（:122-131）；`ExecuteEntry`（:261+）白名单动作四闭集（actions.go:34-37 record_note/set_settings_kv(allowlist :259)/disable_pi_rule/call_admin_api 闭集路由），步骤旧值快照进 steps JSONB ⇒ `RevertExecution`（:357+）可按快照回滚。
- **未知错误 PI 闭环（本审计题眼）**：HandleEvent 未知分支（engine.go:138-175，pending 计数 + KindThreshold:154 + AnalyzeDedup:160）→ **`Engine.AnalyzeUnknown`（analyzer.go:59-147）**：①`LogContextProvider.Gather`（logcontext.go:37-60，candidate_failure_logs_hot 最近 1h 分布+样本）→ ②`proposePlan` 调 LLM（复用正典分析端点，error_playbook_init.go:120-126）→ ③`NormalizePlan` 白名单校验，非法降级 prompt 方案（:80-85，**条目仍落库**）→ ④变更类方案过 `confidenceFloor=0.80`（:39, :93）才 `ExecuteEntry` 执行一次修正，低置信 ⇒ awaiting_approval 留痕（:107-127）→ ⑤**`UpsertFromAnalysis` 条目必落库 source='pi_analysis'**（store.go:447-490，冲突不覆盖取既有行）。
- **自动完善**：worker 每轮对标准执行失败条目 `RefineEntry`（worker.go:123-134 → engine.go:385+，带失败上下文重新提炼，source='refinement'，store.go:499-505）。
- **读/管理面**：/api/admin/error-playbook/{entries,entries/{id},executions,executions/{id},analyze,stats}（admin/error_playbook_handler.go:75-80，AdminMiddleware wrap fail-closed，路由可达+401 钉测 PASS）；手动分析 handleAnalyze（:431+）复用同一 AnalyzeUnknown。
- **生产侧**：995 在三台是否已应用＝db-changelog「pending deploy」⇒ **待主控活体复核**。

## 六、Q5 tool_json_invalid 判定点全仓普查

grep 全仓（非测试）命中 10 文件，**判定产生点唯一**：
1. **唯一产生点** `security/sanitize/output_sensitive.go:210-251`（CheckField `__tool_json*` 标签 → json.Valid 失败 → tryRepairToolJSON（:213）→ 修补失败才 Blocked+Subtype=tool_json_invalid；修补成功按修补后文本走内容检查）。
2. **流式豁免消费点** stream_compliance.go:702/837（本簇两笔根修，见 §二/§三）。
3. 其余为消费/元数据/注释：errorsx/output_policy.go:31（守卫常量+IsFormatGuardRule :41）/ policy_block_feedback.go:107,115,140,332（格式类分类+客户端话术）/ sanitizerules/builtin_rules_meta.go:106（规则展示元数据）/ promptinjection/error_analyzer.go:534（PI 模板）/ dispatch/planner.go:215（换节点重试预算 AttemptCount>=2 终态）/ ir/stream.go:619、tooldialect/tooldialect.go:7、cmd/gateway/main.go:7809（注释）。
- **一致性结论**：截断/失败终帧语义只存在于流式 lane 释放单点；产生点与消费点合同一致（豁免要求 Subtype 逐条相等）；**非流式无对应语义**＝同族第三处（§三 P3）。ir/tooldialect 层的 json.Valid 是组装/方言兜底（丢尾/修补），不是合规判决，与本修无关。

## 七、发现汇总（P 级 + 待复核）

| 级别 | 发现 | 证据 | 处置建议 |
| --- | --- | --- | --- |
| P2 | 6851e072c 引入 HEAD 真 red TestD13ApplyBusinessFailureReturns422（500≠422），与提交信息「admin 全量套件 exit 0」矛盾 | /tmp/sa-c-audit-head 实测 fail；d13_apply_error_split_test.go:60；fix_apply.go:370-372 | 按共享树在途形态补 gate mock（3 行 expect）落仓收口 |
| P2(跨簇) | TestDegradePayloadsCarryMarker 红：session_overview_alignment.go:207/221 两处 200+空载荷无 degraded 标记 | 隔离 worktree 实测；degrade_marker_test.go:463；该文件由 c005445c4 引入 | 归 stats 列车（子审计 B 簇）收口；3643a6408 对该契约文件只加过 errorplaybook 豁免条目，非引入方 |
| P3 | 非流式同族第三处：processBody 无失败/截断豁免语义 | interceptor.go:402-449 | 待主控裁决是否立项（无生产证据） |
| P3 | terminalSeen 不随 clear() 复位（隐性单次语义，终帧后异常续流无钉测） | stream_compliance.go:907-911 vs :460-461 | 可选：clear() 里复位 + 一条钉测 |
| 登记 | 3643a6408 主档自述「真库契约测试（本地 PG 实跑）」无法在本环境复跑（无本地 PG 凭据） | store_realdb_test.go 存在，未跑 | 待主控在有 PG 环境复跑 |
| 待主控活体复核 | ① PI 接入运行时验证（211 开 PI 开关后：格式拦截→队列 output_guard_blocked→分析含归因/建议→apply 422）；② 995 迁移三台部署（db-changelog 标 pending deploy）；③ error-playbook worker 生产轮转实况 | 作者主档 L1 自登记；db-changelog.md:11131 | 主控部署轮逐项验证 |

## 八、测试实跑读数（隔离 worktree /tmp/sa-c-audit-head @ c6bf29aea，go1.27.4）

| 套件 | 命令 | 结果 |
| --- | --- | --- |
| outputcompliance / errorplaybook / promptinjection / sqlguard | `go test <pkgs> -count=1` | 4 包全 ok |
| 钉测四条（断流/完整/failed·incomplete/completed） | `-run "TestInterrupted…|TestCompletedStream…|TestFailedTerminal…|TestResponsesCompleted…"` -v | 6 用例（含 2 子用例）全 PASS |
| streaming 全量（非 race） | `go test ./domains/streaming/ -count=1` | ok 147.9s |
| PI sink/模板/apply 闸钉测 | `TestOutputGuardPISink_…` / `TestApplyFix_OutputGuardBlockedIsViewOnly` | PASS |
| cmd/gateway playbook 路由 | `-run ErrorPlaybook`（可达 11 子路径 + 无鉴权 401） | ok |
| **admin 全量** | `go test ./admin/ -count=1` | **FAIL：TestD13ApplyBusinessFailureReturns422 + TestDegradePayloadsCarryMarker（仅此 2）** |
| -race 定向 | outputcompliance/errorplaybook/promptinjection `-race -count=1` | 3 包全 ok，零竞争 |
| -race streaming | `go test -race ./domains/streaming/ -count=1` | **零 DATA RACE 告警**；唯一失败 TestLargeIngressMemoryScaling（隔离复跑定性，见下） |

**-race 唯一失败的定性（量具形态，非数据竞争、非本簇引入）**：单独复跑 TestLargeIngressMemoryScaling（-timeout 480s）仍 FAIL，失败点＝子进程 TestLargeIngressMemoryChild 的 100MB/concurrency=4 档：race 仪表使 ingress 预处理变慢到 **121s > 网关 ingress 预算** ⇒ 网关自产 `503 request_processing_timeout (stage=ingress, retryable=true)` ⇒ 断言 "unexpected 503"（large_memory_test.go:143,48）；全量 grep **WARNING: DATA RACE = 0**。该测试源自 cd1514c30（实测为 c4b5d00cb 祖先）⇒ 窗口前既有，与本簇 4 笔（6851e072c 只动 policy_block_feedback.go）无关。结论：**新代码并发面（outputGuardPISink 包级 var boot 期一次装配/Engine.mu 保护 pending+lastAnalyzed/complianceStreamState.mu 全程持锁）零数据竞争**。

## 九、复核要点清单（给主控）

1. **收口 P2-1**：把 d13 gate mock（并行树在途形态）落仓；落仓后 admin 全量应只剩跨簇红。
2. **跨簇 P2-2 移交**：degrade 两点归子审计 B 的 stats 列车，避免双开工单。
3. **活体三件**（部署轮）：PI 接入链活体（含 apply 422 断言）、995 三台应用、error_playbook.enabled 打开后 worker 摄取→匹配→落库一轮。
4. **裁决一件**：非流式同族第三处是否立项（§三）。
5. **工作树卫生**：本审计结束时共享树仍有并行会话 20+ 项未提交改动（含 de-DE mojibake 修复与 d13 gate mock）——主控收口时先与并行会话对表，勿重复/覆盖。
