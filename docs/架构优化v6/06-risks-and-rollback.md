# 06 风险登记册与回滚手册（v6）

> 范围：`docs/架构优化v6/01–05` 已经识别的工作项、热思路与自检指标，本文件负责回答"哪些会坏、坏了怎么定位、怎么回滚、什么信号必须停手"。
> 不替代 `docs/security/`、`docs/runbooks/`、`docs/会话优化v4/` 中已有的具体 runbook，只做总册与跨域协调。

## 0. 阅读对象与使用方式

- TLDR：本文件第 1 节是 10 条最高级风险，第 2 节是按 Wave 的风险矩阵，第 3 节是回滚手册（命令级 / 配置级 / 代码级），第 4 节是熔断与暂停触发条件，第 5 节是 Owner 与复盘节奏。
- 使用方式：
  - 在 03 的每个 Wave 启动 PR 前，把对应小节当作"准入检查"。
  - 灰度期间每天对照第 4 节检查指标，超阈值即触发降级。
  - 出现 P0 故障时按第 3 节的顺序回滚，而非按记忆操作。

## 1. 最高级风险 Top‑10

每条都给出"出现概率 / 影响面 / 信号 / 首要 Owner / 默认响应"，用于在周会上快速对齐。

| # | 风险描述 | 概率 | 影响 | 信号 | Owner | 默认响应 |
|---|---|---|---|---|---|---|
| R1 | **插件运行时把持编译 executor**（`plugin.Plugin` 误把 `CompilePipeline`/`ExecuteStream` 当自己的接口替换） | 中 | 高 | `executor_owner` 指标不再是 100% gateway；调度日志出现非 gateway 编译事件 | 编排 Owner | 立即回滚到无插件模式；以 `EXEC_OWNER_REQUIRED=gateway` 启动开关校验 |
| R2 | **契约冻结 v4 被破坏**（错误的字段重命名、JSON Schema 调整绕过 review） | 低 | 高 | `conformance` Job 红；`policy_versions` 出现非预期回滚；下游 SDK 投诉 | 平台架构 | 停掉发版；用 v4→v5 兼容层桥接；除非走"freeze exception"工单 |
| R3 | **PII/RLS 在多租户场景下漏过**（跨租户查询、未带 `tenant_id` 的调度、日志回显租户外 ID） | 中 | 极高 | `rls_audit` Job 红；客户报告"我看到别人数据" | 安全 Owner | 立刻关闭对应租户入口；启动 runbook `docs/security/rls-leak.md`；48h 内客户通告 |
| R4 | **凭证泄露**（admin token / 客户 API key 出现在日志或 metrics label） | 中 | 极高 | `secret_scan` Job 红；`/admin/audit` 中出现 `redact=false` 命中 | 安全 Owner | 轮换全部受影响凭证；按 `docs/runbooks/secret-rotation.md` 走；定位写入点并加 `redact` |
| R5 | **流式分发在多副本下重放**（streaming disconnect/reconnect 导致上游收到重复请求，账单被双倍计） | 中 | 高 | 上游 provider 报告 duplicate request；账单对账 `delta > 5%` | 编排 Owner | 关闭受影响 provider；按"幂等键 + provider 去重窗口"补救；发退款工单 |
| R6 | **Postgres 列存/分区漂移**（`pg-columnar-auto-rotate` 周期内未正确切换，导致审计查询扫全表） | 中 | 中 | `pg_stat_statements` 中 `idx scan` 命中率断崖；`partition_lag` 指标超阈值 | DBA Owner | 立即停止 rotate 周期任务；手动 `ANALYZE` 涉及表；回滚到上一分区模板 |
| R7 | **流式 hot loop / OOM**（dispatch worker 反馈回环、缓冲无限增长） | 中 | 高 | Pod `RSS > 80% limit`；`stream_backpressure_drops` 计数飙升；`p99 TTFT` 翻倍 | 平台架构 | 立即缩容到 1 副本排障；按 `streaming-error-root-cause.md` 抓 `pprof`；上熔断 |
| R8 | **发散建议落地引入隐性耦合**（例如"工具路由 DAG"和"插件编排"争抢同一中间状态） | 中 | 中 | 同一指标被两套代码改写；集成测试出现 race | 平台架构 | 短期只允许其一上线；后续在 `internal/orchestration/api.md` 上明确边界 |
| R9 | **可视化/PII 双写**（dashboard 把 tenant_id 当 label 暴露到前端） | 中 | 高 | 前端抓包出现 `tenant_id`；前端 PR 评审漏过 | 安全 + 前端 | 立即下线 dashboard 对应模块；前端改 `role-scoped fetch`；事后补 UI 自动化扫描 |
| R10 | **指标/告警噪声**（指标过多、自检 Job 自我触发 → 团队疲劳 → 真告警被忽略） | 中 | 中 | 一周内 OnCall 收到 > 200 条同源告警；首次响应时间 > 30min | SRE | 启动"指标减肥"两周专项；按 05 的"告警降噪自检"清单逐项处理 |

