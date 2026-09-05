# 2026-08-24 · V6-W0 (结构 / 门禁) 波次激活

## 背景

hzx-2 round-3 收尾（迁移 568 验证 + 404/500 指标分野 + Grafana provisioning + 8 项审计修复 + 联合提交 `33c834132`）落地后，进入 v6 路线图。W0 是所有后续波次（W1 → W5）的准入门槛，必须先盘点再激活。

## 现状盘点（Pre-W0 baseline @ commit 8ce1e71e3）

| 指标 | 当前 | W0 准入阈值 | 状态 |
|---|---|---|---|
| `cmd/gateway/main.go` 行数 | **6 154** | < 600 / 文件（W0-1） | ❌ 需拆分 |
| `admin/routing.go` 行数 | **5 292** | 按端点族分组（W0-2） | ❌ 需拆分 |
| `db/db.go` 行数 | **4 451** | 拆为 conn/pool/tx/health/tracing（W0-3） | ❌ 需拆分 |
| `bg/supervisor/` 存在 | 否 | 新增 supervisor + worker signal（W0-4） | ❌ 待建 |
| `tests/integration/h2c_final_handler_test.go` | 未建 | h2c + Maintain proxy + final handler smoke（W0-5） | ❌ 待建 |
| `sql/migrations/archive/2026-Q3/` 冻结 | 否 | 冻结非 `startup/` 子目录（W0-6） | ❌ 待冻结 |
| `docs/04-implementation/analysis/credential-health-ownership-map.md` | 未建 | import graph 列出 owner（W0-7） | ❌ 待建 |
| `docs/04-implementation/analysis/routing-impl-ownership-map.md` | 未建 | legacy / state / autoroute 三者边界（W0-8） | ❌ 待建 |
| `cmd/gateway-v2/main.go` 非生产 banner | 未加 | "非生产路径（Pipeline 验证入口）"（W0-9） | ❌ 待加 |
| `go test ./...` 基线 | ✅ 全绿 | 双绿（+ golangci-lint run） | ⚠️ 需补 lint |
| `go test ./sql/migrations/startup` 28 项 | ✅ 全绿 | 568/570 contract + idempotent + scope_revision 验证 | ✅ |
| `installer/embeddata ↔ sql/startup` 一致性 | ✅ md5 match | 每次 embed 变更后 commit 前必过 guardrail | ✅ |

## W0 准入（per §1.2 of 03-roadmap-v6-waves.md）

> 全部 W0-1 ~ W0-9 完成并通过验收。

目前 9 个必做全部为 ❌。**W0 准入门槛未达成，不得宣布 V6-W0 完成。**

## 激活决策（本次提交范围）

考虑到：

1. **基线盘点已完成**（上表），可作为后续子任务的工作清单；
2. **迁移 568 + audit 修复 + 联合提交已落地**（参见 `docs/changelogs/2026-08-23-priority-flag-migration-568-verification.md` 与 `33c834132`），不阻塞 W0 拆分；
3. **W0-1 ~ W0-3 是大型重构**（main.go 6 154 行、routing.go 5 292 行、db.go 4 451 行），应分配独立 worktree + 独立 commit 序列，**不在本激活日志里直接提交**。

本次仅激活 V6-W0 **波次跟踪** 与 **状态可视化**，不进行代码拆分。代码拆分的子任务拆分将在后续独立分支 / worktree 推进。

## 子任务分配（建议）

| 子任务 | 建议分支 | 主要 owner | 协办 |
|---|---|---|---|
| W0-1 cmd/gateway/main.go 拆分 | `v6/w0-1-main-split` | 后端架构 | SRE |
| W0-2 admin/routing.go 拆分 | `v6/w0-2-routing-split` | 后端架构 | DBA |
| W0-3 db/db.go 拆分 | `v6/w0-3-db-split` | DBA | 后端架构 |
| W0-4 bg/supervisor/ 抽出 | `v6/w0-4-supervisor` | 后端架构 | SRE |
| W0-5 h2c + Maintain proxy + final handler smoke | `v6/w0-5-h2c-smoke` | 测试 | SRE |
| W0-6 Migration 单仓 | `v6/w0-6-migration-vault` | DBA | 后端架构 |
| W0-7 credential-health ownership map | `v6/w0-7-cred-ownership` | 后端架构 | 安全 |
| W0-8 routing impl ownership map | `v6/w0-8-routing-ownership` | 路由 Owner | 后端架构 |
| W0-9 v2 banner + shadow header | `v6/w0-9-v2-banner` | 后端架构 | 前端 |

## 退出条件（per §1.4）

- 全部 W0-1 ~ W0-9 完成并通过验收；
- 本目录落盘本次变更日志（✅ 本条目）；
- staging 跑 `load_test.sh` 增量 P99 < 5%。

## 同步

- 上游基线：`main @ 8ce1e71e3`，origin 同步；
- 本次提交（计划）：新增 `docs/04-implementation/changes/2026-08-24-v6-w0-wave-activation.md`；
- 配套索引：`docs/04-implementation/changes/INDEX.md`（目前为空，将在 W0 完成时同步更新）。

## 引用

- [`docs/架构优化v6/03-roadmap-v6-waves.md` §1.1–1.4](../../架构优化v6/03-roadmap-v6-waves.md)
- [`docs/03-design/01-architecture/architecture/optimization-roadmap.md`](../../03-design/01-architecture/architecture/optimization-roadmap.md)
- [`docs/03-design/01-architecture/architecture/routing-and-state.md`](../../03-design/01-architecture/architecture/routing-and-state.md)
- [`docs/changelogs/2026-08-23-priority-flag-migration-568-verification.md`](../../changelogs/2026-08-23-priority-flag-migration-568-verification.md)
