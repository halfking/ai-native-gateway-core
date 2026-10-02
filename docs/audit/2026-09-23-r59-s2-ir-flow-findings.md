# R59 48h 审计轮 — S2 组：IR 生命周期(D01) + 流程闭环(D16) 审计报告

- 日期: 2026-09-23
- 仓库: llm-gateway-go-2 @ main `9f7b0ea3f`
- 审计员: S2（IR 生命周期 D01 + 流程闭环 D16）
- 方式: 只读审计（可跑测试），codegraph + git 考古 + 全库 grep 交叉验证
- 必跑项结果:
  - `go build ./...` → exit 0
  - `go test ./internal/ir/...` → ok（cached）
  - `go test ./transformation/...` → **路径不存在**（仓库无顶层 transformation/，实际包为 `domains/transformation`）；`go test ./domains/transformation/...` → ok（含 anthropic 子包）
  - recall/SSE 仲裁: `go test ./domains/hostedtask/...` ok、`go test ./bg/` ok(24.9s)、`./internal/paramreg/... ./internal/reasonnorm/... ./internal/reasoncap/... ./domains/session/v2/...` 全部 ok

---

## 一、发现总表

| # | 级别 | 摘要 | 证据 (file:line) | 建议 |
|---|------|------|------------------|------|
| F1 | **P1** | **5dc4e9f3b 提交说明与实际代码不符（虚假描述）**：message 声称新增 IR ReasoningContent 字段/parse/serialize/ReasoningDelta/TestParseOpenAIReasoningContent(6 子测试)，但 diff 内 **零处** reasoning_content 代码；实际落码是 ProtocolOllamaChat + MiniMax MaskSensitiveInfo/BotSetting + repetition_penalty + stream.CumulativeContent，测试名为 RepetitionPenalty/MaskSensitiveInfo/BotSetting 四个 | `git show 5dc4e9f3b`（message vs diff 全文；grep ReasoningContent 仅命中 message 文本与 paramreg 上下文行 :279）；paramreg/registry.go 实际改动为 repetition_penalty/mask_sensitive_info/bot_setting → KindIRHandled | 审计台账登记该 commit 的真实内容；后续以 R52 `89a4a9f88` 及 Phase D `d668d9dff` 为 reasoning_content 支持的权威落点。无法改史，登记即可 |
| F2 | **P2** | **recall 回调交付不带 handoff_packet（§3.3④ 读路径断缝）**：RecallTask 注释称"每次召回交付一次最新包"，但 deliverOne 只按任务行重建 `{status,result,attempt}`，eventType 固定 `hosted_task.<status>`，recalled 事件的 payload（recall_status + handoff_packet）从不进入回调体；回调接收方无法区分召回投递与结算投递、也拿不到包 | domains/hostedtask/store.go:387,436-444（写入+注释）；domains/hostedtask/callbacks.go:109-121（deliverOne 重建载荷）；CallbackJob 无事件载荷字段 store.go:749-758 | deliverOne 认领时 JOIN hosted_task_events 取该 seq 的 payload 并透传（或至少透传 recall_status + handoff_packet）；补回调投递断言测试 |
| F3 | **P2** | **{"raw":...} format-anomaly 观测面失效（一次性事件）**：serialize_anthropic 以 requestID="unknown" 上报 ReportProtocolLoss；全库无任何生产方安装 scoped reporter；全局 dedup map 永不过期且 key 含 requestID ⇒ 该 format-anomaly **每进程生命周期只发一次**，无计数无速率 | internal/ir/serialize_anthropic.go:494-499；internal/ir/anomaly_reporter.go:171,196,205,243-250,260-272；`grep -rn WithAnomalyScope` 生产代码 0 命中 | 传真实 request_id（serialize 链路若有 ctx/requestID 可达则透传），或将 anomaly 改为 metrics 计数器+采样日志 |
| F4 | **P2** | **4e84f6a6e hot 表读路径风险的定性结论 + 回退后性能问题无人接盘**：见 §二-6。HEAD 已回退（❌反证 turn_no 回卷风险在 HEAD 不存在）；但回退恢复了 UNION view 逐请求扫描（正是 4e84f6a6e 要治的 sessionv2mirror WARN 104→7 的问题），758bf92c2 一句"视图路径可接受"没有留下任何后续性能跟进项 | domains/session/v2/turn_writer.go:286-290；bodies_writer.go:478,535（HEAD 均为 view）；bg/partition_manager.go:35-45(DefaultRetentionWindow=8h),983-1015(promoteSpecs) | 给 view 扫描立跟进项（如 session_bodies_unified 上 turn_no 方向索引评估，或"hot 优先+空/陈旧回退 view"的带兜底设计——绝不能裸读 hot，理由见 §二-6 技术判定） |
| F5 | **P3** | **reasoning_content 请求侧消息级缺口（半环节）**：IR Message 有 Thinking/RedactedThinking（Anthropic 入向已填），但 OpenAI 入向 message.reasoning_content 不解析、OpenAI 序列化也不回发（grep 两文件 0 命中）。DeepSeek 官方语义本就不允许回喂 reasoning_content，丢弃可辩护，但属"静默且未登记"的不对称 | internal/ir/types.go:304-307；internal/ir/parse_openai.go、serialize_openai.go 零命中；对照 internal/ir/response.go:38-40（响应侧有） | 在 paramreg 或 D01 域文档登记"请求侧 message.reasoning_content 按 DeepSeek 语义有意丢弃"；如需保留多轮思维链则需新增 Message 字段 |
| F6 | **P3** | **{"raw":...} 无解包路径（单向失真）+ 流式方向无包裹（不对称）**：parse_anthropic 原样透传 input，包裹体下轮回灌时模型看到 `{"raw":...}` 参数（协议合法、语义失真、回程无 anomaly）；流式 Anthropic 输出方向 partial_json 原样透传非对象 JSON，同类客户端解析失败仍可发生 | internal/ir/parse_anthropic.go:275,316-325；internal/ir/stream.go:976-997；domains/streaming/anthropic_stream.go:812 | 登记单向不变式；流式侧如需对齐只能"缓冲至 Finalize 后包裹"（tool_arguments_assembler 已有 Finalize 接缝），评估成本后决定 |
| F7 | **P3** | **reconciler 陈旧注释 + 仲裁后测试缺口**：tick 内注释仍写"SSE 订阅 + 轮询兜底"，与文件头"R65 仲裁移除 SSE 订阅（B 删订留询）"矛盾；SSE 测试删除后 projectPass/pollOne 轮询权威路径无新增单测（bg/hosted_task_reconciler_test.go 仅剩 3 个结算类测试） | bg/hosted_task_reconciler.go:9-11 vs :125；bg/hosted_task_reconciler_test.go（3 个测试） | 修注释；为 pollOne 终态判定（raw.stop_reason 强校验→needs_review）补单测 |
| F8 | **P3** | **prompt_compress.go 零消费（裁决证据）**：internal/ir/prompt_compress.go 全部导出/内部符号仅被本文件与自身测试引用；真生产面是 domains/transformation/ctx_compress.go（executor_chat:2167,2307、compressor_trim:9、context_summarize:1210、survival_coordinator:1045） | codegraph brief internal/ir/prompt_compress.go（CompressConfig dead-ffi 0 callers / CompressMessages test-only）；grep 全库交叉验证 | **建议：删除** internal/ir/prompt_compress.go + prompt_compress_test.go，并在 handoff 登记生产面为 ctx_compress。"接线"不建议（与 ctx_compress 同体双压语义冲突）；"保留"仅当 IR 级压缩有近期立项 |
| F9 | **P3** | **轮次摘要链路残余缺口**：v2 MessageSource 只读 request_delta（每轮折叠最后一条），最终轮 assistant 回答要等下一轮请求回灌才进摘要输入；响应侧仅经 digest 源取 digest 字段；消息上限 LIMIT 20 | domains/sessionsummary/message_source_v2.go:78-96,132-164；message_source_digest.go:18-20 | 登记"末轮响应延迟进入摘要"的口径；评估 digest 源是否已覆盖大多数场景（当前看已部分缓解） |

