# Handoff: 2026-09-21 Standard Model Name Cleanup Follow-up

**Session commit:** (本会话待 commit)
**Previous session commit:** `fd7ada6a5` 列出本轮五项任务
**联续上下文:** [`2026-09-20-standard-model-name-cleanup.md`](../audit/2026-09-20-standard-model-name-cleanup.md)

## TL;DR

承接 fd7ada6a5 的 5 项任务,本会话完成:
- **[高优 1] 23 unmapped -cn 行:定性 + rename 决策 + 修复脚本落档**(未自动执行,等运维审批)
- **[高优 2] 9 行 due-diligence:运营方联系人 + 证据收集清单 + 追踪表落档**(需 ops 团队触达)
- **[中优 3] 154/245 共享库实测**:7 组新回种 dot/dash 全部 fold,pm_refs 守恒 54,dedup=0 ✓
- **[低优 4] R51 长期建议落档**:SAVEPOINT/verify-after-commit/索引/慢表策略
- **[低优 5] 周期 health check 已挂 cron**(每 6 小时,自动化 ID 验证存在)

## 1. 23 unmapped -cn 行决策(任务 1)

### 1.1 复核结论(与上轮假设不同)

上轮假设:`-cn` 是「中国区域版」,需 seed 缺失的 base 再 fold。
本轮定性:`-cn` 在这些家族里**只是 discovery/provider_refresh 上游报回的多余字符,本身不是「区域」语义**。证据:
- 19 个 base canonical 在本地 + 252 双库均不存在
- 所有 alias 都是 -cn 形式(纯横线/点/下划线变体)
- 0 流量 + 0 work_routes + 0 业务 alias 解析
- `claude-fable-5`(另一条活体 -cn 链)有 1234 req/7d 证明 -cn 后缀 ≠ 死流量

### 1.2 决策:rename 路径 C

| 维度 | 路径 A | 路径 B | 路径 C(选定) |
|---|---|---|---|
| canonical_id 是否变 | 是 | 否 | 否(同 id) |
| pm_refs 是否动 | 重指到新 base | 重指到 default | **不动** |
| 旧 -cn 名还解析 | 是(deprecated alias) | 否 | 是(deprecated alias 留底) |
| 副作用风险 | 中 | 高 | **低** |
| 前向兼容 | 需补代码 | 需补代码 | **自动(JunkSeedGuardSQL 已会抑制)** |

理由 1:0 流量 + 0 work_routes 证明没有路由层依赖 -cn 后缀,改名不会断流。
理由 2:id 不变 = pm_refs/历史 cache/外键引用全部不动,变更面最小。
理由 3:JunkSeedGuardSQL 已能在下次 auto_discovered 时抑制再次 seed。

### 1.3 落档物

- [`docs/audit/2026-09-21-unmapped-cn-decision.md`](../audit/2026-09-21-unmapped-cn-decision.md) — 完整决策分析 + 部署清单
- [`sql/fixes/2026-09-21-unmapped-cn-rename.sql`](../sql/fixes/2026-09-21-unmapped-cn-rename.sql) — 幂等事务化修复脚本(per-row SAVEPOINT + COMMIT-after SELECT verify,**已采用 task 4 长期建议**)

### 1.4 执行决策

- **本地 .34**:脚本落档,**待运维审批后执行**(本会话未跑)
- **252**:4 行 252-only(deepseek-v3.1-cn / kling-v1-5/1-6/2-5-turbo-cn)单独 disable 段在脚本末尾,启用前需 uncomment
- **154 / 245**:binary 仍未追到 92f18cf22 之前,**不执行**(discovery 仍在回种),与 task 3 一并等 binary 部署

## 2. 9 行 due-diligence(任务 2)

### 2.1 复核状态(2026-09-21 09:33 实测)

