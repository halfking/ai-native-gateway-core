# D11+D13 合并域子代理报告（窗口：48h = 643735a28^..HEAD；重点未审计增量 = b75c91900..HEAD）

审计人：D11+D13 合并域只读子代理。基线：conventions.md、D11-auto-model.md、D13-free-token-pool.md、docs/planning/TASKPROFILE_MODULE_DESIGN.md（意图基线）已全文亲读。重点增量涉及 taskprofile 全新包（7106e1c5b + a8d3a5bbe）、routingopt CorrectionSource、shadow_actors R42 注释、checker/model_policies R41 产物复核。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 触发路径 | 建议处置 |
|---|---|---|---|---|---|
| 1 | **P1** | FeedbackRecorder 回冲装配断裂：recorder 挂在 optimizer-owned store 上，但该 store 只有读路径（CorrectionStats）被调用；admin 主写路径的 store 实例 recorder=nil，设计 §5.3/§一#2②"Prometheus 分类反馈计数器由此真正开始计数"未达成。全仓 `SetRecorder` 仅 routing_optimizer_init.go:77 一处（grep 证实）。e2e 测试注释自认生产断裂（"admin handlers carry none"），测试用共享实例绕过 | admin/handler.go:1359（`taskprofile.NewHandlers(h.db)` 无 SetRecorder）；cmd/gateway/routing_optimizer_init.go:73-77；autoroute/classification_feedback.go:100-118（RecordFeedback 才触发包级 recordClassificationFeedback）；taskprofile/e2e_loop_integration_test.go:214-217 | 运维经 POST /api/admin/task-profile/corrections 或 /corrections/import 写入修正 → admin 侧 store.recorder=nil → RecordFeedback 永不执行 → ClassificationFeedbackAggregator 零事件、Prometheus 分类反馈计数恒零（闭环空壳，与 R35 emitTuningSignal 全路径死同型） | 在 admin/handler.go 注册处对同一 pool 的 store SetRecorder（或抽共享单例 store），仿 6dbf55973 补守卫测试锁两处接线 |
| 2 | **P2** | registry 词表缺分类器 V2 枚举 6 类：AllTaskTypes 11 类中 agent/long_context/vision/function_call/code_audit/intent_classification 不在档案（V3 十类+V2 五类）。这 6 类永远无法被 human "agree"（human_task_type 校验拒绝），一旦出现修正即 100% 计 corrected → humanRate 系统性偏低 → blend 阻尼偏强、升层建议偏多 | autoroute/classifier.go:87-91；autoroute/task_types_ext.go:9-16（code_audit ≠ V3 的 audit，字符串也不同）；taskprofile/registry.go:33-53；taskprofile/handler.go:124；taskprofile/csv.go:188 | V2 分类器（252 生产主导，registry 注释自证）输出 auto=agent 等类型 → 标注工作台提交 human=agent → 400 "unknown human_task_type"；或标注者被迫选近似类型导致 agrees 失真 | registry 增补 6 类默认档案（或紧急用 TASKPROFILE_OVERLAY 补）；校验放宽为 auto∪registry 亦可 |
| 3 | **P2** | ApplySuggestions 的 ON CONFLICT 表达式目标依赖 202609_02 内联 `UNIQUE(task_type, COALESCE(tenant_id,''))`，而 PostgreSQL 表约束不接受表达式（表达式唯一索引只能 CREATE UNIQUE INDEX）——该迁移 SQL 疑似从未能在 PG 合法执行；e2e 测试 schema 特意改用 `CREATE UNIQUE INDEX uq_task_type_tier_config`（与生产 DDL 不同形，恰好自证正确形态）。窗口内新增的 apply-tier-config 端点建立在未验证对象上 | sql/migrations/202609_02_create_task_type_tier_config.sql:21（自 87700bfc7 零改动、不在 installer embeddata startup）；taskprofile/csv.go:311-321；taskprofile/e2e_loop_integration_test.go:169-170 | 真库若无此表 → apply 503（ErrTierConfigMissing 降级，功能整体不可用）；若表存在但无表达式唯一索引 → upsert 报 "no unique constraint matching ON CONFLICT" → 500 裸错 | 主代理真库 `\d task_type_tier_config` 核实（迁移三纪律 1：新迁移必须真库实跑）；必要时出修复迁移走 revision-sequence |
| 4 | P3 | ApplySuggestions upsert 无条件 `enabled=TRUE` + 全字段覆盖，会静默复活运维手动禁用（enabled=FALSE）的行并抹掉人工调优的 preferred_tier/min_confidence/fallback_tiers，响应与注释均未提示 | taskprofile/csv.go:315-321 | 运维禁用某类型配置 → 后续一次 apply（显式 task_types 或该类型修正率≥0.3 恰好命中）→ 行复活并覆盖，回落 tier-b 的保护被无声撤销（与 D13"自动禁用不被旧快照复活"精神同类） | 对 enabled=FALSE 行跳过并在响应提示，或在 applied 列表中标注 revived=true |
| 5 | P3 | e2e 闭环 Phase 4"置信度阻尼"是公式手抄复刻（`(0.90*50+humanRate*2*C)/(50+2*C)`）而非调用生产 BlendCorrectionsIntoAccuracy；公式双写未来分叉不会被 e2e 捕捉。缓解：routingopt 侧有真实 PostClassify+CorrectionSource 单测 | taskprofile/e2e_loop_integration_test.go:319-328（注释自认）；真实覆盖在 routingopt/confidence_correction_test.go:101-117 | 设计 §一#6 闭环四环中第 2 环在集成测试中为模拟 | 可接受（注释诚实）；如要钉死可抽公共公式或经 adapter 注入真 adjuster |
| 6 | P3 | registry.go 注释引用不存在的测试文件名 `registry_defaults_test.go`（实际为 registry_test.go，pin 测试存在且有效） | taskprofile/registry.go:32 | 维护者按注释找 pin 测试落空 | 顺带修注释 |
| 7 | P3（备忘） | overlay/reload 是进程内 atomic.Pointer 换装：多副本网关需每副本各自设 TASKPROFILE_OVERLAY 并各自 POST /reload，否则副本间档案漂移；设计文档未提 | taskprofile/registry.go:61-76；cmd/gateway/routing_optimizer_init.go:148-159；taskprofile/handler.go:289-296 | 双实例部署中仅一副本 reload → 该副本建议/校验词表与其他副本不一致 | 运维面记入 runbook；长期可考虑 registry 版本号纳入 /health 观测 |

