# SI-LLM-Gateway（超级智能大模型网关）

> **让企业安全、合规、低成本地使用全球各类大模型与 AI 工具** —— 一套网关，统一管控、智能整合。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://golang.org)
[![Multi-Tenant](https://img.shields.io/badge/Multi--Tenant-RLS%20enabled-brightgreen.svg)]()
[![MCP](https://img.shields.io/badge/MCP-Q3%202026-yellow.svg)]()
[![A2A](https://img.shields.io/badge/A2A-Q1%202027-yellow.svg)]()

---

## 🎯 一句话定位

> **让企业安全、合规、低成本地使用全球各类大模型与 AI 工具** —— 一套网关，统一管控、智能整合。

**SI-LLM-Gateway**（Super Intelligent LLM Gateway）是一个面向企业的 AI & Agent 统一入口，
把"调用 OpenAI / Anthropic / Google / 国产模型 / MCP 工具 / Agent 能力"这件事，
做成可路由、可熔断、可审计、可计费、可被企业管控的标准化 API 出口。

不是"防封号"——而是 **"兼容多类资源"**：安全、稳定、低成本、企业资源整合。

---

## ✨ 四大核心价值

| 价值 | 行业术语 | 客户感知 |
|------|---------|---------|
| **安全** | AI Guardrails · DLP · Inline Interception · Vibe Coding 治理 · Model Armor | "用得起，也用得放心" |
| **稳定** | Multi-cloud Orchestration · Circuit Breaker · 99.9% SLA · 自适应探测 | "再也不会因为一家挂了而停业" |
| **低成本** | Semantic Cache · Auto-routing · Token Metering · 提示词压缩 | "同样的活儿，省 30%-60%" |
| **企业资源整合** | MCP 工具网关 · API Hub 资产中心 · 全链路审计 · Agent Registry | "一个面板管模型/工具/Agent" |

---

## 🏗️ 三大产品支柱

```
┌────────────────────────┬────────────────────────┬────────────────────────┐
│   Control（管控）       │   Govern（治理）        │   Secure（安全）         │
├────────────────────────┼────────────────────────┼────────────────────────┤
│ ✅ Token 用量追踪        │ 🔨 API Hub 资产中心     │ 🔨 Model Armor          │
│ ✅ 智能路由 + 粘性会话   │ 🔨 自动发现 (crawler)   │ 🔨 敏感数据脱敏 (SDP)   │
│ ✅ 语义缓存 + Funnel    │ 🔨 SpecBoost 智能富集   │ 🔨 对抗性提示词防护     │
│ ✅ 全链路审计 + OTel    │ 🔨 合规报告自动化       │ ✅ SIEM/SOAR 对接       │
│ ✅ MaaS 计费            │ ✅ 多租户 RLS (L1=0)    │ ✅ RLS 多租户隔离       │
└────────────────────────┴────────────────────────┴────────────────────────┘
✅ = 已上线   🔨 = 路线图中
```

---

## 🛣️ 远期规划（18 个月路线图 · 2026 H2 - 2027 H2）

| 阶段 | 时间 | 主题 | 关键交付 |
|------|------|------|----------|
| **v3.1** | Q3 2026 (7-9月) | **AI Gateway Pro** | API Hub 资产中心 + MCP Server 托管 + 凭据 SLA |
| **v3.2** | Q4 2026 (10-12月) | **AI Gateway Secure** | Model Armor + SDP + SIEM 对接 + SpecBoost v0 |
| **v4.0** | Q1 2027 (1-3月) | **Agentic Gateway** | Agent Registry + A2A 协议 + 编排层 PoC |
| **v4.1** | Q2 2027 (4-6月) | **Agentic Enterprise** | 资产自动发现 + 合规 GA + 4 行业方案 |
| **v5.0** | Q3 2027 (7-9月) | **公测 → GA** | 外部 2 客户 + 计费收费 + 完整文档体系 |

> 📄 完整路线图：[`docs/产品方案/2026-06-23-llmgw-future-roadmap-and-product-design.md`](docs/产品方案/2026-06-23-llmgw-future-roadmap-and-product-design.md)
> 📄 季度实施计划：[`docs/产品方案/2026-06-23-llmgw-implementation-plan.md`](docs/产品方案/2026-06-23-llmgw-implementation-plan.md)

---

## 🎯 当前能力（截至 2026-06-25）

| 能力维度 | 实现 |
|----------|------|
| **协议层** | OpenAI / Anthropic / Responses 兼容 + SSE 流式中继 |
| **路由层** | 智能候选路由 + 粘性会话 + 自动路由 (cost/quality 策略) |
| **多租户** | 身份隧道（virtual IP/MAC/ClientID）+ 凭据池 + 38+ 表 RLS |
| **流量治理** | Token 限流 + 语义缓存 + 提示词压缩 + 滑窗算法 |
| **审计** | 全链路审计 + DLQ + 磁盘回退 + OTel + Prometheus |
| **凭据** | 多凭据 + 指纹池（50+ UA / 11 utls）+ 自适应探测 + 手动 disable |
| **部署** | 双实例（71 host docker + 184 k3s NodePort），共享 PG schema |

详细架构见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

---

## 🔀 双仓库策略

本仓库同时维护两个远端：

| Remote | URL | 用途 | 推送频率 |
|--------|-----|------|----------|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | **默认**（日常开发） | 每次提交 |
| `github` | `git@github.com:halfking/SI-LLM-Gateway.git` | **公开镜像**（阶段发布） | 手动触发 + 严格扫描 |

### 日常推送

```bash
# 默认推 codeup (无附加检查)
git push

# 显式推 github (自动触发 pre-push 严格扫描，命中即阻断)
git push github
```

**敏感信息保护机制**：`scripts/scan-secrets.sh` 在严格模式下扫描 49+ 规则，
覆盖 LLM API Key / DB 连接串 / 私钥 / 内部 IP / 内部域名 / 已知泄露凭据。
详见 [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md)。

---

## 🚦 快速开始

```bash
# 1. 克隆
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
cd llm-gateway-go

# 2. 安装钩子 (pre-commit + pre-push)
./scripts/pre-commit-install.sh        # pre-commit: go vet + SQL lint + migration number
./scripts/install-githooks.sh          # pre-push: github-push 敏感信息扫描

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
| **架构** | [`ARCHITECTURE.md`](ARCHITECTURE.md) — V3 架构方案（错误分类、连接池、流式、审计、灰度） |
| **远期规划** | [`docs/产品方案/2026-06-23-llmgw-future-roadmap-and-product-design.md`](docs/产品方案/2026-06-23-llmgw-future-roadmap-and-product-design.md) |
| **季度实施** | [`docs/产品方案/2026-06-23-llmgw-implementation-plan.md`](docs/产品方案/2026-06-23-llmgw-implementation-plan.md) |
| **双仓库** | [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md) — codeup ⇄ github 工作流 |
| **安全** | [`SECURITY.md`](SECURITY.md) — 漏洞报告流程 + 扫描器用法 |
| **贡献** | [`CONTRIBUTING.md`](CONTRIBUTING.md) — 开发规范、提交规范、PR 流程 |
| **法务** | [`docs/legal/disguise-compliance.md`](docs/legal/disguise-compliance.md) — 请求伪装合规白名单 |
| **A2A** | [`docs/a2a-spec-2027.md`](docs/a2a-spec-2027.md) — Agent 间通信协议调研 |
| **Armor** | [`docs/armor-sdp-feasibility.md`](docs/armor-sdp-feasibility.md) — 提示词注入 + SDP 可行性 |

---

## 📐 差异化定位

| 维度 | Apigee / Kong AI Gateway | **SI-LLM-Gateway** |
|------|--------------------------|---------------------|
| 部署 | SaaS（GCP）/ On-Prem | **完全私有部署**（已 184 k3s 生产） |
| 数据合规 | 出域（GCP） | **数据全在企业内** |
| 计费 | 用量计费（USD） | **套餐 + 积分 + 加油包**（更适合中国 SMB） |
| 上游模型 | 主打 Google + Anthropic | **全模型 + 国产 + 本地** |
| 多凭据指纹池 | 基础 | **50+ UA + 35 Accept-Language + 11 utls profile** |
| MCP 工具网关 | 部分 | **Q3 2026 全量上线** |
| 中文友好 | 一般 | **全中文 UI + 国内模型 + 支付宝接入** |
| 多租户审计 | 标准 | **38+ 表 RLS + 43 轮审计 L1=0** |

**核心差异化**：**私有部署 + 中文 + 抗封号 + 多凭据指纹池** — Apigee 等海外厂商给不出的能力。

---

## 🤝 贡献

参见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。开发规范：
- Go 1.21+ 原生 `net/http`
- 提交前必须通过 `pre-commit-check.sh`（go vet + SQL lint + migration number）
- 多租户改动必须通过 `lint-tenant-scope-llmgw` / `lint-pg-rls` / `lint-otel-tenant` 三条 linter

---

## 🔐 安全

- 漏洞报告：见 [`SECURITY.md`](SECURITY.md)
- 公开仓库敏感信息保护：见 [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md)
- 法务白名单（disguise 模块）：见 [`docs/legal/disguise-compliance.md`](docs/legal/disguise-compliance.md)

---

## 📄 License

[Apache License 2.0](LICENSE) — 详见 [`LICENSE`](LICENSE) 文件。

---

> **最后一句话总结**：
> SI-LLM-Gateway 已经做好"数据面"和"管控"两根柱子，下一步是用 18 个月时间，
> 把第三根柱子——"Agent 网关 + 资产中心 + 智能防护"立起来，
> 从"中国最稳的 LLM 代理"变成"中国最稳的企业 AI 与 Agent 网关"。
