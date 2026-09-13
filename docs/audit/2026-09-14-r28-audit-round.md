# R28 · 24h 修正审计轮（六子代理并行 + 21 项遗留修复落地）

- 日期：2026-09-14
- 审计范围：41b529de5..ace419dcc（R27 修复后的全部新提交；本轮起为主分支同步轮）+ R27 登记遗留 §三 #2-#12 与 §七 #13-#22 共 21 项（#1 死桥已于 R27 补派轮 b7c8d6f39 收口）。
- 方法：codegraph 全库重建（136346 节点/239871 边）→ 主代理 + 6 个只读审计子代理并行（streaming 终态矩阵 / bg 探针与下限守卫 / supplier_errors 数据闭环 + 分区 / dispatch 队列与评分 / lite 双存储 / web+installer+文档）→ 汇总定级 → 6 个修复子代理并行落地（文件域互斥）→ 主代理补三处收尾（admin 读端 RLS 扫尾 / embeddings 账本接线 / 手动 weight 钳制）→ 全量门禁验证。
- 合并轮：origin/main 快进合并 ace419dcc（docs/design/hosted-task-delegation-design.md，纯设计提案无代码，任务托管能力盘点已被本报告 §四.6 引用核对）。

## 一、审计结论总览

R27 的 21 项登记遗留**全部在 HEAD 复核成立**，其中 4 项比登记更严重：

| 项 | 登记级别 | 复核结论 | 调整 |
|---|---|---|---|
| #6 supplier_errors FORCE RLS 无旁路 setter | P2 | 写入/promote/聚合/四处 admin 读端全链在非 superuser 角色下静默 0 行；candidate_failure_logs promote 同殃；hot 表自此无清理路径无界增长 | **升 P1**（数据闭环断裂） |
| #17 full_storage.postgres_url 假字段 | P2 | 生产路径 full 模式根本不调用 Validate()——YAML-only 部署静默落 no-DB 降级启动 | 加重 |
| #3 三协议 integrity-breach 终态矩阵 | P2 | 缺口比登记更广：survival 协调器自己的 anthropic 终态也缺 message_delta/message_stop（只有 event: error） | 加重 |
| #9 color:check 盲区 | P2 | 实锤逃逸 23 个 .vue 的 script 段 ~115 处 hex（含 CredentialDetailDrawer.vue:1046 内联三元色、HealthGradeChart.vue 暗色浅字 #e6edf3）——同一规则对 .ts 全文扫描、对 .vue script 豁免，自相矛盾 | 有实害 |

R27 已修项回归复核（A/B/E 轮交叉验证）全部在位未回退：responsesToolHold、writeFinalEvents 降级、stop_reason 诚实化、writeHealth 硬配额簿记、间隔溢出钳制、BalanceQuotaProbe 守卫、lite 数据面鉴权兜底（d8af9ccb0）。分区存储不变量（promote 原子 CTE / 703 时区钉扎）复核维持 R27 C 轮结论。

## 二、本轮已修复（按逻辑单元）

### 单元 1：streaming 终态闭环（#2/#3/#10/#11）
- **chat 协议 breach 终态**：新增 `writeChatInterruptedTail`（末帧 finish_reason="length" + [DONE]），挂 stream.go 两出口 + anthropic_bridge.go 两出口；守卫 `gate.MayWriteTerminal() || attemptHasClientSemanticOutput`——uncommitted 保持 outcome-only，透明 failover 语义不变。
- **anthropic 协议 breach 终态**：新增 `writeAnthropicInterruptedTail`（复用 writeAnthropicTail→message_delta(stop_reason=max_tokens)+message_stop），挂 anthropic_stream.go 两出口。
- **gate 终态闩**：attempt_commit_gate 增 terminalRendered atomic.Bool；writeFinalEvents 落帧后打标；survival renderTerminal 见闩即跳过——committed breach 不再出现 completed(incomplete)+failed 双终态。l2_alignment_miss 作废 replay gate 闩未置位，行为保持。
- **pendingArgs 有界化**：anthropic 活桥 nameless hold 1MiB 上限（镜像 responses 桥），超限 Interrupted outcome（Reason=anthropic_stream_arg_accumulator_limit）。
- **非流式**：三串行器 nameless 全丢弃且 FinishReason=tool_calls 时降级（chat→stop / anthropic→end_turn / Responses→incomplete）；Responses incomplete 终态三路（桥/ir/原生）补齐 incomplete_details.reason。

