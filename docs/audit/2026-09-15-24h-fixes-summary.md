# 最近 24 小时修正汇总（2026-09-14 12:00 → 09-15 13:00）

窗口基点 a69cbafa9 之前为 R28 收尾；本汇总覆盖窗口内全部 50+ 提交，按六条主线归类。所有结论均有提交号可溯，关键论断经主代理直读验证。

## 一、存储优化方案 v2 主线（S1→S3 + GAP-2）

| 内容 | 提交 |
|------|------|
| 方案 v2 定稿：弃用 request_logs、六表会话族为最终态 | 06815c742 |
| S1a+S1b 落地：六表会话族补全（迁移 706/707/708）+ session_turns 宽表写链 + final_full/正文灰度开关 | efe9e1c83 |
| S2 落地：request_logs_with_current_month 重建为 session 家族拼装体（迁移 710，709 撞号重编）+ 视图自愈链 v2 + D4 合成系统会话 + dual_read_validator 行级对账 + 停写 gate 登记 | a789a05ad |
| S2 审计轮：710 补包装链在场守卫（680 事故态防部署通道中止） | 1cacc62cb |
| S4 前置 GAP-2 闭环：session_mirror_outbox 失败登记持久化+重放器（迁移 712）+ 711 turns cost 精度 12,6→14,8 | 887d29e5e |
| GAP-2 收官：711/712 迁移自登记+通道清单登记、回填脚本物化版（绕 planner perminfoindex bug）、台账 GLOBAL_G2 归零起算观察期 | 29f73ed81 |
| S3 波1 读端改造样板：admin 日志列表/详情原生 turns 读（灰度开关 storage.admin_logs_native_turns_read） | 2b8eb0023 |
| S3 合入收口：plan §4-S3 状态更新 + 合并事故审计与本机 2119 部署验证 | ffb3a214d |
| 每日观察 09-15：首扫 FAIL（PG crash recovery 窗口 2 行缺失，回填兜回归零）；新登记 claim 置位 is_final_success 结构性漏镜像（~3 行/天，暂以每日回填兜底） | ab08dfca2 |

## 二、auto 模型匹配 / autoroute（O1′→O5 → 二轮审计）

| 内容 | 提交 |
|------|------|
| 三轮审计定稿：兜底机制修正为 work_type路由×词表错配×门控掩盖 三因子；O1′ 三件套 | 525bbb21a |
| O1′-a 词表归一化 + O2 分发失败 5min 抑制 + decision_trace 补写（6 项人工确认落地） | a69cbafa9 |
| 迁移 709：work_type 路由补齐 4 个无路由类 + 705 函数链补登记 | e48544bd3 |
| O5 修复：decider 装配拆出 !bgDataPlaneOnly 门控，data-plane 实例恢复 model=auto 决策能力 | fe2548645 |
| O4 超时/截断链三连修：classifier 层 3s 硬编码改读 LLMGatewayAutoLLMTimeout（≤30s 钳位）；HTTPLlmCaller 默认 client 超时跟随 cfg（原 5s 硬编码）；MaxTokens/ExtraBody env 支持推理型分类模型关思考通道 | 8780d57ba + 7cdebfa67 + 741bad4d8 |
| O4/O5 收尾：决策引擎双模式装配 + autoFallback 默认值 + 生产 245 实测 llm_v2 分类采纳(0.85) | 24f93cdae |
| 死埋点接线：RecordLLMMetricCall/RecordLLMCircuitBreakerState 补接 buildAutoLLMCaller + 源级守卫测试；selection 写链断档收口 | bac8b6e9e |
| 审计复核修正：DisabledCaller 绕过 InstrumentedCaller 致 disabled outcome 永不发出，Classify 补 errors.Is 显式记录 | 2aa80828f |
| 二轮提示词端到端审计：v2 套件 40 例（英文/混合信号/陷阱/残余）+9 处分类修复+G1-G9 全判定 | 9dc12e115 |
| N2 仲裁守卫：planning 补引用语境守卫（>50k 长文"材料提到制定方案"判 long_context）与工具上下文守卫（agent 编排场景让位 agent 通道）；100 例套件全绿 | 6c80e8c2b |
| N2 修复状态回填 + 二轮交接（100 例 98.9%、兜底 53%→30%） | a328e0048 + 944b33891 |

