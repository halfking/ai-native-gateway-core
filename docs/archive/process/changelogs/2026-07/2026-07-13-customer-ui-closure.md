# 2026-07-13 — 客户 UI 闭环（P0 customer journey gap closure）

## 概要

按第五轮 spec §5 Next Step #1，清零 README 中标注的 P0 客户旅程缺口：

- **新增客户面向 API 端点**（不需登录）：
  - `GET /api/system/license/status` — 当前授权状态（none/active/grace/expired/revoked）
  - `GET /api/system/license/info` — License 详情（含 features / max_devices / active_devices / 心跳时间）
  - `POST /api/system/license/activate` — 在线激活（License Key）
  - `POST /api/system/license/offline-activate` — 离线激活（粘贴 SignedLicense + ActivationCode）
  - `POST /api/system/license/offline-request` — 生成离线激活请求（base64 envelope）
  - `POST /api/system/license/heartbeat` — 手动触发心跳（自动 6h 间隔）
  - `GET /api/system/upgrade/status` — 当前版本 vs 渠道最新版本
  - `POST /api/system/upgrade/check` — 触发升级检查，返回是否可升级
- **新增 Vue 组件**：
  - `ActivationWizard.vue` — 4 步激活向导（状态概览 → 在线 / 离线 → 完成）
  - `LicenseInfoView.vue` — License 详情 + 心跳管理
  - `UpgradePanel.vue` — 升级面板（只读 + 管理员触发）
  - `UpgradeBanner.vue` — 全局升级提示横幅（按需展示）

## 路由变更

新增三个公开路由：

- `/activate` — 激活向导（4 步）
- `/license` — License 详情
- `/upgrade` — 升级面板

均为 `meta: { public: true }`，无需登录即可访问。`App.vue` 在这些路由上跳过 `/api/auth/me` 探测，避免被 API 客户端的 401 重定向到 `/login` 而锁死。

## 实现细节

### Go handlers

- `licensing/customer_api.go` — `CustomerAPI` 复用现有 `Store` / `Activator` / `OfflineManager`，无新增依赖。状态计算考虑 grace window（7 天）。
- `autoupdate/customer_api.go` — `CustomerAPI` 接收 `VersionProvider` 闭包，注入 `cmd/gateway` 的 `Version` / `BuildNumber` ldflag 值。渠道默认 stable。
- `cmd/gateway/customer_helpers.go` — `noAuthCustomerMiddleware()` 占位（当前只做日志；后续可加 IP 白名单 / 限流）+ `currentGatewayVersionProvider()` 闭包。

### 测试

- `licensing/customer_api_test.go` — 10 个单元测试覆盖 status (none/active/grace/expired/revoked)、info、activate 校验、offline-activate 签名校验、heartbeat、offline-request 不存在的 license_key 等。
- `autoupdate/customer_api_test.go` — 6 个单元测试覆盖 status (no-update/has-update/incompatible/different-channel/unpublished)、check 触发、channel 过滤。

### 设计取舍

1. **共享 Echo 实例**：未新建独立 `customerE`，复用 admin 用的 `e`，通过 `mux.Handle("/api/system/license/", e)` 等多次挂载复用同一 router。`http.ServeMux` 的最长匹配语义保证三个前缀互不冲突。
2. **不做主动激活失败重试**：`ActivateDevice` 内部已有 device limit + 已激活检查，无需在 customer 层重复。
3. **路由元数据 `public: true` 不足以绕过 `App.vue` 的 auth 探测**：API 客户端的 401 handler 在 `req()` 内强制 `window.location.href = '/login'`，必须在 `App.vue` 的 onMounted 跳过 auth probe 才能真正访问到 public 路由。新增 `publicPaths` 列表作为兜底。

## 已知限制（与本轮范围无关）

- **首次访问 /activate 仍然会被 Vite HMR 的旧 chunk 缓存**：HMR 不总是及时拉取 `App.vue` 的新版本；冷启动 / 强制刷新后才能看到完整效果。
- **完整浏览器实测需要本地 PostgreSQL + Redis**：当前开发环境无完整 DB，无法启动带 licensing store 的 gateway 进程跑真实激活流程；已通过单元测试覆盖所有 handler 行为。
- **离线激活流的 UX**：admin 端审批后会向客户线下传递 `signed_license` + `activation_code`，目前 wizard 要求客户同时粘贴两项；未来可考虑合并为单一二维码 / 短链接。

## 文件清单

### 新增

- `licensing/customer_api.go` (~270 行)
- `licensing/customer_api_test.go` (~410 行)
- `autoupdate/customer_api.go` (~140 行)
- `autoupdate/customer_api_test.go` (~190 行)
- `cmd/gateway/customer_helpers.go` (~55 行)
- `web/src/api/customer.ts` (~120 行)
- `web/src/views/ActivationWizard.vue` (~310 行)
- `web/src/views/LicenseInfoView.vue` (~190 行)
- `web/src/views/UpgradePanel.vue` (~150 行)
- `web/src/components/UpgradeBanner.vue` (~80 行)
- `web/src/locales/zh-CN/customer.ts` (~20 行)
- `web/src/locales/en-US/customer.ts` (~20 行)

### 修改

- `cmd/gateway/main.go` — +30 行（注册 customer license / upgrade API，挂载 mux）
- `web/src/router.ts` — +9 行（3 个新 public 路由）
- `web/src/App.vue` — +12 行（auth probe skip + UpgradeBanner 挂载）
- `web/src/locales/zh-CN/index.ts` — +2 行（import + export customer）
- `web/src/locales/en-US/index.ts` — +2 行（同上）

## 验证

- ✅ `go build ./...`
- ✅ `go test ./licensing/ ./autoupdate/ -count=1 -short`
- ✅ `npm run build`（web/dist 生成）
- ⚠️ 浏览器实测受限于本地 DB 不可达；改用单元测试 + 直接 SPA push 验证（r.push('/activate') 后 wizard 正确渲染）