> 这 10 条与 04 的"避免清单"高度对齐（Wasm/OPA、eBPF 内核依赖、prompt‑level 微调、模型输出审核、新协议独占 executor），但本表强调 **"如果不小心走到了这一步，怎么办"**。

## 2. 按 Wave 的风险矩阵（来自 03）

每个 Wave 都列出"上线前必须就位的止血点 / 灰度策略 / 回滚开关"。详细步骤见第 3 节。

### Wave 2‑B（编排插件化）

- 风险：
  - 插件运行时把持编译 executor（→ R1）。
  - 插件与现有 `executor_dispatch.go` 的中间状态竞争（→ R8）。
- 止血点：
  - `EXEC_OWNER_REQUIRED=gateway` 启动开关；插件编译必须 `passthrough=true`。
  - 引入"两阶段编译"：插件只产出 IR，gateway 完成最后编译。
- 灰度：先 1% 流量、24h 后 10%、72h 后 50%，全程监控 `executor_owner` 与 `compile_latency_p99`。
- 回滚开关：feature flag `orchestration.plugin.enabled=false`；插件模块单独 build tag。

### Wave 2‑C（仪表板 & 可观测性）

- 风险：
  - PII 暴露到 dashboard（→ R9）。
  - 指标/告警噪声（→ R10）。
- 止血点：
  - 所有 dashboard 数据源走 `redacted_view`，禁用 raw log。
  - 每个新指标必须配 SLO，否则不接告警（05 自检清单）。
- 灰度：内部租户先开；外部租户按 RLS 角色显式放行。
- 回滚开关：flag `dashboard.enabled=false`；dashboard router 仅返 503。

### Wave 3‑A（凭证治理 + RLS 全量）

- 风险：R3、R4。
- 止血点：
  - 迁移前对全表跑 `redact_audit`，所有命中必须在迁移工单中显式列出。
  - 所有新 admin API 默认 `redact=on`；调用方只能拿到 `redacted_id`。
- 灰度：先 5% 租户；夜间窗口迁移；保留 30 天 dual‑read。
- 回滚：保留旧视图 30 天；旧视图降级标记 `deprecated_audit=true`。

### Wave 3‑B（流式分发韧性）

- 风险：R5、R7。
- 止血点：
  - 幂等键必填；`provider_dedupe_window` 默认 60s。
  - backpressure 必须丢弃而不是无限缓冲；默认上限来自 04 的"per‑tenant concurrency"。
- 灰度：内部租户 → 头 10 名外部租户 → 全量。
- 回滚：flag `streaming.idempotency.required=false`（仅紧急情况允许）；备份配置为旧版 dispatch。

### Wave 3‑C（DB 列存 + 分区）

- 风险：R6。
- 止血点：
  - rotate 必须先 `ANALYZE` 再切；切完立刻跑一致性比对（row count + checksum）。
  - `partition_lag` 告警阈值收紧到 1h。
