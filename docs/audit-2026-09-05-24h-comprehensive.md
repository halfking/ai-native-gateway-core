# 2026-09-05 24小时修正审计综合报告（7轴并行）

- 审计对象：`3970a7de7..f27e412b9`（2026-09-05 全部合入 main 的修正，非 vendor 197 文件 / +35270 -402）
- 审计模式：codegraph 代码图谱 + 主代理编排 + 7 个并行子代理（轴 A-G）
- 分轴详报：`.audit-workspace/2026-09-05/axis-{A..G}-*.md`（证据均在分轴报告中）
- 修复执行：本轮全部 P0/P1 + 高价值 P2 已修复，见 §三

## 一、分轴发现概览

| 轴 | 范围 | P0 | P1 | P2 | P3 |
|----|------|----|----|----|----|
| A IR 定义与多协议转换 | internal/ir + session/v2 适配 | 1 | 3 | 5 | 3 |
| B 双模式存储闭环 | storage/* + cache_v2 + trimmers | 0 | 2 | 4 | 6 |
| C 队列/并发/限流/负载均衡 | domains/dispatch + executor | 0 | 1 | 3 | 7 |
| D hot+分区生命周期 | 655/656 + settle/promote/ensure | 0 | 1 | 4 | 7 |
| E 供应商错误处理闭环 | attempt_outcome + 聚合器 + URSM | 0 | 0 | 4 | 7 |
| F 会话快照/前端闭环 | session_turns_v2 + 抽屉 + locale | 0 | 1 | 4 | 7 |
| G 后台任务/日志监控 | 新 worker + logger/monitor | 0 | 0 | 2 | 5 |

已复核确认（不重复报告）：dual-storage A1-A10（完成报告）；logger/monitor round1-3 全部修复无回归；
dispatch 域 accessor 迁移 100% 完成（生产代码字段直读为 0，仅测试 fixture 保留）。

## 二、需求 → 实现现状（对照审计任务书）

| 需求 | 现状 | 判定 |
|------|------|------|
| 流程/数据/反馈三闭环 | 请求生命周期、会话存储、错误处理、队列调度、限流、错误聚合、摘要七条主链路完整；本轮修复补上取消竞态、附件打开、凭据聚合器三处断点 | ✅（修复后） |
| IR 定义清晰度 | 协议字段显式 + Extensions 前向兼容设计良好；**多轮轮次编号/项目/任务/总轮次/多 tag/凭据/路由来源无显式 IR 承载**（凭据/瀑布按分层归属 dispatch 层，会话业务元数据建议后续以 `json:"-"` 扩展 Metadata） | 🟡 部分覆盖 |
| 上游多厂商标准格式 / 下游客户端匹配 | OpenAI/Anthropic/Gemini/Responses 四协议解析+序列化，fuzz smoke 5s×2 通过；本轮修复 Anthropic Files API file_id 全链路丢失（P0）、tool_result 多模态清空、Gemini generationConfig 罚项丢失 | ✅（修复后） |
| 流式/非流式 | StreamChunk IR 统一表示；`thinking:` SSE 注释即"think 模式"非中断反馈（dispatch notices/retry/node_switch 三路接入）；非流式无等价机制（结构性限制，已记录） | 🟡 |
| IR 创建/赋值/解析/存储/序列化全链路 | L1 内存→L1.5 文件→L2 Redis→L3 PG 四层回填/失效/打点完整；本轮修复 FileCache 记账自愈负漂移；lite 工厂 store 零生产调用方（持久化未闭环，已记录为边界） | 🟡 |
| 每轮会话摘要（去格式供人查看） | digest 生成端（格式去除/tool_result 排除/rune 安全 260 截断/版本化 envelope）扎实；前端 TurnDigestCard 消费；同轮双轨摘要（前端派生 vs 后端持久化）未统一（P3 已记录） | ✅ |
| 多层队列/并发/限流/权重负载均衡 | Tier-0/1/2 三层 + Governor 三维限流 + 加权抽签；本轮修复取消路径 single-owner 破坏（P1）；shutdown 漏排空/worker 无 recover（部分本轮修复）/complete() 同步 journal sink 为已知 P2 | 🟡 |
| 安全场景 | 新代码 SQL 全参数化、路径遍历防护、密钥不落日志、快照 0600；本轮补 4 个新 goroutine panic guard、keystore_sync 锁内 DB 迭代 | ✅（修复后） |
| 可观测性/展示统一性/菜单 | menu-config 无漂移、vue-tsc 干净、复用既有控件模式良好；本轮修复附件打开按钮与错误态 retry 文案；z-index 三套体系无共享 token（P3 已记录） | ✅ |
| hot+分区（columnar）大数据表 | 写 hot→8h promote→月分区→统一视图读，promote 原子性/advisory lock/DEFAULT 分区 rescue 到位；本轮修复 settle worker 直写父表违反"只在 hot 更新"不变式（P1）；auto_route_selections 分区目前为 heap 非 citus columnar（口径差异已记录） | ✅（修复后） |
| 供应商错误记录+凭据详情呈现 | candidate_failure_logs_hot → 聚合 → provider_error_details → `/error-detail`+`/provider-error-stats` → ErrorDetailTab 链路完整；本轮修复聚合器 Scan 列数失配（曾致聚合器全盲）；error_message 参与聚合键碎片化为 P2 | ✅（修复后） |

## 三、本轮修复清单（全部含验证）

| # | 级别 | 修复 | 文件 |
|---|------|------|------|
| 1 | **P0** | Anthropic Files API `source:{type:"file",file_id}` 图片/文档解析+序列化全链路接线（此前 parse 丢弃 file_id、serialize 输出残缺 source 必被上游 400）；DocumentSource 增 FileID 字段 | internal/ir/parse_anthropic.go、serialize_anthropic.go、types.go + anthropic_file_source_test.go |
| 2 | P1 | tool_result 内容序列化保留多模态块（此前只保留 text，image/document 静默清空）；单一 text 块保持字符串简写契约 | internal/ir/serialize_anthropic.go + 回归测试 |
| 3 | P1 | Gemini generationConfig 补齐 presencePenalty/frequencyPenalty 解析+序列化；未知子字段不再静默（ReportUnknownField 上报） | internal/ir/parse_gemini.go、serialize_gemini.go + gemini_generation_config_roundtrip_test.go |
| 4 | P1 | lite 模式 /healthz ready 恒 false、/readyz 恒 503：HealthHandler 增 depsOptional 语义（PG 旁路=配置事实而非初始化失败；Redis 已配置才参与判定），full 模式行为零变化 | domains/streaming/handler.go、cmd/gateway/main.go + handler_lite_readiness_test.go |
| 5 | P1 | pipeline 取消路径打破 single-owner：move()/routeFailover() 入口 completed/abandoned 短路，onRetryDue 补 abandoned 检查——客户端取消与 pre-firstbyte failover 并发时不再竞写 journal/counts | domains/dispatch/failover.go、pipeline.go |
| 6 | P1 | settle worker 直写分区父表违反"更新/删除只在 hot"：settleAbandonAfter 24h→4h（压进 8h hot 窗口）、pending 查询移除父表分支、writeReward/abandon 固定写 hot，过时注释（~7d retention）一并修正 | bg/auto_route_settle_worker.go |
| 7 | P2 | 供应商错误聚合器 Scan 列数失配（SELECT 5 列 Scan 4 dest→每行报错→groups/total 恒 0、水位自检死代码）：补 endpoint_unknown dest + unknownEndpointGroups 打点 | bg/provider_error_aggregator.go |
| 8 | P2 | FileCache 记账自愈负漂移：ensureSpaceLocked 返回 healed，自愈后 Set 按新增口径累加而非差额（防超卖容量） | domains/session/v2/cache_v2_file.go |
| 9 | P2 | 4 个新常驻 goroutine（settle/affinity/keystore sync/selection writer）补 panic guard：防进程崩溃 + done/wg 永久悬挂 | bg/auto_route_settle_worker.go、auto_route_affinity_worker.go、domains/authentication/keystore_sync.go、domains/hooks/observability/telemetry/selection_writer.go |
| 10 | P2 | keystore_sync delta 的 invalid-set 查询不再持 storeMu 写锁迭代 DB 行（先收集后换锁，对齐 keyStoreFullLoad 范式），消除 DB 变慢时全进程认证串行卡顿 | domains/authentication/keystore_sync.go |
| 11 | P2 | ensureSessionSummariesCanonical 前置 to_regclass 守卫：表被 DROP 时告警跳过而非启动崩溃 | db/session_summaries_schema.go |
| 12 | P1(FE) | V2 轮次附件「可见不可开」：TurnDigestDrawer 改走既有 `GET /api/attachments/{object}` 通道（404 签名端点弃用注记）；错误态 retry 按钮文案修复（8 语言 locale 补 retry 键） | web/src/components/session/TurnDigestDrawer.vue、web/src/api/sessions_v2.ts、locales/*/turnDigest.ts + 组件测试更新 |

