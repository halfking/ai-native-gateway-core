# 架构优化 v6 — 文档总览

> **版本**：v6.0（2026-08-22 起草，对照代码基线 `cmd/gateway` 当前工作树）
> **读者**：架构组 / 后端 Owner / DevOps / SRE / 安全 / 数据 / 前端
> **范围**：在 [`docs/03-design/01-architecture/architecture/ARCHITECTURE.md`](../03-design/01-architecture/architecture/ARCHITECTURE.md)、[`optimization-roadmap.md`](../03-design/01-architecture/architecture/optimization-roadmap.md)、[`runtime-request-flow.md`](../03-design/01-architecture/architecture/runtime-request-flow.md)、[`routing-and-state.md`](../03-design/01-architecture/architecture/routing-and-state.md)、[`omniroute-integration-boundary.md`](../03-design/01-architecture/architecture/omniroute-integration-boundary.md)、[`REPO_LAYOUT.md`](../03-design/01-architecture/architecture/REPO_LAYOUT.md)、[`armor-sdp-feasibility.md`](../03-design/01-architecture/architecture/armor-sdp-feasibility.md) 与 [`会话优化v4/CONTRACT_FREEZE_2026-08-22.md`](../会话优化v4/CONTRACT_FREEZE_2026-08-22.md) 的基础上，重新整理"事实→方案→门禁→发散"。
> **不在范围**：不重写已存在的顶层架构 ADR；不替换 [`docs/会话优化v4/`](../会话优化v4/) 已冻结的契约；不修改 `cmd/gateway` 源码或 SQL 迁移。
> **证据等级**：`DESIGN` + `LOCAL_REVIEWED`（基于工作树只读勘察，未在生产复跑）。

---

## 0. TL;DR

`llm-gateway-go` 是一个 **Go 单二进制 + 进程内 Vue3 控制面**的企业级 LLM 网关，承载 6 大协议适配、URSM 候选路由、P2C/Bandit 凭据调度、node probe、telemetry/audit、Maintain/ASM 投影。代码体量 ~75 万行 Go，56 个 `domains/` 子包、62 个 `internal/` 子包、200+ 后台 worker、两套并行 migration 系统、双 routing 实现。

**v6 的目的不是重画系统，而是解决三类长期债务**：

1. **结构债**：`cmd/gateway/main.go` 6156 行 + `domains/streaming/handler.go` 8329 行 + `domains/streaming/executors/executor.go` 3342 行单点超大文件（2026-08-24 `wc -l` 快照，起草时为 6076/8266/3308，持续小幅增长）；`domains/credential*` 与顶层 `credentialhealth/`、`credentialfpslot/` 重叠职责；`autoroute/` 三套并行 scoring（`scoring.go` / `scoring_new.go` / `scoring_simplified.go`）。
2. **正确性债**：retry budget 多层叠加（TPM/RPM 资源 acquire/release 分步、stream retry 与 survival 与 dispatch 重复累积 attempts）、session V2 仍是 shadow owner、Maintain/ASM RLS 仅声明、未实证 tenant GUC 缺省 fail-closed。
3. **能力债**：MCP/A2A/Fusion 三个 "TARGET/PARTIAL" 协议在仓库里有目录但无完整 transport 与生产接线；OpenTelemetry GenAI semantic conventions、eBPF L7 观测、prompt 语义缓存、模型级联路由等 2024-2026 行业基线尚未落地。
4. **运行闭环债**（2026-08-25 增补，D28–D34）：hot 表 8h 存储闭环未完全统一（LP1 未 apply、两张表无转移 worker、分区表残留写路径）；同一对象多处存储（双 waterfall 环、V1/V2 双缓存）；上传导入缺版本管理；打包位宽回绕（2038/45 天）；网络流读缺 stall watchdog；前端 9 套手写抽屉/97 处直接 ElMessage 与性能指标上报缺失。

**v6 的策略**：分 6 波次（V6-W0 → V6-W5）从"门禁→拆分→契约→编排→协议→智能"逐层推进；每波次独立 commit、独立可回滚、独立证据；不动 v4 已冻结契约与生产 wiring。

---

## 1. 文档结构

