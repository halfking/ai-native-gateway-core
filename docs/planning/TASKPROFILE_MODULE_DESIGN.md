# taskprofile — 任务类型档案与分层建议插件模块设计

- 日期: 2026-09-18
- 状态: 已实现（见本文末尾交付清单）
- 关联: `docs/planning/AUTO_ROUTING_OPTIMIZATION_PLAN.md`（OmniRoute 研究与 P2 路线图）、
  `docs/p2-ml-routing/p2.2-routing-optimization-plugin-design.md`（routingopt 插件）

## 一、需求完善（原始诉求 → 可交付口径）

原始诉求：针对 auto 模型专项测试；从测试/实际运行中对**任务的解析归类**与**模型的选择**
做自动优化；建立机制让每个请求的自动任务类型分配可以被**人工优化与标注后反馈回系统**；
把"任务类型识别 + 建议模型分层"数据整合成一个**插件式模块**，减少对其它模块的干扰，
将来可独立升级。

逐条落成可交付口径：

| # | 需求 | 交付 |
|---|------|------|
| 1 | 每请求任务类型分配的人工标注 | `POST /api/admin/task-profile/corrections`（按 request_id 记录 auto vs human 任务类型；与既有 first-turn 标注工作台同一权限层级） |
| 2 | 标注反馈回系统 | ① 冲进入 `routingopt.PostClassify` 置信度调整（人工权重×2，与 WeightedAccuracy 口径一致）② 冲进 `autoroute.ClassificationFeedbackAggregator`（补上孤儿聚合器的 Prometheus 通路）③ 修正密度驱动分层升级建议 |
| 3 | 任务识别+模型分层整合为一个模块 | 新包 `taskprofile/`：任务类型档案（V3 十类 + 默认分层 + 置信度阈值 + 回退链）+ 修正存储 + 分层建议引擎，全部内聚在一个包 |
| 4 | 减少对其它模块干扰 | 请求热路径零改动（autoroute/decider/scoring 一行未动）；对外只暴露 4 个 admin 端点 + 1 个可选的 routingopt 注入点；既有表/视图零迁移（新增独立表） |
| 5 | 将来可独立升级 | 档案数据带 `schema_version`，支持 overlay JSON 文件热加载（`TASKPROFILE_OVERLAY` env + `POST /api/admin/task-profile/reload`）；升级档案 = 换数据文件/版本号，不改代码 |
| 6 | auto 模型专项测试 | `taskprofile/e2e_loop_integration_test.go`（真库）：分类假设 → 人工修正注入 → 分层建议变化 → 置信度阻尼 的闭环验证；本地与 252 部署后按 §六 脚本验证 |

## 二、OmniRoute 及高星项目可借鉴模式（对照）

基于 `AUTO_ROUTING_OPTIMIZATION_PLAN.md` §2 的 OmniRoute v3.8.49 研究结论，本模块吸收：

1. **taskFit（任务类型适配度）因子**（OmniRoute Auto-Combo 12 因子之一，规划文档标注
   "Phase 2 增加，基于历史成功率"）：本模块用**人工修正率**直接量化 taskFit——某个任务
   类型被人工频繁改判，说明分类器对该类最弱，建议引擎据此收紧（升层/抬阈值），优化器
   据此阻尼置信度（更早触发 LLM 重分类）。这把规划文档里"缺失"的 taskFit 以人工反馈
   数据补上了第一块。
2. **分层弹性架构**（tier-a/b/c）：沿用既有 `TaskTypeTierMapping` 语义，但把"档案数据"
   （每类默认层、回退链、最低置信度）从代码常量提升为**可版本化、可 overlay 的数据资产**。
3. **人工反馈 ×2 权重**（LiteLLM/label-studio 类标注回流项目的通用做法，本仓库 670 迁移
   起的既定口径）：`blended = (auto_acc×S + human_rate×2×C) / (S + 2×C)`。

明确**不**引入的 OmniRoute 模式：session stickiness 的 headroom 解绑、压缩引擎——与本次
目标无关，且已有独立 track。

## 三、模块边界（插件式）

```
taskprofile/                 ← 一切新逻辑都在这里
  registry.go                档案注册表（内嵌 V3 默认 + overlay 热加载，atomic.Pointer）
  corrections.go             人工修正存储（pgx）+ 可选 FeedbackRecorder 回冲
  suggest.go                 分层建议引擎（纯函数，可独立单测）
  handler.go                 4 个 admin 端点
```

对外触点（全部加法、可整体摘除）：

| 触点 | 方向 | 说明 |
|------|------|------|
| `admin/handler.go` RegisterRoutes | 注册 4 端点 | 仅在 `h.db != nil` 分支加一行 |
| `routingopt/confidence.go` | 可选注入 `CorrectionSource` | 不设置 = 行为与现状 byte-identical |
| `cmd/gateway/routing_optimizer_init.go` | 装配 adapter | ROUTING_OPT_ENABLED=false 时整段不执行 |
| 迁移 724 | 新表 `task_type_corrections` | 独立表，无既有对象改动 |

**热路径声明**：decide/classify/scoring/tier_selector 一行未改。运行时影响仅通过
PostClassify（已在 routingopt 插件内、受 ROUTING_OPT_ENABLED 与 hook 超时门控）间接触达。

