# R27 · 24h 修正审计轮（五子系统并行 + 修复落地）

- 日期：2026-09-14
- 审计范围：dafcbc873..41b529de5（24h 内 45+ 提交，含一次 origin/main 同步合并 af7a41248 与并行会话部署修复 490e8e989）
- 方法：包级代码图谱（go list 依赖投影，~119 domains 子包）→ 主代理 + 6 个只读子代理并行审计（bg 探针调度 / streaming+IR 协议转换 / 供应商错误链+分区存储 / admin+凭据写入 / web 前端 / installer+SQL 同步）→ 汇总分级 → 按逻辑单元修复、测试、提交、推送。
- 修复落点：89ec7f8f8（streaming）、381504e4e（web）、9ad8bf28a（admin）、5ab158f76（bg）、41b529de5（errorsx/sql）。

## 一、合并轮（会话前半）

- 起点为进行中的 merge（MERGE_HEAD=8e127fa24，89 文件已解决，剩 1 个 UU handoff 文档）。
- handoff 文档冲突取远端超集版本（含 870fac658 惰性 plan 下限补记）。
- 合并门禁 6 项全绿后提交 af7a41248 推送；期间发现 pre-commit token 合规真实拦截了
  AnnotationStatsView 两处 var(--token,#hex) 兜底（修复后入合并提交）。
- 并行会话同期落 367601764（merge）与 490e8e989（703 clobber 链补登 + readyz 诊断），
  后者恰好是子代理 F 独立发现的 P0（详见 §三 F-P0-1），已随之推送。

## 二、已修复（按严重度）

### P0（部署阻断，F 轮发现）
- **703 未登记 clobber chain → 升级通道预检必然 exit 5**：所有部署卡 pre-flight。
  并行会话 490e8e989 已补 6 行注册；本轮补齐根因——shell 契约测试此前仅 `bash -n` +
  grep 抽查，现提取部署脚本真实守卫段 eval 执行（阴性测试复现 703 事故 exit 5），
  并新增目录驱动不变量：最大编号 startup 迁移必须在 files=() 中（防 704 漏登复发）。

### P1
- **writeHealth 硬配额守卫吞失败簿记（A-P1-1）**：硬配额行（permanently/balance_exhausted）
  探测失败时主 UPDATE 被 WHERE 拒成 0 行，last_probe_at/失败计数不前进 → BalanceQuotaProbe
  指数到期闸失效，2min tick 每 tick 重打死上游（720 次/天/凭据，f8322dc04 R4 对该类行不成立）。
  修法：0-rows 分支补簿记 UPDATE（仅梯子+可用性列，quota_state/lifecycle/auto_* 不动），
  源码钉桩测试锁定。
- **Responses 桥 arg-first 悬空 delta（B-P1-1）**：name 未到达的 argument 片段被序列化为
  function_call_arguments.delta，引用永不开项的 item_id，且终态守卫再剔除 → 客户端工具调用
  静默丢失。修法：responsesToolHold（平衡缓冲 + name 到达重放，1MiB 有界），两桥接入，
  anthropic_stream.go pendingArgs hold 同型。
- **color:check 兜底盲区（E-P1-1）**：扫描器把 var(--token,#hex) 兜底剥除后再扫，被禁模式
  对门禁不可见；42 处 hex 兜底 + 12 处 rgba 兜底存量全部漏报。修法：stripVarFallbacks 纯函数
  （平衡括号，支持嵌套 var/十进制三元组）提取兜底并上报 hex/rgba 字面量；54 处存量剥为裸 var
  （行为保持）；color:check 接入 verify.sh --web（此前无自动执行载体）。

### P2
- force_enable/reset-state 部分失败审计缺口（D-P2-1）：tx.Commit 后 URSM 失败 → DB 已落地
  却 500 无审计。db_committed 分界标记 + 错误分支补写 audit_outcome=partial_failed。
- 假成功空响应（B-P2-3）：writeFinalEvents 全部工具项被空名守卫丢弃且无文本 → 终态降级
  incomplete（桥路径经空流门走 failover，scaffold 级守卫为纵深防御）。
- stop_reason 语义矛盾（B-P2-4）：仅剩被丢弃 nameless 调用时不再报 tool_calls/tool_use。
- BalanceQuotaProbe 无重入守卫 + 两探针 tick 无 panic guard（A-P2-2/A-P2-3）：lifecycleMu +
  顶层/per-tick recover。
- 间隔解析溢出（A-P3-5）：balance_floor_guard/periodic_quota_probe 分钟数路径大数溢出为负
  duration → NewTicker panic → guard 静默死亡；加 parsed>0 复检。
- scan_scheduler 每轮无预算/首轮失败 6h 盲区（D-P2-2）：60s cycleTimeout + 30s/60s 有界重试；
  Stop 先于 Start 不再触发首轮扫描（A-P3-1）；Status 增 started 字段（A-P3-2/D-P3-7 日志去重）。
- model_probe recoveringSweeper per-tick recover（A-P3-3，该循环是 recovering 行唯一驱动）。
- supplier_errors months CTE 补 ORDER BY 1（C-F-2，六副本同步 + 契约测试）。
- scan-scheduler status 收敛 superAdmin（D-P3-3，last_error 跨租户信息面）。

### P3
- 701 补 schema_migrations 双账本自登记（F-P3-1）；escapeTenantID 死代码删除（D-P3-6）；
  errors.Is(pgx.ErrNoRows) 替代字符串匹配（A-P3-4）；planTypes 六死键 8 locale 清理 +
  floorPercent 八语文案语义修正（E-P3-7/E-P3-9）；HTTPStatusForKind godoc 归位与表述修正
  （C-F-1/C-F-4）；pre-commit token 检查豁免 *.test.ts 断言夹具。

## 三、登记遗留（下一轮候选，均有证据未修）

| # | 级别 | 内容 | 证据 |
|---|------|------|------|
| 1 | P2 | transformation/anthropic 第三条死桥未退役（commit 97aa179ab 声明与事实不符）：anthropic_stream.go(StreamOpenAIToAnthropicSSE 7 参版)+split test+pendingCapturer 副本，缺 nameless lazy-open 修复 | domains/transformation/anthropic/anthropic_stream.go:30 |
| 2 | P2 | survival 模式 committed breach 双终态：response.completed(incomplete) 后再 response.failed | responses_bridge.go:801-806 + survival_wiring.go:257-260 |
| 3 | P2 | 三协议 integrity-breach 终态矩阵不对称（仅 Responses 格补齐；chat/anthropic 桥仍硬截断无终态） | anthropic_bridge.go:1034、stream.go:978、anthropic_stream.go:802 |
| 4 | P2 | plan 厂商"币下限已清+plan 下限在+plan 探测持续失败"三维全堵永久卡死；建议 plan_quota_checked_at 落后 N 小时逃生门 | balance_floor_guard.go:557-564,874-879,695 |
| 5 | P2 | probe writeHealth 与 writer RestoreOnSuccess last-write-wins，可静默回退 P2 closeout 效果一次；建议 state_updated_at 条件写 | credential_probe_v2.go:1152-1266 vs writer.go:73-105 |
| 6 | P2 | supplier_errors FORCE RLS 依赖 SUPERUSER 角色兜底；NOBYPASSRLS 部署下写入/promote 静默失效，建议 set_config('app.bypass_rls') | V371:90-97 vs supplier_error_logger.go:44-101 |
| 7 | P2 | supplier_error_stats 聚合器只读 hot 表，聚合器停摆 >8h 产生永久统计洞 | supplier_error_stats_aggregator.go:59 |
| 8 | P2 | embeddings 400/402/408/422 原样转发单供应商错误体且不 failover，与 classify.go 契约声明相悖 | embeddings.go:196-227 |
| 9 | P2 | color:check 不覆盖 .vue template/script 段与跨行 rgba；基线 44 条无 reason 字段且 update 会抹手工字段 | color-token-audit.mjs:94 |
| 10 | P2 | pendingArgs 无上限累积（anthropic_stream.go:716），违反本仓有界 accumulator 纪律 | streaming |
| 11 | P3 | 非流式 nameless 丢弃无旗标/finish_reason 不同步；两侧 incomplete 终态形状不一致（缺 incomplete_details.reason）；embeddings Retry-After 与终态家族可错配 | response.go:540 等 |
| 12 | P3 | sweepPlanQuotas 失败凭据不 bump checked_at 可饿死健康凭据；ScanScheduler 无 Prometheus 指标；quota floor 对无探测能力供应商静默空转（UI 文案过度承诺）；floor 输入无客户端预校验；主题缺系统偏好变更/跨标签页同步路径；701 installer 侧无 StartupFiles 条目（fresh install 不盖账） | 各子代理报告 |

## 四、闭环验证结论（对照审计维度）

1. **流程/数据闭环**：probe v2 摘出/回池对称、credential_recovery 各恢复块链路完整、
   balance_floor_guard pull/restore 带 ownership 校验（A 轮确认）；supplier_errors
   写入→promote→分区→凭据详情→web 展示全链闭环（C 轮确认）；本轮修复补齐硬配额行
   簿记断点。
2. **IR 定义与传输转换**：Response IR 覆盖 content/tool_calls/reasoning/usage/unknown-blocks；
   三协议×流式/非流式矩阵本轮补齐 Responses 桥 arg-first 格与退化终态，其余格登记 §三.3。
3. **创建/赋值/解析/存储/序列化**：桥侧有界 accumulator 纪律维持；pendingArgs 无界为唯一
   例外（§三.10）。
4. **多轮与轮次摘要**：requestjourney/sessionmeta/sessionaudit 链路本轮零改动；
   tool_name_never_arrived 旗标经 audit.go→reqLog.QualityFlags 落 request_logs 验证通畅。
5. **队列/并发/安全**：bg 三 worker（balance/periodic/floor-guard）panic 守卫补齐；
   scan_scheduler 重入/预算/退避闭环；无锁内阻塞调用（A 轮全量核对）。
6. **分区存储**：hot 8h + columnar promote 原子 CTE（FOR UPDATE SKIP LOCKED→DELETE
   RETURNING→INSERT）幂等不丢不重；703 时区钉扎必要性成立；promote_supplier_errors
   与 ensure 分区边界时区一致（C 轮确认）。
7. **供应商错误处理**：错误表（supplier_errors 双账本）+ 凭据详情呈现 + 流式
   FailoverNotices 不中断通道均闭环；无备用时的错误返回按协议族映射（chat 429/503、
   anthropic 503 overloaded、embeddings 四族）。
8. **双存储架构/auto 模型**：本轮范围无改动，未审计（登记为后续轮次范围）。
9. **门禁/可观测性**：六门禁本轮全部真实执行过（含两次真实拦截）；color:check 补入
   verify.sh 后七门禁有自动载体。

## 五、验证记录

- 每修复单元独立提交，pre-commit 六门禁逐次全绿（多次真实拦截/放行记录见提交历史）。
- 全量 `go test ./...`：270 包 ok，0 失败（41b529de5）。
- web：vue-tsc、vitest（color-audit 11 用例、i18n parity 6 用例）、color:check strict PASS。
- installer 独立 module go build/test 通过；deploy_readiness_contract_test 11 项 PASS。
- 推送：490e8e989..41b529de5 → origin/main。

## 六、补派审计轮（同日晚：autocombo / 双存储 / 多层队列，验证器缺口收口）

补派 3 个只读子代理（G/H/I）覆盖 §四.8 明示未审的两个维度与多层队列独立结论，
并以 §三 #1 死桥退役收口代码结构维度。

### 6.1 auto 模型全量实现（G 轮，domains/autocombo + handler_autocombo + autoroute）

仓库内 "auto" 实为两条平行链路：精确 `model="auto"` → autoroute.Decider；
`model="auto/*"` → autocombo/OmniFree 虚拟路由。

- **P0-1（已修复，3d6620a0f）**：`CanonicalizeClientModel` 的通用 last-'/' 厂商前缀
  剥离把 `auto/free` 折叠为 `free`，`shouldTryOmniFree`（要求 `auto/` 前缀）在线上
  恒 false——OmniFree 全域（Resolver 14 内置模板/VirtualFactory/Engine/配额预取/
  429 校准/Prometheus 指标族）代码完备但 wire 上不可达；唯一直测前缀的单测绕过了
  入口规范化，HTTP 级 e2e 缺失是漏网根因。修复：`auto/` 保留命名空间豁免 + 3 组
  钉桩用例；OmniFree 未接线时 nil 守卫保持原 400 行为，严格可加。
- P1-1：autocombo 六维评分排序被 executor Router（Bandit/P2C）重排覆盖，评分实际
  只影响 top-50 截断——排序所有权冲突。
- P2：combo 语义未实现（实为单选+failover，无流式拼接/usage 聚合）；`HideTrainableModels`
  无源死配置；`ExplorationRate` 死字段；D5 模型级 failover 只护 autoroute 非流式。
- 闭环结论：autoroute 链路基本闭环；autocombo 链路「代码完备、链路断头」——P0-1
  修复后需补 HTTP 级 `auto/*` e2e 与排序所有权定性（登记 §七）。

### 6.2 双存储架构（H 轮，full: pg+redis+memory+files vs lite: sqlite+memory+files）

事实链：`LLM_GATEWAY_STORAGE_MODE`（config/storage.go）→ storage_mode_init.go →
storage/factory → lite_telemetry_sink.go 唯一生产写接缝。

- **P0-1（已修复，d8af9ccb0）**：lite 模式（及 full 的 no-DB 降级态）数据面鉴权
  完全旁路——middleware sk-* 透传直达无鉴权 handler，任意 Bearer sk-* 可消耗上游
  凭据。修复：verifier 禁用且部署静态密钥时 handler 侧兜底闸（consttime 精确匹配）；
  无静态密钥时 ERROR 告警明示暴露面与收口开关（不 fail-start，no-DB 降级为生产
  依赖路径）。
- P1：lite 不门控 Redis（半开形态）；full 模式 `full_storage.postgres_url` 校验
  假字段（实际连接走 DATABASE_URL）；SQLite sessions/turns/logs 无保留期清理
  （bodies trimmer 只删文件不删 meta，制造永久 Missing 告警）；providers/credentials/
  models/bindings 四家 SQLite store 零生产接线（schema 占位）。
- P2/P3：SQLite 无版本化迁移机制（PRAGMA user_version 建议）；多个部署脚本
  CGO_ENABLED=0 与 lite 硬 CGO 冲突（fail-fast 有防呆）；MemoryStateStore 零生产
  消费者（"memory 替代 Redis" 文案与实现不符）；bodies 目录无容量上限；文档多处
  漂移（"生产就绪⭐5" 宣称与自留未完成项矛盾）。
- 闭环结论：**部分闭环**。lite 单机审计主干（开关→工厂→SQLite 三 store→files→
  trimmer→对账→telemetry 接缝）真实可运行且质量好；full 侧在工厂内为诚实标注的桩
  （既有 pgx/redis 装配不走工厂）。memory 层未闭环；P0-1/P1 各项修复前 lite 只应
  定位"可信网络内单机审计部署"。

### 6.3 多层队列与权重负载均衡（I 轮，dispatch pipeline + executors router + limiter）

生产主链路：入口 RPM 门 → dispatch.Pipeline（Tier-0 总队列 → Tier-1 模型队列 →
Tier-2 凭据队列+Governor → ForwardFunc）→ Router.PlanCandidates（候选过滤→
billing round→tier→P2C/加权首轮→优先级桶）。

- P1-1：admin `scoring_weights_update` 是展示-only 假旋钮——`CalculateCompositeScore`
  仅被诊断/预览端点调用，真实选路用 env 可调的 `DefaultLoadScoreWeights()`。
- P2：routeFailover 阻塞投递 failoverCh（容量 FailoverWorkers×4）不受请求 ctx 约束，
  上游大面积故障时凭据级头阻塞；retry/due 堆无显式容量上限（有界性依赖客户端 ctx）；
  权重负载均衡三套并行实现（Bandit 接线被注释、domains/routing WeightedRouter 仅
  测试引用，均为生产死代码）；手动 `model_offers.weight` 无上限钳制（可垄断首轮）。
- P3：weightCounters sync.Map 无淘汰（慢性缓涨）；Tier-0 单 drainer 跨模型队头阻塞；
  候选硬截断 12 大池尾部失真；concurrencyGovernor 2ms 轮询；RPM 旁路后溢出退避
  仅 1s。
- 闭环结论：入口并发/出口限流/调度瀑布**闭环**（分钟桶 RPM+预算队列、Governor+
  Limiter 四层+熔断三段恢复、429 三层钳制、ladder maxAttempts=100 硬顶、shutdown
  全链无孤儿、惊群三重抑制——工程完成度显著高于平均）；多层队列解耦与权重负载均衡
  **部分闭环**（P1-1 假旋钮与 P2-2 头阻塞为优先修复项；评分逻辑散布 4 处为最大改进面）。

### 6.4 死桥退役（§三 #1 收口）

`domains/transformation/anthropic/anthropic_stream.go`（StreamOpenAIToAnthropicSSE
7 参无 gate 版）+ writeAnthropicTail/writeSSEWithCapturer/writeSSE/flushBufferedText
及桥专属 pendingCapturer/StreamWriter/runtime-config/keepalive 集群整体退役；
stream_support.go 收缩至唯一活符号 IsAnthropicStreamEmpty，包文档钉住 4 符号活面
（选择器扫描穷尽验证：外部导入方仅 streaming 两文件）。提交见 git log（refactor
transformation）。

## 七、登记遗留增补（R28 候选，均有证据未修）

| # | 级别 | 内容 | 来源 |
|---|------|------|------|
| 13 | P1 | admin scoring_weights 假旋钮：注入路由热路径或文档明示 display-only | I-1-1 |
| 14 | P1 | autocombo 评分与 Router 重排所有权冲突（评分仅影响 top-50 截断） | G-1-1 |
| 15 | P2 | routeFailover 阻塞投递 failoverCh 头阻塞；retry 堆无上限 | I-2-2/2-4 |
| 16 | P2 | lite 不门控 Redis 半开形态 + start-lite.sh 未 unset REDIS_ADDR | H-1-2 |
| 17 | P2 | full_storage.postgres_url 校验假字段 | H-1-3 |
| 18 | P2 | SQLite 无保留期清理 + trimmer/对账互相制造永久 Missing | H-1-4 |
| 19 | P2 | SQLite catalog 四 store 零接线（接线或摘除死表） | H-1-5 |
| 20 | P2 | Bandit/WeightedRouter 生产死代码（删除或标 UNUSED） | I-2-3 |
| 21 | P2 | OmniFree 补 HTTP 级 auto/* e2e + HideTrainableModels/ExplorationRate 死配置收口 | G-P3-5 等 |
| 22 | P3 | 手动 weight 上限钳制、weightCounters 淘汰、Tier-0 drainer 公平性、SQLite user_version 迁移、bodies 容量上限、文档漂移修订 | 各轮 |
