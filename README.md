# LLM Gateway Go

> 企业级 LLM Gateway：在多租户、多个 Provider、长流式 Agent 请求、成本和审计约束下，统一完成协议适配、候选路由、凭据治理、请求执行、可观测性与交付运维。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8.svg)](https://go.dev/)

> **当前版本：** 以 [`version.json`](version.json) 和 [`VERSION`](VERSION) 为准。
> **事实规则：** 代码、SQL、runtime wiring 和已执行测试优先于 README、路线图和历史报告；feature flag、migration 或目录存在不等于已上线。

## 定位

Gateway 不是简单的模型反向代理。当前生产入口 [`cmd/gateway`](cmd/gateway) 将以下能力组合在同一运行单元中：

- OpenAI、Anthropic、Responses、Gemini native 等协议与 SSE 中继；
- `model=auto` 任务识别、候选模型选择、tier/billing/sticky、P2C/Bandit、URSM 状态和故障转移；
- 多凭据健康、限流、fingerprint slot、egress identity、连接与资源压力治理；
- request WAL、request/usage 日志、审计、Prometheus、OpenTelemetry 和 Admin live stream；
- 会话缓存/压缩、Session V2 shadow persistence、Gateway → ASM 事件投递；
- Admin API、Vue 管理台、后台探针/清理/聚合 worker；
- Installer、License、分发、升级和回滚兼容能力。

当前主路径是 `cmd/gateway` 的 v1 handler。`cmd/gateway-v2`、`/v2/*` 和 v1 Pipeline wrapper 是验证或灰度路径，不能直接当作默认生产执行路径。

## 架构入口

| 主题 | 权威入口 |
|---|---|
| 当前系统架构 | [`docs/03-design/01-architecture/architecture/ARCHITECTURE.md`](docs/03-design/01-architecture/architecture/ARCHITECTURE.md) |
| 运行时请求流 | [`runtime-request-flow.md`](docs/03-design/01-architecture/architecture/runtime-request-flow.md) |
| 路由、URSM 与资源状态 | [`routing-and-state.md`](docs/03-design/01-architecture/architecture/routing-and-state.md) |
| 优化路线和代码指导 | [`optimization-roadmap.md`](docs/03-design/01-architecture/architecture/optimization-roadmap.md) |
| OmniRoute 集成边界 | [`omniroute-integration-boundary.md`](docs/03-design/01-architecture/architecture/omniroute-integration-boundary.md) |
| 仓库布局 | [`REPO_LAYOUT.md`](docs/03-design/01-architecture/architecture/REPO_LAYOUT.md) |
| 包布局 ADR | [`ADR-0002`](docs/adr/ADR-0002-target-go-package-layout.md) |
| 测试矩阵 | [`docs/05-testing/01-strategy/test-matrix.md`](docs/05-testing/01-strategy/test-matrix.md) |
| 部署入口 | [`docs/06-deployment/README.md`](docs/06-deployment/README.md) |
| 安全报告与政策 | [`SECURITY.md`](SECURITY.md) / [`docs/03-design/05-security-design/`](docs/03-design/05-security-design/) |
| 三服务拆分与 ownership 门禁 | [`../docs/拆分/README.md`](../docs/拆分/README.md) |

## 当前架构概览

```text
Client / Agent / Admin
          |
          v
+---------------------------------------------------------+
| cmd/gateway                                             |
|  protocol -> auth/tenant -> route -> resource -> stream |
|  request/usage/audit -> metrics/trace -> admin/workers  |
+-------------+---------------------+---------------------+
              |                     |
              v                     v
        PostgreSQL                Redis
        durable facts             hot state / queues
              |                     |
              +----------+----------+
                         |
        +----------------+----------------+
        v                                 v
ai-session-manager                 ai-native-maintain
projection / analysis / governance license / distribution / upgrade
```

### 当前状态摘要

| 能力 | 状态 | 说明 |
|---|---|---|
| v1 多协议数据面 | `CURRENT` | 主生产 handler 处理 OpenAI、Anthropic、Responses、Gemini 等请求。 |
| 路由与凭据治理 | `CURRENT/PARTIAL` | URSM、P2C/Bandit、tier、sticky、health、failover 已有；TPM、统一 retry/lease 仍需收口。 |
| Pipeline | `CURRENT/PARTIAL` | 引擎、v2 demo、旁路和 v1 wrapper 已有；不是唯一生产主链。 |
| Session V2 | `SHADOW` | schema/writer/cache/persisted hook 已有，默认关闭或灰度，不是 canonical owner。 |
| Gateway → ASM | `CURRENT/PARTIAL` | 依赖 endpoint/secret 配置和 durable event 契约；必须监控 lag/DLQ/replay。 |
| Maintain | `CURRENT/PARTIAL` | 已有控制面和 Gateway proxy 回退；生产 tenant/RLS/ownership 门禁另见拆分文档。 |
| MCP / A2A / Fusion | `TARGET/PARTIAL` | registry/policy、任务投影等基础存在；完整 transport/execution 不应写为已上线。 |

## 快速开始

### 编译

```bash
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
cd llm-gateway-go
go build -o gateway ./cmd/gateway

cd installer
go build -o llm-gw-installer ./cmd/llm-gw-installer
cd ..
```

### 本地启动前提

生产/接近生产环境至少需要：

```text
PostgreSQL URL
Redis URL 与明确降级策略
credential encryption key
API/Admin/JWT/Cursor 等认证密钥
Provider credentials 和 tenant/API key 数据
```

不得把缺少 DB、认证密钥、tenant context 或 Redis 的降级行为默认视为安全。部署前请阅读 [部署入口](docs/06-deployment/README.md) 和 [测试矩阵](docs/05-testing/01-strategy/test-matrix.md)。

### 健康检查

默认端口、实际 health/readiness 和 deployment contract 以当前 compose/systemd/K8s 配置及主入口为准。典型本地检查：

```bash
curl http://localhost:8781/healthz
```

## 核心模块导航

| 模块 | 主要位置 | 职责 |
|---|---|---|
| 生产入口 | `cmd/gateway/` | composition root、HTTP/SSE、DB/Redis、worker、shutdown。 |
| 数据面 | `domains/streaming/`, `domains/streaming/executors/` | Handler、候选执行、流式中继、错误映射。 |
| 调度 | `domains/dispatch/` | model/credential queues、forwarder、failover、retry scheduler。 |
| 路由 | `autoroute/`, `domains/routing/`, `domains/ursm/v2/` | auto route、状态、候选排序和约束。 |
| 凭据/资源 | `domains/credential/`, `credentialfpslot/`, `ratelimit/`, `pool/` | 健康、slot、限流、连接与资源压力。 |
| 协议/IR | `adapter/`, `internal/ir/`, `domains/transformation/`, `upstream/` | 协议转换、Provider 请求和响应。 |
| 会话/审计 | `domains/session/`, `telemetry/`, `domains/requestjourney/` | cache、shadow writer、WAL、request/usage/audit。 |
| 控制面 | `admin/`, `bg/`, `web/`, `settings/` | Admin API、worker、Vue UI、运行时设置。 |
| 交付 | `installer/`, `deploy/`, `packaging/` | 安装、升级、回滚、部署物料。 |

## 当前生产门禁

以下事项是扩流、ownership 切换或删除旧路径前必须解决/验证的工作，不是已完成能力：

1. `/api/quality/*` 和独立 quality-service 的 auth、tenant scope、RLS 与数据源一致性；
2. legacy admin fallback、DB 不可用时数据面 auth、`?token=` JWT 的 fail-closed 边界；
3. Maintain tenant policy、FORCE RLS、least-privilege role 与资源 ownership；
4. Session V2 shadow 的 durable compensation、reconciliation、backfill、dual-read、replay 和 rollback；
5. h2c 最终 handler 对 Maintain proxy/static 的 wiring；
6. TPM、FP/concurrency union lease、统一 retry budget、多模态 billing 和 worker shutdown；
7. Compose/systemd/K8s 的端口、环境变量、healthcheck 和 timeout 契约。

完整任务、代码落点、测试和退出条件见 [`optimization-roadmap.md`](docs/03-design/01-architecture/architecture/optimization-roadmap.md)。

## 三服务边界

- **Gateway**：唯一 Provider executor、流式执行 owner、routing/limiter/cost enforcement point，以及切换前的 canonical request/session/body producer。
- **Session Manager**：消费 Gateway 事实，负责 projection、analysis、approval、task、audit 与治理 UI；不保存完整 prompt/response，不重建 Provider/router/MCP execution。
- **Maintain**：负责 artifact、License、activation、install、upgrade 和控制面运维；不承载 Gateway 数据面执行。

详细迁移状态和 Gate 见 [`../docs/拆分/README.md`](../docs/拆分/README.md)。

## OmniRoute 参考

Gateway 会选择性吸收 Provider catalog、request-aware strategy、compression stage、MCP/A2A/Fusion 的协议与治理语义；不会直接复制 Node/SQLite/JS VM/provider executor。上游 provider/tool/节省率数字仅视为声明或验收目标。详见 [`omniroute-integration-boundary.md`](docs/03-design/01-architecture/architecture/omniroute-integration-boundary.md)。

## 安全与贡献

- 漏洞报告和支持版本：[`SECURITY.md`](SECURITY.md)
- 贡献规范：[`CONTRIBUTING.md`](CONTRIBUTING.md)
- 文档和部署示例不得包含真实 API key、token、密码或第三方 App Secret；历史泄露应走脱敏、轮换和审计专项。

## License

[Apache License 2.0](LICENSE)
