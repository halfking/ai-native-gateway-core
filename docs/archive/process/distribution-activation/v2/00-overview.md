# 增强方案总览 v2 · 2026-07-14

> 基于 `audit/` 缺口分析，给出"客户-中心-内核"三端完整增强方案 + **升级推送**专题。
> 设计原则：**主链路优先、渐进增强、可逆升级、安全可审计**。

## 1. 设计原则（5 条）

| 原则 | 含义 | 落地体现 |
|------|------|---------|
| **主链路优先** | 客户旅程 P0 必通（CLI + GUI 都行）| G-C1/C2 浏览器侧 API |
| **渐进增强** | P0 → P1 → P2，每阶段 e2e 验证 | 12 周 4 phase 切分 |
| **可逆升级** | 所有自动升级可回退（5 分钟内）| deploy-seamless.sh + upgrader rollback |
| **安全可审计** | 每个动作都有 trail（who/what/when）| license_audit_log + upgrade_log + command_log |
| **离线优先** | 客户断网时产品仍能用 80% | 离线激活 + 命令队列本地持久 |

## 2. 三端拓扑（v2）

```
┌────────────────────────────────────────────────────────────────────────┐
│                          客户 (Customer Side)                          │
│                                                                        │
│  ┌──────────────────────────┐  ┌─────────────────────────────────┐   │
│  │ Browser SPA (Vue)         │  │ Native CLI                      │   │
│  │ /setup (wizard)           │  │ installer activate              │   │
│  │ /settings/license         │  │ installer upgrade               │   │
│  │ /settings/upgrade         │  │ installer heartbeat             │   │
│  │ /settings/telemetry       │  │ installer rollback              │   │
│  │ /components/UpgradeBanner │  │                                 │   │
│  └──────────────────────────┘  └─────────────────────────────────┘   │
│              │                              │                          │
│              ▼                              ▼                          │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ Gateway (cmd/gateway/main.go)                                  │  │
│  │ /api/system/license/*        ◄─────── licensing.CustomerAPI  │  │
│  │ /api/system/upgrade/*        ◄─────── 新增 upgrade_user_handler.go│ │
│  │ /api/system/telemetry/pref   ◄─────── 新增 telemetry_handler.go │ │
│  │ /api/system/info             ◄─────── 新增 info_handler.go       │ │
│  │ /api/center/command          ◄─────── 新增 (push 接收器)         │ │
│  │ /internal/collector/*        ◄─────── 新增 (5min 上报)          │ │
│  └────────────────────────────────────────────────────────────────┘  │
│              │                                                          │
└──────────────┼──────────────────────────────────────────────────────────┘
               │ TLS 1.3 + Instance Token (Ed25519)
               ▼
┌────────────────────────────────────────────────────────────────────────┐
│                          中心 (Authority Side)                         │
│                                                                        │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ License Authority (cmd/license-authority/main.go)              │  │
│  │ /api/v1/instances/{register,refresh,heartbeat}                 │  │
│  │ /api/v1/license/{trial,activate,refresh,crl,offline/*}         │  │
│  │ /api/v1/updates/{latest,manifest,report,rollback}              │  │
│  │ /api/v1/collect/runtime        ◄─────── 新增 (接收采集)        │  │
│  │ /api/v1/commands/dispatch      ◄─────── 新增 (push 通道)        │  │
│  │ /api/admin/licenses* (CRUD)                                    │  │
│  │ /api/admin/releases* (上传 + 发布 + 灰度)                     │  │
│  │ /api/admin/center/* (节点列表 + 命令下发)                     │  │
│  │ /api/admin/alerts/*           ◄─────── 新增 (告警工作流)      │  │
│  └────────────────────────────────────────────────────────────────┘  │
│              │                                                          │
│              ▼                                                          │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ Master Frontend (web/src/views/ops/)                           │  │
│  │ CenterOpsView (节点列表 + 健康度)        ◄─────── 已有         │  │
│  │ LicenseManagementView (CRUD + 离线审批)   ◄─────── 已有        │  │
│  │ AutoUpdateView (上传 + 发布 + 灰度)       ◄─────── 扩展       │  │
│  │ AlertsView (告警规则 + Webhook 配置)      ◄─────── 新增        │  │
│  │ DownloadStatsView (下载埋点)              ◄─────── 新增        │  │
│  │ CommandDispatchView (下发命令 + 审批)     ◄─────── 新增        │  │
│  │ UpgradeProgressView (升级进度地图)         ◄─────── 新增        │  │
│  └────────────────────────────────────────────────────────────────┘  │
│              │                                                          │
│              ▼                                                          │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ Marketing Site (Next.js llm.kxpms.cn)                          │  │
│  │ /pricing /download /docs /register /login        ◄─────── 待建 │  │
│  └────────────────────────────────────────────────────────────────┘  │
│              │                                                          │
└──────────────┼──────────────────────────────────────────────────────────┘
               │
               ▼
┌────────────────────────────────────────────────────────────────────────┐
│                          内核 (Kernel Side)                            │
│                                                                        │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ licensing/ (现有 + 新增)                                       │  │
│  │ crypto.go / fingerprint.go / clock.go / activator.go (已有)    │  │
│  │ enhanced_fingerprint.go      ◄─────── 新增 (G-K5)              │  │
│  │ antitamper.go                ◄─────── 新增 (G-K2)              │  │
│  │ antidebug.go                 ◄─────── 新增 (G-K3)              │  │
│  │ nonce.go                     ◄─────── 新增 (G-K4)              │  │
│  └────────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ gateway/internal/collector/                                     │  │
│  │ collector.go / metrics.go / reporter.go      ◄─────── 新增      │  │
│  └────────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │ installer/internal/upgrader/ + cmd/llm-gw-installer/ (已有)    │  │
│  └────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘
```

