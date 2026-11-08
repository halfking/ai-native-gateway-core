// Package mcp 实现 llm-gateway-go 内置的企业级 MCP（Model Context Protocol）
// 服务网关：数据面（无状态 JSON-RPC over HTTP 工具路由）+ 控制面（适配器 /
// 工具 / 智能体 / 技能 / 凭证的注册与治理）+ 企业治理链（身份、工具级授权、
// 参数校验、敏感度分级审批、限流配额、熔断、出参脱敏、审计与可观测）。
//
// # 域边界
//
// 本包承担 MCP 协议翻译与治理，**不**承担以下职责（各有既有权威方）：
//
//   - 模型调用与计量：llm-gateway-go 的 relay/dispatch 链路（见 domains/routing）。
//   - 工具定义目录的既有事实源：registry 包（tool_registry 表），本包只做**引用**
//     与**网关侧策略**，不复制工具定义的权威。
//   - 组织身份与审批队列：Casdoor / RedClaw（组合部署时通过 JWT 或外部账本接入）。
//
// apihub 包的注释早已声明「MCP protocol translation (mcp/ package)」不属于 apihub，
// 本包即是该声明的落地：apihub 只认资产身份（kind=mcp_server），协议与治理在此。
//
// # 与既有 tools/mcp-gateway 的关系
//
// 历史上 tools/mcp-gateway（Node.js，独立仓）承担工具面强制入口，本包把它的
// **全部已完成能力**按语义移植并扩展，映射表见
// docs/mcp-gateway/03-legacy-merge-map.md。要点：
//
//   - 路径分流 → Adapter（命名空间化路由 + 工具归属）。
//   - capability 路由 → Capability（请求头注入 + 能力目录）。
//   - API key 限流 → 每 key 令牌桶（按 read/write/llm 分类）。
//   - bindings 执法 → Key.Bindings（一 key 多后端单边禁用）。
//   - tool-allow 执法 → Key.AllowedTools/DeniedTools（403 + -32003）。
//   - keys apply/status/claim → Ledger 接口（本地账本 + ACC 远端账本两种实现）。
//   - 审计环形缓冲 + 管理端点 → Audit（含 JSONL 落盘与导出）。
//   - Prometheus 指标 → metrics.go（指标名保持同名，便于既有告警规则平移）。
//
// # 传输与无状态
//
// 数据面默认**无状态**：每个请求独立携带协议元数据（`MCP-Protocol-Version`、
// `Mcp-Method`、`Mcp-Name` 头或 `params._meta`），网关据此逐请求解析路由目标，
// 不依赖 transport session 亲和（对齐 microsoft/mcp-gateway 的无状态路由）。
//
// 命名冲突消解：聚合多个上游后工具名按 `<adapter>__<tool>` 规范化；显式注册
// 的工具可用其注册名覆盖（对齐 MCPJungle 的规范化命名思路）。
package mcp