## 四、已记录未修（下轮工作包候选，按优先级）

### P1/P2（建议下轮优先）

1. **B2** lite 工厂五个 store（Session/Turns/RequestLog/Bodies/State）零生产调用方——SQLite 建库后无读写、body 无持久化、`/metrics/storage` writes 恒 0。属 dual-storage 完成报告「lite 请求路径未整体切换」边界的补充证据，需要独立工作包做装配接线。
2. **B4** cache_v2 Invalidate 窗口内 L1.5 命中回填 L1 重播种（脏数据最长 30min）：失效顺序 L1.5→L1 或 Get 回填前二次检查。
3. **B5** AsyncFileWriter 同路径并发写共用固定 `path+".tmp"`（当前无调用方，接线前必须改 CreateTemp）。
4. **C-#2** dispatcher/failover worker shutdown 漏排空：缓冲队列残留请求永不 complete（survival 流 ctx 2h）。
5. **C-#4** complete() 同步执行 JournalSink（a.mu 进程级串行 + 无超时 DB 调用），慢 DB 全局放大。
6. **D-#2** settle outcome join 只读 request_logs_hot：若未来 settle 窗口再放宽需同步评估 promote 窗口（当前 4h<8h 已闭合）。
7. **D-#3** 656 无网关侧 ensure（655 有）：只升二进制的存量库缺 hot 表 → selection 写入静默丢弃。
8. **D-#4** `sql/tests/auto_route_selections_hot_tests.sql` 未接入任何自动执行。
9. **E-#2** request_logs 错误路径未脱敏（preview[:320] 无 SanitizeErrorText，供应商 401/403 回显 key 时泄漏）。
10. **E-#3** XML 工具调用片段 >64KB 溢出静默丢弃（tool_call_xml.go:145-149 记录代码被注释）。
11. **E-#4** error_message LEFT(200) 参与聚合键 → provider_error_details 碎片化。
12. **F-#2/#3/#4** 请求详情抽屉对 useRequestDetailLoader 守卫模式是部分移植：omitBody/ensureBodies 并发覆盖、onSelectTurn/openAsRequest 不 bump seq、bodiesLoading 粘滞。建议整体迁移共享组合式函数。
13. **G-#5** handoff.notify_webhook 租户设置级 SSRF（无 scheme/host 白名单）+ 失败日志带完整 URL。
14. **A-#5** 流式 signature_delta 只解析不序列化（启用 response_format_adapter 通用路径前必须修）。
15. **A-#6** OpenAI max_completion_tokens 折叠进 MaxTokens 并以 max_tokens 回写（o 系列语义漂移）。