## 四、数据模型（迁移 724）

```sql
CREATE TABLE IF NOT EXISTS public.task_type_corrections (
    id                     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id             text NOT NULL UNIQUE,
    auto_task_type         text NOT NULL,
    human_task_type        text NOT NULL,
    agrees                 boolean NOT NULL,          -- auto == human（预计算，聚合免函数）
    classifier_confidence  double precision,          -- 决策时置信度（可空）
    profile                text,                      -- 决策时 profile（可空）
    annotator              text NOT NULL,
    reason                 text NOT NULL,             -- performance/cost/availability/quality/other/correct
    created_at             timestamptz NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_task_type_corrections_auto_type
    ON task_type_corrections (auto_task_type, created_at DESC);
```

- 无 RLS：与 `routing_feedback_log`（670）同族——网关运行时/管理员表，无租户维度。
- `agrees` 预计算：聚合查询退化为一次 GROUP BY，且语义在写入时冻结（档案改名不追溯改历史）。

## 五、运行时语义

### 5.1 修正 → 置信度（routingopt.PostClassify）

`ConfidenceAdjuster.taskStatsSnapshot` 在取到 auto 统计后，若注入了 CorrectionSource，
按人工×2 权重混入：

```
acc' = (acc×S + human_rate×2×C) / (S + 2×C)     human_rate = agrees/C
samples' = S + 2×C
```

效果：被频繁改判的任务类型置信度被阻尼（更早触发 LLM 重分类 = 自动优化归类）；
被人工确认的任务类型置信度获得支撑（快路径更稳）。未注入该源时快照与现状完全一致。

### 5.2 修正 → 分层建议（suggest，admin 视图 + 将来可挂点）

纯函数 `Suggest(registry, stats, taskType, confidence)`：

1. base = 档案默认（未知类型 → tier-b 保守缺省）；
2. 修正率 ≥ 0.30 且样本 ≥ 5 → 升一层（c→b→a，a 不再升），MinConfidence +0.05；
   （阈值做成 var，测试可收紧；语义=OmniRoute taskFit 反向指标）
3. confidence < MinConfidence → 升到 tier-a（与既有 TierSelector 置信度升级语义一致）。

v1 不自动改写 `task_type_tier_config`（显式优于隐式）；TierSelector 若要消费建议，
由运维按 admin 视图人工决策——避免运行时配置被反馈环静默漂移。

### 5.3 回冲孤儿聚合器

`CorrectionStore.Insert` 成功后调用可选 `FeedbackRecorder.RecordFeedback(auto, agrees)`；
`*autoroute.ClassificationFeedbackAggregator` 结构性满足该接口（taskprofile 不 import
autoroute，接口在 taskprofile 侧定义、测试侧编译断言）。接线点在 cmd/gateway 装配处，
Prometheus 分类反馈计数器由此真正开始计数（该聚合器此前零调用方）。

## 六、验证与部署

1. 单测：registry（overlay 校验/版本/原子换装）、suggest（全分支表驱动）、blend 公式。
2. 真库集成（`-tags=integration`，testcontainers）：迁移 724 DDL + DAO 语义 + E2E 闭环。
3. 专项 E2E：见 §一#6。
4. 本地部署：`scripts/deploy-local.sh`（或 llm-gateway-deploy-test skill 指定脚本）+
   `scripts/apply-db-revision-sequence.sh` 到位 724 → curl 冒烟四个端点。
5. 252 部署：`scripts/deploy-252-gateway.sh` → 同上冒烟。252 为共享生产库，
   只做只读+标注表写入验证，不注入合成流量（与 245 traffic-only 教训一致）。

## 七、交付清单（实现后回填）

- [x] 迁移 724（canonical + embeddata + runner + embeddedSQLFiles + sequence 通道，四门禁绿）
- [x] taskprofile 包（registry/corrections/suggest/handler）+ 单测
- [x] routingopt CorrectionSource 混入 + 单测
- [x] admin 路由 + cmd/gateway 装配
- [x] 真库集成 E2E 闭环测试
- [x] 本地部署验证（071x 记录见会话总结）
- [x] 252 部署验证
- [x] **变更端点审计 hook**（R43 §五#3 登记 → R45 2026-09-19 落地）：`Handlers.SetAuditHook(func(AuditEvent))`，四个变更端点（corrections create / import / reload / apply-tier-config）的**真实变更尝试**（success 与 failure）都投递事件；校验拒绝与 nil-pool 503 不投递（无变更意图）；hook panic 隔离、nil 零开销。taskprofile 不 import admin（避免反向依赖成环）——`AuditEvent` 携带原始 *http.Request，生产 sink 由 admin/handler.go 注入（首个 sink = 结构化 slog `taskprofile.audit`，actor 从 admin 鉴权上下文提取；未来可换 DB 审计表 sink 无需再动本包）。钉桩：默认门 `handler_audit_test.go` ×5（reload 成功/失败、无尝试不发、panic 隔离、nil no-op）+ 真库集成 `e2e_audit_hook_integration_test.go`（create 成功/重复失败、import 成功 detail.imported、apply 成功 detail.applied=1 行 documentation 升级）。
