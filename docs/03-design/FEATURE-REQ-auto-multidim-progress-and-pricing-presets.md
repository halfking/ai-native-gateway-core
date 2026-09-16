# 特性需求:成本设置快捷化 + auto 多维会话评分 + 停滞换模型

> 状态:**RFC(方案评审已通过,待分阶段实施)**
> 范围:覆盖原始诉求"成本设置" / "auto 正确可靠" / "经济但高效" / "停滞换模型" / "多维会话评分驱动 3 种动作"
> 设计者:本轮方案评审
> 关联模块:`autoroute/`、`admin/pricing.go`、`domains/streaming/auto_route.go`、`bg/session_health_worker.go`
> 关联缺陷:O5(已记录,本 RFC 不解 O5)、2026-09-14 audit O2 decision_trace 已落地

---

## 1. 背景与问题

`model=auto` 已具备完整的多维评分(2 维/4 维/5 维版本共存,见 §6.1 现状),
`admin/pricing.go` 已具备 9 个成本管理端点 + 1286 行 Vue UI,但用户提出 3 类痛点:

1. **成本设置繁琐**:运营在 `PricingManagementView` 改一个 provider 全部模型成本需要逐条编辑。
2. **auto 模式不够"可靠"**:缺乏会话级 *进展状态*,无法区分"模型表现差"与"用户没进展"两种情况。
3. **auto 没有"自我纠错"**:连续多轮失败/重复/延迟爆炸时,auto 仍按既定评分走,没有"换模型/加上下文/建议停止"的回路反馈。

## 2. 原始需求 → 规格条目对照

| 原始需求(摘录) | 规格条目 | 验收方式 |
|---|---|---|
| 增加方便快捷的供应商模型成本设置 | §3.1 成本预设模板端点 | 新增 3 个端点,UI 顶部加 chip 栏 |
| 优化成本设置能力,让 auto 模式正确可靠 | §3.2 decision_trace 增强 | 每次 auto 决策携带 4 维评分 |
| 既经济又确保效率 | §3.3 auto 评分权重改造 | 5 维评分中价格 0.2 + 通道质量 0.3(已具备,本 RFC 把它显式落地到用户协议) |
| 一段时间没进展,auto 可以自动更换模型 | §3.4 评分 → 换模型回路 | `X-Gw-Auto-Switched-Model` 头非空 |
| 对当前会话有多维状态评分,据此决定停止/换模型/加上下文 | §3.5 4 维评分 + 3 种动作 | 至少 4 维可见 + 至少 1 种动作可被 SQL 验证 |

## 3. 规格条目(可验收)

### 3.1 成本设置快捷化

- `GET /api/pricing/presets`:列出内置预设(OpenAI 官方 / Anthropic 官方 / 国内大厂 / 第三方聚合 / 免费层 / 性能旗舰),按 `provider` 分组
- `POST /api/pricing/presets/preview`:dry-run,返回将更新的 `offer_id` 列表 + 改动字段
- `POST /api/pricing/presets/apply`:批量写入,默认 `pricing_source='preset'`
- 新表 `pricing_presets`(migration 715),`UNIQUE(preset_key, provider_id, raw_model_name)`
- 仅 super_admin(沿用现有 `writeEndpoints` map 模式)

### 3.2 decision_trace 增强(已具备,本 RFC 强化字段)

`autoRouteDecision` 新增字段:
- `health_score int` —— 0-100 综合
- `progress_score / quality_score / cost_efficiency / task_fit float64` —— 4 维
- `switched_from string` —— 本轮是否换模型
- `switched_reason string` —— 换模型原因
- `suggested_action string` —— `continue | enrich_context | switch_model | suggest_stop`

### 3.3 auto 评分权重(已具备,本 RFC 显式落到协议)

- 走 `ScoreWithChannelQuality`:intent 0.4 + price 0.2 + channel 0.3 + reliability 0.1
- 走 `ScoreWithAffinity`:4 维 × 0.85 + affinity 0.15(生产默认)
- 价格分 `scorePriceByCostContext` 用 cohort P75 归一化(避免 94-100 钳制)

### 3.4 评分 → 换模型回路

- 新增 `autoroute/session_progress.go::SessionProgressScorer`:
  - 4 维 + 综合
  - Redis 单 key 多 field,key=`auto:progress:{sessionID}`,TTL=1h
- `DecideWithFeatureFlags` 末尾读 progress → 算动作 → 必要时调 `RecommendModelAlternatives`
- 任一会话 5 分钟内最多 1 次自动换模型(Redis 限流 key=`auto:switch:cooldown:{sessionID}`)

### 3.5 4 维评分公式

| 维度 | 数据源 | 公式 |
|---|---|---|
| D1 进展度 | 近 5 轮成功标志 + finish_reason + token 增量 | `success_rate*0.6 + finish_natural_ratio*0.3 + token_growth_factor*0.1` |
| D2 质量度 | 近 5 轮 p95 + tool 错误率 + 输出重复度 | `latency_score*0.4 + tool_success_rate*0.4 + novelty_score*0.2` |
| D3 成本效率 | 累计 cost / 同类任务 session P50 | `clamp(100*(1 - cost/p50), 0, 100)` |
| D4 任务契合 | 任务类型切换次数 + 负反馈 | `100 - task_switch_penalty*10 - negative_feedback*30` |
| 综合 | 加权和 | `0.35*progress + 0.30*quality + 0.20*cost_eff + 0.15*task_fit` |