### P3（择机）

- A-#8 网关侧业务元数据（项目/任务/轮次/多 tag/凭据归属）在 IR 层无归宿——建议 Metadata 以 `json:"-"` 扩展或文档明确分层归属。
- C-#6 加权抽签 totalWeight int 溢出；C-#9 PriorityCluster 未接线死代码；C-#7/#8/#11 忙等/轮询/timer 堆积。
- D-#7 promote_request_logs 默认 7d 与 8h 口径漂移；D-#9/#10 迁移簿记；D-#12 auto_route_selections 月分区无 TTL 出口。
- E-#8 outage grace 窗口内失败无"降级路由"归因标记；E-#10 UTF-8 按 byte 截断劈字符。
- F-#5 附件版本管理（引用计数/注册表）缺失；F-#6 双轨摘要统一；F-#8 err.Error() 外泄 SQLSTATE；F-#12 z-index 无共享 token。
- G-#4 selection writer drain 最坏 ~7min 越过 systemd 停止预算；G-#3 worker cancel 无同步保护。

## 五、验证记录（2026-09-05）

- `go build ./...` 通过；`go vet`（涉及包）干净；改动文件 gofmt 干净（failover.go/types.go 的格式漂移为 HEAD 预存，未扩大处理面）。
- `-race`：internal/ir、domains/dispatch、domains/session/v2、db、domains/authentication、telemetry、pkg/logger、pkg/monitor 全绿；bg、domains/streaming 定向测试通过。
- 新增回归测试 5 组：Anthropic file_source roundtrip、tool_result 文本简写契约、Gemini 罚项 roundtrip、lite readiness×4、（组件）TurnDigestDrawer 9 用例。
- 前端：`vue-tsc --noEmit` 改动文件零错误；vitest TurnDigestDrawer 通过。

## 六、方法论

codegraph build → 7 子代理并行分轴审计（只读，报告落 .audit-workspace/2026-09-05/）→ 主代理汇总定级 → 统一修复 → 验证 → 提交。分轴报告含全部 file:line 证据，本报告只保留结论与索引。
