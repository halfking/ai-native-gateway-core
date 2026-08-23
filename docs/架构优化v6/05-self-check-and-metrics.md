# 05 · 自检与指标（Self-Check & Metrics）

> **目的**：把"完成"从"PR 合了 / HTTP 200 / 目录存在"提升为"可在 Grafana 看到，且对账无漂移"。
> **原则**：每个 SLO 必须有 (1) 定义清晰的指标名；(2) 数据源；(3) 看板；(4) 告警阈值与升级路径；(5) 回归证据要求。
> **不在范围**：不修改 Prometheus / Grafana 现存指标命名（必须新增）；不重写 `deploy/prometheus/rules/` 已有规则。

---

## 1. v6 通用自检流程

每个波次 / 每项任务在标"完成"前必须依次通过：

```text
[1] 代码就绪        go test ./... + golangci-lint run + race detector
[2] 集成测试        至少 1 个真实 PG/Redis（或 testcontainers）集成测试
[3] 文档就绪        docs/04-implementation/changes/YYYY-MM-DD-v6-wN-*.md
[4] 指标就绪        新增 Prometheus 指标 + Grafana 面板
[5] 告警就绪        告警规则落 deploy/prometheus/rules/ + oncall 演练
[6] 回滚就绪        scripts/rollback/v6-wN-<task>.sh + staging 演练一次
[7] 数字审计        任何对外 / 对内承诺的数字必须标注证据等级（DESIGN/LOCAL/REAL/RELEASE）
```

只有 [1]~[7] 全过才允许标注 `RELEASE_READY`；其它状态（`IMPLEMENTED` / `LOCAL_VERIFIED` / `REAL_DEPENDENCY_VERIFIED`）必须在交付物明确。

---

## 2. v6 必落地的 12 个 SLO

> 命名沿用现有 `metrics/` 包前缀（如 `gateway_*`、`stream_*`、`autoroute_*`、`bg_*`）；新增 `slo_*` 与 `v6_*` 两个命名空间。

### SLO-1 · 请求成功率（gateway level）

- **指标**：`gateway_request_total{outcome,protocol,tenant}` / `gateway_request_failed_total{...}`
- **SLO**：99.5%（rolling 30 天），错误预算 0.5%
- **看板**：Grafana `Gateway / Overview / Success Rate by Protocol`
- **告警**：5 分钟成功率 < 99.0% → page
- **数据源**：`request_logs_hot` 派生 + OTel span outcome

### SLO-2 · TTFB P95 / P99（stream）

- **指标**：`stream_ttfb_ms{protocol,model_class}` histogram
- **SLO**：P95 < 1500 ms / P99 < 3000 ms
- **看板**：`Streaming / TTFB`
- **告警**：P95 > 2000 ms 持续 10 分钟 → warn；> 3000 ms 持续 5 分钟 → page
- **数据源**：`domains/streaming/ttfb_tracker.go`

### SLO-3 · 请求 End-to-End P95 / P99

- **指标**：`gateway_request_duration_ms{protocol,outcome,model_class}` histogram
- **SLO**：P95 < 8 s / P99 < 20 s（非流式）；流式按 token 量阈值
- **看板**：`Gateway / Latency`
- **告警**：P99 > 30 s 持续 5 分钟 → page

### SLO-4 · 资源租约对称性（acquire/release）

- **指标**：`v6_lease_acquire_total{resource}` / `v6_lease_release_total{resource}` / `v6_lease_inflight{resource}` gauge
- **SLO**：inflight 偏差（acquire - release）≤ 5% / 1 小时；超 0 即告警
- **看板**：`Lease / Symmetry`
- **告警**：偏差 > 5% → page；持续 30 分钟 → P0
- **数据源**：V6-W1-W8 Candidate lease coordinator

### SLO-5 · 重试预算（attempts 上限）

- **指标**：`v6_retry_attempts_total{request_id_hash,layer}` / `v6_retry_exhausted_total{reason}`
- **SLO**：单请求 attempts ≤ MaxAttempts（默认 5）；超过率 < 0.1%
- **看板**：`Retry / Budget`
- **告警**：超 MaxAttempts 请求数 > 0 → page（critical）
- **数据源**：V6-W1-W5 统一 RetryBudget

### SLO-6 · PII 拦截（FPR / FNR）

