# 客户侧增强 v2 · 2026-07-14

> 对应 `audit/03-gaps.md` 的 6 个客户侧缺口（G-C1 ~ G-C6）。
> 目标：补齐用户旅程（Phase 2-5）的所有浏览器侧 API + UI。

## 1. 增强矩阵

| 缺口 | 用户痛点 | 落地 | 工作量 |
|------|---------|------|--------|
| G-C1 | 浏览器无法激活 | `/api/system/license/*` 5 端点 + ActivationWizard.vue | 3d |
| G-C2 | 升级 Banner 缺失 | `/api/system/upgrade/*` + UpgradeBanner + UpgradePanel | 5d |
| G-C3 | 回退 CLI 不显眼 | 独立 `kx-gateway-rollback` + UI 入口 | 1d |
| G-C4 | Telemetry 开关无 UI | `/api/system/telemetry/pref` + TelemetryView.vue | 3d |
| G-C5 | License 详情无 UI | `/api/system/license/info` + LicenseInfoView.vue | 2d |
| G-C6 | 离线激活 UI 缺失 | ActivationWizard 内嵌 offline 子步骤 | 2d |

## 2. 浏览器侧 License API（G-C1）

### 2.1 端点设计（现有实现：`licensing/customer_api.go`；后续收敛契约）

```go
package api

// SetupHandler 浏览器侧 license 激活 handler。
// 内部委托给 installer 子命令（CLI 等价路径），返回相同结果。
type SetupHandler struct {
    installerPath string  // 绝对路径 e.g. /opt/llm-gateway/bin/llm-gw-installer
    masterURL     string  // 主控端 URL
    workDir       string  // /var/lib/kx-gateway (license.dat 目录)
    activator     *licensing.Activator  // 直接复用 (不走 CLI)
}

func (h *SetupHandler) RegisterRoutes(g *echo.Group) {
    g.POST("/license/trial", h.HandleTrial)
    g.POST("/license/activate", h.HandleKey)
    g.POST("/license/offline/request", h.HandleOfflineRequest)
    g.POST("/license/offline/import", h.HandleOfflineImport)
    g.GET("/license/status", h.HandleStatus)
    g.GET("/license/info", h.HandleInfo)
    g.GET("/license/fingerprint", h.HandleFingerprint)  // 客户端指纹 + 实例 ID
    g.POST("/license/restart", h.HandleSoftRestart)     // 软重启
}
```

### 2.2 关键端点细节

#### `POST /api/system/license/trial`
```json
// Request
{
    "email": "ops@acme.com",
    "agree": true,
    "telemetry_opt_in": false
}
// Response 200
{
    "success": true,
    "license_type": "trial",
    "trial_expires_at": "2026-08-15T00:00:00Z",
    "max_devices": 1,
    "features": ["basic-routing", "2-tenants", "audit-log"],
    "instance_id": "a3b4c5d6-...",
    "next_action": "soft_restart_in_3s"
}
```
**实现**：
1. 收集 fingerprint（`/api/system/license/fingerprint` 客户端已有）
2. 直接调 `licensing.Activator.ActivateTrial(ctx, email, fingerprint)` — **不**走 installer CLI（避免 process spawn 开销）
3. 写 `license.dat` + DB license_devices 记录
4. 设置 `telemetry_opt_in` 到 `telemetry_prefs` 表
5. 触发软重启（3s 后）

#### `POST /api/system/license/activate`
```json
// Request
{
    "license_key": "LIC-a1b2c3d4..."  // 当前实现格式
    "agree": true
}
// Response 200
{
    "success": true,
    "license_type": "standard",
    "expires_at": "2027-07-12T00:00:00Z",
    "max_devices": 3,
    "active_devices": 1,
    "next_action": "soft_restart_in_3s"
}
// Response 400 (fingerprint_mismatch)
{
    "success": false,
    "error": "fingerprint_mismatch",
    "message": "硬件指纹不匹配；如更换硬件请联系 support@kxpms.cn"
}
// Response 403 (license_invalid)
{
    "success": false,
    "error": "license_invalid",
    "message": "License Key 不存在或已撤销"
}
```

#### `POST /api/system/license/offline/request`
```json
// Response 200
{
    "request_id": "req-uuid-...",
    "request_b64": "<base64 encoded ActivationRequest>",
    "valid_until": "2026-07-21T00:00:00Z",
    "next_action": "upload_to_https://llm.kxpms.cn/offline-activation"
}
```

#### `POST /api/system/license/offline/import`
```json
// Request
{
    "signed_license_b64": "<base64 encoded SignedLicense>"
}
// Response 200
{
    "success": true,
    "next_action": "soft_restart_in_3s"
}
```