| # | 文档 | 主题 | 一句话目标 |
|---|---|---|---|
| 01 | [`01-core-value-and-positioning.md`](01-core-value-and-positioning.md) | 核心价值与定位 | 把"为什么这是企业级网关，不是另一个 LiteLLM"讲清楚，并给出对外不可替代的 6 项能力。 |
| 02 | [`02-code-vs-design-deltas.md`](02-code-vs-design-deltas.md) | 设计 vs 代码对照 | 列出 34 处文档承诺但代码未到位的差距（D1–D27 起草与复审；2026-08-25 四向审计增补 D28–D34：存储闭环/内存/健壮性/前端一致性），给出每个差距的状态标记与回归证据要求。 |
| 03 | [`03-roadmap-v6-waves.md`](03-roadmap-v6-waves.md) | v6 路线图 | V6-W0 ~ V6-W5 六波次拆分，每波次目标 / 允许文件 / 退出条件 / 风险等级。 |
| 04 | [`04-hot-ideas-and-divergent-suggestions.md`](04-hot-ideas-and-divergent-suggestions.md) | 网上热门思路 + 发散 | 引入 MCP/A2A fusion、eBPF L7、OTel GenAI semantic conventions、semantic cache、cascade router 等行业基线；并列出 8 项发散型尝试。 |
| 05 | [`05-self-check-and-metrics.md`](05-self-check-and-metrics.md) | 自检与指标 | 把"完成"定义成可观测事实：HTTP 200 不算完成，给出 12 个 SLO 与对应面板/告警。 |
| 06 | [`06-risks-and-rollback.md`](06-risks-and-rollback.md) | 风险与回滚 | 不在文档里写"我们有信心"，写"在 X 场景下怎么退"。 |
| 07 | [`07-audit-report-2026-08-24.md`](07-audit-report-2026-08-24.md) | 审计报告 | 2026-08-24 对照审计：credentialfpslot 修复记录、文档事实修正清单、测试证据、未解决项移交。 |
| 08 | [`08-dispatch-executor-loop.md`](08-dispatch-executor-loop.md) | 调度执行器闭环 | 2026-08-27 需求对照（待处理队列/执行器/定时请求/回队打标/think 通知/分维队列），G-Ⅰ~G-Ⅵ 缺口分析与 V6-W1.5 任务定稿。 |
| 09 | [`09-ir-class-journal-decoupling.md`](09-ir-class-journal-decoupling.md) | IR 类型 + 轨迹队列 + 解耦 | V6-W1.6：IR 即时/定时类型、AttemptJournal 执行轨迹（复用分维队列存储）、planner 决策层抽取、100 次限额口径；含方案-代码匹配度核查与异常矩阵。 |

---

## 2. 与既有文档的关系

| 既有 | 关系 |
|---|---|
| [`ARCHITECTURE.md`](../03-design/01-architecture/architecture/ARCHITECTURE.md)（2026-08-21 快照） | v6 的事实基线；本目录所有"现状"引用其状态标记 (`CURRENT`/`SHADOW`/`TARGET`)。 |
| [`optimization-roadmap.md`](../03-design/01-architecture/architecture/optimization-roadmap.md)（P0-P2） | v6 把 P0/P1 重新映射到 V6-W0~V6-W3 的"门禁+契约"层，P2 拆为 V6-W4 架构演进与 V6-W5 智能层。 |
| [`runtime-request-flow.md`](../03-design/01-architecture/architecture/runtime-request-flow.md) | v6 不修改主路径，仅在 V6-W2 引入"边界明确的可插拔观察"层。 |
| [`routing-and-state.md`](../03-design/01-architecture/architecture/routing-and-state.md) | v6 在 V6-W1 收口 URSM 单一 owner + 联合 lease。 |
| [`omniroute-integration-boundary.md`](../03-design/01-architecture/architecture/omniroute-integration-boundary.md) | v6 在 V6-W4 严格遵守其"ADOPT / CONSUME / REJECT"决策表。 |
| [`REPO_LAYOUT.md`](../03-design/01-architecture/architecture/REPO_LAYOUT.md) | v6 把"目录该不该存在"作为 V6-W0 自检项的输入。 |
| [`armor-sdp-feasibility.md`](../03-design/01-architecture/architecture/armor-sdp-feasibility.md) | v6 把 Presidio sidecar + 中文 PII 规则包并入 V6-W3 安全观测层。 |
| [`会话优化v4/CONTRACT_FREEZE_2026-08-22.md`](../会话优化v4/CONTRACT_FREEZE_2026-08-22.md) | **不修改**，v6 不进入 v4 已冻结的 5 identities / 4 lanes / 3 lifecycle / 11 error_kinds。 |
| [`ADR-0001-handoff-goal-state-at-rest-encryption.md`](../adr/ADR-0001-handoff-goal-state-at-rest-encryption.md) | v6 在 V6-W3 引用其 KMS/Vault 假设，不重新定义。 |
| [`ADR-0002-target-go-package-layout.md`](../adr/ADR-0002-target-go-package-layout.md) | v6 的 V6-W0/W4 拆分波次对齐其波次划分。 |

