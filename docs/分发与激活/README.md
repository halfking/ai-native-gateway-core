# llm-gateway-go 分发与激活 总览

> 本目录收录 llm-gateway-go 商业化分发、激活、运行监控、自动升级的完整方案。
> 适用于：将产品以 SaaS + 私有化 + 离线授权多种模式发行给客户使用。

## 一、文档目录

| 文档 | 主题 | 状态 |
|------|------|------|
| [01-产品概述与商业模式](./01-产品概述与商业模式.md) | 商业模型、套餐、计费模式（按月/按token） | ✅ |
| [02-用户旅程](./02-用户旅程.md) | 完整 5 阶段用户旅程 | ✅ |
| [03-部署架构](./03-部署架构.md) | 5 种部署模式 | ✅ |
| [04-License算法与验证](./04-License算法与验证.md) | RSA / AES / 指纹 / 防回拨 | ✅ |
| [05-注册激活流程](./05-注册激活流程.md) | 在线激活 / 离线激活 / 15 天试用 | ✅ |
| [06-自动升级与蓝绿部署](./06-自动升级与蓝绿部署.md) | 守护进程 / 蓝绿切换 / 回退 | ✅ |
| [07-运行状态采集](./07-运行状态采集.md) | 遥测采集 / 授权开关 / 隐私承诺 | ✅ |
| [08-防盗版与安全](./08-防盗版与安全.md) | 盗版攻击场景与防御 | ✅ |
| [09-分发与下载](./09-分发与下载.md) | 官网分发 / CDN / 版本管理 | ✅ |
| [10-多租户与计费架构](./10-多租户与计费架构.md) | 未来按月+用户 / 按 token 用量计费预留 | ✅ |
| [11-实施路线图](./11-实施路线图.md) | 5 个里程碑 | ✅ |
| [12-执行计划与并发任务](./12-执行计划与并发任务.md) | 并发任务拆解与依赖 | ✅ |
| [13-双版本构建与分发策略](./13-双版本构建与分发策略.md) | Master/Customer 双版本物理隔离 | ✅ |
| [附录 A-deploy.yml 完整 Schema](./appendices/A-deploy.yml完整Schema.md) | 部署配置文件 | ✅ |
| [附录 B-API 端点清单](./appendices/B-API端点清单.md) | 所有 REST API | ✅ |
| [附录 C-数据库 Schema](./appendices/C-数据库Schema.md) | 关键表结构 | ✅ |

## 二、关键决策速查

| 决策项 | 选择 | 理由 |
|--------|------|------|
| 官方门户域名 | `llm.kxpms.cn` | 用户指定 |
| License 算法 | RSA-2048 + SHA-256 | 现有实现稳定；签名验证快 |
| 数据加密 | AES-256-GCM | AEAD，自带认证 |
| 机器指纹 | machineid + CPU + HostID + MAC + Disk Serial + BIOS UUID | 多维度防克隆 |
| 默认 License Key 编码 | `LIC-<32位hex>`（37 字符） | 代码 `admin_api.go:generateLicenseKey()` |
| 免费试用时长 | 15 天 | 用户指定 |
| 免费版租户限制 | 2 个租户（`MaxDevices` 字段控制设备数，租户上限按 tier 硬编码） | 用户指定 |
| 部署模式数 | 5 种（单机离线/单机在线/K8s/Docker/DB 独立） | 覆盖主流场景 |
| 升级方式 | 蓝绿部署 + 守护进程 | 零停机、可回退 |
| License 验证时机 | 启动时 + 每 24h + 关键操作前 | 平衡安全与性能 |
| 采集授权时机 | 首次激活时询问 | 合规优先 |
| 采集频率 | 默认 5 分钟 | 可配置 5/15/60 |
| 试用期续费提醒 | 到期前 10/5/1 天 | 渐进式提醒 |
| 计费维度预留 | 租户数 + Token 用量 | 为未来计费打基础 |

## 三、核心能力现状摘要

### 已实现 ✅

| 能力 | 实现位置 |
|------|---------|
| License 算法 (RSA/AES/JWT) | `licensing/crypto.go` |
| 机器指纹采集 | `licensing/fingerprint.go` |
| License 验证器 | `licensing/validator.go` |
| 激活器（在线） | `licensing/activator.go` |
| 离线激活 | `licensing/offline.go` |
| 设备管理 | `licensing/device_manager.go` |
| License 数据库表 | `sql/migrations/startup/372+374+375` |
| 管理 API（主控端） | `licensing/admin_api.go` → `/api/admin/licenses*` |
| Center API（客户端暴露） | `licensing/center_api.go` → `/api/center/*` |
| 离线激活请求审批 UI | `web/src/views/ops/LicenseManagementView.vue` |
| 自动升级（Downloader/Installer/Rollback） | `autoupdate/*.go` |
| 自动升级管理 UI | `web/src/views/ops/AutoUpdateView.vue` |
| 中心运维（实例/心跳/命令） | `center/*.go` |
| 中心运维 UI | `web/src/views/ops/CenterOpsView.vue` |
| 请求遥测记录 | `domains/hooks/observability/telemetry/client.go` |

### 缺失待补 ❌

| 能力 | 目标位置 |
|------|---------|
| 用户侧激活向导 | `web/src/views/setup/ActivationWizard.vue` |
| 首次访问授权对话框 | `web/src/components/TelemetryConsentDialog.vue` |
| 设置页 - License 详情 | `web/src/views/settings/LicenseInfoView.vue` |
| 设置页 - 遥测开关 | `web/src/views/settings/TelemetryView.vue` |
| 设置页 - 用户升级面板 | `web/src/views/settings/UpgradePanel.vue` |
| 首页升级 Banner | `web/src/components/UpgradeBanner.vue` |
| 网关侧激活 API | `gateway/internal/api/setup_handler.go` |
| 网关侧升级 API | `gateway/internal/api/upgrade_user_handler.go` |
| 网关侧遥测 API | `gateway/internal/api/telemetry_handler.go` |
| 网关侧 License API | `gateway/internal/api/license_user_handler.go` |
| 运行状态采集器 | `gateway/internal/collector/*.go` |
| 升级守护进程 | `upgrader/cmd/kx-gateway-upgrader/main.go` |
| 独立回退 CLI | `scripts/kx-gateway-rollback` |
| 时钟防回拨 | `licensing/clock.go` |
| 增强机器指纹 | `licensing/enhanced_fingerprint.go` |
| 防篡改自检 | `licensing/antitamper.go` |

## 四、阅读顺序建议

**业务方/产品经理**：
1. 01-产品概述与商业模式
2. 02-用户旅程
3. 10-多租户与计费架构
4. 11-实施路线图

**架构师/技术负责人**：
1. 03-部署架构
2. 04-License算法与验证
3. 06-自动升级与蓝绿部署
4. 10-多租户与计费架构
5. 12-执行计划与并发任务

**开发工程师**：
1. 02-用户旅程（理解上下文）
2. 03-部署架构
3. 04-License算法与验证
4. 05-注册激活流程
5. 12-执行计划与并发任务（领取任务）
6. 附录 A/B/C（编码时参考）

**运维/SRE**：
1. 03-部署架构
2. 06-自动升级与蓝绿部署
3. 07-运行状态采集
4. 08-防盗版与安全

**法务/合规**：
1. 01-产品概述与商业模式
2. 02-用户旅程
3. 07-运行状态采集
4. 08-防盗版与安全