#### `GET /api/system/license/status`
```json
{
    "activated": true,
    "license_type": "standard",
    "customer": "ACME Corp",
    "expires_at": "2027-07-12T00:00:00Z",
    "days_remaining": 365,
    "max_devices": 3,
    "active_devices": 1,
    "instance_id": "a3b4c5d6-...",
    "needs_telemetry_consent": false,
    "is_grace": false,
    "grace_remaining_seconds": 0
}
```

#### `GET /api/system/license/info`
```json
{
    "instance": {
        "id": "a3b4c5d6-...",
        "hostname": "prod-app-01",
        "fingerprint_hash": "9f8e7d6c..."
    },
    "license": { ... 全 status 字段 },
    "signed_license_created_at": "2026-07-12T...",
    "signed_license_age_hours": 24,
    "needs_renewal": false,
    "audit_trail": [
        {"at": "2026-07-12T10:00:00Z", "action": "activate", "by": "ops@acme.com"},
        {"at": "2026-07-13T03:00:00Z", "action": "heartbeat", "by": "installer"}
    ]
}
```

### 2.3 安全考量

| 风险 | 缓解 |
|------|------|
| 浏览器绕过 license 校验直接调 API | 在 `restricted_mode.go` 启动期检测：缺 license.dat 时所有 `/api/system/license/*`（除 trial/activate）都返回 503 |
| CSRF 攻击 | `/api/system/license/trial` 必须是 POST + JSON，浏览器默认 SameSite=Lax 即可防 |
| 同局域网未授权访问 | 强制要求 `Authorization: Bearer <admin-api-key>`（与现有 `/api/system/info` 一致） |

## 3. 升级推送 + UI（G-C2）

### 3.1 端点设计（落地位 `gateway/internal/api/upgrade_user_handler.go`）

```go
package api

type UpgradeUserHandler struct {
    upgrader     *upgrader.Upgrader  // installer/internal/upgrader
    upgraderSock string              // /var/run/kx-gateway-upgrader.sock
}

func (h *UpgradeUserHandler) RegisterRoutes(g *echo.Group) {
    g.POST("/upgrade/check", h.HandleCheck)
    g.POST("/upgrade/start", h.HandleStart)
    g.GET("/upgrade/stream", h.HandleStream)        // SSE
    g.GET("/upgrade/status", h.HandleStatus)
    g.POST("/upgrade/rollback", h.HandleRollback)
}
```

### 3.2 关键端点

#### `POST /api/system/upgrade/check`
```json
// Response 200
{
    "has_update": true,
    "current_version": "v1.13.0",
    "current_build_seq": 770,
    "latest_version": "v1.14.0",
    "latest_build_seq": 800,
    "channel": "stable",
    "mandatory": false,
    "manifest_url": "https://llm.kxpms.cn/api/v1/updates/manifest?v=v1.14.0",
    "release_notes": "- 多租户配额管理\n- 修复 5 个已知问题"
}
// Response 200 (up_to_date)
{
    "has_update": false,
    "current_version": "v1.14.0"
}
```

#### `POST /api/system/upgrade/start`
```json
// Request
{
    "target_version": "v1.14.0",
    "channel": "stable",
    "no_migrate": false,
    "auto_rollback_on_health_fail": true
}
// Response 200 (job started)
{
    "job_id": "job-uuid-...",
    "stream_url": "/api/system/upgrade/stream?job_id=job-uuid-...",
    "estimated_duration_secs": 240
}
// Response 409 (another upgrade running)
{
    "error": "upgrade_in_progress",
    "active_job_id": "job-existing-..."
}
```

#### `GET /api/system/upgrade/stream?job_id=...` (SSE)
```
event: state
data: {"state": "DOWNLOADING", "progress": 0.4, "bytes": 1300000000, "total": 3200000000}

event: state
data: {"state": "TESTING", "progress": 0.7}

event: state
data: {"state": "DONE", "version": "v1.14.0", "duration_secs": 187}

event: error
data: {"error": "health_check_timeout", "auto_rollback": true}
```

#### `GET /api/system/upgrade/status`
```json
{
    "current_state": "MONITORING",
    "current_version": "v1.13.0",
    "active_version": "v1.14.0",
    "monitoring_remaining_secs": 1820,
    "rollback_available": true
}
```

#### `POST /api/system/upgrade/rollback`
```json
// Request
{
    "to_version": "v1.13.0"  // 可选；留空回滚到上一个 verified
}
// Response 200
{
    "success": true,
    "rolled_back_to": "v1.13.0",
    "duration_secs": 8
}
```

### 3.3 前端 UI

