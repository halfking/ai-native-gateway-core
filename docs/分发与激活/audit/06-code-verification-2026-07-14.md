# 代码核验补充报告 · 2026-07-14

> 本报告是 `audit/02-current-state.md` 和 `audit/03-gaps.md` 的当前实现补充。旧文件保留为历史审计快照；涉及“现在是否已实现”的判断以本报告为准。

## 1. 核验范围

核对了客户网关、License Authority、`licensing`、`autoupdate`、`center`、installer 和 `web/src`。核验方法是路由注册、实现文件、单元测试和构建结果交叉确认，不把设计文档或类型定义当作已交付能力。

## 2. 当前已实现能力

### 客户侧

| 能力 | 代码证据 | 当前结论 |
|---|---|---|
| License 状态 | `licensing/customer_api.go` → `/api/system/license/status` | 已实现并测试 |
| License 详情 | `licensing/customer_api.go` → `/info`；`LicenseInfoView.vue` | 已实现；审计时间线未实现 |
| Key 激活 | `CustomerAPI.handleActivate`；`ActivationWizard.vue` | 已实现；需真实浏览器 e2e |
| 离线申请/导入 | `offline-request`、`offline-activate`；ActivationWizard | API/UI 已实现；文件审批 e2e 未完成 |
| Trial | Authority `/api/v1/license/trial`；网关 `/api/system/license/trial` | 已实现；生产要求 Redis，需部署验收 |
| 升级查询 | `autoupdate/customer_api.go` → `/status`、`/check` | 已实现 |
| 升级 UI | `UpgradeBanner.vue`、`UpgradePanel.vue` | 已实现查询展示；不能直接执行升级 |
| 本地升级/回滚 | `installer/internal/upgrader`、installer CLI | 已实现；多副本/生产演练未完成 |

### 中心侧

| 能力 | 代码证据 | 当前结论 |
|---|---|---|
| License 管理 | `licensing/admin_api.go` | 已实现基础 CRUD、设备和离线审批 |
| 节点注册/心跳 | `cmd/license-authority/*handler.go`、`center` | 已实现基础注册、心跳和状态监控 |
| 命令记录 | `center/admin_api.go`、`center/server.go`、`center/store_pgx.go` | 已实现创建/查询命令记录 |
| Release 查询/管理 API | `autoupdate/admin_api.go`、Authority update handlers | 已有基础能力 |
| 主动命令投递 | 无 client receiver、无 stream、无 durable delivery loop | 未实现 |
| 下载统计 | 无 download event ingest/aggregation | 未实现 |
| 告警 | 无 alerts migration/rule engine/notification lifecycle | 未实现 |

### 内核侧

| 能力 | 代码证据 | 当前结论 |
|---|---|---|
| 签名/加密 | `licensing/crypto.go` | 已实现基础 RSA/AES 能力 |
| 基础指纹 | `licensing/fingerprint.go` | 已实现 |
| 时钟保护 | `licensing/clock.go` | 已实现基础回拨检测/grace |
| 采集器 | 未发现 v2 `gateway/internal/collector` | 未实现；现有 heartbeat/telemetry 不等价 |
| 运行指标接收 | 未发现 `/api/v1/collect/runtime` 注册 | 未实现 |
| 高级反篡改/反调试 | 未发现 `antitamper.go`、`antidebug.go` | 未实现 |
| 客户命令执行器 | 未发现白名单 executor/幂等 receiver | 未实现 |

## 3. 文档漂移清单

以下历史文档描述已过期，实施时必须按本报告修正：

| 文档表述 | 事实 | 处理原则 |
|---|---|---|
| “客户 License API 缺失” | CustomerAPI 已挂载 | 改为“已实现，需契约/e2e/试用部署验收” |
| “ActivationWizard 缺失” | `web/src/views/ActivationWizard.vue` 已存在 | 改为“UI 已有，真实浏览器流程未验收” |
| “UpgradePanel/UpgradeBanner 缺失” | 两者均存在 | 改为“查询展示已有，执行/push 未有” |
| “中心命令通道完全缺失” | 命令记录创建/查询已有 | 改为“投递、签名、审批、客户端执行缺失” |
| “Trial 15 天” | 当前默认 `LICENSE_TRIAL_DAYS` 为 15，可由 Authority 配置覆盖 | 方案统一使用 Authority 返回值，禁止客户端自行计算 |
| “客户总链路 production-ready” | 真实 Authority、Redis、浏览器 e2e 未验收 | 降级为“代码链路已实现，生产验收未完成” |

## 4. 当前发布结论

当前可以进入“代码链路集成测试”，不能宣称“完整生产闭环”。发布阻断项：

1. Authority Redis 高可用、密钥和 TLS 配置必须在目标环境验证。
2. Trial、在线激活、离线激活必须完成真实浏览器和 installer e2e。
3. 升级命令必须实现签名 envelope、命令幂等、TTL、审批、客户端白名单执行和结果回写。
4. 采集必须实现 opt-in、字段 allowlist、认证 ingest、保留期和删除流程。
5. `scan-secrets.sh` 当前仓库既有 BLOCK 项必须在发布前单独清零，不能以历史 baseline 继续放行。
