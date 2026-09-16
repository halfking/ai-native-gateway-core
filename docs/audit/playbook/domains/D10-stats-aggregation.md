# D10 — 数据统计与聚合模块

> 领域编号: D10 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：数据统计模块（用量/凭据/路由/模型维度的统计与聚合表）、聚合 worker、统计对账（影子读/对账工作流）、仪表盘数据口径。
**不管**：节点状态计数（D09）；分区存储机制（D07）；前端控件复用（D15）。

## 2. 参考基线

设计文档：
- `docs/stats-reconciliation.md` — 统计对账工作流（API/DB schema/影子读切换）
- `docs/03-design/02-feature-design/design/DASHBOARD_V2_TECHNICAL_DESIGN.md` / `DASHBOARD_V2_IMPLEMENTATION.md` / `DASHBOARD_API.md`
- `docs/metrics-catalog.md` — 指标目录

代码入口：
- `domains/stats/`、`bg/`（routing_feedback_log 5 分钟聚合 worker、metrics 聚合器）
- `exporter/`、`telemetry/`、`metrics/`、`monitoring/`、`internal/observability/`
- 只读端点：`GET /api/admin/routing-opt/metrics`（R32 落地）

## 3. 检查清单

1. **聚合正确性**：聚合 worker 双实例不双计（distlock 选举 + advisory lock 兜底基准）；NULL 维度 global 行唯一性；窗口内新增聚合维度沿用同一防重模式。
2. **写必有读**：新聚合表/新指标必须有消费方（API 端点/仪表盘/告警）；写-only 表 = 登记债（R30 routing_optimization_metrics 教训，R32 已补读端点）。
3. **对账通道**：统计口径变更时影子读/对账工作流可执行；新增统计字段进入对账清单。
4. **口径一致**：同一指标在 /metrics、admin API、仪表盘三处口径一致（时间窗、去重键、时区）；上海时区 vs UTC 的分组边界显式钉扎。
5. **有界**：聚合查询 LIMIT/时间窗 clamp（hours 1..720 基准）防护；大表聚合走分区裁剪。

## 4. 历史回归点（轮末回注区）

- [R30] 聚合器 DELETE+重插 NULL 维度 global 行双实例重复 → SUM 双倍 — 修复 f19ba5d5a（advisory_xact_lock）
- [R32] routing_optimization_metrics 写-only 债收口：挂只读端点 + 聚合器 distlock 选举（40e198c60）；hours clamp 1..720
- [09-16] P2.2 反馈聚合：routing_feedback_log 5min 聚合落 metrics 表（2e8bfdce1）——本域消费链基准

## 5. 子代理派发提示词

```text
你是 D10（数据统计与聚合）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D10-stats-aggregation.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增统计/聚合产物的防双计与消费方。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
