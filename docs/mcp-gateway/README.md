# MCP 网关（llm-gateway-go 内置）

> 企业级 MCP 服务网关：数据面（无状态 JSON-RPC over HTTP）+ 控制面（注册与治理）
> + 企业治理链（身份 / 工具级授权 / 参数校验 / 敏感度审批 / 限流配额 / 熔断 /
> 出参脱敏 / 审计）+ 运营看板。

## 文档索引

| 文档 | 内容 |
|---|---|
| [01-design.md](./01-design.md) | 目标架构、治理链 8 闸、敏感度与审批模型、凭证账本、与开源的取舍 |
| [02-capability-matrix.md](./02-capability-matrix.md) | 开源对标（Microsoft / IBM ContextForge / Docker / MCPJungle / agentgateway …）+ 基线能力清单与实施状态 |
| [03-legacy-merge-map.md](./03-legacy-merge-map.md) | 既有 `tools/mcp-gateway` 已完成功能的逐条合并映射（含明确未移植项） |
| [04-operations.md](./04-operations.md) | 环境变量、部署形态、安全要点、排障 |

## 代码位置

| 位置 | 职责 |
|---|---|
| `mcp/` | 网关实现（数据面 / 控制面 / 治理链 / 存储 / 账本 / 指标 / 审计 / 看板） |
| `cmd/mcp-gateway/` | 独立进程（边缘部署 / 联调 / 演示模式 `--demo`） |
| `cmd/gateway/mcp_gateway.go` | 主网关接线（`/mcp`、`/mcp/control/*`、`/mcp/admin`） |
| `middleware/auth_mw.go` | 全局 API key 门对 `/mcp` 的放行登记（端点自守卫） |

## 快速开始

```bash
# 独立进程 + 内置示例上游（演示/联调）
go run ./cmd/mcp-gateway --demo --admin-token demo-token
# stdout 会打印 DEMO_API_KEY=sk-mcp-...

# 数据面
curl -s -X POST http://127.0.0.1:8089/mcp \
  -H "Authorization: Bearer $DEMO_API_KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# 控制面（管理令牌）
curl -s http://127.0.0.1:8089/mcp/control/health -H "X-Admin-Token: demo-token"

# 运营看板
open http://127.0.0.1:8089/mcp/admin?token=demo-token
```

## 验证

```bash
go test ./mcp/...                      # L1 治理链单测/集成测
bash tests/mcp-gateway-maestro/run.sh # L1 + L2 API 场景(S01~S20) + L3 Maestro 看板 flow
```
