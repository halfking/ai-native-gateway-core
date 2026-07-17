# LLM Gateway Go — 企业级 LLM 网关

> **让企业安全、合规、低成本地使用全球各类大模型与 AI 工具** —— 一套网关，统一管控、智能整合、自动升级。

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://golang.org)
[![Multi-Tenant](https://img.shields.io/badge/Multi--Tenant-RLS%20enabled-brightgreen.svg)]()
[![Version](https://img.shields.io/badge/Version-v2.4.2-green.svg)](VERSION)

---

## ✨ 核心功能

### License 管理与分发
- **在线激活**：通过主控端 `llm.kxpms.cn` 实时激活
- **离线激活**：支持完全断网环境的 license 授权
- **试用模式**：7 天免费试用（1 租户 / 基础 API）
- **设备绑定**：基于硬件指纹的设备管理

### 实例注册与心跳
- **自动注册**：实例启动时自动向主控端注册
- **实时心跳**：60s 心跳上报 + 状态监控（online/degraded/offline）
- **健康检查**：自动探测实例健康状态，支持 30s/2min 离线判定
- **Token 续期**：24h JWT 自动续期

### 自动升级与回滚
- **在线升级**：自动检查更新 + 一键升级（6h 检查周期）
- **离线升级**：U 盘携带升级包 + 本地安装
- **备份保护**：升级前自动备份 + 失败自动回退
- **健康验证**：升级后 5s 健康检查，失败自动回退

### 四种部署模式
- **M1 单机部署**：二进制 + systemd（离线模式）
- **M2 单机 Docker**：docker-compose 快速部署
- **M3 K8s Sidecar**：kustomize 模板 + sidecar 心跳
- **M4 K8s Operator**：CRD + 声明式管理（规划中）

---

## 🎯 核心能力

| 能力维度 | 实现 |
|----------|------|
| **协议层** | OpenAI / Anthropic / Responses 兼容 + SSE 流式中继 |
| **路由层** | 智能候选路由 + 粘性会话 + 自动路由（cost/quality 策略） |
| **多租户** | 身份隧道（virtual IP/MAC/ClientID）+ 凭据池 + 38+ 表 RLS |
| **流量治理** | Token 限流 + 语义缓存 + 提示词压缩 + 滑窗算法 |
| **审计** | 全链路审计 + DLQ + 磁盘回退 + OTel + Prometheus |
| **License** | 在线/离线激活 + 设备管理 + CRL 撤销 + 过期续期 |
| **升级** | 在线/离线升级 + 自动回滚 + 版本检查 + 健康验证 |
| **部署** | M1-M4 四种模式 + systemd + Docker + K8s |

详细架构见 [`docs/architecture/ARCHITECTURE.md`](docs/architecture/ARCHITECTURE.md)。

---

## 🚦 快速开始

### 编译

```bash
# 克隆仓库
git clone https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
cd llm-gateway-go

# 编译网关
go build -o gateway ./cmd/gateway

# 编译安装器
cd installer
go build -o llm-gw-installer ./cmd/llm-gw-installer
cd ..
```

### 激活

```bash
# 方式 1: 试用模式（7 天免费）
./installer/llm-gw-installer activate --mode trial --email your@email.com

# 方式 2: License Key 激活
./installer/llm-gw-installer activate --mode online --license-key LIC-xxx

# 方式 3: 离线激活（完全断网）
./installer/llm-gw-installer activate --mode offline --request-file activation.req
# ... 拷贝 activation.req 到联网电脑，上传到 llm.kxpms.cn/offline，获取 license.dat
./installer/llm-gw-installer activate --mode offline --import-file license.dat
```

### 启动

```bash
# 启动网关（默认监听 :8781）
./gateway --listen :8781

# 健康检查
curl http://localhost:8781/healthz
# 返回: {"status":"ok","version":"v2.4.2"}
```

### 升级

```bash
# 检查更新
./installer/llm-gw-installer upgrade check

# 在线升级
./installer/llm-gw-installer upgrade apply --to v2.5.0

# 回滚
./installer/llm-gw-installer upgrade rollback --to v2.4.2
```

---

## 🏛️ 架构简图

```
┌─────────────────────── 客户机器 ───────────────────────┐
│  ~/llm-gateway/                                         │
│   ├── gateway                    (主进程，:8781)        │
│   ├── llm-gw-installer           (CLI 工具)            │
│   ├── license.dat                (RSA 签名的 License)   │
│   ├── VERSION                    (当前版本)            │
│   └── compose.yml / systemd      (部署配置)            │
└──────────────────────┬─────────────────────────────────┘
                       │ HTTPS (TLS 1.3, Ed25519 签名)
┌──────────────────────▼─────────────────────────────────┐
│  主控端 llm.kxpms.cn:8443                               │
│   ├─ /api/v1/license/*   (激活/续期/CRL)                │
│   ├─ /api/v1/instances/* (注册/心跳/状态)               │
│   └─ /api/v1/updates/*   (检查/下载/上报)               │
└─────────────────────────────────────────────────────────┘
```

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

## 📚 文档索引

| 类别 | 文档 |
|------|------|
| **部署** | [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — M1-M4 四种部署模式 |
| **API** | [`docs/API.md`](docs/API.md) — 主控端 8 个 API 端点 |
| **升级** | [`docs/UPGRADE.md`](docs/UPGRADE.md) — 在线/离线升级流程 |
| **架构** | [`docs/architecture/ARCHITECTURE.md`](docs/architecture/ARCHITECTURE.md) — V3 架构方案 |
| **会话优化V2** | [`docs/会话优化v2/配置说明.md`](docs/会话优化v2/配置说明.md) — Sessions V2 Feature Flag 配置与灰度发布 |
| **双仓库** | [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md) — codeup ⇄ github 工作流 |
| **安全** | [`SECURITY.md`](SECURITY.md) — 漏洞报告 + 扫描器用法 |
| **贡献** | [`CONTRIBUTING.md`](CONTRIBUTING.md) — 开发规范 + 提交规范 |

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