### 单元 2：bg 探针与下限守卫（#4/#5/#12a，迁移 704）
- **plan 三维全堵逃生门**：releaseStaleProbeFloorCredentials——balance_floor 所有权行 plan_quota_checked_at 陈旧 >24h（env LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS，0=关闭，解析带溢出复检）幂等回池；重摘后 checked_at 变新鲜天然限频，fail-closed 摘出方向不变。
- **writeHealth 乐观并发闸**：失败写加 `state_updated_at < EvidenceAt` 条件（EvidenceAt=探测开始时间，decrypt 前取样）；成功写无条件；R27 硬配额簿记分支不受闸（0-rows 判别 SELECT 前置，竞态丢写与守卫 miss 日志拆分）。
- **失败退避**：迁移 704 增 credentials.plan_quota_probe_failed_at；失败戳+15min 冷却+COALESCE 沉底排序；成功清戳。**绝不写 plan_quota_checked_at**（否则废掉逃生门判据——两项必须配对理解）。

### 单元 3：supplier_errors 数据闭环（#6 升 P1/#7/#8+R27#11-3）
- **RLS 旁路 setter 全链接线**：写入（supplier_error_logger 事务级 set_config('app.bypass_rls','true',true)，is_local）、promote（partition_manager advisory lock 后一行，顺带修 candidate_failure_logs）、聚合（三 rollup 包单事务获得原子性）、admin 读端（errors_trend withTrendReadTx + vendor_credential_error_handlers withVendorRLSBypassReadTx + candidate_failure_handlers withRLSBypassTx，均只 *pgxpool.Pool 走事务路径、测试替身保持直连）。仅具体池类型断言以保护 pgxmock 契约测试。
- **聚合器读源**：rollup base 改 hot∪父表 UNION ALL（promote 原子 DELETE+INSERT，无重复计数；UPSERT 覆盖写幂等），maxWindow 8h→48h——停摆不再产生永久统计洞；两处 SQL 契约钉扎同步。
- **embeddings 分类分流**：直通收窄为 2xx；retryable/credential-fatal 族 failover；六终态 client-determined 族渲染网关形状错误（400/404/410），禁透传厂商 body/状态码（对齐 classify.go 契约"NEVER forwards one vendor's error text"）；Retry-After 按族归属（map[Kind]max）；失败候选接 CandidateFailureWriter 入账本（main.go 已接线，nil 安全）。

### 单元 4：dispatch/队列与评分收口（#13/#14/#15/#20/#22a/b）
- **routeFailover ctx 感知**：select 增 ctx.Done() 分支（零丢弃保守变体，failoverCh/stopCh 保留）；计数挂既有 metricOverflow（failover_ctx_done）。
- **retry 堆上限**：HeapRetryScheduler maxItems（DispatcherWorkers×64），满时 return false 走既有回退路径，调用方零改动。
- **scoring 假旋钮 display-only**（P1）：PATCH/GET 响应带 display_only+note；DefaultLoadScoreWeights 注释钉死热路径唯一权重语义；PATCH 解析改容错 map[string]any（前端 RoutingDashboard 回显整包不炸）。前端 RoutingPolicyView 加 el-alert 明示 + 8 locale。
- **排序所有权契约**（P1）：autocombo 评分=粗排（MaxCandidates 截断存活集）、Router=精排（tier 内 P2C）注释契约 + 截断保头钉桩测试。
- **死代码标注**：Bandit 接线块/字段/banditOrder、weighted_router、bandit.go、bandit_flusher.go 全部 UNUSED 标注（物理删除留待独立 PR 评审）。
- **weight 卫生**：手动权重 clamp ≤capacityWeightMax(1000)（provider/client.go，与容量派生分支同上限）；weightCounters 软上限 100k 整表换新（重置仅影响轮转相位）。

### 单元 5：lite 双存储快速收口（#16/#17/#19）
- **lite 门控 Redis**：initStorageMode 后 disableRedisEnv()（6 个 env 非空先 WARN 后 Unsetenv，覆盖 main.go 装配段与 rpm_redis 等 mode-blind 直读者）；main.go Redis 装配段 `storageRt == nil` 二次门控；start-lite.sh unset 六键双保险。
- **postgres_url 对齐**：AlignFullPostgresURL 双向对齐（Promotion/Backfill/Conflict WARN，DATABASE_URL 优先不变），full 模式补跑 Validate 仅 WARN 不 fail-start——YAML-only 部署不再静默降级。
- **catalog 四 store 显性化**：schema EXPERIMENTAL 注释；摘除 storage_mode_init 四个 Get*Store() interface{} getter（测试改走 factory 路径，roundtrip/级联覆盖保留）。

### 单元 6：installer 补登 + 文档 + web 徽标（#12e/#13 前端/文档漂移）
- **701 installer 五点补登**：embeddata 副本（cmp 字节一致）+ main.go go:embed + embeddedSQLFiles + runner.go StartupFiles 升序 + 契约测试 expected map——installer 离线初始化不再跳过 701。
- **lite 文档止血**：5 份 lite-mode 文档统一状态横幅（指向本报告）；LITE_MODE_README 与 index 的 ⭐5"生产就绪"降为"单机审计就绪（⭐3）"。
- **前端徽标**：见单元 4 #13。

