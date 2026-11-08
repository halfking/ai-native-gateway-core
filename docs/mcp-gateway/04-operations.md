# MCP 网关运维说明（环境变量 · 部署 · 安全）

> 日期：2026-11-09

## 1. 环境变量

全部变量都有默认值 ⇒ 不配置任何变量时网关仍可启动（仅控制面不可用）。

| 变量 | 默认 | 说明 |
|---|---|---|
| `MCP_GATEWAY_ENABLED` | `true` | 是否挂载 MCP 网关。关闭 ⇒ 路径落 404（语义＝未部署该子系统） |
| `MCP_GATEWAY_STORE_FILE` | 空 | 注册面持久化文件（空＝内存，重启丢失）。生产必须配置 |
| `MCP_GATEWAY_AUDIT_FILE` | 空 | 审计 JSONL 落盘路径（空＝只留内存环形缓冲 200 条） |
| `MCP_GATEWAY_ADMIN_TOKEN` | 空 | 控制面管理令牌。**为空 ⇒ 控制面整体 503** |
| `MCP_GATEWAY_DEFAULT_TENANT` | `default` | 默认租户 |
| `MCP_GATEWAY_RATE_LIMIT_PER_MIN` | `0` | 每 key 兜底限流（0＝不限；类别限流优先生效） |
| `MCP_GATEWAY_READ_RATE_LIMIT` | `120` | 读类工具每分钟上限（按 key×类别） |
| `MCP_GATEWAY_WRITE_RATE_LIMIT` | `30` | 写类工具每分钟上限 |
| `MCP_GATEWAY_LLM_RATE_LIMIT` | `10` | LLM 类工具每分钟上限 |
| `MCP_GATEWAY_TOOL_TIMEOUT_MS` | `30000` | 上游调用默认超时（适配器可覆盖） |
| `MCP_GATEWAY_AUTO_REGISTER` | `true` | tools/list 时自动登记发现的工具 |
| `MCP_GATEWAY_MASK_PII` | `true` | 出参 PII 掩码（凭据掩码始终开启） |
| `MCP_GATEWAY_LEDGER_URL` | 空 | 外部账本地址（如 ACC）。设置后凭证校验走远端（fail-open） |
| `MCP_GATEWAY_LEDGER_TOKEN` | 空 | 外部账本内部共享密钥（`X-Internal-Token`）。**与 LEDGER_URL 必须同时设置** |
| `MCP_GATEWAY_LEDGER_CACHE_MS` | `10000` | 外部账本查询缓存 TTL |
| `MCP_GATEWAY_CIRCUIT_FAILURES` | `5` | 熔断打开的连续失败阈值 |
| `MCP_GATEWAY_CIRCUIT_OPEN_SECONDS` | `30` | 熔断冷却时长 |
| `MCP_GATEWAY_BOOTSTRAP_ENABLED` | `false` | 免认证引导面开关（apply/status/claim 免管理令牌） |
| `MCP_GATEWAY_BOOTSTRAP_AUTO_APPROVE` | `true` | 引导申请是否自动批准 |
| `MCP_GATEWAY_BOOTSTRAP_AUTO_APPROVE_DEVICES` | 空 | 仅这些设备自动批准（空＝不按设备区分） |
| `MCP_GATEWAY_BOOTSTRAP_APPLY_IP_LIMIT` | `10` | 每 IP 每 10 分钟申请上限 |
| `MCP_GATEWAY_BOOTSTRAP_APPLY_DEVICE_LIMIT` | `3` | 每设备每 10 分钟申请上限 |
| `MCP_GATEWAY_BOOTSTRAP_READ_IP_LIMIT` | `30` | status/claim 每 IP 每分钟上限 |
| `MCP_GATEWAY_LISTEN` | `:8089` | 独立进程的监听地址（`cmd/mcp-gateway` 用） |

## 2. 部署形态

### 2.1 随主网关部署（推荐）

```bash
export MCP_GATEWAY_ENABLED=true
export MCP_GATEWAY_STORE_FILE=/var/lib/llm-gateway/mcp-store.json
export MCP_GATEWAY_AUDIT_FILE=/var/log/llm-gateway/mcp-audit.jsonl
export MCP_GATEWAY_ADMIN_TOKEN="$(cat /run/secrets/mcp_admin_token)"
./gateway
```

挂载点：

| 路径 | 用途 | 鉴权 |
|---|---|---|
| `POST /mcp` | MCP 数据面 | MCP 凭证 |
| `GET /mcp/admin` | 运营看板 | 页面内输入管理令牌 |
| `/mcp/control/*` | 控制面 | 管理令牌 |
| `GET /.well-known/oauth-protected-resource` | 受保护资源元数据 | 公开 |

> 全局 API key 门（`middleware/auth_mw.go`）把 `/mcp` 列入 bypass：MCP 客户端持有
> 的是 MCP 凭证，不是网关主 key；数据面与控制面各自自守卫。

### 2.2 独立进程（边缘 / 工具面专用）

```bash
export MCP_GATEWAY_STORE_FILE=/var/lib/mcp-gateway/store.json
export MCP_GATEWAY_ADMIN_TOKEN="***"
./mcp-gateway --listen 127.0.0.1:8089
```

演示模式（内置示例上游 + 预置凭证，**仅用于联调**）：

```bash
./mcp-gateway --demo --admin-token demo-token
# stdout 会打印 DEMO_API_KEY=sk-mcp-...
```

## 3. 安全要点

1. **凭证只存摘要**：签发/轮换时明文返回一次，注册面只存 SHA-256。
2. **控制面默认不可用**：`MCP_GATEWAY_ADMIN_TOKEN` 为空 ⇒ 503（不是「无鉴权开放」）。
3. **免认证引导面默认关闭**：`MCP_GATEWAY_BOOTSTRAP_ENABLED` 默认 `false`；
   开启后仍受 IP/设备双维限流 + 幂等 + 自动/人工批准约束。
4. **吊销即时生效**：吊销后下一次调用 401；同时清空该 key 的限流桶。
5. **出参脱敏**：凭据始终掩码；PII 掩码默认开启，可按需关闭。
6. **审计可归因**：租户/组织/用户/凭证/智能体/工具/适配器/请求 ID 全量入审计；
   导出为 NDJSON，便于 SIEM 接入。
7. **fail-open 只用于外部账本**：本地账本不存在「查不到就放行」；远端账本不可达时
   放行是为了不因账本故障打断数据面（与既有实现一致，且**每次都记审计**）。

## 4. 排障

| 症状 | 排查 |
|---|---|
| 数据面全 401 | 凭证未登记/已吊销/外部账本不可达；看审计 `reason=unauthorized` 与 `ledger_unavailable` |
| 控制面 503 | 未配置 `MCP_GATEWAY_ADMIN_TOKEN` |
| 控制面 401 | 管理令牌不对（或用了 `X-Admin-Token` 头，值需等于管理令牌） |
| 工具 404 | 工具未注册；控制面 `POST /mcp/control/tools` 注册，或开 `MCP_GATEWAY_AUTO_REGISTER` |
| 工具 403 | 逐条查：scope → 可见性 → 允许表 → backend 绑定 → 审批凭据（审计 `reason` 会指明是哪一道） |
| 上游 502/503 | 看 `/mcp/control/health` 的适配器状态与熔断态；熔断打开时返回 503 |
| 429 | 令牌桶按 key×类别限流；调 `MCP_GATEWAY_*_RATE_LIMIT` 或等窗口重置 |