- 灰度：先 audit 子集，再扩大到 billing，再扩大到全量。
- 回滚：保留 7 天"上一代"分区模板；一键 `pg-columnar-rollback`。

### Wave 4‑A（发散思路·工具路由 DAG）

- 风险：R8。
- 止血点：先以只读模式上线（仅消费、不写）；与编排插件严格接口隔离。
- 灰度：1% → 10% → 100%，每级至少 72h。
- 回滚：flag `tool_router.dag.enabled=false`；移除 IR 消费即可。

### Wave 4‑B（发散思路·OpenTelemetry & 语义约定）

- 风险：R10。
- 止血点：新语义属性必须先入 `docs/observability/semantic-conventions.md`，否则禁止上线。
- 灰度：与 2‑C 同步；先内部租户。
- 回滚：保留旧 Prometheus metric 名 6 个月。

### Wave 4‑C（发散思路·成本可观测 & FinOps）

- 风险：账单对账 `delta > 5%`（间接触 R5）。
- 止血点：上线前必须先有 14 天"成本基线"，否则不允许任何 `cost_alert`。
- 灰度：内部租户 first；对账偏差超过阈值即冻结 FinOps 写。
- 回滚：禁用 `cost_export` 写路径；保留只读查询。

## 3. 回滚手册（按层级）

每条命令/配置都假定运维已经读过 `docs/runbooks/` 里的通用前置（K8s 命名空间、Vault 路径、PG 集群入口）。这里只写"v6 专有"动作。

### 3.1 配置级回滚（最快，0–5 分钟）

- 通过 `feature flag`（仓库 `internal/featureflag/`）关闭：
  - `orchestration.plugin.enabled`
  - `dashboard.enabled`
  - `streaming.idempotency.required`
  - `tool_router.dag.enabled`
  - `cost_export.write.enabled`
- 命令模板（示例，按实际环境替换）：
  - `vault kv put secret/gateway/featureflag orchestration.plugin.enabled=false`
  - `kubectl -n gateway rollout restart deploy/gateway`
- 适用场景：指标刚刚飘红、还不能定位根因。

### 3.2 代码级回滚（5–30 分钟）

- 走 GitOps（Argo CD/Flux）：把 `gateway` App 回滚到上一个 `Revision`，drift 检测会在 5 分钟内同步。
- 若涉及 DB migration：
  - 必须先 `pg-columnar-rollback` 切回旧分区模板（Wave 3‑C）。
  - 不能回滚的 migration 走 `down.sql` 但必须先在 staging 演练；记录在工单中。
- 适用场景：确认是本次发版引入的回归。

### 3.3 数据级回滚（30 分钟–24 小时）

- 凭证轮换：按 `docs/runbooks/secret-rotation.md`；受影响的 `api_keys`、`oauth_tokens` 写 `revoked_at=now()`。
- 列存/分区漂移：用上一代模板 + `pg_restore` 把 audit 表回滚；同步通知下游消费方。
- 双写回退：所有 dual‑read 表保留旧视图 30 天（Wave 3‑A）。
- 适用场景：R3、R4、R6。

### 3.4 跨域协同回滚（紧急，0–60 分钟）

- 触发条件：客户可见的 P0（账单错误、跨租户泄露、流式大范围 5xx）。
- 流程：
  1. 立即冻结新发版（GitOps pause）。
  2. 关闭对应租户的入口（`tenant_quota` 调 0，或在 edge 返 503）。
  3. 拉跨域 war room：编排 / 安全 / DBA / SRE 同时在线。
  4. 沟通模板：`docs/security/incident-comms.md`，包含 24h 内对客户的书面通告。
  5. 48h 内交付 RCA（见第 5 节）。
- 适用场景：R3、R4、R9。

## 4. 熔断与暂停触发条件（硬阈值）

阈值与 05 的指标一一对应；命中即"立刻降级 / 暂停 / 复盘"。

