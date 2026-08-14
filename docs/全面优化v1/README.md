# llm-gateway-go 全面优化 v1 · 功能特性与任务计划

> 编制日期：2026-08-15 ｜ 上层方案：`ai-native-tools/docs/全面优化v1/`
> 基线文档：父仓 `docs/拆分/31-端到端用户流程与三模块一体.md`（SSOT）、本仓 `docs/会话优化v3/`、`docs/修订0811/`

## 一、平台定位

**推理平面唯一 SSOT**：模型/会话/配额/计费的权威所有者；平台内所有 LLM 流量（ACC、RedClaw、Memora、Pocket）的唯一出口。同时是插件运行时宿主（承载 ai-session-manager 等）与 License/分发/升级通道（ai-native-maintain 协同）。

## 二、功能特性清单

### 保留并强化（生产能力，不推倒）

| 特性 | 现状 | v1 动作 |
| --- | --- | --- |
| 智能路由（L1 任务分类选模型 / L2 凭据解吸 + tier 回退 + P2C）、粘性会话 | v3.1 已验证 | 对外提供"平台级模型策略 API"：ACC/RedClaw 只传语义偏好（`acc_task_type`、`agent_client_type` 已有），路由决策全部收归本仓 |
| 凭据池 + 身份隧道 + 50 UA 指纹 + 健康分级 | 生产运行 | 维持；新增平台调用方（façade 代理流量）凭据隔离 |
| 多租户 RLS（38+ 表）+ MaaS 计费 | 生产运行 | 为 ACC/Memora/Pocket 建立独立租户与配额策略（借鉴 LiteLLM virtual-key 模型：key=身份+策略+预算） |
| 全链路审计 + DLQ + columnar 分区 | 生产运行 | 审计事件对齐平台 envelope，可被 SM 聚合检索 |
| 会话事实（session_turns/bodies、SessionCompressor、L0-L3 缓存、注入检测） | 生产运行 | 会话**分析与展示**职责继续外迁 SM（只读投影），事实与写入路径保留 |
| 插件运行时（下载/校验/启动/握手/代理 + plugin-nav） | 单插件（SM） | 通用化：多插件并发、插件级配额与网络隔离 |
| License/升级/分发（maintain 协同） | 生产运行 | 作为平台统一升级通道，承接其他模块发布包的可行性评估（P2） |

### 新增（v1 交付）

1. **平台调用方租户化**：为 ACC、RedClaw worker、Memora、Pocket 后端签发独立 service 身份与配额；旁路直连 provider 的流量归零（ADR-1 落地侧）。
2. **审计事件平台化**：request_logs/审计事件增加平台 envelope 字段（correlation_id 透传、schema_version），暴露订阅通道（outbox/webhook）供 SM projection。
3. **`GET /api/v2/capabilities`**（数据面与管理面各一）。
4. **D1 精简 profile**：非必要 bg worker（~50 个中可选部分）与探针可关闭，支撑 16GB all-in-one（常驻 ≤1GB）。
5. **MCP proxy 评估（P2）**：对齐 Higress 方向，评估工具级（MCP）流量在本网关收口的可行性——仅评估，不实施。

### 明确不做

- 不做业务会话分析/聚类/预算 UI（SM 职责）；不写 SM 库；管理面不改业务语义。
- 不承接任务编排、记忆治理。
- v1 不引入新框架替换 gin/echo 双栈（重构风险大于收益，列 v2 评估）。

## 三、任务计划（对齐顶层 Phase 0-4；注意与 origin/main 同步——两副本本地均落后 3-4 提交）

| ID | 任务 | 对应顶层任务 | 优先级 | 验收 |
| --- | --- | --- | --- | --- |
| GW-0.0 | `git pull` 同步 origin/main（`7186325b2 feat(probe): async TriggerManual`）；确认 `llm-gateway-go/` 为唯一工作副本（`-3` 副本仅审计用） | — | **P0** | 两副本状态一致 |
| GW-0.1 | correlation_id 全链路透传（数据面→request_logs→审计） | 0-4 | P0 | 六系统 trace 校验脚本通过 |
| GW-0.2 | capabilities endpoint | 0-5 | P0 | 探测通过 |
| GW-1.1 | 平台调用方租户/密钥/配额开通（ACC/RedClaw/Memora/Pocket）+ 旁路流量清零校验 | 1-6 | P0 | 网关统计 0 旁路 |
| GW-2.1 | 审计事件订阅通道（outbox/webhook）供 SM projection | 2-3 | P0 | SM 消费联调通过 |
| GW-2.2 | 插件运行时通用化（多插件、配额、隔离） | 2-3 关联 | P1 | 双插件共存冒烟 |
| GW-3.1 | D1 精简 profile（worker/探针开关 + 资源限制） | 3-2 | P0 | all-in-one 常驻 ≤1GB |
| GW-3.2 | 代理附加延迟基准（p95 < 80ms 保持） | 3-4 | P1 | 基准报告 |
| GW-4.1 | MCP proxy 可行性评估报告 | 4-x | P2 | 报告 |
| GW-4.2 | 会话优化 V3.2 收尾与全方面测试整改按既有文档继续 | — | P1 | 既有验收门禁 |

## 四、风险与依赖

- 本仓迭代极快（日均多提交）：跨团队任务（envelope、租户开通）需在 CHANGELOG 标注平台坐标，避免与内部优化冲突。
- SM projection 依赖审计事件稳定性：订阅通道上线前冻结相关表 schema 一个版本周期。
- SOPS/env 与 154/245/252 环境差异：新增租户密钥走同一 env-injector 流程，禁止明文落仓。
