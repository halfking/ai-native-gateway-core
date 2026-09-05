# API Hub 设计意图

> 3 行话说明（参考 `registry/tool_registry.go` 头部注释风格）

**做什么**：统一登记 LLM 端点 / MCP server / Agent 为"资产"（`(kind, ref_id)` 复合主键），并提供拓扑关系图（`asset_relationships` 有向边）。

**不做什么**：不处理 MCP 协议翻译（归 `mcp/` 包）、不执行 Agent（归 `agent_registry/`）、不中继请求（归 `relay/`）。apihub 只管"登记 + 拓扑 + 健康"，是其他模块的数据底座。

**多租户**：所有表启用 RLS（`public.get_current_tenant()`），Go 层 `Service` 从认证上下文派生 `tenant_id`（`apihub.WithTenant`），绝不信任请求体。跨租户读返回 `ErrNotFound`（隐藏存在性），跨租户建边被拒。

详见：`docs/产品方案/2026-06-23-llmgw-implementation-plan.md` §A0。