无 P0。R59 handoff 首要疑点（hot 表 turn_no 回卷）在 HEAD **不成立**（详见 §二-6，❌反证）。

---

## 二、必审项逐项证据

### 1. 5dc4e9f3b reasoning_content IR 支持 — ✅实证"支持已存在"，❌该 commit 未做此事

- **该 commit 实际内容**（与 message 宣称完全不符，F1）：types.go 加 ProtocolOllamaChat + MiniMax 专有字段；parse/serialize_openai 加 repetition_penalty/mask_sensitive_info/bot_setting；stream.go 加 CumulativeContent（非 ReasoningDelta）；测试 4 个均与 reasoning 无关。
- **真实支持链（HEAD 实测）**：
  - 非流式响应双向：`internal/ir/response.go:344`（解析 message.reasoning_content → ir.ReasoningContent）、`:558`（OpenAI 序列化回发）、`:1242-1260`（R52 F6：Gemini thought part 补发 + thinking 块防双发守卫）。
  - 流式：`internal/ir/stream.go:111,231,348,354`（OpenAI SSE 解析+DeltaType="reasoning"）、`:746`（OpenAI 回发）、`:584`（Anthropic thinking_delta 解析）、`:960-970`（Anthropic thinking_delta 回发）；Responses 桥有界累积 reasoningText（domains/streaming/responses_bridge.go:187,675,1121）；遗留转换器同样处理（messages.go:1397,1425；chat_to_anthropic_response.go:73）。
  - 落库：session_bodies/turn 宽表存 `req.ResponseBody` 原文（domains/session/v2/session_writer_v2.go:579,601）——对应方言的 reasoning 字段随 wire body 一并落盘；digest 源只取 digest 字段。
  - 缺口：请求侧消息级（F5）。

