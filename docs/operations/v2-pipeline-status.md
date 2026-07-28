# v2 Pipeline Feature Flag — 状态说明（2026-07-29）

> **配套审计**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md` §5.6 R-5.2
> **配套方案**：`docs/design/2026-07-28-llm-gateway-flow-improvements.md` §2 P0-5
> **目的**：明确 v2 Pipeline（`v2DispatchHandler`）在生产是否启用；文档化当前状态与启用条件。

---

## 1. 当前状态

| 环境 | `LLM_GATEWAY_USE_V2_PIPELINE` | `LLM_GATEWAY_V2_ENABLED` | 4 v1 endpoints | /v2/* 路由 | 备注 |
|---|---|---|---|---|---|
| **生产（154 / llm.kxpms.cn）** | 未设（默认 off） | 未设（默认 off） | v1 chatHandler | 未注册 | 默认生产配置 |
| **245（llmgo.kxpms.cn）** | 未设（默认 off） | 未设（默认 off） | v1 chatHandler | 未注册 | 部署前可手工开启做 shadow |
| **本地（:8781）** | 未设（默认 off） | 未设（默认 off） | v1 chatHandler | 未注册 | 本地默认与生产一致 |

**结论**：v2 Pipeline 在**所有环境默认 OFF**，没有任何生产流量走 v2 Pipeline 路径。

> **真值复核**：实际环境变量真值需通过 ops 通道对照 `.env.184.enc` / `.env.71.enc` / `.env.kaixuan-1.enc`（SOPS 加密文件，本仓库不入仓明文），本节"未设"为代码默认值推断。如发现任一环境实际开启，需在此表更新。

---

## 2. Flag 读取与代码链路

### 2.1 Flag 读取点

`v2UsePipeline()` 在 `cmd/gateway/main_pipeline.go:153-161`：

```go
func v2UsePipeline() bool {
    for _, k := range []string{"LLM_GATEWAY_USE_V2_PIPELINE", "LLM_GATEWAY_V2_ENABLED"} {
        v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
        if v == "1" || v == "true" || v == "yes" {
            return true
        }
    }
    return false
}
```

- **两个环境变量**（OR 语义，保留旧名肌肉记忆）：
  - `LLM_GATEWAY_USE_V2_PIPELINE`（R1.12 首选，2026-06-26）
  - `LLM_GATEWAY_V2_ENABLED`（Round 49 旧名，仍兼容）
- **真值**：1 / true / yes（大小写无关）；其他字符串视为 false
- **默认**：false

### 2.2 行为差异（v1 endpoints）

| Flag | /v1/chat/completions 等 4 个 endpoint |
|---|---|
| **OFF（默认）** | 直走 v1 chatHandler（生产默认），无中间层 |
| **ON** | 走 `v2DispatchHandler` 包装：Pipeline preflight（tracing / security / audit / observability）→ chatHandler fallback → postflight |

**两条不变量**（`cmd/gateway/v2_dispatch_test.go:106-111` 已固化测试）：

1. Flag OFF 时，`v2DispatchMux()` 返回 `(nil, nil, false)`，mux 状态不变；v1 handler 可达性完全不变
2. Flag ON 时，即使 Pipeline stage 报错，**也会进入 fallback**（R1.12 安全契约）— 失败不会让请求 404

### 2.3 /v2/* 路由

- 与 `LLM_GATEWAY_USE_V2_PIPELINE` **无关**，由 `registerV2PipelineRoutes()` 在 `cmd/gateway/main.go:3495` 注册
- 当 `/v2/*` 路由所需的 DB pool 不可用时，**不注册任何 /v2/* 路由**（`main_v2_pipeline_test.go:106-123 TestV2Pipeline_DefaultOff_NoRoutes`）
- 实际生产默认：未注册（依赖 DB pool 检测）

### 2.4 相关配置（flag ON 时的细粒度开关）

`loadV2DispatchConfig()` 在 `main_pipeline.go:164-177`：

| env | 默认 | 说明 |
|---|---|---|
| `LLM_GATEWAY_V2_CACHE` | true | cache stage 开关 |
| `LLM_GATEWAY_V2_SECURITY` | true | security stage 开关 |
| `LLM_GATEWAY_V2_AUDIT` | true | audit stage 开关 |
| `LLM_GATEWAY_V2_OBSERV` | true | observability stage 开关 |
| `LLM_GATEWAY_V2_STREAMING` | true | streaming 兼容开关 |
| `LLM_GATEWAY_V2_AUTH` | **false** | Auth stage（demo mode，2026-06-29 改默认） |
| `LLM_GATEWAY_V2_ANALYSIS` | false | 异步分析 Loop（PR-V4-09） |
| `LLM_GATEWAY_V2_ANALYSIS_INTERVAL` | 5s | 分析 tick 间隔 |
| `LLM_GATEWAY_V2_ANALYSIS_BATCH` | 10 | 分析 batch size |

---

## 3. 启用条件（启用 v2 Pipeline 的验证清单）

任何环境**打开 `LLM_GATEWAY_USE_V2_PIPELINE=true` 前**，必须完成以下步骤：

### 3.1 L1 — HTTP 存活

- [ ] 启动后 `/healthz` 返回 200 + `{"status":"ok"}`
- [ ] 启动日志包含 `v2 pipeline: LLM_GATEWAY_USE_V2_PIPELINE=true; 4 v1 endpoints will be Pipeline-wrapped`

### 3.2 L2 — 依赖连通

- [ ] DB pool 可达（`registerV2PipelineRoutes` 才会成功）
- [ ] Redis / OTel collector（若开启 observability stage）可达

### 3.3 L3 — 功能链路

- [ ] `curl -X POST http://<host>:8781/v1/chat/completions` 仍可走通（fallback 必须生效）
- [ ] 触发一次 security / audit stage 异常，确认**不阻断** fallback（参看 `v2_dispatch_test.go:268-294`）
- [ ] 4 个 endpoint（/v1/chat/completions、/v1/completions、/v1/messages、/v1/responses）都验证

### 3.4 L4 — 业务真实

- [ ] 真实凭据 + 真实模型跑通一次 chat / messages / responses
- [ ] 流式响应 chunk 累积正确（`/v1/chat/completions` 流式模式）
- [ ] 请求日志写入 `request_logs` 字段齐全（含 `agent_name` / `client_protocol` 等近一周加入字段）

### 3.5 灰度推荐流程

```
245 staging 灰度 24h
   ↓ 验证 L1-L4 全过
154 生产 灰度 5% 流量 1h
   ↓ 错误率基线比对
154 生产 50% 流量 30min
   ↓
154 生产 100% 流量
```

任何阶段错误率 > 基线 1.5× → 立即回滚（`LLM_GATEWAY_USE_V2_PIPELINE=false` + 重启 gateway）

---

## 4. 已知问题

### 4.1 `/v2/*` 路由与 v1 共存

- 当前 `registerV2PipelineRoutes` 把 /v2/* 挂到同一个 mux 上
- 与 v1 endpoints 共用 mux → 任何 mux panic 都会同时影响 v1 + v2
- **建议**：未来独立子 mux + recover 中间件（暂未排期）

### 4.2 Pipeline 资源开销

- 每次请求多 5+ stage 处理（cache / security / audit / observability / streaming）
- 估算增加 5-15ms P50 latency（基线 200ms → 205-215ms）
- **建议**：开启后监控 `llm_gateway_pipeline_stage_duration_seconds`（若已注册）

### 4.3 Auth stage 默认 OFF

- `LLM_GATEWAY_V2_AUTH` 默认 `false`（demo mode）
- 意味着 Pipeline 不接管鉴权，仍走 v1 chatHandler 内联鉴权（`handler.go:1256-1261`）
- **生产环境**：**不建议** 同时开启 `LLM_GATEWAY_V2_AUTH=true`，避免鉴权双重判定

---

## 5. 切换动作记录

| 日期 | 操作 | 结果 |
|---|---|---|
| 2026-06-26 (R1.12) | 引入 `v2DispatchHandler` + flag | 默认 OFF，仅 245 灰度 |
| 2026-06-29 | `LLM_GATEWAY_V2_AUTH` 默认改 false | demo mode |
| 2026-07-29 (P0-5) | 文档化状态（本文件） | 默认 OFF 在所有环境 |

---

## 6. 关联文档

- **审计报告**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md` §5.6 R-5.2
- **实施方案**：`docs/design/2026-07-28-llm-gateway-flow-improvements.md` §2 P0-5
- **v2 dispatch 实现**：`cmd/gateway/main_pipeline.go:145-177`
- **v2 dispatch 测试**：`cmd/gateway/v2_dispatch_test.go`（11 个测试覆盖 flag / fallback / 异常路径）
- **v2 pipeline 路由注册测试**：`cmd/gateway/main_v2_pipeline_test.go`（5 个测试覆盖 /v2/* 路由注册）

---

**下次审视**：当 R-5.2 风险消解（flag 状态长期稳定 + 自动化校验）或 P0-3 URSM v2 切换计划启动时复核本文件。