## 三、任务托管 hostedtask + GoalRun 安全

| 内容 | 提交 |
|------|------|
| 任务托管方案 v2（跨仓库核实修订+P0 执行清单） | d23bccc92 |
| P0 落地：网关转交 ACC、进展经网关投影+通知、上下文经 Memora | 5d2565655 |
| GoalRun §0-F2 鉴权修复：直接信任 X-Tenant-ID 改 KeyVerifier 归属校验，store nil 由 panic 改 503 fail-closed，401/403/503 矩阵 | 525981613 |
| GetGoalRun 补显式 tenant 谓词（查询层双保险对齐 hostedtask）+ 跨租户用例迁 404 语义（不泄露存在性） | 57d29b69d + bba0888a8 |
| P0 实施验证报告：完整性二次审查确认无遗漏 | bde1bffa9 |
| 状态机与白名单校验修正（P0 审计修复） | d852e53c6 |

## 四、R29 审计轮（09-15 06:28 定稿，9 修复提交）+ R30 前两项当日落地

R29 五路汇总裁决详见 `2026-09-15-r29-24h-audit-round.md`。12 项修复：installer 711 embed 断链、711 撞号重编 713、installer 五点同步、seq 通道补 711、outbox 链 RLS bypass GUC（00cc1192d）、approval resume 跨租户越权（f1bdb8459+4398f7efb）、SSE 订阅 map 竞争（758af4a42）、熔断 gauge 死埋点（b8cb16ccb）、mirror_outbox kill switch 注册（dce0a96eb）、分页 tiebreaker（221307658）、goalrun tenant 谓词、goalintegration e2e 契约迁移。

R30 遗留 §三 前两项已于本窗口末尾落地：

| R30 # | 修复 | 提交 |
|-------|------|------|
| #1 流式终态闩不对称 | interrupted tail（chat+anthropic）写出成功才 MarkTerminalRendered；写失败保留 coordinator 兜底权威；deadline-before-first-attempt 未提交终态恰好一个 | 18933bc6c |
| #4 canonical parity 门禁 | pre-commit/verify 接入 canonical_delivery_path_check：≥690 startup 迁移必须 ∈ StartupFiles ∪ revision-sequence ∪ Go-ensure 三路之一，助手函数自带自测 | 55a6f9454 |

## 五、首轮会话标注工作台

| 内容 | 提交 |
|------|------|
| 后端 first-turn-samples 接口 + task_type/model 元数据落库 | 888ceee16 |
| 前端标注页重构首轮会话工作台（会话级列表/筛选/标注弹窗） | 9afe4edfb |
| i18n ×8 语言同步 + parity/CJK 棘轮全绿 | e4cc454f3 |
| metadata JSONB 载荷 string 而非 []byte（pgx bytea 22P02） | c66051969 |
| 分页排序补 request_id tiebreaker（同秒多行 OFFSET 重复/漏行） | 221307658 |

## 六、部署与运维动作

- 245 pre-prod：2111-e201f9ed → 2116-741bad4d（O5+O4 最终版）
- 154 生产：2112-d2ea0e1f → 2117-4e7f9810（O5+O4 全量随例行部署）
- 本机：2119=83f19f6c（S3 波1 合入版本身份入库，97b59f28e）
- 文档收尾：db-changelog 补 709/710/712/713；storage plan "711" 双义勘误（cost=713，S5 轮转候选顺延 714）；freediscovery Orbi gap analysis（3fb83f8a7）

## 遗留提示

R30 候选余下 10 项以 `2026-09-15-r29-24h-audit-round.md` §三 #2/#3/#5-#12 为准（编号有位移：#1/#4 已销）。