| # | canonical | pm_refs | req_7d | req_all | 状态 |
|---:|---|---:|---:|---:|---|
| 1 | `claude-fable-5` | 8 | **1234** | **2310** | **活体,高危** |
| 2 | `claude-fable-5-1` | 4 | 25 | 33 | 活体 |
| 3 | `claude-fable-5-thinking` | 1 | 0 | 0 | 静默 |
| 4 | `claude-fable-latest` | 1 | 0 | 0 | 静默 |
| 5 | `claude-opus-4.1` | 2 | 0 | 0 | 静默 |
| 6 | `claude-opus-4.7-fast` | 1 | 0 | 0 | 静默 |
| 7 | `claude-opus-4.8-fast` | 1 | 0 | 0 | 静默 |
| 8 | `claude-opus-5-fast` | 1 | 0 | 0 | 静默 |
| 9 | `grok-4.3` | 2 | 0 | 0 | 静默 |

### 2.2 落档物

[`docs/audit/2026-09-21-due-diligence-tracker.md`](../audit/2026-09-21-due-diligence-tracker.md) — 每行的:
- provider 联系人(OpenRouter 6 行 / Vapeur 3 行 / apiclaude 2 行 / zhima 2 行 / Evol 2 行 / 速云 2 行 / maishouai 1 行)
- 证据收集问题(Anthropic 邀请链接 / Preview 文档截图 / typo 确认等)
- 决策动作三选一(保留 / 重指后 disable / quarantine)
- 时间窗(09-21~09-28 发问,09-29 批量处理)

### 2.3 行动要求

由 ops 团队:
- OpenRouter(21)6 行 → 一次英文工单问完
- 其余 6 家中文 provider → 打包 5 个中文问题集中发送
- 9-29 后批量执行 disable 或保留

## 3. 154 / 245 三库治理(任务 3)— **已执行**

### 3.1 实测发现

- **154 + 245 都用同一 DSN 172.16.2.210:5432**(shared 252 PG)
- 154 binary `2.5.6-90ecd6a6-20260920-2160` 与 245 `2.5.6-90ecd6a6-20260920-2159` **HEAD = 90ecd6a60 < 92f18cf22**(git merge-base 验证 NOT ancestor)
- 因此 154/245 的 discovery worker **仍在周期性回种 dot/dash 双拼**
- 本次实测在 shared 库捕获 7 组新回种:glm-4-7/-.7、glm-4-5-air/-.5-air、glm-5-2/-.2、glm-5-3-flash/-.3-flash、deepseek-v3-2/-.3.2、deepseek-v4-1-flash/-.4.1-flash、doubao-1-5-ui-tars/-.5-ui-tars

### 3.2 已执行动作

通过 154 SSH 隧道重跑 `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql`(加 `SET statement_timeout = 0;` 防 ursm 2083 万行无索引超时)。

| 指标 | 跑前 | 跑后 | delta |
|---|---:|---:|---:|
| models_canonical 总数 | 933 | 926 | -7 |
| active | 787 | 780 | -7 |
| disabled | 146 | 146 | 0 |
| **dot/dash 双拼组** | **7** | **0** | **-7** ✓ |
| model_aliases | 2701 | 2632 | -69(deprecated) |
| pm_refs 守恒 | 54 | 54 | 0 ✓ |

V1-V8 验证查询全部通过(脚本尾部输出确认)。

### 3.3 踩坑留档(给 R51)

dedup-cleanup 在 `ursm_node_snapshot_min` UPDATE 处第一次超时失败(2083 万行无 canonical_name 索引,**且 EXECUTE 在同 DO 块里无 SAVEPOINT 隔离,超时回滚整段事务**)。第二次成功加 `SET statement_timeout = 0;`,整个脚本跑完 ~90 秒。

### 3.4 落档物

- [`docs/audit/2026-09-21-154-245-coverage.md`](../audit/2026-09-21-154-245-coverage.md) — 完整覆盖记录 + 长期策略

### 3.5 下一步

- 154 / 245 binary 升级到 ≥ 92f18cf22(待 ops 运维窗口)
- 升级前每周一次复查 dup_groups;> 0 则重跑 dedup-cleanup(本会话脚本验证可重入)

## 4. Phase 4 Transaction Ordering 长期建议(任务 4)

### 4.1 三处 bug 共同模式