- **指标**：`v6_pii_observe_total{type,mode}` / `v6_pii_blocked_total{type,mode}` / `v6_pii_stream_cross_chunk_total{type}`
- **SLO**（observe 模式 2 周）：FPR < 5% / FNR < 5%
- **看板**：`PII / Observe`
- **告警**：FNR > 5% → page（阻止 enforce 模式上线）
- **数据源**：V6-W3-W1 ~ W7 Presidio + zh_pii_rules

### SLO-7 · Maintain / ASM tenant RLS 一致性

- **指标**：`v6_rls_negative_test_total{role,scope,expected}` / `v6_rls_violation_total{role,scope}`
- **SLO**：negative test 100% 通过；运行时 violation = 0
- **看板**：`RLS / Matrix`
- **告警**：任何 violation → page（P0 / security）
- **数据源**：V6-W3-W8 / W9

### SLO-8 · Session V2 shadow freshness / 完整率

- **指标**：`v6_session_v2_freshness_seconds{tenant}` / `v6_session_v2_completeness_pct{field}`
- **SLO**：freshness P95 ≤ 60 s；完整率 ≥ 99%
- **看板**：`SessionV2 / Shadow`
- **告警**：freshness P95 > 300 s 或完整率 < 98% → page
- **数据源**：V6-W1-W6 dual_read_validator

### SLO-9 · 成本对账（cost reconciliation）

- **指标**：`v6_cost_reconcile_diff_pct{tenant,model}` gauge / `v6_cost_reconcile_status_total{status}`
- **SLO**：diff ≤ 0.5% / tenant / day
- **看板**：`Cost / Reconciliation`
- **告警**：diff > 0.5% → warn；> 2% → page
- **数据源**：V6-W2-W3 cost_reconciliation_worker

### SLO-10 · Drain time（systemd / compose / k3s）

- **指标**：`v6_drain_seconds{mode}` histogram / `v6_drain_timeout_total{mode}`
- **SLO**：drain ≤ 30 s（systemd）/ 60 s（compose）/ 90 s（k3s）
- **看板**：`Lifecycle / Drain`
- **告警**：drain 超时 → page（P1）；同一 mode 连续 3 次超时 → page（P0）
- **数据源**：V6-W0-W4 supervisor + V6-W2-W4 drain matrix

### SLO-11 · MCP / A2A / Fusion 安全性（v6-W4 引入后）

- **指标**：`v6_mcp_tools_listed_total{server,tenant}` / `v6_mcp_tool_call_denied_total{reason}` / `v6_a2a_task_total{outcome}` / `v6_fusion_sources_total{decision}`
- **SLO**：denied = 0（除非 tenant policy 显式拒绝）；disabled tool 不可被 list/call
- **看板**：`Protocols / MCP/A2A/Fusion`
- **告警**：任何 disabled tool 被 list/call → page（P0 / security）

### SLO-12 · 文档新鲜度（doc freshness）

- **指标**：`v6_doc_stale_count{doc_class}` gauge（CI 跑 docs-link-checker + 数字审计）
- **SLO**：过期链接 = 0；STALE 标记文档 ≤ 10
- **看板**：`Docs / Health`
- **告警**：过期链接 > 0 → warn；STALE > 20 → page（process）

---

## 3. 自检清单（每个波次 / 每个任务）

| # | 类别 | 检查项 | 验证方式 |
|---|---|---|---|
| Q1 | 代码 | `go test ./... -race -count=1` PASS | CI |
| Q2 | 代码 | `golangci-lint run` 无新增 warning | CI |
| Q3 | 代码 | 新增 / 修改文件 line count 增量 ≤ 500 / 文件；超大文件按 V6-W0 拆分 | code review |
| Q4 | 集成 | 至少 1 个 testcontainers 集成测试（PG + Redis） | CI |
| Q5 | 集成 | 真实 provider mock 调用（`tests/local/python/` 谐振或 fixture） | CI |
| Q6 | 文档 | `docs/04-implementation/changes/YYYY-MM-DD-v6-wN-*.md` 包含 commit / migration / flag / scenario / rows / hash / P50/P95/P99 / 负向测试 / 回滚动作 | PR 模板 |
| Q7 | 指标 | 新增指标在 `metrics/` 包内定义；命名以 `v6_` 或 `slo_` 前缀 | code review |
| Q8 | 看板 | Grafana 看板 JSON 落 `deploy/grafana/dashboards/` 并 import 至对应 Grafana | SRE 评审 |
| Q9 | 告警 | 告警规则落 `deploy/prometheus/rules/`；oncall 演练一次 | oncall 复盘 |
| Q10 | 回滚 | `scripts/rollback/v6-wN-<task>.sh` 存在；staging 演练一次 | SRE 评审 |
| Q11 | 数字审计 | 任何对外 / 对内数字标 `DESIGN/LOCAL/REAL/RELEASE` | docs review |
| Q12 | 风险登记 | `06-risks-and-rollback.md` §3 风险矩阵更新 | TL 评审 |
| Q13 | 跨波次 | 不动 v4 冻结契约（5 identities / 4 lanes / 3 lifecycle / 11 error_kinds） | CI（v4 fixture freeze test） |
| Q14 | 跨波次 | 不修改生产路径的执行语义（仅拆分 / 加固） | code review |
| Q15 | 跨波次 | 不引入新协议独占执行器（除非 V6-W4 范围内） | code review |