| 触发条件 | 阈值 | 行动 | Owner |
|---|---|---|---|
| `executor_owner != gateway` | > 0% | 立即停插件模式；按 R1 回滚 | 编排 |
| `rls_audit.fail` | > 0 | 关闭对应租户入口；安全 war room | 安全 |
| `secret_scan.hit` | > 0 | 凭证轮换；定位写入点 | 安全 |
| `p99_latency_ms`（流式 TTFT） | > 上一基线 2× | 触发 backpressure；缩容排障 | 平台 |
| `stream_backpressure_drops` | > 5% 请求 | 关闭对应 provider；R5 流程 | 编排 |
| `partition_lag` | > 1h | 停止 rotate 周期；手动对齐 | DBA |
| `dashboard.pii_label.hit` | > 0 | 下线对应模块；前端补扫描 | 安全 + 前端 |
| `alert_noise.week_count` | > 200 | 启动"指标减肥"专项 | SRE |
| `cost_reconcile.delta` | > 5% | 冻结 FinOps 写；对账工单 | FinOps |
| `conformance.fail` | > 0 | 暂停发版；走 freeze exception | 平台架构 |

> 阈值在 05 的自检脚本里有"基线采集"，每月自动校准一次。若业务流量季节性变化，需要在变更窗口同步更新阈值。

## 5. Owner、节奏与复盘

### 5.1 Owner 表

| 角色 | 职责 | 备份 |
|---|---|---|
| 编排 Owner | R1、R5、R7、R8 | 平台架构 |
| 安全 Owner | R3、R4、R9 | SRE |
| DBA Owner | R6 | 平台架构 |
| SRE | R10 | 编排 |
| 平台架构 | R2、R8、跨域协调 | DBA |
| FinOps | 账单对账相关 | DBA |

### 5.2 节奏

- **每周**：风险登记册 review（30 分钟）；聚焦本周末要上的 Wave 子项。
- **每次灰度升级前**：必须填"准入 checklist"（基于本文件第 2 节）。
- **每次 P0 之后**：48h 内 RCA；模板见 `docs/runbooks/rca-template.md`；同步更新到本文件。
- **每季度**：重做一次 Top‑10 排序；剔除已闭环、纳入新识别风险。

### 5.3 RCA 必须回答的 5 个问题

1. 我们到底要达成的业务结果是什么？（避免"为了发版而发版"。）
2. 这次故障离这个业务结果偏离了多少？影响多少人/多少钱？
3. 我们的检测-响应-恢复链路里，**最先**断在哪一环？
4. 我们本来可以多做哪一件事来预防？（关注"信号早于故障"的指标。）
5. 这次学到的最大教训会改变哪一条流程或哪一份文档？（必须落到具体文件。）

## 6. 与现有 runbook 的对接

- `docs/runbooks/streaming-error-root-cause.md` → R5、R7。
- `docs/runbooks/secret-rotation.md` → R4。
- `docs/runbooks/incident-comms.md` → 第 3.4 节。
- `docs/security/rls-leak.md` → R3。
- `docs/audit/` 最近一次审计的 follow‑ups → 本文件每次更新要 diff 一次。
- `docs/会话优化v4/` 中的契约冻结清单 → R2 的"freeze exception"工单模板来源。

> 不复制现有 runbook 内容；本文件只做"指针 + 总册"。具体步骤仍以最新版本 runbook 为准。

## 7. 自检：本文件本身是否仍然有效

每季度 / 每次大 Wave 完成后，回答以下问题；任一答案为否，立即修订：

- [ ] Top‑10 是否仍然覆盖了 04 的所有"避免清单"反例？
- [ ] 第 2 节的 Wave 列表是否与 03 的最新路线图一致？
- [ ] 第 3 节的回滚命令是否仍能在当前 GitOps 流程下执行？
- [ ] 第 4 节的阈值是否与 05 的指标一一对应？
- [ ] 第 5 节的 Owner 是否仍然在岗且接受职责？

---

> 维护原则：本文件不是"事后记录"，而是"事前约束"。任何 Wave 上线前，PR 必须引用本文件第 2 节对应小节，并在 PR 描述里回答"我们的止血点 / 灰度策略 / 回滚开关分别是什么"。
