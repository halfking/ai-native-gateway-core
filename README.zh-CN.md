# AI Native Gateway（AI 原生网关）

> AI 原生 LLM 网关：不止是代理 —— 面向企业级 AI 流量的**管理平面与会话治理平台**。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)
[![Version](https://img.shields.io/badge/version-2.5.x-green.svg)](CHANGELOG.md)

[English](README.md) | **简体中文** | [日本語](README.ja.md)

[快速开始](#-快速开始) • [核心价值](#-四大核心价值) • [会话治理](#-会话治理) • [功能预览](#-产品功能预览) • [差异化对比](#-差异化定位与竞品对比) • [架构](docs/architecture.md) • [路线图](ROADMAP.md)

---

## AI Native Gateway 是什么？

AI Native Gateway 是一个**开源、可私有部署的 AI 原生 LLM 网关**，为 AI Agent 与 Vibe Coding 时代而生。它远不止转发请求：它对流经企业的每一个 Token 进行**分类、路由、治理、审计与计费**。

- **协议归一化**：兼容 OpenAI / Anthropic / Gemini / Responses API —— 一个端点接入所有模型
- **双层智能路由**：L1 选模型（任务自动分类 → 6 维评分 → Profile 锁定），L2 选凭据（Tier 回退 → 计费轮次 → P2C 评分 → 执行 / 熔断）
- **会话治理**：粘性会话、全量内容会话回放、自动提示词压缩、4 档数据生命周期 —— 会话（而非单次请求）是一等治理对象
- **多租户**：PostgreSQL RLS 数据库级隔离（38+ 表），配套租户配额、用量计量与 MaaS 计费
- **凭据管理**：多凭据池 + 指纹伪装 + 自适应探测 + 自动熔断
- **可观测性**：实时请求流、路由全景（热力图 + Sankey）、成本追踪、OTel + Prometheus
- **隐私合规**：100% 私有化部署 —— 所有数据留在企业内

技术栈 **Go + PostgreSQL + Redis**，已在 k3s 生产环境运行（双实例共享同一 PG schema），面向需要对 AI 基础设施拥有完全控制权的组织。

---

## ✨ 四大核心价值

| 价值 | 客户感知 |
|------|----------|
| **安全** | AI Guardrails · DLP · Inline Interception · Vibe Coding 治理 · SIEM/SOAR 对接 |
| **稳定** | Multi-cloud Orchestration · Circuit Breaker（自动熔断恢复）· 99.9% SLA 目标 |
| **低成本** | Semantic Cache · Auto-routing（cost/quality 策略）· Token Metering · 提示词压缩 |
| **企业资源整合** | MCP 工具网关（路线图）· API Hub 资产中心（路线图）· 全链路审计 · SIEM/SOAR |

## 🏗️ 三大产品支柱

| Control（管控） | Govern（治理） | Secure（安全） |
|-----------------|----------------|-----------------|
| ✅ Token 用量追踪 | 🔨 API Hub 资产中心 | 🔨 Model Armor |
| ✅ 智能路由 + 粘性会话 | 🔨 自动发现 | 🔨 敏感数据脱敏（SDP） |
| ✅ 语义缓存 + Funnel | 🔨 SpecBoost 智能富集 | 🔨 对抗性提示词防护 |
| ✅ 全链路审计 + OTel | ✅ 多租户 RLS（L1=0） | ✅ SIEM/SOAR 对接 |
| ✅ MaaS 计费 | | |

✅ = 已上线 &nbsp;·&nbsp; 🔨 = 路线图中

## 🎯 当前能力

| 能力维度 | 实现 |
|----------|------|
| **协议层** | OpenAI / Anthropic / Gemini / Responses 兼容 + SSE 流式中继（增量完整性校验） |
| **路由层** | 双层路由（模型 → 凭据）+ 粘性会话 + 自动路由（cost/quality 策略） |
| **多租户** | 身份隧道（virtual IP/MAC/ClientID）+ 凭据池 + 38+ 表 RLS |
| **流量治理** | Token 限流（TPM/RPM）+ 语义缓存 + 提示词压缩 + 滑窗算法 |
| **审计** | 全链路审计 + DLQ + 磁盘回退 + OTel + Prometheus |
| **凭据** | 多凭据 + 指纹池 + 自适应探测 + 手动 disable |
| **部署** | 双实例（Docker + k3s NodePort），共享 PG schema |

详细架构见 [架构文档](docs/architecture.md)。

---

## 🧠 会话治理

大多数网关把每个请求当作孤立事件处理。AI Native Gateway 把**会话**作为治理的基本单元 —— 因为在编码 Agent 与长时助手的时代，一次"请求"往往不是事情的全部。

- **粘性会话绑定**：会话与凭据绑定，模型切换与故障转移后对话上下文不丢失 —— 任务中途不会发生静默的上下文丢失
- **会话级审计与回放**：system prompt + 响应内容完整留存、可逐条回放（生产环境已留存 13,000+ 会话），字段覆盖任务类型、客户端模型、出站模型、供应商、Token、延迟、结束原因 —— 可按 Key / 租户 / 时间段切片，支撑排查与合规审计
- **自动提示词压缩**：当请求接近供应商真实上下文窗口（约 80% 触发）时，在分发前执行消息级压缩 —— 长 Agent 会话适配更小窗口而不是直接失败。每次压缩事件（策略、阈值、压缩前后大小）都记录在请求上，可在请求详情中查看
- **会话元数据智能**：自动任务类型标注（10 类工作类型）、项目归属、标题提取，把原始流量变成可检索的知识
- **身份隧道**：Agent 流量通过 virtual IP/MAC/ClientID 归属到租户 —— 即使大量 Agent 共享同一出口，多租户隔离依然成立
- **4 档数据生命周期**：热（0–7 天）/ 温（7–30 天）/ 冷（30–90 天）/ 过期（>90 天）自动分层，归档预览先告知"执行后会动多少条记录"再执行 —— 防止误删

---

## 🎛️ 产品功能预览

以下功能模块均已上线，部署在 k3s 生产环境。截图来自真实本地部署（1728×1050，全量数据加载后）。

### 1. 凭据监控 — 多源模型 × 多凭据的实时健康仪表盘

![凭据监控](docs/assets/screenshots/credential-monitor.png)

- **19 凭据 × 18 模型** 二维可用性矩阵，一眼看出哪个凭据下哪个模型出问题
- 每个凭据的 **P95 延迟、滑动窗口成功率（最近 1 小时）、并发槽位占用**
- **指纹池 + 自适应探测** 自动避开被上游风控的 IP / UA（50+ User-Agent · 35 种 Accept-Language · 11 种 uTLS profile），失败熔断无需人工介入

### 2. 路由全景 — 双层路由 + 实时决策可观测

![路由全景](docs/assets/screenshots/routing-panorama.png)

- **L1 选模型**：Prompt → 任务自动分类（10 类工作类型）→ 6 维评分 → Profile 锁定
- **L2 选凭据**：模型解析 → Tier 回退 → 计费轮次 → P2C 评分 → 执行 / 熔断
- **任务 × 模型热力图** 直观告诉你"什么任务该用什么模型"
- **Sankey 路由流向** 实时展示 14,000+ 请求的最终去向（任务 → 模型 → 供应商）

### 3. 请求日志 — 全链路可检索的会话级审计

![请求详情](docs/assets/screenshots/request-detail.png)

- **13,000+ 请求会话**，system prompt + 响应内容完整留存，可逐条回放
- 字段覆盖：任务类型、客户端模型、出站模型、供应商、Token、延迟、结束原因
- 对接 OTel + Prometheus，可按 Key / 租户 / 时间段切片，便于排查与合规审计

### 4. 数据生命周期 — 4 档热温冷分层 + 归档治理

- 热数据（0–7 天）/ 温数据（7–30 天）/ 冷数据（30–90 天）/ 过期（>90 天）自动分层
- **归档预览** 先告知"执行后会动多少条记录"，再执行 —— 防止误删
- 增长趋势 + 租户分布，存储治理成本可视化

### 5. 租户管理 — 多租户 + MaaS 计费一体化

- 单租户维度实测：**13 用户 / 38 密钥 / 7 天 14,208 请求 / 7.26 亿 Token / $295.50 成本**
- **套餐 + 积分 + 加油包** 三段式计费模型，适合中国 SMB
- MaaS 子菜单：标准模型 / 套餐与充值 / 消耗统计 / 钱包管理 / 账本流水

### 6. 成本价格 — 1000+ 模型 Offer 覆盖可视化

- **1045 个 Offer、410 个模型** 100% 覆盖，CNY + USD 双币种
- 按**凭据 × 模型**树形视图，一眼看出某个凭据下哪些模型还没定价
- 状态维度：已定价（输入 / 输出）/ 免费 / 缺价 —— 定价审计自动化

### 更多界面实录

**实时请求流看板**（按处理队列分组的实时请求流 + 分发链路统计）：

![实时请求流](docs/assets/screenshots/dashboard-request-stream.png)

**工作类型配置**（任务自动分类 + 24h 分布 + 路由决策统计）：

![工作类型](docs/assets/screenshots/work-types.png)

**免费资源池**（自带 Key、供应商模板：Groq / Google AI Studio / OpenRouter / SiliconFlow / 智谱，按模型设置路由优先级）：

![免费资源池](docs/assets/screenshots/free-pool.png)

---

## 🚀 快速开始

### 方式 A：Docker Compose（推荐，10 分钟内跑通）

```bash
# 1. 克隆
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git ai-native-gateway
cd ai-native-gateway
# （GitHub 镜像：git clone https://github.com/halfking/SI-LLM-Gateway.git）

# 2. 生成安全密钥
cp .env.quickstart.example .env
# 编辑 .env，填入安全随机值（生成命令见文件内注释）

# 3. 启动整套栈（PostgreSQL + Redis + Gateway）
docker compose -f docker-compose.quickstart.yml up -d

# 4. 健康检查
curl http://localhost:8781/healthz
# {"status":"ok"}

# 5. 打开内嵌管理界面
open http://localhost:8781/admin
```

### 方式 B：源码构建

```bash
go build -o gateway ./cmd/gateway
./gateway --config=configs/local.yaml
curl http://localhost:8781/healthz   # → 200 OK
```

详细步骤见 [入门指南](docs/getting-started.md)。

安装 Git 钩子（可选，推荐开发者）：

```bash
./scripts/install-githooks.sh --pre-commit  # pre-commit: go vet + SQL lint + migration 编号校验
./scripts/install-githooks.sh               # pre-push: 推送 github 时敏感信息扫描
```

---

## 部署模式

AI Native Gateway 同时支持**完整生产栈**与**单机最小化部署**：

| 模式 | 说明 | 状态 |
|------|------|------|
| **Docker Compose** | 一键启动含 PostgreSQL/Redis 的完整栈 | ✅ 评估推荐 |
| **二进制 + systemd** | Linux 主机生产部署 | ✅ 提供 installer |
| **Kubernetes** | Deployment + ConfigMap + Service 清单 | ⚠️ 测试级（内部生产实际运行于 k3s） |

**生产栈组成**：Gateway + PostgreSQL 14+（持久状态、RLS）+ Redis 7+（热状态、限流）+ 内嵌管理界面，可选 Prometheus/Grafana 监控。

快速启动栈与生产环境使用**同一二进制与同一 schema** —— 上生产只需切换到托管的 PostgreSQL/Redis 并加 TLS，无需改变配置语义。内部生产环境运行**双实例（Docker 主机 + k3s NodePort）共享同一 PG schema**。

**生产要求**：外部 PostgreSQL 14+ 与 Redis 7+、TLS 终结（反向代理）、密钥管理、备份与监控。

详见 [生产部署文档](docs/06-deployment/)。

---

## 📐 差异化定位与竞品对比

### 对比通用 AI Gateway

| 维度 | 通用 AI Gateway | AI Native Gateway |
|------|-----------------|-------------------|
| **部署** | SaaS / On-Prem | 完全私有部署（k3s 生产验证） |
| **数据合规** | 数据出域 | 数据全在企业内 |
| **计费** | 用量计费（USD） | 套餐 + 积分 + 加油包，适合中国 SMB，支持支付宝 |
| **上游模型** | 主打少数厂商 | 全模型 + 国产模型 + 本地模型 |
| **多凭据指纹池** | 基础 | 50+ UA · 35 Accept-Language · 11 uTLS profile |
| **会话治理** | 请求级日志 | 粘性会话 + 全量回放 + 压缩 + 4 档生命周期 |
| **MCP 工具网关** | 部分 | 2026 Q3 全量交付 |
| **中文友好** | 一般 | 全中文 UI + 国产模型 + 支付宝接入 |
| **多租户审计** | 标准 | 38+ 表 RLS · 租户审计 L1=0 |

### 对比具名竞品

| 特性 | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|------|-------------------|---------|-----------|---------|---------|
| **部署** | 私有化自部署 | SaaS + OSS | 自托管（Node） | SaaS | OSS |
| **多租户** | 原生（PG RLS） | 基础 | 单机导向 | 完整（SaaS） | 插件实现 |
| **管理界面** | 内嵌 Vue SPA | CLI | Web UI | SaaS UI | Kong Manager |
| **数据驻留** | 100% 私有 | 视模式而定 | 100% 私有 | 云端（SaaS） | 可自托管 |
| **许可证** | Apache 2.0 | MIT | 见上游 | 商业专有 | Apache 2.0 |

**选 AI Native Gateway，如果你需要**：

- 完全的数据驻留控制（无外部 SaaS 依赖）
- 数据库级的深度多租户隔离
- 会话级治理与取证，而不只是请求日志
- 单个 Go 二进制内嵌完整管理界面
- 适合中国 SMB 的 MaaS 计费（套餐 + 积分 + 加油包）

**选其他方案，如果你需要**：

- 最大供应商覆盖（100+ 供应商）→ LiteLLM
- Node.js 技术栈的自托管网关 → OmniRoute
- 零运维托管服务 → Portkey
- 通用 API 网关 + LLM → Kong

详见 [详细对比](docs/comparison.md)。

---

## 路线图

**当前（v2.x）**：

- ✅ OpenAI / Anthropic / Gemini / Responses 协议支持
- ✅ PostgreSQL RLS 多租户隔离
- ✅ 智能路由 + 粘性会话
- ✅ 会话治理：压缩、回放、生命周期
- ✅ Vue.js 管理控制台
- ✅ Docker Compose 快速启动

**近期（3–6 个月）**：

- 🚧 增强 cost/quality 感知路由
- 🚧 生产级 Kubernetes Helm Charts
- 🚧 Grafana 仪表盘模板
- 🚧 高阶可观测性（会话取证、决策回放）

**探索中（6–12+ 个月）**：

- 🔬 MCP（Model Context Protocol）网关集成 —— 2026 Q3 全量交付
- 🔬 Agent 间通信（A2A）协议支持
- 🔬 Kubernetes Operator（CRD 化部署）

完整路线图见 [ROADMAP.md](ROADMAP.md)。

---

## 📚 文档索引

| 类别 | 文档 |
|------|------|
| 入门 | [docs/getting-started.md](docs/getting-started.md) — 10 分钟部署 |
| 架构 | [docs/architecture.md](docs/architecture.md) — 系统设计与组件 |
| API | [docs/03-design/01-architecture/architecture/API.md](docs/03-design/01-architecture/architecture/API.md) — 数据面与管理面 API 规范 |
| 环境 | [docs/environment.md](docs/environment.md) — 部署环境与变量 |
| 速查 | [docs/QUICK_REFERENCE.md](docs/QUICK_REFERENCE.md) — 常用命令与排障 |
| 对比 | [docs/comparison.md](docs/comparison.md) — vs LiteLLM / OmniRoute / Portkey / Kong |
| 项目总览 | [docs/PROJECT_OVERVIEW.md](docs/PROJECT_OVERVIEW.md) — 功能与模块地图 |
| 文档总目录 | [docs/INDEX.md](docs/INDEX.md) — 全量文档导航 |
| 双仓库策略 | [docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md) — codeup ⇄ github 工作流 |
| 安全 | [SECURITY.md](SECURITY.md) — 漏洞报告 + 扫描器用法 |
| 法务 | [docs/02-resources/compliance/legal/disguise-compliance.md](docs/02-resources/compliance/legal/disguise-compliance.md) — 请求伪装合规白名单 |
| A2A 调研 | [docs/03-design/01-architecture/architecture/a2a-spec-2027.md](docs/03-design/01-architecture/architecture/a2a-spec-2027.md) — Agent 间通信协议调研 |
| Armor/SDP | [docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md](docs/03-design/01-architecture/architecture/armor-sdp-feasibility.md) — 提示词注入 + SDP 可行性 |

---

## 🔀 双仓库策略

| Remote | URL | 用途 |
|--------|-----|------|
| `codeup`（origin） | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | 默认（日常开发） |
| `github` | `git@github.com:halfking/SI-LLM-Gateway.git` | 公开镜像（阶段发布） |

```bash
git push              # → codeup（无附加检查）
git push github       # → github（自动严格扫描，命中即阻断）
```

敏感信息保护：`.githooks/pre-push` 推送 github 时自动运行 `scripts/scan-secrets.sh` 严格模式（49 规则）。详见[双仓库策略文档](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md)。

---

## 🤝 贡献

欢迎参与贡献！开发环境搭建、代码规范与 PR 流程见 [CONTRIBUTING.md](CONTRIBUTING.md)。

多租户相关改动必跑三条 linter：`lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant`。

## 🔐 安全

- 漏洞报告：见 [SECURITY.md](SECURITY.md)
- 公开仓库敏感信息保护：见[双仓库策略](docs/06-deployment/04-runbooks/operations/REPO-MIRROR-POLICY.md)
- 法务白名单：见 [docs/02-resources/compliance/legal/disguise-compliance.md](docs/02-resources/compliance/legal/disguise-compliance.md)

## 许可证

基于 [Apache License 2.0](LICENSE) 开源。再分发时须保留版权声明与 [NOTICE](NOTICE) 文件。

**商用说明**：Apache 2.0 允许商用。以源码或二进制形式再分发时，须保留版权声明与 NOTICE 文件；不再分发的内部商用仅需遵守许可证基本义务。

## 致谢

AI Native Gateway 使用了以下开源项目组件：

- Go 标准库（BSD-3-Clause）
- PostgreSQL 驱动（MIT）
- Redis 客户端（BSD-2-Clause）
- Vue.js 与 Element Plus（MIT）
- 完整清单见 [NOTICE](NOTICE)

---

由 AI Native Gateway 社区用 ❤️ 构建