## 3. 升级推送（v2 关键能力）

详见 `v2/04-upgrade-push.md`。

**核心：从"客户端轮询"变"主控主动 + 客户端补偿"**

```
旧 (v1): installer 6h 轮询 /api/v1/updates/latest
新 (v2): 主控端 WebSocket/SSE 主动推 + 客户端 7 天 TTL 补偿队列
```

## 4. 数据流（v2 升级推送）

```
Operator 在 admin 面板选 v2.1 → 创建灰度（10% 客户）
    │
    ▼
Master API: POST /api/admin/releases
    │
    ▼
Authority Store: 写 releases 表 + 灰度规则
    │
    ▼
Async dispatcher:
    ├─ 在线实例: WebSocket push (TLS, Ed25519-signed envelope)
    └─ 离线实例: 命令队列持久化 (7d TTL)
    │
    ▼
Client 接收:
    ├─ WebSocket: 实时升级命令 → /api/center/command handler
    └─ Rejoin: 7d 内重连 → pull queue → 执行
    │
    ▼
Upgrade 执行 → 进度回写 /api/v1/updates/report
```

## 5. 12 周 4 Phase 切分

| Phase | 周次 | 关键交付 |
|-------|------|---------|
| **Phase 0** | W1-2 | G-C1 / G-U3 / G-S2 基础 |
| **Phase 1** | W3-5 | G-K1 / G-U1（依赖 Phase 0）|
| **Phase 2** | W6-8 | G-C2 / G-C4 / G-C5（前端消费）|
| **Phase 3** | W9-10 | P1 全部（告警 / 灰度 / 远程协助 / 防盗版）|
| **Phase 4** | W11-12 | 集成 + e2e + 1.14 GA |

## 6. 与既有文档的关系

| 旧文档 | 升级 |
|--------|------|
| `04-License算法与验证.md` | ✅ 已实现；v2 在 enhanced_fingerprint / antitamper 上扩 |
| `05-注册激活流程.md` | ✅ CLI 部分已实现；v2 补 `/api/system/license/*` |
| `06-自动升级与蓝绿部署.md` | ✅ 客户端部分已实现；v2 加主控 push 通道 |
| `07-运行状态采集.md` | ❌ 设计文档；v2 落地 collector 代码 + 接收端 |
| `08-防盗版与安全.md` | 🟡 4/9 实现；v2 补 antitamper / antidebug / nonce / 增强指纹 |
| `09-分发与下载.md` | ❌ 客户端+CDN 在；v2 补官网门户 + 下载埋点 |
| `11-实施路线图.md` | 🟡 路线图框架；v2 重排为 G-C1/G-C2/... |
| `13-双版本构建与分发策略.md` | ⚠️ 历史设计，与 04 §九 + seamless-deployment-guide 兼容 |

## 7. 文件清单

- `audit/01-scope.md`  范围与方法
- `audit/02-current-state.md`  现状能力矩阵
- `audit/03-gaps.md`  缺口分析
- `audit/04-priorities.md`  优先级 + 工作量
- `v2/00-overview.md`  本文档（总览 + 拓扑 + 12 周 Phase）
- `v2/01-client-side.md`  客户侧增强
- `v2/02-center-side.md`  中心侧增强
- `v2/03-kernel-side.md`  内核侧增强
- `v2/04-upgrade-push.md`  升级推送专题
- `v2/05-implementation.md`  实施 + 验收 + 回滚
- `v2/06-references.md`  API 表 / Schema 增量 / 命令清单

## 8. 立即可执行（本周）

P0 第一刀：**G-C1 浏览器侧 License API**（用户立刻感受到的痛点）。

现有 `licensing/customer_api.go` 已实现客户 License API；后续实现重点是契约收敛、试用语义和 ActivationWizard 接入：

- `POST /api/system/license/trial` — 包装 `installer activate --mode trial`
- `POST /api/system/license/activate` — 包装 `installer activate --mode key`
- `POST /api/system/license/offline/request` — 生成请求文件
- `POST /api/system/license/offline/import` — 导入响应
- `GET /api/system/license/status` — 读 license.dat

完成后客户打开 `http://server:8781` 直接看到 `/setup` 引导，4 个入口全部走通。
