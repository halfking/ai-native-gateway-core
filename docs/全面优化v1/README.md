# llm-gateway-go 全面优化 v1 · 审计修正版任务计划

> 编制日期：2026-08-14 ｜ 上层方案：`ai-native-tools/docs/全面优化v1/`
> 基线：父仓 `docs/拆分/31-端到端用户流程与三模块一体.md`、本仓 `docs/会话优化v3/`、`docs/修订0811/`
> 状态词：`CURRENT` 已由源码证明；`PARTIAL` 基础存在未闭环；`PLANNED` 目标能力。

> API 与事件细化：见 [API-DETAILS.md](API-DETAILS.md)，其中定义 gateway 主入口、outbox 事件、SM 投递、插件协议与 capabilities 目标契约。

## 一、定位与事实边界

gateway 是推理平面 SSOT，但 v1 文档必须区分事实与目标：

- `cmd/gateway` 主入口默认 `:8781`，是 CURRENT。
- `cmd/gateway-v2` 默认 `:8782`，是并行演示入口，不是生产主入口。
- `ai-session-manager` TCP 默认也是 `:8782`，因此部署文档必须显式区分端口与服务名。
- durable outbox/writer/dispatcher 已存在，不能写成“待上线”纯未来项；但通用 webhook/订阅与跨系统对账仍需闭环。
- `GET /api/v2/capabilities` 目前不是源码事实能力，应作为计划项。
- D1 资源档位是目标，不是已经有证据的事实指标。

## 二、能力现状矩阵

| 能力 | 当前事实 | 状态 | v1 动作 |
| --- | --- | --- | --- |
| primary gateway | `cmd/gateway :8781` | CURRENT | 作为生产主入口 |
| gateway-v2 demo | `cmd/gateway-v2 :8782` | CURRENT/PARTIAL | 仅并行演示，文档中明确非主入口 |
| request/session facts | session/request 事实已存在 | CURRENT | 保持 SSOT |
| durable outbox | outbox table/writer/dispatcher 已存在 | CURRENT/PARTIAL | 冻结事件 schema、HMAC、防重放、DLQ/replay |
| SM projection dispatch | 已投递到 SM `/internal/v1/events` | CURRENT/PARTIAL | 继续联调与对账 |
| plugin runtime | 已有 plugin 机制与 SM plugin manifest | CURRENT/PARTIAL | 通用化多插件与隔离 |
| capabilities endpoint | 未见统一 `/api/v2/capabilities` 路由 | PLANNED | 先定 contract，再实现 |
| D1 资源档位 | 未有实测证据/配置 | PLANNED | 加采样方法与报告 |

## 三、服务间身份与标准参数

### service JWT

入站/出站都要明确：

- `iss`、`aud`、`sub`、`tenant_id`、`scope`、`exp`、`nbf`、`iat`。
- gateway 到 SM 的 service token 可单独签发，但必须与目标 audience 一一对应，不能共享泛 token。
- legacy `AI_SESSION_MANAGER_GATEWAY_SERVICE_TOKEN` 只作迁移兼容，不应写成平台标准。

### 写/订阅请求

- 写入/管理类 API：`Idempotency-Key` + `X-Correlation-ID` + `Traceparent`。
- outbox 事件：`event_id/schema_version/event_type/tenant_id/session_id/request_id/correlation_id/occurred_at`。
- 传输：HMAC 签名、timestamp/nonce、防重放、幂等消费、checkpoint、DLQ/replay。

## 四、事件与投影闭环

### 当前状态

- durable outbox 已存在。
- dispatcher 目标投递 SM `/internal/v1/events`。
- SM projection schema、RLS、consumer、local-first read 已存在，但 ownership 切换/对账未完成。

### v1 目标

1. 冻结 event envelope 与版本。
2. 明确 `request.completed.v1` 等事件字段和消费者幂等策略。
3. 补 webhook/通用订阅能力时，必须与既有 outbox 区分。
4. 设置 freshness 指标和 shadow 对账门禁，未通过不得切换 ownership。

## 五、插件协议边界

SM plugin 协议已存在 `http-unix-socket`、`/plugin/healthz`、`/plugin/handshake`、`X-Gateway-Plugin-ID`、`X-Gateway-Tenant-ID`、`X-Gateway-Context-Timestamp`、`X-Gateway-Context-Nonce`、`X-Gateway-Context-Signature`。

gateway 文档中的“plugin runtime 通用化”应只描述为：

- 已有 SM plugin 为样板。
- 目标是多插件并发、插件级配额和网络隔离。
- capabilities 仍需按插件/服务分别声明，不要把审计/预算/聚类写成当前所有 plugin 都已暴露。

## 六、D1 资源档位

“D1” 仅表示资源档位，不是 Cloudflare D1。

必须补充：

- 目标组件清单：gateway、SM、acc-go、Memora、RedClaw core、PG、Redis。
- 关闭哪些 bg workers/probes。
- RSS/PSS 采样方法、采样时长、典型流量。
- 是否包含浏览器 UI。
- `≤1GB`、`≤300MB`、`≤12GB` 必须标为 TARGET，等待实测。

## 七、任务计划

| ID | 任务 | 优先级 | 验收 |
| --- | --- | --- | --- |
| GW-0.1 | 明确端口矩阵与主/演示入口，杜绝 8780/8781/8782 混淆 | P0 | 文档、compose、启动日志一致 |
| GW-0.2 | `GET /api/v2/capabilities` contract（目标） | P0 | 先生成 contract，再实现路由 |
| GW-0.3 | correlation_id/tenant/service JWT standardization | P0 | 六系统 trace 可串联 |
| GW-1.1 | 平台调用方租户化与旁路清零（ACC/RedClaw/Memora/Pocket） | P0 | 网关统计 0 旁路 |
| GW-1.2 | outbox → SM event envelope freeze + HMAC/replay/dlq | P0 | consumer contract test 通过 |
| GW-2.1 | plugin runtime multi-plugin/isolation 设计 | P1 | 双插件并存冒烟 |
| GW-3.1 | D1 资源档位实测与报告 | P1 | 采样报告 |
| GW-4.1 | 会话优化 V3.2 按既有文档继续，明确 CURRENT/PARTIAL/TARGET | P1 | 状态表更新 |

## 八、风险

- 不要把 gateway-v2 的 `:8782` 和 session-manager 的 `:8782` 混为一谈。
- outbox/dispatcher 是 CURRENT，但 webhook/通用订阅不是；不要把两者合并描述。
- 资源档位必须以实测支撑，不得写成已验收事实。