## 三、登记遗留（R29 候选，均有证据未修）

| # | 级别 | 内容 | 来源 |
|---|------|------|------|
| 1 | P2 | color:check 扩扫 .vue template/script 段（~116 处实锤）+ 跨行 rgba 状态机 + 基线 reason 字段与 update 合并写（落地方案已设计，M） | F-#9 |
| 2 | P2 | SQLite 保留期清理 LiteRetentionWorker（meta 先删、单事务双删 turns+sessions、request_logs 按 RequestLogsDays 分批）+ trimmer/对账 Missing 告警根治（M） | E-#18 |
| 3 | P2 | SQLite PRAGMA user_version 迁移机制 + bodies 目录容量上限（S-M；容量逐出建议 #2 后上） | E-#22 |
| 4 | P2 | OmniFree HTTP 级 auto/* e2e（复用 auto_route_e2e 模板）+ HideTrainableModels/ExplorationRate 死配置收口（M） | D-#21 |
| 5 | P3 | Bandit/WeightedRouter 物理删除独立 PR（净删 ~2k 行，热路径文件 diff，需单独评审） | D-#20 |
| 6 | P3 | Tier-0 单 drainer：先上 per-model handoff 延迟指标实锤队头阻塞，再评估多 drainer（M-L） | D-#22c |
| 7 | P3 | ScanScheduler Prometheus 指标（对齐 bg/metrics.go promauto 模式，S-M） | F-#12a |
| 8 | P3 | quota floor 8 locale 文案范围限定 + 厂商不匹配警示 + saveSelected 预校验 + normalizeFloorInput 停止静默吞错（S） | F-#12b/c |
| 9 | P3 | 主题三档化：theme-init.js 停止固化系统偏好快照 + matchMedia/storage 监听 + ThemeToggle 订阅（M） | F-#12d |
| 10 | P3 | 非流式 nameless 丢弃 QualityFlags 旗标管道（finish_reason 同步已修，旗标回填 M） | A-#11 |
| 11 | P3 | installer canonical parity 门禁（编号≥690 的 canonical 迁移必须在 StartupFiles，防 701 式漏登复发，M） | F-#12e 根治项 |
| 12 | P3 | 余额下限溢出钳制无单测（NewBalanceFloorGuard 大数 env 用例）； BalanceQuotaProbe / balance_floor_guard 指标面缺口 | B 轮 |

## 四、闭环验证结论（对照审计维度）

1. **流程/数据/反馈闭环**：supplier_errors 写入→promote→聚合→凭据详情→web 展示全链在非 superuser 角色下恢复可用（本轮 #6 为 R27 C 轮"全链闭环"结论的必要修正——彼时结论在本地 superuser 连接下取得，本轮已在报告如实降级并修复）；embeddings 错误入账本补齐最后一块数据面。
2. **IR 与协议矩阵**：三协议×流式/非流式 integrity-breach 终态矩阵本轮补齐 chat/anthropic 两列（R27 仅 Responses），双终态矛盾以 gate 闩消除；非流式 finish_reason/incomplete_details 对齐。
3. **存储与序列化**：活桥 accumulator 全部有界（pendingArgs 1MiB 收口）；无界 retry 堆加上限。
4. **队列/并发/安全**：failover 投递 ctx 感知；探针写回乐观并发闸；plan 卡死三维逃生门；RLS 提权全部 is_local 事务级、连接归还不保留。
5. **双存储**：lite Redis 半开收口、postgres_url 假字段对齐、P0-1 鉴权兜底复核在位；#18/#19 清理与接线登记 R29。
6. **hosted-task delegation 设计提案**（ace419dcc）复核：提案引用的 goal_runs/outbox/pending 等既有设施盘点与代码一致；未实施，无审计动作。
7. **门禁/可观测性**：pre-commit 六门禁全绿（go vet/SET LOCAL/迁移编号/迁移 down/vue-tsc/token 合规）；全量 go test 275 包 ok 零失败；installer module 测试全绿；web vue-tsc + i18n parity 通过。

## 五、验证记录

- 全量 `go test ./... -count=1`：275 包 ok，0 失败。
- admin/provider/cmd/gateway/bg/domains/streaming/domains/dispatch/internal/ir/config/storage/sql migrations：全部 -count=1 实跑通过。
- installer：`cd installer && go test ./...` 全绿（TestStartupFilesAreAllEmbedded / TestStatsStartupMigrationsMatchCanonicalSources 含 701）。
- web：`npx vue-tsc --noEmit` 无输出；`npm run i18n:check` 无缺失 key。
- 每逻辑单元独立提交，精确路径 staging（防并行会话扫走）。

## 六、下一步

R29 以 §三 12 项遗留为准；建议先做 #1（color:check 扩扫）与 #2（SQLite 保留期清理）两项 P2，其余按表推进。hosted-task delegation 设计提案如需启动实施，另立设计评审轮。
