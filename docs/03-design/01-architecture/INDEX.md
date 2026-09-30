# 03-design/01-architecture · 索引

> 最后更新：2026-10-01（手工重建，替代 2026-08-18 自动生成的空壳索引；子目录 `architecture/INDEX.md` 仍为旧自动版，以其文件实际内容为准）

## 本层根文件

| 文件 | 主题 | 状态 |
|---|---|---|
| [parallel-implementations-comparison.md](parallel-implementations-comparison.md) | **新旧并行实现对比与统一收敛方案（🔴 重点标注，2026-10-01 审计）** | 现行 |
| [unified-optimization-prompts.md](unified-optimization-prompts.md) | **统一优化提示词方案（逐项可执行收敛提示词包）** | 现行 |
| [README.md](README.md) | 目录说明 | 2026-08-18 |

## architecture/ 子目录（深度设计文）

| 文件 | 主题 |
|---|---|
| [architecture/ARCHITECTURE.md](architecture/ARCHITECTURE.md) | **当前系统架构（内部权威，事实快照 2026-10-01，证据分级 CURRENT/SHADOW/PARALLEL）** |
| [architecture/runtime-request-flow.md](architecture/runtime-request-flow.md) | 运行时请求流 |
| [architecture/routing-and-state.md](architecture/routing-and-state.md) | 路由与状态管理 |
| [architecture/optimization-roadmap.md](architecture/optimization-roadmap.md) | 优化路线图与代码指导 |
| [architecture/omniroute-integration-boundary.md](architecture/omniroute-integration-boundary.md) | OmniRoute 集成边界 |
| [architecture/REPO_LAYOUT.md](architecture/REPO_LAYOUT.md) | 仓库布局权威地图 |
| [architecture/API.md](architecture/API.md) | 主控端 API（注意：仅 llmgateway.internal.example.com 8 端点，非数据面 API） |
| [architecture/ARCHITECTURE_REFACTOR_GUIDE.md](architecture/ARCHITECTURE_REFACTOR_GUIDE.md) | 架构重构指导 |
| [architecture/architecture-decisions.md](architecture/architecture-decisions.md) | 架构决策记录 |
| [architecture/admin-api-authentication.md](architecture/admin-api-authentication.md) | Admin API 鉴权 |
| [architecture/a2a-spec-2027.md](architecture/a2a-spec-2027.md) | A2A 协议规格 |
| [architecture/adaptive-response-format-converter.md](architecture/adaptive-response-format-converter.md) | 自适应响应格式转换 |
| [architecture/apihub-design.md](architecture/apihub-design.md) | apihub 资产图谱设计 |
| [architecture/armor-sdp-feasibility.md](architecture/armor-sdp-feasibility.md) | Armor SDP 可行性 |
| [architecture/attachment-auth-integration.md](architecture/attachment-auth-integration.md) | 附件授权集成 |
| [architecture/node-probe-mechanism.md](architecture/node-probe-mechanism.md) | 节点探测机制 |
| [architecture/routing-domain.md](architecture/routing-domain.md) | 路由域模型 |

## 其他子目录

| 文件 | 主题 |
|---|---|
| [domains/DOMAIN_REGISTRY.md](domains/DOMAIN_REGISTRY.md) | 域名 SSOT 注册表（含 file:line 证据与 drift 检测） |
| [deploy-zero-downtime/zero-downtime-design.md](deploy-zero-downtime/zero-downtime-design.md) | 零停机部署设计 |