### 3.6 评分 → 动作映射(规则)

```
composite >= 80         → continue
70 <= composite < 80    → enrich_context(注入更早 tool result 摘要)
50 <= composite < 70    → switch_model(走 RecommendModelAlternatives)
composite < 50          → suggest_stop(写 X-Gw-Auto-Suggested-Action=suggest_stop)

硬升级(任一维独立触发):
  D1 < 30 或连续 3 轮 composite < 50 → switch_model
  latency p95 > 10s 持续 2 轮        → switch_model
  task_fit < 20                       → suggest_stop
```

## 4. 集成位置(挂哪里)

| 改动 | 挂点 |
|---|---|
| `SessionProgressScorer` | 新文件 `autoroute/session_progress.go` |
| Redis 桶读写 | 复用 `autoroute/session_intent_cache.go` 模式 |
| 评分 → 动作 | `autoroute/decision_v2.go::DecideV2` 末尾(已存在路径) |
| 上一轮结果回流 | `domains/streaming/auto_route.go` 在 `recordAutoSelectionFromWire` 旁增 `recordAutoOutcome` |
| 切换模型 wire 标注 | `autoRouteDecision` 加 4 字段(见 §3.2) |
| 成本预设端点 | `admin/pricing.go` 末尾(沿用现有 `RequireSuperAdminForWrite`) |
| 前端 | `web/src/views/PricingManagementView.vue` 顶部加 `<PricingPresetBar>` |
| 迁移 | `sql/migrations/startup/715_pricing_presets.{up,down}.sql` |

## 5. 实施阶段(本 RFC 拆分提交)

| 阶段 | 范围 | 风险 | 提交 commit |
|---|---|---|---|
| 0 方案评审 | 本文档 | 低 | `docs(design): auto 多维评分 + 成本预设 RFC` |
| 1 成本预设 | 后端 3 端点 + migration 715 + Vue chip 栏 + i18n | 中 | `feat(pricing): 成本预设模板一站式应用` |
| 2 评分骨架 | `SessionProgressScorer` + Redis 桶 + 单测 | 低 | `feat(autoroute): 会话 4 维进度评分骨架 + Redis 桶` |
| 3 评分→动作 | 包装层 + header 标注 + feature flag | 中 | `feat(autoroute): 进度分驱动 auto 换模型/扩上下文/建议停止` |
| 4 可观测 | SQL 视图 + Grafana 指标 | 低 | `feat(observability): 4 维评分 + 动作 SQL 视图` |

每阶段单独 PR,默认 feature flag `AUTO_USE_SESSION_PROGRESS=false`(行为不变)。

## 6. 现状盘点(避免重复造轮子)

### 6.1 auto 评分已具备
- `autoroute/scoring_simplified.go`:`ScoreSimplified / ScoreWithChannelQuality / ScoreWithAffinity` 三个版本
- `StratifyByChannelQuality` + `ApplyFallbackDemotion` 实现池分层
- `ComputeCorrectionScore` 校正分

### 6.2 auto 换模型回路已有基础
- `domains/streaming/auto_route.go::maybeResolveAuto`:入口
- `autoroute/model_alternatives.go::RecommendModelAlternatives`:失败时返回次优候选
- `routing_decision_log.decision_trace` 已落地(2026-09-14 O2)

### 6.3 成本管理已具备
- `admin/pricing.go` 9 端点:`tree/summary/bulk-update/export/import/table/stats-window/copy/auto-inherit`
- `web/src/views/PricingManagementView.vue` 1286 行
- 8 国语言 locale 已有

### 6.4 session health 已有"事后"评估(不能驱动实时)
- `bg/session_health_worker.go` 写 `session_summaries.health_score / health_grade / quality_score / outcome`
- 维度:ErrorCount / AvgLatency / ModelSwitchCount / Compliance / PromptInjection / PII / Toxic
- **本 RFC 不动此模块,只新增实时版**

## 7. 风险与缓解

| 风险 | 概率 | 缓解 |
|---|---|---|
| 评分误判误换模型 | 中 | feature flag 默认关;super_admin 灰度 |
| Redis 写频率 | 低 | 写频率受 `recordAutoOutcome` 控制(每请求 1 次) |
| 换模型风暴 | 中 | 5 分钟 Redis 限流(同一 session) |
| 预设价格错误 | 中 | 端点默认 dry-run;`pricing_source='preset'` 单独审计 |
| 与 O5 耦合 | 高(已知) | 本 RFC 不解 O5,只解决"评分正确时如何动作" |
| Windows syscall 编译错误 | 已知与本任务无关 | 不在本 RFC 范围 |

## 8. 显式不做

- 不训练 ML 模型
- 不改变 OpenAI/Anthropic 协议(只用额外 X-Gw-* 头)
- 不引入新存储依赖
- 不做跨会话联邦学习

## 9. 关联文档

- `autoroute/V2_IMPLEMENTATION_STATUS.md` —— V2 评分历史
- `autoroute/scoring_simplified.go` —— 评分函数
- `autoroute/decision_v2.go` —— 决策函数
- `autoroute/model_alternatives.go` —— 模型替代推荐
- `admin/pricing.go` —— 现有成本管理
- `bg/session_health_worker.go` —— 现有 health_score
- `domains/streaming/auto_route.go` —— auto 入口
- 2026-09-14 O2 audit —— decision_trace 落地
- O5 audit —— canonical 遮蔽问题(本 RFC 不解)
