# 2026-07-15 — 路由状态与能力替代影子基础

## 变更

- 新增 `docs/2026-07-15-routing-state-probe-capability-unification.md`，定义状态机、名称契约、SSOT、ProbeCoordinator、能力等级和替代阈值。
- 新增 domain migration `342_routing_state_capability_foundation`：
  - 历史 `availability_state='degraded'` 规范为 `cooling` 并保留 `legacy_degraded` 原因；
  - 历史非法 `health_status` 规范为 `unreachable`；
  - `recent_success_rate` 统一读取 `request_logs_hot`；
  - 创建能力画像、画像审计和 canonical 替代覆盖表。
- 修复管理端和 legacy probe/cycler 的非法状态字面量写入。
- 将凭据级状态变化与连续失败 checker 的候选缓存失效改为 credential 定向失效。
- 新增默认关闭、热加载的 shadow observer：请求成功、失败、流中断和无候选仅记录状态/探测裁决指标，不写入状态、不失效缓存、不调度 probe、不改变 routing winner。

## 配置

以下平台设置默认均为 `off`：

```text
routing_state.coordinator_mode=off|shadow
routing_state.probe_coordinator_mode=off|shadow
routing_state.capability_substitution_mode=off|shadow
```

当前只实现前两个 observer 的影子采集。`authoritative` 和自动替代执行尚未实现；未知模式会 fail-closed 为无操作。

## 245 影子验证

部署 migration 与网关后，先仅设置：

```text
routing_state.coordinator_mode=shadow
routing_state.probe_coordinator_mode=shadow
```

持续至少 24 小时观察：

- `llmgw_routingstate_evidence_total`
- `llmgw_routingstate_probe_decisions_total`
- 路由 p95、无候选率、候选缓存命中与 DB 查询数

不设置 `capability_substitution_mode`，因为首轮尚未将能力候选池接入 routing hot path。任一异常可立即将两个设置改回 `off`；该动作无需重启，且不会回滚既有状态或探测逻辑。
