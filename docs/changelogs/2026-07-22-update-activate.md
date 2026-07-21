# 2026-07-22 — 更新与激活（非核心节点运维整理）

## 变更摘要

非核心节点将原「站点信息 / 许可激活 / 许可状态 / 用户协议」四页合并为单一入口 **更新与激活**，挂在「数据运维」菜单下。核心节点（同域 deploy ai-native-maintain）超级管理员仍看到完整「运维中心」外链菜单。

## 行为

1. **一键激活**：填写注册名称 → 同意协议 → `POST /api/system/bootstrap/activate-quick` → 中心 `POST /maintain-api/public/license/issue` → 本地校验/绑定设备。
2. **站点信息**：IP、服务版本、注册名、激活状态、激活时间（bootstrap status 按 hardware_hash 回填）。
3. **版本与升级**：读取 `/api/system/upgrade/*`；有新版本显示升级按钮（跳转 `/maintain/upgrade`）。
4. **已开通模块**：读取 `/maintain-api/modules/catalog`（只读）；申请入口指向中心 `/admin/modules`。

## 关键路径

- `licensing/bootstrap_*.go` + `cmd/gateway/main.go` 注册
- `web/src/views/lifecycle/UpdateActivateView.vue` + 子卡片组件
- `web/src/config/appNav.ts` / `router.ts` / `edition.ts`（`isCoreNode`）
- 旧 URL redirect → `/customer/update-activate`

## 后续（P2）

中心 `/admin/modules` 已挂载「发版模块开通」面板；maintain 提供内存态
`/maintain-api/admin/module-entitlements`（apply / list / open / reject）。
持久化入库与完整审批工单可后续迭代。