#### `web/src/components/UpgradeBanner.vue`
```vue
<template v-if="updateAvailable">
  <el-alert type="info" :closable="false">
    🆕 KX Gateway v{{ latestVersion }} 已发布
    <div v-html="releaseNotes"></div>
    <el-button @click="$router.push('/settings/upgrade')">立即升级</el-button>
    <el-button @click="dismissFor24h">稍后提醒</el-button>
  </el-alert>
</template>
```
**触发**：
- 路由进入 `/` 时 GET `/api/system/upgrade/check`
- 用户 dismiss 后 24h 不再显示
- mandatory=true 时强制显示 + 无 dismiss 按钮

#### `web/src/views/settings/UpgradePanel.vue`
- 步骤条（IDLE → CHECK → DOWNLOAD → INSTALL → TEST → SWITCH → MONITORING → DONE）
- 实时进度（SSE 订阅）
- `[立即升级] [暂停] [紧急回退]` 3 个按钮
- 升级历史列表（最近 10 条）

## 4. 独立回退 CLI（G-C3）

### 4.1 落地位：`scripts/kx-gateway-rollback`

```bash
#!/usr/bin/env bash
# KX Gateway 独立回退 CLI
# 任意时刻可执行（不依赖 installer 二进制）
# 用法:
#   kx-gateway-rollback                # 回滚到上一个 verified
#   kx-gateway-rollback --to v1.13.0   # 回滚到指定版本
#   kx-gateway-rollback --list         # 列出所有可回滚版本
#   kx-gateway-rollback --self-update  # 更新自身 (从 CDN)

set -euo pipefail
DATA_ROOT="${KX_GATEWAY_DATA_ROOT:-/var/lib/kx-gateway}"
STATE_FILE="$DATA_ROOT/upgrader.state.json"

# 实现（参考 docs/分发与激活/06-自动升级与蓝绿部署.md §六）
```

### 4.2 关键能力
- 自带恢复逻辑（不依赖 installer 二进制存在）
- 列出 `<data_root>/backups/*.backup` 候选
- 校验 SHA256 后 `os.Rename` 原子替换
- systemctl restart + 5s healthz 校验
- 失败时回退到上一版本（双重回退）

## 5. Telemetry 设置 UI（G-C4）

### 5.1 端点（落地位 `gateway/internal/api/telemetry_handler.go`）

```go
func (h *TelemetryHandler) RegisterRoutes(g *echo.Group) {
    g.GET("/telemetry/pref", h.HandleGet)
    g.PUT("/telemetry/pref", h.HandleUpdate)
    g.DELETE("/telemetry/pref", h.HandleOptOut)  // 撤销采集 + 删除已采集数据
}

// Request
{"enabled": true, "interval_sec": 300}
// Response
{
    "enabled": true,
    "interval_sec": 300,
    "updated_at": "2026-07-14T...",
    "data_deletion_requested": false
}
```

### 5.2 UI：`web/src/views/settings/TelemetryView.vue`
- 大开关 + 频率选择（5min/15min/1h）
- 采集项明细表（07 §一）
- "删除已采集数据" 红色按钮

## 6. License 详情 UI（G-C5）

`web/src/views/settings/LicenseInfoView.vue`
- License Key（脱敏显示前 4 后 4）
- 类型 / 到期 / 设备数 / 实例 ID / Grace 状态
- 最近 10 条 audit 记录（谁在什么时间做了什么）

## 7. 离线激活 UI（G-C6）

`web/src/views/setup/ActivationWizard.vue` 加 sub-step：
- Step 4.1 "生成激活请求" → 下载 `activation.req` 文件
- Step 4.2 "上传激活响应" → 选择 `activation.resp` 文件 → 显示成功
- Step 4.3 "等待主控端审批" → 轮询 status（30s 间隔）

## 8. e2e 验收清单

```gherkin
Feature: 浏览器侧 License 激活 (G-C1)
  Scenario: 试用激活
    Given 客户打开 http://server:8781，未激活状态
    When 浏览器 GET /
    Then 重定向到 /setup
    And 选择 [免费试用]
    And 输入邮箱 + 勾选同意
    And POST /api/system/license/trial
    Then 收到 license.dat + 重启 + 跳转到首页
    And 24h 后心跳正常
```

```gherkin
Feature: 升级推送 Banner (G-C2)
  Scenario: 首页出现 Banner
    Given 主控端发布 v2.1
    When 客户打开 /
    Then 看到 UpgradeBanner
    When 点击 [立即升级]
    Then 跳转到 /settings/upgrade
    And 看到 UpgradePanel + 进度条
```

```gherkin
Feature: 独立回退 CLI (G-C3)
  Scenario: 紧急回退
    Given 升级到 v2.1 后服务故障
    When SSH 到机器
    And 跑 kx-gateway-rollback
    Then 4s 内回滚到上一个 verified 版本
    And /healthz 200
```