## 二、核实为健康的面

- **D13 免费池增量零改动**：b75c91900..HEAD 中 domains/freediscovery、domains/freeresource、discovery 无任何文件改动（git diff --name-status 为空）；48h 窗口内 freeresource 仅 R38 a50182ca2（经 merge-base 确认为 b75c91900 祖先，属已审计段）。D13 §3 五条（RLS 特权写 runPrivileged、template 动态 SET、注册幂等、聚合口径、auto 接入）无窗口内回归面，按派发指示快速收尾。
- **blend 公式/播种/门控与设计 §5.1 一致**：routingopt/confidence.go:133-154 —— `acc'=(acc×S+humanRate×w×C)/(S+w×C)`、`samples'=S+w×C`、w=2.0（注明 670 迁移 WeightedAccuracy 口径）；GetTaskTypeAccuracy（routingopt/dao.go:691-718，窗口零改动）`SUM(success)/COUNT(*)` 与之数学等价；播种（无 auto stats 类型以 2×C 起步）与 confidenceMinSamples=20 门自洽（~10 corrections 才生效）；SetCorrectionSource 走 mu 锁、nil receiver 安全。
- **suggest 边界与设计 §5.2 一致**：taskprofile/suggest.go:17-25 阈值全 var（0.30 / 5 样本 / +0.05 封顶 1.0）；escalateTier c→b→a、a 封顶（:96-105）；confidence<MinConfidence→tier-a 与 autoroute/tier_selector.go:145-153、:360-368 语义一致；未知类型 tier-b 缺省与 TierSelector:344-347 一致。suggest_test.go 7 用例覆盖含 a 封顶与 fallback 拷贝防别名。
- **request_id 有效性闭环健康**：taskprofile/corrections.go:87-98 经 auto_route_selections_all（sql/migrations/startup/656:163-174，hot UNION ALL parent，含 task_type/confidence/profile/ts）解析 auto 类型，ErrNoRows→ErrUnknownRequest→400；`ON CONFLICT (request_id) DO NOTHING RETURNING`→ErrAlreadyCorrected→409（first-annotation-wins，与 training_human_annotations 同幂等语义）；CSV 导入 re-import 幂等（csv_test.go + e2e Phase 8 skipped=7/9 双验证）。
- **热路径零改动声明核实**：decider/scoring/tier_selector/classifier 均不在 b75c91900..HEAD 改动清单；autoroute 侧仅 shadow_actors.go 注释变更。**R42 注释修正复核为真**：handler.go:1729-1731 TrimSpace X-Gw-Source-Actor 落 logCtx.OriginActor→request_log_pipeline.go:488-489 落库 ✓；middleware/origin_mw.go TrimSpace ✓——ctx 路径（auto_route.go:372-375、auto_route_nonchat.go:145/187）虽不 trim，消费侧 autoroute/shadow_actors.go:40-41 IsSyntheticActor TrimSpace 弥补 ✓——注释所断言的"双侧 trim"成立。
- **checker.go / model_policies.go R41 产物自洽**：internal/modelpolicy/checker.go ReloadAll 显式事务+`app.current_role=super_admin` GUC（725 bypass policy 在 embeddata 存在）；reloadTenant 改 tenant GUC 窄路径；admin/model_policies.go 两查询改 withTenantTx（admin/tenant_ctx.go:16）。
- **迁移 724 四点同步**：canonical 与 installer embeddata byte-identical（diff 为空）；parity map 已由 ac39850e0 收口。
- **测试资产齐备**：routingopt/confidence_correction_test.go 8 用例（恒等/权重/阻尼/播种/不可变输入/PostClassify 阻尼/错误降级/nil 安全，t.Cleanup 恢复全局 var）；registry_test pin V3 映射+overlay 原子性；e2e 真库 9 相位覆盖写路径护栏（409/400×2）。
- **词表数值对照**：taskprofile defaultProfiles 与 autoroute TaskTypeTierMapping/MinConfidenceThresholds（task_types_v3.go:344-375）十类逐项一致（tier+阈值），且有 TestDefaults_MirrorV3TierMapping 钉桩；fallback_tiers 与 202609_02 DB seed 一致（testing [tier-c]、documentation/summary []）。autoroute.getDefaultFallbacks 与 DB seed 的既有分叉为窗口外历史状态，非本轮引入。

