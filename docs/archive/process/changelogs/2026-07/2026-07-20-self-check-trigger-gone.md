# Self-Check Trigger 端点 410 Gone 修复

**Date**: 2026-07-20
**Priority**: P0
**Type**: Bug Fix + UI 契约修正

## Summary

首页 "系统自检 → 触发测试" 在新探针模式下恒定返回 503，本批修改把错误码改为 410 Gone 并新增 `/api/self-check/trigger/availability` 端点 + 前端按钮按可用性 disable。

## 根因

- 2026-07-14 探针体系重写：默认 `LLM_GATEWAY_USE_NEW_PROBE_MODE=true`。
- 2026-07-18 加双重门禁：legacy featured-mode `SelfCheckWorker` 仅在
  `LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK=true` 才启动。
- 默认配置下 `selfCheckWorker == nil`，但 `/api/self-check/trigger` 路由仍挂载，
  admin handler 在 `h.worker == nil` 时硬编码 503 + 模糊 `"worker not available"` 文案 —
  前端 UI 把它当成 "临时服务故障" 反复红 banner，不能定位。

## Fix 内容

### 后端 `admin/self_check_handlers.go`

| 改动 | 说明 |
|---|---|
| `handleTrigger` worker=nil 分支 | 503 → **410 Gone** + `error_code` 字段 + 区分 "新探针模式已下线" vs "worker 未初始化" |
| 新增 `handleTriggerAvailability` | `GET /api/self-check/trigger/availability` 永远返回 200 + JSON `{available, new_probe_mode, reason, error_code}` |
| 新增 `scNewProbeMode()` helper | 镜像 `cmd/gateway.useNewProbeMode()` 默认 true 的契约，避免双源 |
| `RegisterRoutes` | 注册 `/api/self-check/trigger/availability`（admin 权限） |

### 后端 `admin/self_check_handlers_test.go`（新增，6 个测试 + 12 sub-cases）

- `TestHandleTriggerAvailability_NoWorker` —— 新探针模式 + worker=nil → 200 + 字段验证
- `TestHandleTriggerAvailability_OldProbeFallback` —— rollback 模式 + worker=nil → 200 + 通用 worker_unavailable code
- `TestHandleTrigger_NoWorkerReturnsGone` —— POST /trigger → **410 Gone** + `disabled_in_new_probe_mode` code
- `TestHandleTrigger_OldProbeFallbackGone` —— rollback + worker=nil → 410 + 通用 code
- `TestHandleTrigger_MethodNotAllowed` —— GET /trigger → 405
- `TestScNewProbeMode_EnvMatrix` —— env 解析 12 子用例锁住契约（unset/true/TRUE/1/yes/on/false/0/no/off/random/+空白）

### 前端 `web/src/api-selfcheck.ts`

| 改动 | 说明 |
|---|---|
| `req<T>` 抛错增加 `status` + `body` 字段 | 让 catch 端可识别 4xx/5xx 而不只解析文本 |
| 新增 `SelfCheckTriggerAvailability` interface + `fetchSelfCheckTriggerAvailability()` | 对应新端点 |

### 前端 `web/src/views/SelfCheckPanel.vue`

| 改动 | 说明 |
|---|---|
| 加载 `loadAll()` 加一个 `avail = fetchSelfCheckTriggerAvailability()` 入 Promise.all | mount 与 60s 轮询都跟随 |
| `triggerAvailability` ref | 驱动按钮 disable + tooltip |
| 顶部 "▶ 手动触发" 按钮 | `:disabled="triggerBusy \|\| !triggerAvailability.available"` + 文案改 `⛔ 触发已下线` + `:title="reason"` |
| 每个模型卡片的 "触发测试" 按钮 | 同样 disable + tooltip |
| `onTrigger()` 增加前端 gate | 不可用直接显示 reason，不再发请求；catch 410 时解析 payload 显示后端 message 并 best-effort refresh availability |

## 验证结果

| 项 | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./admin/...` | exit 0 |
| `go test ./admin/ -run 'HandleTrigger|ScNewProbeMode'` | 7 个 case 全过 |
| `pnpm vue-tsc --noEmit` | exit 0 |
| `pnpm build` | ✓ 11.6s |
| `scripts/deploy-seamless.sh deploy 245` | seq=1196 自动 atomic 切换 + healthz + DB 通过 |

## 浏览器实测（待老板）

后端契约 + 前端 UI 校验完成：
- ✅ `GET /api/self-check/trigger/availability` 返回 `{available:false, new_probe_mode:true, reason:"self-check worker disabled in new probe mode …", error_code:"self_check.trigger.disabled_in_new_probe_mode"}`
- ✅ `POST /api/self-check/trigger` 返回 **410 Gone** + 同 error_code
- ✅ 顶部按钮 + 模型卡片按钮在 available=false 时显示 `⛔ 触发已下线` 并 disabled
- ⚠️ 浏览器视频级交互（rule 11 §6）：**等老板到 https://llmgo.kxpms.cn 实际打开系统自检 tab** 验证按钮变灰 + hover tooltip 文案 + 页面无 console error

## 影响范围

- 仅 `admin/self_check_*` 和自检 UI — 不影响 `trigger` 之外的 self-check 端点（settings / stats / models / runs）
- 不影响 legacy 用户启用 `LLM_GATEWAY_ENABLE_LEGACY_SELFCHECK=true` 的回滚路径（worker 一旦存在，trigger 走 200 happy path 不变）

## 后续

- 2026-07-18 changelog 提到"待新探针稳定运行 1 个月后考虑完全移除 legacy worker" — 当 legacy 下线后，本 availability 端点 / 按钮 / 410 错误码均可下线（本 commit 是"可下线化"的一步）

## 相关文件

- `admin/self_check_handlers.go`
- `admin/self_check_handlers_test.go` (new)
- `web/src/api-selfcheck.ts`
- `web/src/views/SelfCheckPanel.vue`
- `docs/changelogs/2026-07-18-legacy-selfcheck-double-gate.md` （背景）

## 部署记录

- 245 staging: seq=1196, version=`phase0-complete-9d5a6d59-20260719-1196`
- 154 生产: 未推送（按 ACC 策略走 245→154 晋级门禁）
