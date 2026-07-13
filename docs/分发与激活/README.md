# llm-gateway-go 分发与激活 总览

> 客户侧闭环：从 `llm.kxpms.cn` 下载 → 本机安装 → 激活 → 自动升级 → 运行状态采集。
>
> 本目录与 `cmd/license-authority`（License Authority 服务）+ `installer/`（客户端安装/激活/注册/升级）同步更新；下文"已实现 ✅ / 缺失 ❌"反映当前 main 分支代码状态。

## 一、文档目录

| # | 文档 | 主题 | 当前重点 |
|---|------|------|----------|
| 01 | [01-产品概述与商业模式](./01-产品概述与商业模式.md) | SaaS + 私有化 + 离线授权；v2 按 token 留空间 | 套餐矩阵 + 商业触点 |
| 02 | [02-用户旅程](./02-用户旅程.md) | 5 阶段下载→部署→激活→使用→续费 | 4 种激活入口 + 升级流程 |
| 03 | [03-部署架构](./03-部署架构.md) | 5 种客户本机部署形态（M1-M5） | install.sh 安装向导 |
| 04 | [04-License算法与验证](./04-License算法与验证.md) | RSA/AES/指纹/防回拨 | 离线/在线签名验证 |
| 05 | [05-注册激活流程](./05-注册激活流程.md) | Center API + `/api/system/license/*` 用户入口 | 4 入口实现差异 |
| 06 | [06-自动升级与蓝绿部署](./06-自动升级与蓝绿部署.md) | `installer/internal/upgrader` + slot 切换 | 上报/回退 API |
| 07 | [07-运行状态采集](./07-运行状态采集.md) | 白名单采集 + 用户授权 | 订阅/退订偏好 |
| 08 | [08-防盗版与安全](./08-防盗版与安全.md) | 盗版攻击场景与防御 | 反调试/反篡改策略 |
| 09 | [09-分发与下载](./09-分发与下载.md) | 官网分发 + 版本管理 | SHA256 + 签名校验 |
| 10 | [10-多租户与计费架构](./10-多租户与计费架构.md) | 计费维度预留 | 字段预留清单 |
| 11 | [11-实施路线图](./11-实施路线图.md) | 5 个里程碑 | 主/客双版本进度 |
| 12 | [12-执行计划与并发任务](./12-执行计划与并发任务.md) | WBS + 依赖 | 工程师排期 |
| 13 | [13-双版本构建与分发策略](./13-双版本构建与分发策略.md) | master/customer 物理隔离 | 编译产物差异 |
|  | [附录 A — deploy.yml Schema](./appendices/A-deploy.yml完整Schema.md) | 部署配置文件 | schema_version 1 |
|  | [附录 B — API 端点清单](./appendices/B-API端点清单.md) | 主控/客户端 REST API | 已实现状态 |
|  | [附录 C — 数据库 Schema](./appendices/C-数据库Schema.md) | license/upgrade/collect 表 | 迁移版本号 |

## 二、客户旅程速查

```
Phase 1 官网发现   ─→  Phase 2 本机部署   ─→  Phase 3 首次激活   ─→  Phase 4 使用+升级   ─→  Phase 5 试用转化
https://llm.kxpms.cn    install.sh 5 模式     4 入口激活（trial/key/      自动升级守护进程      到期/续费/升级
下载离线/在线包          daemon/launchd       offline/cached）           升级 → 回退上报          商业触点
```

## 三、关键决策速查（用户向）

| 决策项 | 选择 | 理由 |
|--------|------|------|
| 官方门户 | `llm.kxpms.cn` | 入口一致 |
| 客户端下载 | 完整离线包 + 在线拉镜像 fallback | 覆盖断网/在线场景 |
| License 算法 | RSA-2048 + SHA-256 + AES-256-GCM + Ed25519 (HeartbeatToken) | 现成稳定 + AEAD |
| 机器指纹 | machineid + CPU + HostID + MAC + Disk + BIOS | 多维度防克隆 |
| License Key 编码 | `LIC-<32hex>`（37 字符） | `licensing/admin_api.go:generateLicenseKey()` |
| 免费试用 | 15 天 | 行业基准 |
| 部署模式 | 5 种（M1-M5） | 覆盖客户自有机/容器/K8s |
| 升级方式 | 应用产物 + Slot 切换 + 守护进程 | 零停机 + 可回退 |
| License 验证时机 | 启动时 + 24h 心跳 + 关键操作前 | 性能与安全平衡 |
| 采集授权时机 | 首次激活时询问 + 后续设置页可改 | 隐私优先 |

