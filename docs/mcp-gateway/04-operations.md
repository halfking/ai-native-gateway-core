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
| `MCP_GATEWAY_MAX_BODY_BYTES` | `1048576` | 请求体上限（字节）。超限 ⇒ HTTP **413** + JSON-RPC **-32004**（沿用既有 mcp-gateway 契约；此前是静默截断 ⇒ 表现为 JSON 解析失败，把真因掩盖了） |
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
| `MCP_GATEWAY_CONTROL_SCOPE` | `on` | 控制面 scope 鉴权开关。`off` ⇒ 控制面**只认管理令牌**（回退到「业务 key 无控制面权限」）；`on` ⇒ 业务 key 可按 `mcp.register`/`mcp.manage`/`mcp.analyze`/`mcp.topology` 访问控制面 |
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
| `/mcp/control/*` | 控制面 | 管理令牌 **或** 带对应 scope 的业务 key |
| `GET /.well-known/oauth-protected-resource` | 受保护资源元数据 | 公开 |

> 全局 API key 门（`middleware/auth_mw.go`）把 `/mcp` 列入 bypass：MCP 客户端持有
> 的是 MCP 凭证，不是网关主 key；数据面与控制面各自自守卫。

### 2.1.1 控制面能力位与授权口径

业务 key 也能进控制面，按**能力位**判定（`mcp.admin` 是超集，其余各位互不派生）：

| 能力位 | 能做什么 |
|---|---|
| `mcp.register` | 注册面：创建/查看适配器、工具、智能体、技能 |
| `mcp.manage` | 管理面：变更/删除注册项、签发/吊销/轮换凭证、审批裁决 |
| `mcp.analyze` | 统计分析面：审计查询与导出、路由清单、能力目录、`/mcp/control/stats` 指标快照 |
| `mcp.topology` | 内部拓扑面：`/mcp/control/health`（含上游端点与熔断态） |
| `mcp.admin` | **超集**：以上全部 |

签发一把「能注册 MCP 的 key」：

```bash
# ① 用管理令牌签一把只带注册权限的 key
curl -s -X POST http://127.0.0.1:8089/mcp/control/keys \
  -H "X-Admin-Token: $MCP_GATEWAY_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"tenant_id":"default","name":"registrar","scopes":["mcp.register"]}'

# ② 用这把 key 注册适配器（凭据放 Authorization: Bearer 或 X-Api-Key）
curl -s -X POST http://127.0.0.1:8089/mcp/control/adapters \
  -H "Authorization: Bearer sk-mcp-..." -H 'Content-Type: application/json' \
  -d '{"name":"memora","endpoint":"http://memora:8080","capability":"memory","enabled":true}'
```

★ **权限放大防护**：业务 key 签发/轮换出来的凭证**不得超出它自己的权限**
（`mcp.manage` 的 key 签不出 `mcp.analyze` 的 key），且**不能跨租户**操作。
逐条口径见 `03-legacy-merge-map.md` §6.7。

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
8. **控制面 fail-closed**：控制面与管理动作在**身份无法判定**时一律不放行
   （账本不可达 ⇒ 503）；只有**数据面**保留既有的 fail-open。
9. **控制面权限放大防护**：业务 key 只能签出/轮换出不超出自身 scope 的凭证，
   且只能操作自己所属租户 ⇒「拿到凭证即可提权」的口被堵住。

## 4. 排障

| 症状 | 排查 |
|---|---|
| 数据面全 401 | 凭证未登记/已吊销/外部账本不可达；看审计 `reason=unauthorized` 与 `ledger_unavailable` |
| 控制面 503 | 未配置 `MCP_GATEWAY_ADMIN_TOKEN`（且请求方不是带 scope 的有效 key） |
| 控制面 401 | 管理令牌不对（或用了 `X-Admin-Token` 头，值需等于管理令牌）；业务 key 不在账本/已吊销 |
| 控制面 403 | 身份有效但**权限不足**：响应体的 `required_scope` 就是这条路由要的能力位；`reason=scope_escalation` 表示试图签出超出自身权限的凭证；`reason=tenant_mismatch` 表示跨租户 |
| 控制面 503（账本不可达） | 外部账本不可达 ⇒ 控制面 fail-closed（数据面仍 fail-open） |
| 控制面 404 | 路径写错（控制面挂在 `/mcp/control` 前缀下） |
| 工具 404 | 工具未注册；控制面 `POST /mcp/control/tools` 注册，或开 `MCP_GATEWAY_AUTO_REGISTER` |
| 工具 403 | 逐条查：scope → 可见性 → 允许表 → backend 绑定 → 审批凭据（审计 `reason` 会指明是哪一道） |
| 上游 502/503 | 看 `/mcp/control/health` 的适配器状态与熔断态（该面需 `mcp.topology`）；熔断打开时返回 503 |
| 429 | 令牌桶按 key×类别限流；调 `MCP_GATEWAY_*_RATE_LIMIT` 或等窗口重置 |

## 5. 指标与告警口径（既有规则按旧名平移）

`/metrics` 同时暴露**两套**族名 ⇒ 既有告警规则**不用改**：

| 族 | 说明 |
|---|---|
| `mcp_gateway_*` | 网关原生族（共 17 个：请求数、限流数、上游错误数、健康检查失败数、运行时长、上游健康、按能力域请求数、按方法请求数、鉴权拒绝数、敏感审批数、脱敏数、凭据校验数、绑定校验数、能力位放行校验数、工具调用数、工具调用时延、熔断打开数） |
| `tool_allow_*` | **既有旧名**：判定次数 / 拒绝 / ★**空允许表（执法跳过）** / 查询失败（fail-open）/ 不在账本 / 远端路由缺失 / 404 形状不认得 |
| `bindings_*` | **既有旧名**：检查 / 拒绝 / 凭证失效 / 查询失败 / 不在账本 |
| `keys_*` | **既有旧名**：apply / 幂等重放 / 限流 / claim / status |

★ 运维判断「收口到底生不生效」看这两个数：

| 指标 | 读数含义 |
|---|---|
| `tool_allow_not_enforced_total` 持续增长 | 很多 key **没有非空允许表** ⇒ 允许表闸根本没在收口（既有同名指标，语义一致） |
| `tool_allow_lookup_errors_total` 持续增长 | 允许表**查询失败** ⇒ 走的是 fail-open 放行 ⇒ 看着没报错，实际没收口 |

★ 逐族登记（含 3 项「不适用」的理由）见 `03-legacy-merge-map.md` §6.6；
  守护用例见 `mcp/legacy_contract_test.go` 与 harness 的 S24。

★ 给**业务 key**用的统计出口是 `GET /mcp/control/stats`（需 `mcp.analyze`），
  返回 MCP 网关自己的指标族快照（JSON）；全局 `/metrics` 仍是 Prometheus 抓取面
  （运维网络域，不在 key 的 scope 体系内）。
