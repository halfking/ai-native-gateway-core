# capability 回填每日出网预算 —— Owner 签核简报（R38）

**决策请求**：批准（或改定）capability_backfill 每日真实出网探测预算的形态与默认值。这是 R34 §四#2 → R36 遗留#3 → R37 遗留#4 一路承继的「Owner 拍板」项——机制已全部落地，只差签核记录。

## 一、现状（机制面，全部已落地）

| 件 | 状态 | 位置 |
|---|---|---|
| 预算闸门（滚动 24h 窗口，出网点记账） | ✅ 93e718873 | bg/capability_backfill.go chargeProbe |
| 默认 2400/天 = 加闸门前的隐式账单（零行为变化） | ✅ | capabilityBackfillDefaultDailyBudget |
| env 覆盖（`LLM_GATEWAY_CAPABILITY_BACKFILL_DAILY_BUDGET`；0/负=显式不设限，非法值回默认） | ✅ | capabilityBackfillDailyBudget |
| kill switch（不重启即可掐任务） | ✅ | 每 tick 重读 |
| 台账 cap 随配置定尺寸 + 触顶截断最旧（不再静默 fail-open） | ✅ R37 修⑥ | probeLedgerCap |
| distlock 蓝绿单跑（配置 Redis 时全家族只有 leader 出网） | ✅ R34 | SetDistLock |
| attempt 退避 1h + 扫描窗口×4 反饥饿 | ✅ R34 | attemptedRecently / scanLimit |
| **Prometheus 指标**（charged / budget_blocked / daily_budget gauge） | ✅ **R38 本轮** | metrics/capability_backfill_budget_metrics.go |

## 二、需要 Owner 拍板的三个选项

1. **维持默认 2400/天（推荐）**：等于旧配置的隐式账单，风险趋零。154 条 eligible 绑定 × staleAfter 6h 的稳态需求约为 616 探/天，2400 有 3.9× 余量；被退避/预算挤掉的行下一轮让位补探。
2. **调大/调小**：看新指标 `rate(llmgw_capability_backfill_probe_budget_blocked_total[1h])` 持续 >0 说明刷新开始落后 staleAfter——届时再调，不必现在猜。
3. **改形态（自然日 / 每绑定配额）**：当前滚动窗消解了时区日界问题；每绑定配额会改变「无证据行让位」语义。无观测证据支持改动前不建议。

## 三、签核即认可的风险边界

- **重启清账**：预算是进程内台账，重启后从零起算（当日至多双倍）。Prometheus counter 的重启重置在面板上可见。
- **多实例交接窗双花**：配置 Redis（distlock）时全家族单跑，无双花；未配置 Redis 时退化为每实例独立预算（与 settle/affinity 同一降级路径）。
- **账单保真边界**：>cap 截断只会低估剩余（保守方向）；旁路出网点记账会被 cap 触顶 Warn 暴露。

## 四、运维判据（本轮指标落地后首次可用）

- `llmgw_capability_backfill_probe_charged_total`：真实出网计数（=账单口径）。
- `llmgw_capability_backfill_probe_budget_blocked_total`：闸门拒绝数；持续增长 = 刷新落后，触发选项 2 复议。
- `llmgw_capability_backfill_daily_budget`：本进程配置值；跨进程不一致 = 配置漂移。

---

**签核**：（Owner 签名 / 日期 / 选定选项）
