# 01 · 核心价值与定位

> **核心问题**：把"我们到底是什么"讲清楚，挡住"那为什么不直接用 LiteLLM / Portkey / Kong AI Gateway"的灵魂拷问。
> **本节断言**：v6 的所有优化动作都围绕这 6 项核心价值展开；任何优化如果削弱其中任一项，应当被否决。

---

## 1. 一句话定位

> **llm-gateway-go 是面向多租户、强可观测、可审计、可插拔的 LLM 流量枢纽**。它的核心不是"协议转换"或"Provider 路由"，而是"在多 Provider × 多凭据 × 多租户 × 强合规约束下，把每一次 LLM 调用变成可解释、可追责、可回滚、可计费的工程事件"。

通俗一点：**让"给一批企业客户同时跑 5 家 LLM 厂商"这件事真的能跑一年不出事**。

---

## 2. 6 项核心价值（不可替代）

每一项都对应一类外部客户痛点；如果未来某一项被某竞品彻底超过，应当围绕其余 5 项继续投资，而不是模仿。

| # | 核心价值 | 客户痛点 | 当前证据 | 关键代码 / 文档 |
|---|---|---|---|---|
| **V1** | **多协议等价数据面** | 企业客户端混用 OpenAI/Anthropic/Responses/Gemini SDK；要求同一后端、不同入站协议能产出等价结果 | `domains/streaming/` 348 文件覆盖 Chat/Messages/Responses/Embeddings/Gemini native；`adapter/unified/` | [`runtime-request-flow.md`](../../03-design/01-architecture/architecture/runtime-request-flow.md)、[`ARCHITECTURE.md` §3.1](../../03-design/01-architecture/architecture/ARCHITECTURE.md) |
| **V2** | **企业级多租户凭据治理** | 不同客户、不同部门、不同项目共用同一网关但凭据、限速、配额、审计必须严格隔离 | URSM v2（Redis/Lua 实时 + PG durable）+ `domains/credentialstate/` + `pool/` + `resolve/` + `credentialfpslot/` | [`routing-and-state.md`](../../03-design/01-architecture/architecture/routing-and-state.md)、[`omniroute-integration-boundary.md`](../../03-design/01-architecture/architecture/omniroute-integration-boundary.md) |
| **V3** | **请求级可解释可重放** | 一次 LLM 调用必须能告诉客户"为什么挑了这个模型 × 这个凭据 × 这个时间窗；这条路径上发生了多少次失败 / 切换 / 重试 / 限速" | `domains/requestjourney/` + `internal/liveactions/` + `lifecycle_states_v1.json` 冻结 + `restart_semantics_v1_valid.json` 4 lane | [`CONTRACT_FREEZE_2026-08-22.md`](../../会话优化v4/CONTRACT_FREEZE_2026-08-22.md) §1–§2 |
| **V4** | **可插拔协议扩展（MCP/A2A/Fusion）** | Agent 类客户需要"网关不仅代理模型，还能作为 Agent 协作枢纽 / Tool 注册中心" | `plugin-runtime/` 完整 sandbox + registry；MCP stdio/HTTP 计划、`/v2/*` 验证路径 | [`omniroute-integration-boundary.md`](../../03-design/01-architecture/architecture/omniroute-integration-boundary.md) §6–§8 |
| **V5** | **强合规可观测** | 监管 / 客户法务要求"每一次调用有审计、敏感数据可识别可脱敏、计费可对账" | `security/armor/` PII 检测 + Presidio sidecar（Q4 B2）+ `telemetry/` + `metrics/` + OTel + `request_logs_hot` 分区 | [`armor-sdp-feasibility.md`](../../03-design/01-architecture/architecture/armor-sdp-feasibility.md)、[`stats-reconciliation.md`](../../stats-reconciliation.md) |
| **V6** | **强韧部署模型** | 客户现场有"离线交付、跨厂商硬件、运维人力薄弱、断电/重启/网络抖动频发"等约束；要求网关在故障/重启/灰度/跨厂商之间都能"平滑存活" | 零停机部署、Maintain 兼容代理、`installer/` 独立 module、systemd + K3s + docker-compose 多模式 | [`deploy/zero-downtime-design.md`](../../03-design/01-architecture/deploy-zero-downtime/zero-downtime-design.md)、[`REPO_LAYOUT.md`](../../03-design/01-architecture/architecture/REPO_LAYOUT.md) |

> 这 6 项不是技术清单，是"为什么不替换我们"的销售论据。v6 的优化必须能解释"对每一项是加固、补缺、还是不动"。