## 四、当前代码状态速查

### 已实现 ✅

| 能力 | 路径 |
|------|------|
| License 算法 (RSA / AES-GCM / Ed25519) | `licensing/crypto.go` |
| 机器指纹 + 时锺防回拨 | `licensing/fingerprint.go`、`licensing/clock.go` |
| 在线 / 离线激活器 | `licensing/activator.go`、`licensing/offline.go` |
| 设备管理 | `licensing/device_manager.go` |
| License DB Schema | `sql/migrations/startup/372+374+375` |
| Center API (`/api/center/*`) | `licensing/center_api.go` |
| Master License 管理 API | `licensing/admin_api.go` |
| License Authority 服务（独立二进制） | `cmd/license-authority/`（register/heartbeat/refresh/update/manifest/rollback/report）|
| 安装器 CLI (`llm-gw-installer`) | `installer/cmd/llm-gw-installer/{main,activate,heartbeat,upgrade}.go` |
| 升级执行器 + 数据库迁移协调 + GPG 验证 | `installer/internal/upgrader/{apply,rollback}.go` |
| 离线激活请求审批 UI | `web/src/views/ops/LicenseManagementView.vue` |
| 自动升级 UI | `web/src/views/ops/AutoUpdateView.vue` |
| 中心运维（实例/心跳/命令） | `center/*.go` |
| 中心运维 UI | `web/src/views/ops/CenterOpsView.vue` |
| 请求遥测 | `domains/hooks/observability/telemetry/client.go` |
| License 离线验证（过期/吊销） | `licensing/offline.go`（已加 ExpiresAt/RevokedAt 校验，第四轮审计） |
| 版本比较（alpha/beta/rc） | `autoupdate/version.go` |
| Docker HEALTHCHECK | `Dockerfile`（第四轮审计） |

### 缺失待补 ❌

| 能力 | 目标路径 | 优先级 |
|------|----------|--------|
| 用户侧激活向导 UI | `web/src/views/setup/ActivationWizard.vue` | P1 |
| 首次访问授权对话框 | `web/src/components/TelemetryConsentDialog.vue` | P1 |
| License 详情 / 遥测 / 升级 用户侧 UI | `web/src/views/settings/{LicenseInfo,Telemetry,UpgradePanel}.vue` | P1 |
| 首页升级 Banner | `web/src/components/UpgradeBanner.vue` | P2 |
| 用户侧网关 API（`/api/system/license/*`、`/api/system/upgrade/*`） | `gateway/internal/api/{setup_handler,upgrade_user_handler,telemetry_handler,license_user_handler}.go` | P0（Phase 2 入口） |
| 运行状态采集器（系统 + 流量 + 业务聚合） | `gateway/internal/collector/*.go` | P1 |
| 独立回退 CLI | `scripts/kx-gateway-rollback` | P2 |
| 反调试 / 自检 / 防重放 | `licensing/{antitamper,antidebug,nonce}.go` | P2 |
| License 状态实时仪表板 / 设备筛选 / 到期预警 | 后续会话切片 | P2 |

> 详细缺口与排期见 [11-实施路线图](./11-实施路线图.md)。

## 五、阅读顺序建议

**客户侧角色（最快上手）**
1. 02-用户旅程（完整 5 阶段）
2. 03-部署架构（M1-M5 本机形态）
3. 05-注册激活流程（4 入口）
4. 06-自动升级与蓝绿部署

**架构师 / Tech Lead**
1. 01-产品概述与商业模式
2. 03-部署架构
3. 04-License算法与验证
4. 05-注册激活流程
5. 10-多租户与计费架构
6. 11-实施路线图 + 12-执行计划

**开发工程师**
1. 02 + 03（上下文）
2. 04 + 05（License 流程）
3. 06（升级）
4. 附录 A/B/C（编码参考）

**运维 / SRE**
1. 03-部署架构
2. 06-自动升级
3. 07-运行状态采集
4. 08-防盗版与安全
5. 09-分发与下载

**法务 / 合规**
1. 01（商业模式）
2. 02（用户旅程）
3. 07（采集范围）
4. 08（防盗版）