### 2. 0fc829916 tool_use.input {"raw":...} 包裹 — ✅包裹正确+5 测试钉桩；❌下游不解读（按设计单向），❌观测面失效

- 包裹逻辑：非对象 JSON（解析失败/数组/标量/null）→ `{"raw":原文}`，合法对象透传，空参 `{}`（internal/ir/serialize_anthropic.go:473-499）；测试五例在 HEAD（serialize_anthropic_tooluse_input_test.go）。
- 下游：无任何解包路径（全库 grep `"raw"` 无相关命中）；parse_anthropic.go:275 原样透传。**不会导致上游 400**（{"raw":...} 本身是合法 JSON 对象），但下一轮模型看到的是包裹形态参数（F6）。Anthropic 上游 400 的原始病灶已消除。
- format-anomaly：上报存在但 requestID="unknown" + 全局永久 dedup ⇒ 每进程一次（F3）。
- 流式响应方向无同类包裹（F6）。

### 3. 758bf92c2 recall P0 501 + R36/R63 残留移除 — ✅闭环（后被 e2b0c75b2 反转）

- 时间线：4a81fc3e4(18:38 落码 recall) → 758bf92c2(19:41 撤回 P1 子集改回显式 501) → e2b0c75b2(21:02 带迁移 742 重落全量 recall)。HEAD 的 recall 是**完整实现**（route → handler.recall，domains/hostedtask/handler.go:169-174,515-540），非 501。
- 残留 grep：`workerCtx`/`refreshStreamTask`/`ensureStream`/`config-gate-check` 在代码与 scripts 均零命中；scripts/hostedtask-config-gate-check.sh(+test) 已删；无悬空引用、无半删状态。唯一残留是注释矛盾（F7）。

### 4. recall 快照路径 + 迁移 742 + SSE 仲裁(B 删订留询) — ✅写路径/迁移/注册闭环；❌回调读路径断缝

