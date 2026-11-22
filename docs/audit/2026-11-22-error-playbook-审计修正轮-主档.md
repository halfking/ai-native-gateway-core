# 错误处理手册 48h 灰度观察——审计修正轮主档

> **2026-11-22 审计修正轮（goal 会话被叫停后 owner 指令「审计→修正→继续」）**。结论：**48h 观察自始在正常进行，此前本会话产出的 4 份「worker 未启动 / 阻塞 / 需 50 小时」状态文档全部错误，已撤**（mavis-trash）。真问题只有一处生产合规缺口，已修。

## 一、审计发现（3 处，全为本会话自身）

1. **★★★★ 重复循环（最重）**：会话在「3/4 完成，等待决策」上重复输出几十轮，零新证据、零新动作，直到 owner 明确下达审计指令才解套。目标第 4 项的 48h 是**时间窗不是阻塞**，观察自动化早已在跑（`automation-3094bae8`，每 2h 采样，已 11 轮，自带 exit 3 收口逻辑）；把「还没到收口时间」误报成「功能未启动、等待决策」＝把进行中的任务误判为待裁决状态。
2. **★★★ 过早定「阻塞」结论**：22:21–22:31 CST 三次采到 `last_run_at: null` 即断言「worker 从未运行」。实测翻案：settings 是在 12:23–14:18 UTC 窗口才被（并行车）置 true，worker 首跑落在 14:31 UTC 之后；15:35 UTC 实测 `last_run_at: 2026-11-22T15:35:11Z`。**三次 null 只是「还没轮到」，不是「从不轮到」——短窗采样不足以证伪周期性任务**。
3. **★ 量具与日志通道弃用顺序**：SSH 探测失败后未转向本地代码取证（`grep NewWorker/last_run_at` 本地即可定案 worker 是否接线），多绕了一大圈。另：jq 投影对不存在键恒返 null，凡以「null」定罪先看原始响应。

## 二、生产实测翻案（2026-11-22 15:35Z 原始 /stats）

```
last_run_at: 2026-11-22T15:35:11Z          ← worker 在跑
executions.total: 6
  auto_analyze×2 succeeded（entry_id=0）    ← 未知错误→PI 分析→落手册闭环已通
  standard×4 awaiting_approval              ← 全部停在人工闸，零破坏性自动执行
entries_by_source: {seed:5, pi_analysis:2}  ← 条目 7(concurrent)/8(canceled) 为 PI 新落
pending_unknown: {fp_slot_saturated: 4}     ← 距 kind_threshold=5 差 1 次
```

**风险查证**：条目 7/8 曾带 `auto_execute=true`（AGENTS.md 设计：非变更方案 PI 落库默认 TRUE），但全部执行留痕停在 `awaiting_approval`——观察窗内**未发生任何越过人工闸的自动执行**；缺口的性质是「旗子与灰度意图不符的潜在风险」，非既成事故。

## 三、修正动作

- **P0 合规修复**：`PUT /entries/7`、`PUT /entries/8` 置 `auto_execute=false`（只带旗子字段，不触发方案替换），复验 `auto_execute_entries: 0`。生产现役约束达标：`enabled=true / kind_threshold=5 / auto_execute_entries=0 / enabled_entries=7`。
- **工作区净化**：撤 4 份错误状态文档（本档取代），untracked 面收窄到观察 JSONL（自动化在追加，收口时归档）。

## 四、收口路径（无需再等决策）

观察自动化 `automation-3094bae8` 每 2h 采样追加 `docs/audit/evidence/2026-11-22-error-playbook-48h-observations.jsonl`；**距 2026-11-22T01:00+08:00 满 48h（即 11-24 01:00 CST）后采样器 exit 3** → 自动产出收口分析 `2026-11-22-error-playbook-48h-final-summary.md` → 该轮读总结给出误报率代理指标（failed/触发、awaiting_approval/触发、标准执行成功率）并 `CronPauseSelf`。收口轮再补 AGENTS.md ★ 条目与证据 commit。

## 五、通用式

- **「观察 48h」是时间窗不是阻塞**——观察类任务的进行中状态是「等收口」，不是「待决策」；每次想问 owner 之前先查观察器自己的运行状态与收口条件。
- **短窗 null 证不了周期性任务的缺席**——周期任务判活要么等满一个周期，要么读代码定接线（本地 grep 比远程轮询便宜且决定性）。
- **系统自建条目的默认旗子要按轮次约束复核**——「设计默认 TRUE」会顶掉「本轮先关」的显式指令；灰度期开闸前逐条核 `auto_execute_entries`。
