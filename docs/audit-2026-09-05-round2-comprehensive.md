# 2026-09-05 第二轮 24 小时修正审计综合报告（8 轴并行）

- 审计对象：`bb93bf6fe..3c51c2e36`（2026-09-05 00:00–08:45 合入 main 的全部修正：7 轴并行修复轮 + 八项闭环轮 + auto 模型/持久化存储/部署系列）
- 审计模式：codegraph 图谱（129,217 节点 / 226,113 边）+ 主代理编排 + 8 个并行只读子代理（轴 A–H）
- 分轴详报：`.audit-workspace/2026-09-05-round2/axis-{A..H}-*.md`（全部 file:line 证据在分轴报告中，本报告只保留结论与修复索引）
- 本轮修复：**1 P0 + 6 P1 + 16 P2** 已全部落地（部分为跨轴合并项），见 §三；修复后验证见 §四

## 一、分轴发现概览

| 轴 | 范围 | P0 | P1 | P2 | P3 |
|----|------|----|----|----|----|
| A IR 与多协议转换 | internal/ir + transformation + 流式桥 | 1 | 1 | 6 | 3 |
| B 双模式存储/缓存/附件 | storage/* + cache_v2 + sqlite | 0 | 1 | 4 | 5 |
| C 队列/并发/限流/安全 | dispatch + executors + governor | 0 | 0 | 3 | 7 |
| D hot+columnar 分区 | V371/656/657 + promote/cleanup 调度 | 0 | 2 | 5 | 9 |
| E 供应商错误闭环 | supplier_errors 链路 + trend/详情 | 0 | 2 | 4 | 3 |
| F 会话摘要/前端统一 | session_turns_v2 + 词表/i18n/菜单 | 0 | 0 | 6 | 8 |
| G 修复质量复核（审计审计者） | 八项闭环新代码逐项验证 | 0 | 1 | 5 | 10 |
| H auto 模型全量实现 | autoroute 链路 + settle/affinity + 文档 | 0 | 1 | 4 | 5 |

与上两轮重复的发现只保留编号引用，不再展开。

## 二、系统级分析（跨轴根因归纳）

本轮 66 项发现并非孤立缺陷，收敛为六个系统性根因：

1. **「写入侧归一化 vs 读取侧假设」不对称（A-#13/A-#17）**：解析端把多形态输入归一到 IR（system 数组→Parts、redacted_thinking→独立字段），序列化端却只处理单一形态（System.Content、"thinking" 键），且无 loss 事件。**对策**：本轮以" Parts 回填 + wire 格式对齐 + 回归测试"修复；残留同类（A-#18 file_id 双轨、A-#19 降级无上报）列入下轮，并以 ReportProtocolLoss 作为门禁惯例。
2. **闭环的「最后一公里」断裂（G-#7/D-2#3、B-#2、D-2#1、E-#4）**：修复代码本身正确，但配套装配缺失——657 迁移没进 installer/db ensure、Reconcile/Repair 无生产调用方、supplier_errors_hot 只注册了 admin 手动表而漏了后台调度、stats 只写 minute 而读端默认查 hour/day。**对策**：本轮补齐装配；后续新增闭环项的验收标准必须含"从入口到可见的端到端证据"。
3. **部署矩阵的隐式假设（E-#1、D-2#4/H-2）**：V371 的 FORCE RLS 假设 superuser 或已设置 GUC，生产 llm_gateway 角色两条都不满足；656 假设 installer 重跑。**对策**：V371 policy 补 bypass_rls 旁路（对齐 V367），db.go 补 auto_route_selections_hot 幂等 ensure；后续迁移评审加入"非 superuser 应用角色"检查项。
4. **固定短轮询的放大效应（C-#13）**：Redis-enforce governor 2ms 平轮询在队列深 × 凭据数下放大为 15 万 EVALSHA/s 的重试风暴。**对策**：指数退避（5ms 起、250ms 封顶）；同类忙等（C-#7/#8/#11）维持 P3 观察名单。
5. **取消路径的 TOCTOU 长尾（C-#12）**：single-owner 修复覆盖了 move/routeFailover 入口，但 dispatch/parkScheduled/move 中段三个入口仍可绕过。**对策**：三处补终态守卫；长期方案（recordDecision 互斥）仍列为架构债。
6. **展示层的「最后一跳」缺失（F2-#1~#4）**：闭环1/8 的结构化字段到 API 为止，UI 无列、词表两处独立实现且击穿 i18n 棘轮门禁。**对策**：共享 errorVocab 模块（单一词表源 + badge 规范）+ supplier/error_code 列 + request_id 跳转；CJK 棘轮基线下调。

## 三、本轮修复清单（全部含验证）

### 主代理直接修复（Go 运行时）

| # | 级别 | 修复 | 文件 |
|---|------|------|------|
| 1 | **P0**（A-#13） | System.Parts 在 OpenAI/Responses 序列化方向被整体丢弃——新增 systemPlainText 归一（Content 优先，Parts 文本 join），Claude Code→OpenAI 系上游与 Gemini 原生客户端的系统提示词不再静默丢失；补 array-system roundtrip 测试×2 | internal/ir/serialize_openai.go、serialize_responses.go、system_parts_test.go |
| 2 | **P1**（A-#14） | ParseOpenAIStreamChunk 识别 in-band `{"error":{...}}` 帧→ChunkTypeError（原落成空 delta，responses_bridge 错误分支为死代码、failover 不触发）；writeFinalEvents 对 content_filter/network_error/sensitive 降级 incomplete（修复注释与实现漂移） | internal/ir/stream.go、domains/streaming/responses_bridge.go + openai_stream_error_test.go |
| 3 | **P1**（H-1） | /v1/messages、/v1/responses 的 model=auto 在 flag-off（默认）时 `bodyBytes = newBody` 无 nil 守卫把请求体置空——对齐 chat 路径加守卫 | domains/streaming/messages.go、responses.go |
| 4 | **P1**（B-#1） | lite request_logs UPSERT `has_body = excluded.has_body` 被终态后无 body 的用量回填 UPDATE 清零——改 `MAX(request_logs.has_body, excluded.has_body)` + 双向回归 | storage/sqlite/request_log_store.go + 测试 |
| 5 | P2（C-#12） | dispatch()/parkScheduledRequest 入口补 completed/abandoned 终态守卫 + move() 在 routeFunc 长回调返回后二次检查（single-owner TOCTOU 残余闭合） | domains/dispatch/dispatcher.go、pipeline.go、failover.go |
| 6 | P2（C-#13） | Redis governor 饱和等待从固定 2ms 平轮询改为指数退避（5ms 起 ×2、250ms 封顶，governorBackoff）——消除 15 万 EVALSHA/s 级重试风暴 | domains/dispatch/redis_backend.go |
| 7 | P2（C-#14） | runDispatcher/runFailover/runTotalDrainer 三个调度 worker 循环补 per-item panic recover（对齐 forwarder.attempt 模式，panic 转 ForwardOutcome 完成请求，worker 不死） | domains/dispatch/dispatcher.go、failover.go、pipeline.go |
| 8 | **P1**（E-#2） | request_logs 错误路径 response_body/preview 未脱敏——新增 errorsx.RedactCredentialShapes（全量脱敏不截断）接入 buildEntry 出口，供应商 401/403 回显凭据不再入库 | errorsx/sanitize.go、domains/streaming/request_log_pipeline.go |
| 9 | P2（轴G） | SanitizeErrorText 按字节截断劈开 UTF-8 序列→PG UTF8 拒收 INSERT——truncateUTF8Safe 按字符边界截断 + 回归测试 | errorsx/sanitize.go + truncate_utf8_test.go |
| 10 | P2（F2-#6） | session_turns_v2 四处 err.Error() 直拼客户端响应（SQLSTATE/约束名外泄）+ 两处 `err.Error()=="no rows..."` 字符串比较——改 errors.Is(pgx.ErrNoRows) 与干净文案（细节仅留 slog） | admin/session_turns_v2.go |
| 11 | P2（E-#5） | anthropic 出口补 RoutingTracker.Add（原仅 OpenAI 出口记录，闭环2 的 foldCandidateOutcomes 恢复对 anthropic 候选退化为合成 no_available_channel） | domains/streaming/executors/executor_anthropic.go |
| 12 | **P1**（A-#17） | redacted_thinking 线格式错位：请求方向按真实 `data` 键解析（兼容旧 `thinking` 键 IR 行）、序列化输出 `data` 键；响应方向补 redacted_thinking parse/build 全链（原整体丢弃，下一轮 thinking 链校验可能 400） | internal/ir/parse_anthropic.go、serialize_anthropic.go、response.go + redacted_thinking_test.go |
| 13 | P2（H-5） | estimateTokens 把 CJK 计数也除以 4（每汉字≈0.5 token，注释契约 1.5-2），long_context 路由门限对中文永不触发——改 ascii/4 + cjk×2 | domains/streaming/auto_route.go |

### SQL/迁移修复子代理（后台并行）

| # | 级别 | 修复 | 文件 |
|---|------|------|------|
| 14 | **P1**（D-2#1） | supplier_errors_hot 补注册后台 promoteSpecs 调度 + 注册表漂移守卫测试（bg 侧测试直接解析 admin 源文件双向断言 fnName 集合相等，规避 import 环；hot 8h 不变式对该表重新生效） | bg/partition_manager.go + 测试 |
| 15 | **P1**（D-2#2） | V371 promote 函数从「temp+块外 DELETE+EXCEPTION 吞错」非原子模式（602 事故同款）改写为 656 模板单 CTE（显式列 + FOR UPDATE SKIP LOCKED） | deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql |
| 16 | **P1**（E-#1） | V371 RLS policy 补 `OR current_setting('app.bypass_rls',true)='true'` 旁路（对齐 V367）——hot 表与**父表**（promote 以应用角色写父表、unified 视图 security_invoker 扫父表）两处；stats 表本为 `USING(true)` 全放行无需处理。已用非属主角色探针实测：无 GUC 时 INSERT 报 42501，SET bypass 后成功 | deploy/sql/migrations/V371__*.sql |
| 17 | **P1**（G-#7/D-2#3） | 657 迁移孤儿修复：installer embeddata 嵌入 + main.go 三处登记 + **dbinit/runner.go StartupFiles 第四处**（漏掉则 SQL 被拷贝但永不执行）+ stats_migrations_test/runner_test 同步——闭环3 的 decision_history 在 installer 装机上真正生效 | installer/cmd/llm-gw-installer/* |
| 18 | P2（D-2#4/H-2） | db.go 补 ensureAutoRouteSelectionsHotSchema（幂等建表+索引+视图）——只升二进制的存量库不再整批丢弃 selection | db/db.go |
| 19 | P2（H-3/H-4） | promote 窗口下限（auto_route_selections_hot ≥5h 钳制）+ 批选条件改「settled 或 7d 兜底」——settle/promote 时序约束不再只靠默认值 | bg/partition_manager.go、sql/migrations/startup/656_*.sql + 测试 |
| 20 | P2（D-2#5/E-#8） | supplier_error_stats 补 TTL cleanup（minute 桶）+ 聚合器水位推进（失败不推进自然重算，迟到行补聚） | bg/partition_manager.go、supplier_error_stats_aggregator.go |
| 21 | P2（E-#4） | 聚合器补 minute→hour→day 二次 rollup——趋势 API 默认 24h/168h 窗口不再恒走明细全表扫兜底 | bg/supplier_error_stats_aggregator.go |
| 22 | P3（D-2#13） | ensureSpecs 补 ensure_supplier_errors_partition | bg/partition_manager.go |

### 前端修复子代理（后台并行）

| # | 级别 | 修复 | 文件 |
|---|------|------|------|
| 23 | P2（F2-#1+#2） | 共享词表模块 errorVocab.ts（error_kind/stage/retryable/result 单一来源 + 统一 badge class），RoutingAttemptsTimeline 与 ErrorDetailTab 两处消费同源；硬编码中文全部 i18n 化（8 语言），CJK 棘轮门禁恢复绿 | web/src/utils/errorVocab.ts、两组件、locales/* |
| 24 | P2（F2-#3） | journey 事件路径 credential_id/from/to 统一过 credentialDisplayName（闭环8「绝不回显 raw」补完）；useCredentialLabels 默认前缀 i18n 化 | RoutingAttemptsTimeline.vue、useCredentialLabels.ts |
| 25 | P2（F2-#4） | ErrorDetailTab 失败表补 supplier/error_code 列 + request_id 可点击跳转请求详情（闭环1 可观测字段「人眼可见」） | ErrorDetailTab.vue |
| 26 | P3（F2-#9/#10） | TurnDigestDrawer 调用点传 TurnListItem.title/summary（死契约激活）；附件失败改行内提示不炸全局错误态 | 三个调用点 + TurnDigestDrawer.vue |

## 四、验证记录（2026-09-05）

```
go build ./...                                          → exit 0
go test ./internal/ir/ ./errorsx/ ./storage/... -count=1 → ok（含 6 组新回归）
go test ./domains/dispatch/ -count=1                    → ok（25s）
go test ./domains/dispatch/ -race -count=1              → ok（33s）
go test ./domains/streaming/... -count=1                → ok
go test ./bg ./db -count=1                              → ok（SQL 子代理；含水位/注册漂移/ensure 新测试）
go test ./installer/... -count=1（模块内）               → ok
go test ./deploy/sql/verify/（kx-citus-pg17 真实容器）    → 7 组全 PASS：V371 原子 CTE + bypass policy 干净应用、
                                                          8h 保留+批次上限、columnar 不可变、unified+RLS、
                                                          UPSERT 幂等、迁移重放幂等；非属主角色 RLS 探针通过
更新后 656 在本地 llm-gateway-pg 幂等重放 + hot 行为脚本 9 项全过
web: npx vue-tsc --noEmit                                → 0 错误
web: npx vitest run（全量）                              → 112 文件 / 792 用例全绿（连续两次）
web: vitest src/i18n/                                    → parity 6 + CJK 棘轮 2 + keys_referenced 4 全绿，
                                                          CJK 计数 6877→6836（基线更新，净降 41）
```

（子代理详细验证输出见各分轴报告与提交信息）

遗留提示：本地 llm-gateway-pg 已重放新 656，但 **V371 的新函数体/RLS bypass 尚未重放到该库**，需统一部署时执行（deploy-154 / pg-schema-sync 流程）。

## 五、已记录未修（下轮工作包候选）

### P2（跨轴优先）

- A-#18 file_id 三缺口（documents 顶层、OpenAI/Anthropic 双轨编码、file_id 图片空 URL——最后者同协议 roundtrip 即损坏）；A-#19 降级无 loss 上报族；A-#20 tool_result→Gemini functionResponse.name 取自 ID。
- E-#3 provider_error_details 聚合键含 LEFT(error_message,200) 碎片化（M）；E-#6 可重试率/阶段分布无聚合载体（M）。
- B-#2 Reconcile/Repair 仍无生产调用方（接线工作包）；B-#3 lite 轮号续排跨保留期复用；B-#4 Invalidate 窗口 L3 读在锁外残余；B-#5 附件 mtime 清理与 dedup 互斥（升级 F-#5）。
- C-#15 complete() 终态路径串行 Redis 往返 ~9-12s 上限；F2-#5 UnifiedRequestSessionDrawer 跨请求正文串页（整体迁移 useRequestDetailLoader，M）。
- H-6 autoclass-bench prompt 与生产分类器漂移。

### P3（择机/清理库存档）

- A-#15 signature_delta 只解析不序列化；A-#16 max_completion_tokens 语义漂移；A-#21 ir_transport 流式休眠缺陷；A-#22 业务元数据 IR 归宿。
- C-#15~#21（Stop 无预算、Transport HTTP/2、加权溢出、PriorityCluster 死代码、忙等族）。
- D-2#6 七张遗留 hot 表 promote 非原子（M，建议单迁移批量重装）；D-2#8~#16（窗口口径、advisory lock、metrics 读法、快照漂移）。
- E-#7 domains/routing 重复 CandidateFailureWriter 死代码；E-#9 candidate_failure_handlers 读端未切 unified。
- F2-#7 双轨摘要统一（M）；F2-#8 z-index token；F2-#11~#14（格式 util、附件 util、nav fail-closed 运维可见性、三态组件）。
- H-7 chat selection 记录早于 session 解析；H-8 热查询直打父表；H-9 auto 大小写；H-10 文档措辞。

### 冗余/待清理代码清单（汇总自各轴，见分轴报告详单）

- 死代码：`mapEffortToBudget`（serialize_anthropic）、`planLegacy`（executors/router）、`rpmSHAFallback`（redis_sliding）、`itoa`（pipeline）、`emptySet`（handler_autocombo）、`getAttachmentSignedUrl`+404 stub 成对、`SessionTurnDrawer.vue`（已 DEPRECATED）、`domains/routing/candidate_failure_logger.go`（E-#7）、`PriorityCluster`+`sortPriorityClusters`。
- 重复实现：joinTextParts 三胞胎、admin/turn_digest.go vs domains/sessiondigest、admin 手动 promote vs promoteSpecs 注册割裂、CredentialRef/GovernorSpec 双轨镜像。
- 生成物漂移：sql/objects/functions promote 快照（重放会回退 602/624/628 原子修复，需加禁用横幅或重新生成）。

## 六、方法论与对照

- codegraph build → 8 子代理并行分轴只读审计（报告落 .audit-workspace/2026-09-05-round2/）→ 主代理系统级归纳（§二）→ 按文件域拆三个修复执行流（主代理 Go 运行时 / SQL 迁移子代理 / 前端子代理，文件集互斥并行）→ 统一验证 → 提交。
- 与 `docs/audit-2026-09-05-24h-comprehensive.md`（第一轮）的关系：本轮复核确认第一轮 12 项修复全部成立（含 f0447666c、闭环2/4/6/7、keystore_sync、settle hot-only）；§五承接其 §四 未修清单并更新现状（B2 已接线、C-#2/C-#4 已闭合等）。
- 对 `docs/audit-2026-09-05-eight-closures.md` 的勘误：闭环3 的「迁移 657」实际未进 installer/db ensure（G-#7，本轮修复）；闭环1 的趋势 API 默认窗口仍走明细兜底（E-#4，本轮修复）；闭环8 击穿 CJK 棘轮门禁（F2-#1，本轮修复）。