- **写路径闭环**：RecallTask 单事务（store.go:390-450）：FOR UPDATE 锁行 → 非终态抢占 cancelled（状态机校验 CanTransition）→ 以抢占后最终行 buildHandoffPacket → appendEvent(recalled, payload 含 recall_status+handoff_packet) → 回调台账 pending+EventID 幂等键 → commit。终态任务只补事件+重置回调。
- **读了谁**：①recall HTTP 200 直接带 handoff_packet（handler.go:533-539）；②GET /v1/hosted-tasks/{id} 事件时间线含 payload（handler.go:424-432）；③❌回调投递不带包（F2）——写了没人读的部分恰是回调 seam。
- **迁移 742 四处注册齐全**：sql/migrations/startup/742_...sql + .down.sql；installer embeddata 同名 up+down（up 与主库**逐字节一致**，diff 实测）；installer/cmd/llm-gw-installer/main.go:530-531（go:embed）+:689（embed map）；installer/internal/dbinit/runner.go:245（执行列表）；sql/migrations/startup/migration_742_test.go。三方同步（types.go:115 EventRecalled ↔ CHECK 白名单 ↔ 设计文档 §4.3）实测成立；down 先删 recalled 行再还原 711 白名单。
- **SSE 仲裁 B**：acc_client.go 379→精简后零 SSE 引用（grep 0 命中）；reconciler 头注释明确"轮询 GET command（状态权威）"；轮询路径测试缺口见 F7。

### 5. prompt_compress 生产面对齐 — 证据齐全，建议：**删除**（见 F8）

### 6. 4e84f6a6e route MAX(turn_no)/latest-body 走 *_hot 表（本组最高优先级疑点）— ❌反证：HEAD 不存在该风险；✅实证：风险窗口内真实存在、且回退正确

- **(a) 查询有无时间窗兜底/分区回退**：4e84f6a6e 版本**没有任何兜底**——三处全部裸读 hot（MAX(turn_no) 、getLatestBodies 主读、final_full 回退）。这正是疑点核心。
- **技术判定**：commit 的安全性论证（"advisory lock 序列化 ⇒ 分区不会有 active session 的最新 turn_no"）只对**持续活跃**会话成立（最新轮必然 <8h 仍在 hot）。它防不了**闲置复活**：会话闲置超过保留窗（DefaultRetentionWindow=8h，bg/partition_manager.go:35-45；session_turns_hot/bodies_hot 均 8h 档）→ 全部行被 promote（promote_session_turns_hot_to_partition 只按 ts 阈值，不感知会话活跃性）→ 复活后 `COALESCE(MAX(turn_no),0)+1` 在空 hot 上得 **1**，与分区中历史行撞号。advisory lock 锁的是并发操作，锁不住"已经发生过的迁移"。
- **撞击后果**：session_turns 父表**无** UNIQUE(tenant,session,turn_no)（430:104 仅 id PK + request_id 索引；hot 表 UNIQUE 含 partition_date，526:68-70，跨日期不冲突）→ **静默重号**；getLatestBodies 裸读 hot 对复活会话 ErrNoRows → latest-body 断供。即：若该版本上线，"超过 8h 后再来一轮"必现 (a)(c) 两类缺陷。
- **(b) 调用方语义是否限近期会话**：不限。appendTurnInLockedTx 是所有 V2 会话写入的公共路径，无法保证 8h 内活跃。
- **(c) turn_no 回卷/覆盖风险**：同上，回卷确定性发生（闲置>8h 复活场景）；因无唯一约束，表现为重复 turn_no + latest 读错行，而非报错——更隐蔽。
- **实际暴露面**：**零生产暴露**。245 最后一次部署 build 2221=49ca6332（2026-09-23T06:40Z）早于 4e84f6a6e(17:45+0800)；build 2238=81e4ab93 已含回退；db-changelog 9-23 当日无中间部署。hot-only 仅存活于本地 8782 约 2 小时（17:45–19:41）。
- **残留问题**：回退使诱发性性能问题（view 全扫描、sessionv2mirror WARN 104/5min）重新上岗且无跟进项（F4）。

### 7. 轮次摘要（D16）— ✅链路存在且多源，登记口径缺口（F9）