---

## 3. 阅读路径建议

- **新成员**：先读 §0 TL;DR → `01-core-value-and-positioning.md` → `02-code-vs-design-deltas.md` 中标 ✅ 的项 → `03-roadmap-v6-waves.md` 中"我在哪一波"。
- **架构 Owner**：跳读 `02` 的所有 `⚠️` / `❌` 行 → `03` 的每个波次出口条件 → `06` 的回滚矩阵。
- **DevOps / SRE**：先读 `05-self-check-and-metrics.md` → `03-roadmap-v6-waves.md` 的运维影响列 → `06` 的"在故障时的退路"。
- **安全 / 合规**：`01` 的"数据与合规"段 → `02` 的 §3 / §7 → `04` 的 §6 提示词安全 → `05` 的 SLO §10。
- **前端 / Vue Owner**：`01` 的 §6 仪表盘 → `02` 的 §10 前端接口稳定性 → `03` 的 V6-W2 前端契约。

---

## 4. v6 不做的事

- ❌ 重新做架构顶层 ADR（仅在 v6 范围内引用既有 ADR，不新增 ADR 编号）。
- ❌ 修改 v4 冻结的契约表面（5 identities / 4 lanes / 3 lifecycle / 11 error_kinds / snapshot codec v1）。
- ❌ 修改 `cmd/gateway` 生产路径的执行语义（仅拆分文件边界、不改行为）。
- ❌ 修改 SQL 迁移内容（仅诊断与补强门禁，不动 schema）。
- ❌ 把任何 `SHADOW` 标记的能力直接写成生产能力。
- ❌ 引入新的协议独占执行器（v6 强调 Gateway 是 provider executor 唯一 owner）。

---

## 5. v6 的"完成"标准

每个差距 / 任务 / 波次的"完成"必须同时标注**证据等级**（与 [`05-self-check-and-metrics.md`](05-self-check-and-metrics.md) 的自检流程对齐，替代"任一满足即可"的旧口径）：

| 等级 | 定义 | 典型证据 |
|---|---|---|
| `IMPLEMENTED` | 代码/SQL/wiring 已落盘并编译通过 | commit hash；`go build ./...` |
| `LOCAL_VERIFIED` | 本地可回归 | `go test ./...` + `golangci-lint run` + race detector；新增/增强的 `_test.go` |
| `REAL_DEPENDENCY_VERIFIED` | 真实依赖（PG/Redis/provider）下验证 | testcontainers / staging 集成测试；SLO 在 Prometheus 暴露且告警规则落 `deploy/prometheus/rules/` |
| `RELEASE_READY` | 可发布 | 回滚脚本存在于 `scripts/rollback/` 或 runbook 并在 staging 复演；`docs/04-implementation/changes/` 记录 commit / migration / flag / scenario / 负向测试 |

- 波次**退出**要求：该波次全部任务 ≥ `LOCAL_VERIFIED`，其中安全/租户/计费相关任务 ≥ `REAL_DEPENDENCY_VERIFIED`；对外宣布完成前关键路径需 `RELEASE_READY`。
- 不得用 `IMPLEMENTED` 冒充任何更高等级；`SHADOW` 能力不得标为生产能力。

详见 [`05-self-check-and-metrics.md`](05-self-check-and-metrics.md) 与 [`06-risks-and-rollback.md`](06-risks-and-rollback.md)。
