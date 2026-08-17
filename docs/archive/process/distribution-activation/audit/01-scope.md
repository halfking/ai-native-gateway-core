# 审计范围与方法 · 2026-07-14

> **作者**：ACC Agent
> **范围**：`docs/分发与激活/`（13 篇 + 3 个 appendices + seamless-deployment-guide）+ 相关代码模块
> **目标**：基于代码现状 + 用户特性诉求，给出"客户-中心-内核"三端的完整差距分析与方案增强

## 1. 审计输入

### 1.1 文档审计 (13 + 3 + 2)

| 类别 | 文件 | 主题 | 行数 |
|------|------|------|------|
| 产品 | `01-产品概述与商业模式.md` | SaaS / 私有化 / 离线授权 + 套餐矩阵 | 188 |
| 客户旅程 | `02-用户旅程.md` | 5 阶段（官网发现 → 本机部署 → 首次激活 → 使用监控 → 试用转化）| 556 |
| 部署 | `03-部署架构.md` | M1-M5 五种部署模式 | 332 |
| 算法 | `04-License算法与验证.md` | RSA/AES-GCM/指纹/防回拨 | 241 |
| 激活 | `05-注册激活流程.md` | 4 入口（试用/Key/离线/状态）| 490 |
| 升级 | `06-自动升级与蓝绿部署.md` | 守护进程 + slot 切换 + nginx | 468 |
| 采集 | `07-运行状态采集.md` | 白名单 + opt-in 授权 | 456 |
| 安全 | `08-防盗版与安全.md` | 9 类攻击 + 防御 | 262 |
| 分发 | `09-分发与下载.md` | CDN + SHA256 + 签名 | 379 |
| 计费 | `10-多租户与计费架构.md` | 字段预留 | — |
| 路线图 | `11-实施路线图.md` | M0-M4 5 个里程碑 | 235 |
| 执行计划 | `12-执行计划与并发任务.md` | WBS + 10 Agent 并行 | 413 |
| 双版本 | `13-双版本构建与分发策略.md` | master/customer 物理隔离 | — |
| A | `appendices/A-deploy.yml完整Schema.md` | 配置 schema | — |
| B | `appendices/B-API端点清单.md` | 主控端/客户端 REST API | 221 |
| C | `appendices/C-数据库Schema.md` | license/upgrade/collect 表 | — |
| 部署 | `seamless-deployment-guide.md` | 245/154/docker/k3s 实测 | 272 |

### 1.2 代码审计 (3 大模块)

```
licensing/                 39 files, ~5000 LOC
├── crypto.go              RSA-2048 + AES-256-GCM + JWT
├── fingerprint.go         机器指纹 + MatchScore
├── clock.go               ClockGuard (防回拨)
├── activator.go           在线激活器
├── validator.go           验证器 + 5min cache
├── offline.go             离线激活 + ExpiresAt/RevokedAt 校验
├── restricted_mode.go     启动期 503 + /setup 引导
├── grace.go               GracePolicy + FailureMarker
├── daemon_health.go       health snapshot
├── device_manager.go      max_devices + 绑定
├── token_refresh.go       24h refresh + 指数退避
├── types.go               License / SignedLicense
├── admin_api.go           /api/admin/licenses CRUD
└── center_api.go          /api/center/* (反向调用)

cmd/license-authority/     14 files, ~3000 LOC
├── main.go                Echo + Ed25519 keys + Redis nonce
├── routes.go              /api/v1/{license, instances, updates, ...}
├── register_handler.go    客户端首次注册
├── refresh_handler.go     instance_token 续期
├── heartbeat_handler.go   5min 实例心跳
├── update_handler.go      /updates/latest (manifest 查询)
├── manifest_handler.go    /updates/manifest (下载 manifest)
├── update_report_handler  /updates/report (结果回写)
├── rollback_handler.go    /updates/rollback (admin 命令)
└── middleware/            Sigverify + redis_nonce

installer/cmd/llm-gw-installer/  7 files, ~3000 LOC
├── main.go                Cobra 7 子命令
├── install.go             install 流程
├── uninstall.go           --purge 清理
├── doctor.go              环境检测
├── activate.go            trial / key / offline-request / offline-import
├── heartbeat.go           60s instance heartbeat
└── upgrade.go             upgrade check / apply / rollback

installer/internal/upgrader/    9 files, ~2500 LOC
├── apply.go               状态机 (IDLE→CHECK→DOWNLOAD→...→DONE)
├── rollback.go            备份/原子切换 + healthz
├── check.go               manifest 校验
├── manifest.go            SHA256SUMS / GPG
├── state.go               upgrader.state.json
├── offline_apply.go       离线包应用
└── instance_id.go         UUID 生成 + 持久化
```

## 2. 评估维度（5 维评分）

每项 0-10 分，10 分制。

| 维度 | 描述 | 当前 |
|------|------|------|
| **功能完整性** | 文档承诺的能力是否实现 | ? |
| **代码成熟度** | 关键路径是否 production-ready | ? |
| **集成度** | 三端（客户/中心/内核）能否端到端跑通 | ? |
| **运维性** | 监控、告警、回滚、远程协助是否齐全 | ? |
| **防盗版与安全** | 9 类攻击场景防御是否到位 | ? |

## 3. 审计输出

- `audit/02-current-state.md` — 现状能力矩阵（每维 × 每能力）
- `audit/03-gaps.md` — 缺口分析（缺失 / 部分实现 / 设计缺陷）
- `audit/04-priorities.md` — 优先级矩阵（P0/P1/P2）+ 工作量估算
- `audit/05-recommendations.md` — 增强方案总览

## 4. 关联方案（`v2/`）

审计完成后，输出**客户-中心-内核**三端增强方案 + **升级推送**专题：

```
v2/00-overview.md        总览 + 设计原则
v2/01-client-side.md     客户侧 9 个能力补全
v2/02-center-side.md     中心侧 8 个能力补全
v2/03-kernel-side.md     内核侧 7 个能力补全
v2/04-upgrade-push.md    主控主动推送升级专题
v2/05-implementation.md  6 阶段实施 + 验收 + 回滚
```

## 5. 验收口径

- **覆盖率**：当前文档列出的能力 vs 代码实际实现 ≥ 95%
- **P0 完成率**：12 个月内 P0 项 100% 落地
- **三端 e2e**：trial → activate → heartbeat → upgrade → rollback 全链路跑通
- **安全审计**：9 类攻击场景至少 7 类有现成防御
- **远程协助**：从主控端远程 SSH 到客户实例的命令通道可用