- 写面：轮级 title/summary 预览（migration 456，SessionWriterV2 从首条 user/assistant 消息填充，非 LLM、列表 UI 即时非空，turn_writer.go:104-109）；会话级 LLM 摘要 summarystore.Upsert → session_summaries（internal/summarystore/store.go 头注释列明双写方：domains/sessionsummary/summarizer.go + admin/auto_summary_generator.go）；计数/成本 KPI 由 563 触发器挂 request_logs_hot 聚合；564 幂等回填；admin SessionSummaryV2API 按需 LLM 摘要（读 session_bodies_unified，admin/session_summary_v2.go:272-289）。
- 去格式供人查看：v2ContentText 将 string|block-array content 拍平为纯文本（并修了 IR-audit P1-2 多模态整轮丢弃）；summarizer 引入 secretmask 脱敏。
- 多轮双向解析：正向=按 ts 升序增量拼接（request_delta 每轮折叠末条，message_source_v2.go:132-164；排除 goal-% 影子轮）；反向/校验=cmd/tools/validate_sessions_v2/reconstruct.go 独立重建工具。缺口见 F9。

### 8. IR 定义清晰度盘点（如实登记）

`internal/ir/types.go`（750 行）是**协议域 IR**（wire 表示）。所列 15 个概念的落点：

| 概念 | 落点 | 状态 |
|------|------|------|
| 多轮对话 | IR：Message[]/InternalResponse/StreamChunk | ✅ IR 内 |
| 附件媒体 | IR：ContentBlock/MediaSource/DocumentBlock/ImageSource/InputAudioBlock；旁路：TurnRecord attachment_* + ir_attachment_adapter.go | ✅ IR+旁路双层 |
| 模型 | IR：InternalRequest.Model / InternalResponse.Model；旁路：TurnRecord canonical/raw model | ✅ |
| 用户 | IR：InternalRequest.User/Metadata（透传）；旁路：TurnRecord EndUserID/CustomerID/APIKeyID | ✅ |
| 路由数据 | 旁路：TurnRecord 路由组（turn_writer.go:155-164）+ routing_decision_log_hot | ⚠️ 旁路 only |
| 流程跟踪(调度瀑布) | 旁路：TurnRecord T0–T9（turn_writer.go:115-131）+ trace_events | ⚠️ 旁路 only |
| 压缩脱敏 | 旁路：TurnRecord Compression*（:68-71）+ ctx_compress + secretmask | ⚠️ 旁路 only |
| 轮次 turn_no | 旁路：session_turns(_hot) | ⚠️ 旁路 only |
| 总轮次 | 旁路：session_summaries.request_count（触发器聚合，可漂移） | ⚠️ 聚合列 |
| 项目 | 旁路：TurnRecord.ProjectID / partition_date(日期) | ⚠️ 旁路 only |
| tag | 旁路：session_summaries agent_type/expert_type/tags（606）+ analysis/tagger | ⚠️ 旁路 only |
| 任务 | 独立域：domains/hostedtask Task（自带状态机/事件台账/回调），有意不进 IR | ✅ 独立域 |
| 供应商 | 旁路：TurnRecord.Provider；IR 仅有 SourceProtocol（协议≠供应商） | ⚠️ 旁路 only |
| 凭据 | 旁路：TurnRecord.CredentialID | ✅ 正确不在 IR |
| 日期 | 旁路：partition_date | ⚠️ 旁路 only |

**判定**：边界本身清晰（wire IR vs 运维/会话旁路），无概念被错误塞进 IR；缺的是 D16 要求的"15 概念 → 权威落点"映射登记文档，而非缺定义。

---

## 三、结论

- 最高优先级疑点（hot 表 turn_no 回卷）在 HEAD **反证成立**：4e84f6a6e 的裸 hot 读确有确定性回卷缺陷，但已在同日 758bf92c2 自纠且从未部署；遗留的是性能回退无人接盘（F4）。
- 近期改动闭环总体良好：recall §3.3④ 写路径/迁移 742/SSE 仲裁均闭环，唯回调投递 seam（F2）与两处观测面（F3）/注释-测试卫生（F7）待收口。
- 5dc4e9f3b 为**虚假描述 commit**（F1），审计追溯时应以 R52 89a4a9f88 为 reasoning_content 权威落点。