| 日期 | bug | 共同点 |
|---|---|---|
| 2026-09-20 | -ga 行 status 未 persist | 「事务边界即真相」违反 — UPDATE 报告说改 N 行,SELECT 后未持久 |
| 2026-09-20 | qwen3-max-cn disable 被 defensive guard 拒 | 同上 |
| 2026-09-21 | ursm 2083 万行 UPDATE 超时回滚 | 长事务无 SAVEPOINT 隔离,EXECUTE 失败回滚整段 |

### 4.2 落档物

[`docs/audit/2026-09-21-phase4-tx-ordering-rec.md`](../audit/2026-09-21-phase4-tx-ordering-rec.md) — R51 范围:
- P1 给 ursm_node_snapshot_min 加 `lower(canonical_name)` 索引(migration 732)
- P1 改写 canonical-dedup-cleanup.sql §2 DO 块为 per-EXECUTE SAVEPOINT
- P1 改写 §7 DELETE 段为 per-snapshot SAVEPOINT + COMMIT-after verify
- P2 统一所有 sql/fixes/ 清理脚本的 verify-after-commit 模板
- P3 SQL lint 检测裸 UPDATE 缺配套 SELECT-after-COMMIT

### 4.3 已有正面案例

- `sql/fixes/2026-09-21-unmapped-cn-rename.sql`(本会话)— 已采用 per-row SAVEPOINT + COMMIT-after SELECT 断言,可作为 R51 改写模板。

## 5. 周期 health check(任务 5)— **已挂 cron**

### 5.1 自动化

- **Cron ID:** `automation-f64755bf-a352-4777-b0de-dfc37147e1a0`
- **频率:** 每 6 小时(`0 */6 * * *`)
- **入口:** `scripts/cron/check-canonical-health.sh`
- **基线:** .34 active=887 / shared active=780 / dup_groups=0
- **告警阈值:** active 行数漂移 > 10

### 5.2 行为

| 退出码 | 含义 | 动作 |
|---|---|---|
| 0 | HEALTHY | 仅日志 |
| 1 | local Suspects > 0 或 shared dup > 0 | 自动重跑对应 dedup 脚本 |
| 2 | 基础设施问题(SSH / DSN 不可达) | 仅日志,转人工 |

### 5.3 验证

`CronList` 工具验证:automation 已 enabled, lifecycleStatus=active, recurring=true, runCount=0, nextRunAt=已排程。

## 6. 改动清单

```
A  docs/audit/2026-09-21-unmapped-cn-decision.md      | 任务 1 决策分析 + 部署清单
A  sql/fixes/2026-09-21-unmapped-cn-rename.sql        | 任务 1 rename 脚本(未执行)
A  docs/audit/2026-09-21-due-diligence-tracker.md     | 任务 2 追踪表
A  docs/audit/2026-09-21-154-245-coverage.md          | 任务 3 覆盖记录
A  docs/audit/2026-09-21-phase4-tx-ordering-rec.md    | 任务 4 R51 范围
A  scripts/cron/check-canonical-health.sh             | 任务 5 健康检查脚本
A  docs/handoff/2026-09-21-canonical-name-followup.md | 本文件
```

(仓库内未含: cron automation 配置 — 在 zcode runtime 注册,通过 CronList 可查。)

## 7. 下一轮接力建议

| 优先级 | 任务 | 等什么 |
|---|---|---|
| 高 | 触达 ops 团队跑 unmapped-cn-rename.sql(本地优先) | 运维审批 |
| 高 | 9 行 due-diligence 联系人发问 | ops 团队动作 |
| 中 | 154 / 245 binary 升级 ≥ 92f18cf22 | 运维窗口 |
| 低 | R51:加 ursm 索引 + 改写 dedup-cleanup 为 SAVEPOINT | R51 周期 |
| 持续 | cron 健康检查每 6h 跑,告警转 ops | 已挂,自动 |

## 8. 已知未做的事(本会话明确不触)

- **不在 shared 库跑 unmapped-cn-rename.sql** — 154/245 binary 仍在回种 -cn 行(同根因),与 task 3 联动,等 binary 升级后统一处理
- **9 行 due-diligence 行 status 不动** — 必须 ops 团队触达 provider 后才有决策依据
- **discovery 剥 -cn 后缀的代码根修** — R51 范围,本轮不抓(避免打断 ops 审核流程)