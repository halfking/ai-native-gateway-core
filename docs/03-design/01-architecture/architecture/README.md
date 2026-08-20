# architecture — 当前架构文档索引

> **事实快照：** 2026-08-21  
> 本目录描述当前 `llm-gateway-go` 架构事实、生产/实验边界和演进路线。历史计划、审计与一次性报告应引用而不覆盖当前事实。

## 从这里开始

| 文档 | 用途 | 状态 |
|---|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | 当前系统上下文、生产路径、数据/控制面、风险和门禁 | `CURRENT` |
| [runtime-request-flow.md](runtime-request-flow.md) | v1 主路径、Pipeline 旁路、retry、WAL/telemetry/outbox 时序 | `CURRENT` |
| [routing-and-state.md](routing-and-state.md) | URSM、Legacy、资源治理、策略、TPM/lease/retry | `CURRENT` |
| [optimization-roadmap.md](optimization-roadmap.md) | P0/P1/P2 问题、代码落点、测试、回滚和 N0-N5 波次 | `CURRENT` |
| [omniroute-integration-boundary.md](omniroute-integration-boundary.md) | OmniRoute 的 ADOPT/CONSUME/REJECT 边界 | `CURRENT` |
| [REPO_LAYOUT.md](REPO_LAYOUT.md) | 仓库当前目录地图与入位规则 | `CURRENT` |
| [ARCHITECTURE_REFACTOR_GUIDE.md](ARCHITECTURE_REFACTOR_GUIDE.md) | 历史/目标重构指导；需与 ADR 对照 | `TARGET/PARTIAL` |
| [architecture-decisions.md](architecture-decisions.md) | 历史和局部决策记录 | `HISTORICAL/PARTIAL` |
| [routing-domain.md](routing-domain.md) | 路由领域专题 | `PARTIAL` |
| [admin-api-authentication.md](admin-api-authentication.md) | Admin API 认证专题 | `PARTIAL` |
| [attachment-auth-integration.md](attachment-auth-integration.md) | 附件鉴权专题 | `PARTIAL` |
| [a2a-spec-2027.md](a2a-spec-2027.md) | A2A 目标设计，不代表当前 transport | `TARGET` |

## 相关入口

- [包布局 ADR](../../../../../adr/ADR-0002-target-go-package-layout.md)
- [领域内核文档](../../../../../../domain/ARCHITECTURE.md)
- [测试矩阵](../../../../../05-testing/01-strategy/test-matrix.md)
- [部署入口](../../../../../06-deployment/README.md)
- [安全设计](../../../../../05-security-design/README.md)
- [父仓拆分/ownership 门禁](../../../../../../../docs/拆分/README.md)

## 使用规则

1. 当前代码、SQL、runtime wiring 和实际测试优先于本文档。
2. `SHADOW`、`TARGET`、`UNKNOWN` 不能写成已上线或通过。
3. 改变请求、状态、session/body、tenant/RLS、retry、worker 或部署行为时，同时更新对应专题和测试矩阵。
4. 过程性报告、审计和 handoff 应归档到 `docs/archive/` 或其既有归档目录；不要覆盖当前事实文档。
5. 文档示例不得包含真实 secret；发现历史明文后应先轮换，再脱敏和记录审计。
