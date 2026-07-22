# 2026-07-22 — 更新与激活：激活效果展示 + 版本目录 + 升级切换流程

## 变更摘要

`/customer/update-activate` 页面改造：

1. 引入「激活效果」卡片，**复用 `/maintain/activate` 的 status-panel 视觉**：状态点（success / warning）+ License 状态文案 + 订阅 tier chip + License Key + 有效期至 + 设备名 + 客户名 + 最近心跳。
2. 版本模块改造为系统版本列表，**展示最新 5 个版本**，已安装的版本标记「已安装」，未安装的标记「未安装」，其余历史版本。
3. 把单一「升级」按钮替换为**升级→下载→安装→启动切换 4 步流程**，每步可观察状态（待执行 / 进行中 / 已完成 / 失败），完成后通过 `/maintain-api/upgrade/report` 向中心上报。

## 行为

1. **激活效果卡片** — 进入页面时并发请求 `/maintain-api/license/status?instance_id=`；返回后渲染状态点 + tier + license_key + expires_at + device_name + customer_name + last_heartbeat；不可达时显示 el-empty 占位。
2. **版本列表** — 从 `/maintain-api/downloads/catalog` 取版本目录，slice(0, 5) 展示。已安装的版本（与 `upgradeStatus.current_version` 或 `BootstrapStatus.service_version` 匹配）显示绿底「已安装」并加 `.is-installed` 高亮；最新版本显示「未安装」黄标；其余「历史版本」。
3. **升级流程** — 点击「升级到 vX.Y.Z」按钮 → 二次确认 → 串行执行 4 步：
   - **下载**：调 `/maintain-api/downloads/ticket` 获取下载 URL，`window.open`；`/downloads/events` 上报 `started`。
   - **安装**：调 `/maintain-api/upgrade/report` 上报 `started`，UI 切到 in_progress。
   - **启动切换**：调 `/maintain-api/upgrade/report` 上报 `completed`，UI 切到 done 并刷新状态。
   - 任一步骤失败即停后续步骤，对应卡片红色边框 + 「失败」红标。

## 数据源

| 内容 | API |
|---|---|
| 激活效果（LicenseStatus） | `GET /maintain-api/license/status?instance_id=` |
| 版本目录（CatalogResponse） | `GET /maintain-api/downloads/catalog` |
| 当前/最新版本（UpgradeStatus） | `GET /api/system/upgrade/status` |
| 升级检查 | `POST /api/system/upgrade/check` |
| 下载 ticket | `POST /maintain-api/downloads/ticket` |
| 下载事件 | `POST /maintain-api/downloads/events` |
| 升级上报 | `POST /maintain-api/upgrade/report` |

## 改动清单

| 类型 | 文件 | 说明 |
|---|---|---|
| 新增 | `web/src/components/lifecycle/UpdateActivateLicenseCard.vue` | 激活效果卡片 |
| 修改 | `web/src/components/lifecycle/UpdateActivateVersionsCard.vue` | 重写：5 个版本列表 + 4 步升级流程 |
| 修改 | `web/src/views/lifecycle/UpdateActivateView.vue` | 接入 LicenseCard；新增 catalog/license 状态与刷新；改 VersionsCard props/events |
| 修改 | `web/src/api/updateActivate.ts` | 增加 `licenseStatus`、`downloadsCatalog`、`downloadsTicket`、`downloadsEvent`、`upgradeReport` |
| 新增 | `web/src/utils/labels.ts` | `licenseStateLabel` / `licenseModeLabel` / `isLicenseActive`，与 maintain 同名映射 |

## 验证

- `vue-tsc --noEmit` 通过（exit 0）
- `vite build` 通过（exit 0）
- `vitest run src/smoke.test.ts` 3/3 通过
- browser-use 实测（mock 后端）：5 个版本列表 + 已安装标记 + 4 步流程 + LicenseCard 状态点/tier/license_key/有效期/设备名/客户名/心跳全部正确渲染；点击「升级到 v2.4.7」→ 二次确认 → 4 步全部 done → 提示「已切换到 v2.4.7」

## 已知妥协 / 待办

1. install / 启动切换 在浏览器侧只是状态机展示 + 向中心上报，实际解压/重启由部署侧的安装器/agent 完成。当前 UI 与后端职责对齐（前端无破坏性写入）。
2. LicenseCard 在 `instance_id` 为空时不请求（`loadLicense` 提前 return），生产环境 instanceId 由 bootstrap status 回填，预览场景需手动写入 `localStorage.llmgw_instance_id`。