## 三、未覆盖项与原因

- **task_type_tier_config 真库实况**（发现 #3 的定级前提：表是否存在、索引实际形态）——需真库 `\d` 查询，子代理无凭据且只读纪律，交主代理按迁移三纪律 1 核实。
- **go test / go vet 实跑**——只读审计未执行构建验证；发现 #1 修复后的三门验证留给主代理修复流程。
- **a8d3a5bbe 的 web 前端部分**（工作台闭环 UI）——前端资产未逐行审，本轮重点为数据链路；前端仅消费上列 admin 端点。
- **D13 运行时健康复测**（真库 RLS 实测、runPrivileged 行为、免费池额度聚合）——增量零改动即无回归面，未做真库复测（D13 §4 历史回归点 501ef519d/8f00bde9c 未触碰）。
- **TASKPROFILE_OVERLAY 在 252/154 的实际部署值**——需运维面核实，关联发现 #7 的多副本一致性。
- **意图基线 TASKPROFILE_MODULE_DESIGN.md 与实现的偏差登记**：设计 §三写"4 个 admin 端点"、实际 7 个（a8d3a5bbe 增 export/import/apply-tier-config），设计交付清单未回填——文档漂移未单列发现，建议主代理回注设计文档 §七。

**主代理复核结论（R43）**：#1 实锤（与 D15#1 汇聚）→ 已修（admin 侧 SetRecorder + 双向源码守卫测试）；#2 实锤（与 D15#2 汇聚，含 web L1 种子对照）→ registry 已补 6 类（tier-b/0.70/[tier-a,tier-c]，镜像 autoroute unknown-type fallback）+ pin 测试 + 前端不再吞错；#3 亲核真库实锤且比报告更深一层：真实表所有者是 deploy V370（tenant_id BIGINT + COALESCE(tenant_id,0)），202609_02 从未可执行，ApplySuggestions 原写法在真表上 42P10、缺表库上 503 死胡同——已按 V370 形态对齐 ensure/迁移桩/e2e/ON CONFLICT 四面；#4 登记；#5 采信注释诚实；#6 已修；#7 登记 runbook。设计文档 §七 端点数漂移在轮文档登记。