---

## 3. 与同类的差异化对照

| 维度 | LiteLLM / Portkey（云优先） | Kong AI Gateway / Cloudflare AI Gateway（边缘/网关优先） | **llm-gateway-go（私有 / 多租户优先）** |
|---|---|---|---|
| 主要卖点 | 一行代码替换 Provider | 在 L7 网关上做 LLM 治理 | **多租户、审计、可解释、离线交付** |
| 部署形态 | SaaS + Docker | SaaS / Edge | 离线 RPM 包、systemd / docker-compose / K3s / 自定义硬件 |
| 多租户粒度 | 团队级 | 路由级 | **API key × 租户 × 项目 × 业务线** 4 层 |
| 凭据治理 | 简单 failover | 部分 credential pool | **URSM 状态机 + 探针 + 指纹槽位 + 限速 + 信誉** |
| 协议覆盖 | OpenAI 兼容 + 少量 Anthropic | OpenAI 兼容 | **OpenAI + Anthropic + Responses + Gemini native + 私有 Responses** |
| Agent 协议 | 实验 | 实验 | **plugin-runtime + MCP stdio/HTTP/SSE + A2A message/send 计划 + Fusion 设计** |
| 合规 / 审计 | 通用 SaaS 日志 | 边缘日志 | **PII 脱敏（Presidio sidecar）+ tenant RLS + ASM 投影 + Maintain license** |
| 数据驻留 | 厂商云 | 边缘节点 | **客户自有 154/245/252/71 多机房** |

> 对外口径：v6 阶段我们打的不是"通用 LLM 代理"市场，而是"**多租户 LLM 流量枢纽 + 离线可交付 + 可合规审计**"市场。

---

## 4. 核心价值的当前强弱盘点

| 价值 | 强 | 中 | 弱 | v6 目标 |
|---|---|---|---|---|
| V1 多协议 | OpenAI / Anthropic / Responses / Gemini native 4 协议都跑通 | Responses 私有扩展尚有兼容性补强空间 | GLM 等私有协议桥接靠 `executors/executor_glm.go` 单文件 | V6-W1 收口私有协议桥；V6-W4 引入 litellm-style 兼容层（按需） |
| V2 多租户凭据 | URSM 状态机 + Redis Lua + 探针双轮 + 指纹槽位 | `domains/credentialstate/` 与顶层 `credentialhealth/`/`credentialfpslot/` 重叠职责 | Maintain / ASM 真实 RLS negative test 未实证；tenant GUC 缺省默认回退（fail-open） | V6-W0 收口；V6-W3 强制 RLS negative tests |
| V3 可解释可重放 | 五类身份契约 + 四种 lane + lifecycle 三态 + 11 个 error_kind 已冻结 | 重试预算仍多层叠加（stream retry / survival / dispatch / goal）；attempts 计数未统一 | 真实租户重放工具未到运维手册级别 | V6-W1 统一 retry budget；V6-W2 落重放 runbook |
| V4 协议扩展 | plugin-runtime 完整 | MCP stdio read-only 部分就绪；A2A / Fusion 仅有目录与概念 | 缺统一 transport + tenant/policy/audit 串联 | V6-W4 落地 MCP stdio→HTTP→SSE；A2A 仅同步 message/send 试点；Fusion 仅 Opt-in non-stream |
| V5 合规可观测 | armor PII、Presidio sidecar、OTel、Prometheus、`request_logs_hot` 分区 | `armor-sdp-feasibility.md` 还在 Q4 B2 实施清单（6.1-6.10） | 真实生产 PII 命中率 / 误报率缺乏 2 周 observe 数据 | V6-W3 完成 Presidio sidecar + 中文规则包；V6-W3.5 跑 2 周 observe |
| V6 强韧部署 | 零停机 + K3s/compose/systemd 三模式 + Maintain 兼容 | 部分 worker 启动顺序与 shutdown budget 未对齐 systemd timeout | 跨厂商 ARM/x86 镜像维护成本 | V6-W0 worker 生命周期 supervisor；V6-W2 跨架构镜像 matrix CI |

---

## 5. 数据与合规 — "为什么 PII 脱敏是 V6 的 P0 之一"

法律与商业逻辑（v6 范围内不讨论具体法条）：

