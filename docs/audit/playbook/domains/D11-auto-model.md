# D11 — auto 模型全量实现

> 领域编号: D11 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：model=auto 的自动路由/模型选择全量链路：会话分析→分类→推荐→re-rank（P2.2）→affinity→settle→feedback_log→AdaptiveLearner 回灌；ML/ONNX 推理；多维进度评分与成本预设（新 RFC）；auto 的数据管道（特征/promote 漂移）。
**不管**：通用队列与限流（D04）；统计聚合表（D10）；免费凭据池的发现（D13，但 auto 如何消费免费池在本域）。

## 2. 参考基线

设计文档：
- `docs/03-design/02-feature-design/design/AUTO_SELECTION_SPEC.md` + `AUTO_SELECTION_IMPLEMENTATION_PLAN.md`
- `docs/03-design/02-feature-design/design/AUTO_MODEL_OPTIMIZATION_V2.md` / `V3_*` 系列
- `docs/auto-model-optimization/`（02 架构 / 05 存储优化 / 06 免费 LLM 接入 / 08 ML 训练管道 / 09 ONNX / 11 路线图）
- `docs/planning/AUTO_ROUTING_OPTIMIZATION_PLAN.md`、`docs/p2-ml-routing/`
- 新 RFC（48h 内）：`docs/03-design/FEATURE-REQ-auto-multidim-progress-and-pricing-presets.md` + `AUDIT-auto-multidim-progress-reuse-analysis.md`

代码入口：
- `autoroute/`、`routingopt/`、`domains/analysis/`、`domains/modelquality/`、`capabilityscore/`、`modeliqdata/`、`recentmodels/`
- `bg/`（settle/affinity worker）、`annotation/`、`ml-training/`

## 3. 检查清单

1. **决策链闭环**：分类→推荐→re-rank→affinity→settle→feedback_log→AdaptiveLearner→回灌，每环有产出与消费方；窗口内改动不断链（R30 已证实闭环健康，防回退）。
2. **终态 outcome 回填**：auto 请求（流式与非流式）所有终态（成功/中断/失败）都回填 outcome 到路由事实——**成功路径也要**（F1 教训：matched/expired/accuracy 统计依赖）。
3. **数据管道三缺陷基线**：promote 不丢特征列、chat 记录不丢 signals、exporter 扫描无 bug（历史缺陷，窗口内触碰相关代码必须复核）。
4. **回放契约**：回放/造数流量 model 参数必须小写 `auto`；大小写混用会绕过 auto 路径——新增测试/工具沿用。
5. **ML/ONNX 配对**：zipmap=False、ORT 版本配对（1.29.0 基准）、词汇表一致性——窗口内模型/词汇表变更需三对齐。
6. **新 RFC 落地一致性**：多维进度评分与成本预设的实现与 RFC v2（~370 行版）一致，不实现 v1 已废弃口径。
7. **灰度门槛**：放量构建 ≥ e8ddff41d；灰度验证报告口径（matched/expired/accuracy）可复算。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] e8ddff41d 单标记半修双 P1 回归：成功终态只透传 IsAutoRequest（TaskType 恒 nil）→ isInternalAutoEntry 兜底把业务 auto 成功轮剔出 session_turns 镜像 + emitTuningSignal 全路径死（门需 TaskType，还读 AutoDecision/AutoConfidence）——并行 R34（01bd55ed7）扩展 propagateIsAutoRequestToEntry 全字段修复、R35 撤并采用并补钉桩；**教训：终态 entry 补 auto 字段必须全字段（单标记传播会翻转 isInternalAutoEntry 兜底语义），且 claim/镜像双门必须共享同一判定（telemetry.IsInternalAutoEntry）**；放量门槛构建随之更新

- [09-16/17] F1 成功终态 outcome 回填缺失 → matched 22→120 / expired 98→0 / accuracy 0→95.8%（闭环断裂，P0 级）— 修复 e8ddff41d（propagateIsAutoRequestToEntry）；放量门槛构建 ≥ e8ddff41d
- [历史] AUTO 路由数据管道三缺陷：promote 漂移丢特征列 / chat 记录丢 signals / exporter 扫描 bug —— 复核基线
- [R30] auto 决策链闭环证实健康（分类→…→回灌双回路有消费方）—— 防回退基准
- [运维] 回放必须小写 auto；154 旁路造数实例为验证环境

## 5. 子代理派发提示词

```text
你是 D11（auto 模型全量实现）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D11-auto-model.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内 auto 链路改动的闭环完整性与终态 outcome 回填覆盖。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
