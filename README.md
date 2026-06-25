# SI-LLM-Gateway（超级智能大模型网关）

> **让企业安全、合规、低成本地使用全球各类大模型与 AI 工具** —— 一套网关，统一管控、智能整合。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://golang.org)
[![Multi-Tenant](https://img.shields.io/badge/Multi--Tenant-RLS%20enabled-brightgreen.svg)]()

---

## ✨ 四大核心价值

| 价值 | 客户感知 |
|------|---------|
| **安全** | AI Guardrails · DLP · Inline Interception · Vibe Coding 治理 |
| **稳定** | Multi-cloud Orchestration · Circuit Breaker · 99.9% SLA |
| **低成本** | Semantic Cache · Auto-routing · Token Metering |
| **企业资源整合** | MCP 工具网关 · API Hub 资产中心 · 全链路审计 |

---

## 🏗️ 三大产品支柱

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control（管控）       │   Govern（治理）        │   Secure（安全）         │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ Token 用量追踪        │ 🔨 API Hub 资产中心     │ 🔨 Model Armor          │
│ ✅ 智能路由 + 粘性会话   │ 🔨 自动发现             │ 🔨 敏感数据脱敏 (SDP)   │
│ ✅ 语义缓存 + Funnel    │ 🔨 SpecBoost 智能富集   │ 🔨 对抗性提示词防护     │
│ ✅ 全链路审计 + OTel    │ ✅ 多租户 RLS (L1=0)    │ ✅ SIEM/SOAR 对接       │
│ ✅ MaaS 计费            │                        │                        │
└────────────────────────┴────────────────────────┴────────────────────────┘
✅ = 已上线   🔨 = 路线图中
```

---

## 🎯 当前能力

| 能力维度 | 实现 |
|----------|------|
| **协议层** | OpenAI / Anthropic / Responses 兼容 + SSE 流式中继 |
| **路由层** | 智能候选路由 + 粘性会话 + 自动路由（cost/quality 策略） |
| **多租户** | 身份隧道（virtual IP/MAC/ClientID）+ 凭据池 + 38+ 表 RLS |
| **流量治理** | Token 限流 + 语义缓存 + 提示词压缩 + 滑窗算法 |
| **审计** | 全链路审计 + DLQ + 磁盘回退 + OTel + Prometheus |
| **凭据** | 多凭据 + 指纹池 + 自适应探测 + 手动 disable |
| **部署** | 双实例（71 host docker + 184 k3s NodePort），共享 PG schema |

详细架构见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

---

## 🎛️ 产品功能预览

> 以下功能模块均已上线，部署在 184 k3s 节点生产环境。

### 1. 凭据监控 — 多源模型 × 多凭据 的实时健康仪表盘


- **19 凭据 × 18 模型** 二维可用性矩阵，一眼看出哪个凭据下哪个模型出问题
- 每个凭据的 **P95 延迟**、滑动窗口成功率（最近 1 小时）、**并发槽位占用**
- **指纹池 + 自适应探测** 自动避开被上游风控的 IP / UA，失败熔断无需人工介入

### 2. 路由全景 — 双层路由 + 实时决策可观测


- **L1 选模型**：Prompt → 8 类任务分类 → 6 维评分 → Profile 锁定
- **L2 选凭据**：模型解吸 → Tier 回退 → 计费轮次 → P2C 评分 → 执行 / 熔断
- **任务 × 模型热力图** 直观告诉你"什么任务该用什么模型"
- **Sankey 路由流向** 实时展示 14,000+ 请求的最终去向（任务 → 模型 → 供应商）

### 3. 请求日志 — 全链路可检索的会话级审计


- 13,000+ 请求会话，**system prompt + 响应内容** 完整留存可逐条回放
- 字段覆盖：任务类型、客户端模型、出站模型、供应商、Token、延迟、结束原因
- 对接 **OTel + Prometheus**，可按 Key / 租户 / 时间段切片，便于排查与合规审计

### 4. 数据生命周期 — 4 档热温冷分层 + 归档治理


- **热数据 (0-7 天) / 温数据 (7-30 天) / 冷数据 (30-90 天) / 过期 (>90 天)** 自动分层
- **归档预览** 先告知"执行后会动多少条记录"，再执行 — 防止误删
- 增长趋势 + 租户分布，存储治理成本可视化

### 5. 租户管理 — 多租户 + MaaS 计费一体化


- 单租户维度下：**13 用户 / 38 密钥 / 7 天 14,208 请求 / 7.26 亿 Token / $295.50 成本**
- **套餐 + 积分 + 加油包** 三段式计费模型，适合中国 SMB
- MaaS 子菜单：标准模型 / 套餐与充值 / 消耗统计 / 钱包管理 / 账本流水

### 6. 成本价格 — 1000+ 模型 Offer 覆盖可视化


- **1045 个 Offer、410 个模型 100% 覆盖**，**CNY + USD 双币种**
- 按凭据 × 模型 树形视图，一眼看出某个凭据下哪些模型还没定价
- 状态维度：已定价（输入 / 输出）/ 免费 / 缺价 — 定价审计自动化

---

## 🔀 双仓库策略

| Remote | URL | 用途 |
|--------|-----|------|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | **默认**（日常开发） |
| `github` | `git@github.com:halfking/SI-LLM-Gateway.git` | **公开镜像**（阶段发布） |

```bash
git push              # → codeup（无附加检查）
git push github       # → github（自动严格扫描，命中即阻断）
```

敏感信息保护：`.githooks/pre-push` 推送 github 时自动运行 `scripts/scan-secrets.sh` 严格模式（49 规则）。
详见 [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md)。

---

## 🚦 快速开始

```bash
# 1. 克隆
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
cd llm-gateway-go

# 2. 安装钩子
./scripts/pre-commit-install.sh        # pre-commit: go vet + SQL lint + migration 编号
./scripts/install-githooks.sh          # pre-push: github 推送敏感信息扫描

# 3. 构建
go build -o gateway ./cmd/gateway

# 4. 启动
./gateway --config=configs/local.yaml
```

健康检查：`curl http://localhost:8781/healthz` → `200 OK`

---

## 📚 文档索引

| 类别 | 文档 |
|------|------|
| **架构** | [`ARCHITECTURE.md`](ARCHITECTURE.md) — V3 架构方案 |
| **双仓库** | [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md) — codeup ⇄ github 工作流 |
| **安全** | [`SECURITY.md`](SECURITY.md) — 漏洞报告 + 扫描器用法 |
| **贡献** | [`CONTRIBUTING.md`](CONTRIBUTING.md) — 开发规范 + 提交规范 |
| **法务** | [`docs/legal/disguise-compliance.md`](docs/legal/disguise-compliance.md) — 请求伪装合规白名单 |
| **A2A** | [`docs/a2a-spec-2027.md`](docs/a2a-spec-2027.md) — Agent 间通信协议调研 |
| **Armor** | [`docs/armor-sdp-feasibility.md`](docs/armor-sdp-feasibility.md) — 提示词注入 + SDP 可行性 |

---

## 📐 差异化定位

| 维度 | 通用 AI Gateway | **SI-LLM-Gateway** |
|------|-----------------|---------------------|
| 部署 | SaaS / On-Prem | **完全私有部署**（已 184 k3s 生产） |
| 数据合规 | 出域 | **数据全在企业内** |
| 计费 | 用量计费（USD） | **套餐 + 积分 + 加油包**（适合中国 SMB） |
| 上游模型 | 主打少数厂商 | **全模型 + 国产 + 本地** |
| 多凭据指纹池 | 基础 | **50+ UA + 35 Accept-Language + 11 utls profile** |
| MCP 工具网关 | 部分 | **Q3 2026 全量上线** |
| 中文友好 | 一般 | **全中文 UI + 国内模型 + 支付宝接入** |
| 多租户审计 | 标准 | **38+ 表 RLS + 43 轮审计 L1=0** |

---

## 🤝 贡献

参见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。多租户改动必跑 `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant` 三条 linter。

---

## 🔐 安全

- 漏洞报告：见 [`SECURITY.md`](SECURITY.md)
- 公开仓库敏感信息保护：见 [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md)
- 法务白名单：见 [`docs/legal/disguise-compliance.md`](docs/legal/disguise-compliance.md)

---

## 📄 License

[Apache License 2.0](LICENSE)