1. **企业客户的合规门槛是采购合同的硬指标**。"我们记录了所有 prompt 但没法证明不泄漏"在金融/医疗/政企场景等于"不允许接入"。
2. **网关在请求路径上做 PII 检测是最经济的位置**。模型客户端五花八门，App 各自实现不现实；统一在网关拦截后，仅对命中片段做 mask / hash / block，干净又可审计。
3. **流式输出必须一起脱敏**。否则"用户的手机号被切成两个 chunk 输出"会绕过所有正则。这是 [`armor-sdp-feasibility.md`](../../03-design/01-architecture/architecture/armor-sdp-feasibility.md) 专门设计 `SDPStreamSanitizer`（maxBuffer=32 字节滑窗）的原因。
4. **不能假设 fail-open**。对高安全租户（如金融、政企）必须支持 fail-closed；v6 不能只交付"安全但不可用"的网关。

→ v6 把 PII（armor + Presidio sidecar + 中文规则包）从 Q4 B2 提升到 V6-W3 关键路径，详见 [`03-roadmap-v6-waves.md` §V6-W3](03-roadmap-v6-waves.md) 与 [`04-hot-ideas-and-divergent-suggestions.md` §6](04-hot-ideas-and-divergent-suggestions.md)。

---

## 6. 控制台与可观测 — "为什么 Dashboard 也是 V6"

[`docs/04-implementation/plan/2026-08-20-dashboard-overview-node-optimization-plan.md`](../../04-implementation/plan/2026-08-20-dashboard-overview-node-optimization-plan.md) 已经把 D1–D4 实现到 `LOCAL_VERIFIED`，证明一件事：**前端必须可观测、必须可解释，否则后端再强的优化也"看不见"**。

v6 把"Dashboard 可观测"提升为与 P0 同等优先的交付物：
- **节点维度**：状态四态点 + 拖拽重排（已 D1）；
- **请求维度**：滑动窗口单元格可点击 → RequestLogDetail（已 D5）；
- **决策维度**：X-Gw-Auto-Decision 头 + 决策审计（[`routing-domain.md`](../../03-design/01-architecture/architecture/routing-domain.md) §3）；
- **租户维度**：tenant 视图 Dashboard V2（[`DashboardView.vue`](../../ui-verification/)）+ RLS-friendly 自检；
- **会话维度**：Session 详情 + 对比 + 会话健康（[`ops/session-health-operations.md`](../../runbooks/session-health-operations.md)）。

→ v6 不重写 Dashboard，而是把"D1+D5+后续补丁"作为 **V6-W2 的可观测交付件**，详见 [`03-roadmap-v6-waves.md` §V6-W2](03-roadmap-v6-waves.md)。

---

## 7. 对外宣讲口径（给销售 / TL）

> 一句话："**我们让企业把多供应商 LLM 流量跑得稳、看得清、算得准；不是简单的 API 反向代理**。"

3 个核心数字（如未实测必须标 `TARGET`）：

| 数字 | 当前 | 目标（v6 末） |
|---|---|---|
| 接入供应商数（生产验证） | ~10（已合同） | 30+（参考 OmniRoute 290 仅做路线图，不当事实） |
| 协议支持 | OpenAI / Anthropic / Responses / Gemini native | + MCP stdio + A2A message/send + Fusion opt-in |
| 月活企业租户 | 见 `admin/` 看板（不公开） | 不在此处承诺 |

3 个不承诺的数字：

- ❌ "节省 78-95% 成本" — 这是上游声明，对我们无效。
- ❌ "QPS X 万" — 取决于硬件与多租户隔离粒度，不在此处承诺。
- ❌ "100% PII 拦截" — 任何 PII 系统都做不到；承诺 **observe 模式 2 周 + 误报率 < 5% / 漏报率 < 5%**。

---

## 8. v6 的"价值反退化"红线

任何波次如果出现以下信号，应立即冻结、回滚、复盘：

- **V1 反退化**：某协议回归测试失败；客户端响应格式不匹配（[`adaptive-response-format-converter.md`](../../03-design/01-architecture/architecture/adaptive-response-format-converter.md) 描述的 184 环境案例不可重现）。
- **V2 反退化**：跨租户 negative test 出现 tenant A 可读 tenant B 数据；credentials 解密失败率 > 1%。
- **V3 反退化**：五类身份契约或四种 lane fixture 出现 drift；replay 工具与生产不匹配。
- **V4 反退化**：plugin 启用未签名 / 未授权能力；MCP `tools/list` 暴露 tenant-denied 工具。
- **V5 反退化**：PII observe FNR > 5%；流式输出跨 chunk 命中未拦截。
- **V6 反退化**：systemd SIGTERM 时 worker goroutine 泄漏；跨架构镜像 CI 红。

详见 [`06-risks-and-rollback.md` §2](06-risks-and-rollback.md)。