---

## 4. v6 交付物模板（docs/04-implementation/changes/）

每个波次 / 每个任务交付物模板（最小可用）：

```markdown
# v6-W<N>-<task> · <标题>

**日期**：YYYY-MM-DD
**分支**：feat/v6-wN-<task>
**证据等级**：IMPLEMENTED | LOCAL_VERIFIED | REAL_DEPENDENCY_VERIFIED | RELEASE_READY
**Commit / Migration / Flag**：
- commit: <sha>
- migration: <number> or N/A
- flag: <feature_flag> or N/A

## 场景与租户范围
- 场景：...
- tenant scope：...

## 回归证据
- request_id / attempt_id / turn_id / charge_id / event_id（hash）
- rows / hash / orphan / duplicate / lag / retry / DLQ 指标
- P50 / P95 / P99 / TTFB / queue depth / resource usage
- 负向测试：tenant A/B、regular/admin/super-admin 矩阵
- replay 输出：...

## 回滚动作
- 回滚脚本：scripts/rollback/v6-wN-<task>.sh
- 回滚演练日期 / staging 副本 ID
- 回滚后对账：...

## 风险登记
- 已识别风险：...
- 缓解：...

## 看板 / 告警
- Grafana 看板：...
- Prometheus 规则：...
```

---

## 5. v6 与现有 SLO 体系的关系

| 现有 | v6 增加 |
|---|---|
| `gateway_*`、`stream_*`、`autoroute_*`、`bg_*`（已存在） | 新增 `v6_*`、`slo_*` 命名空间（仅增不改） |
| `request_logs_hot` 派生指标 | 复用 + 加对账 worker 派生 |
| `metrics/prometheus.go` | 新增 `metrics/slo.go`、`metrics/v6_lease.go`、`metrics/v6_retry.go`、`metrics/v6_pii.go`、`metrics/v6_rls.go`、`metrics/v6_doc.go` |
| `deploy/prometheus/rules/` | 新增 `slo-gateway.yml`、`slo-streaming.yml`、`v6-lease.yml`、`v6-retry.yml`、`v6-pii.yml`、`v6-rls.yml`、`v6-doc.yml` |
| `deploy/grafana/dashboards/` | 新增 `v6-overview.json`、`v6-lease.json`、`v6-retry.json`、`v6-pii.json`、`v6-rls.json` |

不修改任何现有规则与看板命名；不重写指标前缀。

---

## 6. 周期检查

- **每日**：oncall 看 `v6-overview` 看板 + 告警；
- **每周**：TL 看 `v6-doc-health` + 各波次完成度（[`03-roadmap-v6-waves.md` §退出条件](03-roadmap-v6-waves.md)）；
- **每月**：架构组评审全部 v6 交付物；未达 5 完成标准的退回对应波次 Owner。

---

## 7. 异常处理

- 任一 SLO 红色 / 任一波次未达 5 完成标准 → 冻结该波次剩余任务 → 启动根因分析 → 修补后重新跑回归；
- 任一价值反退化信号（见 [`01-core-value-and-positioning.md` §8](01-core-value-and-positioning.md)）→ 立即冻结、回滚、复盘；
- 任一 oncall 演练未通过 → 不能进入下一波次。

---

## 8. 与 V4 契约的关系

- v4 已冻结的 fixture（`test/events/fixtures/identity_*_v1.json`、`restart_lane_*_v1.json`、`lifecycle_states_v1.json`、`error_classification_v1.json`、`session_identity_v1_valid.json`、`restart_semantics_v1_valid.json`、`vocabulary_v1_valid.json`）必须继续 PASS；
- v6 不增加新的 frozen fixture；新增的 SLO / 指标是 v6 私有，不进入 v4 冻结集；
- 任何 V4 freeze test FAIL → 立即回滚 v6 当次 commit + 走 V4 评审流程（不在 v6 内处理）。
