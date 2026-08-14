# 2026-08-15 Node Operations Audit Fixes

## 做了什么

审计并修复节点状态矩阵和在线会话时间线中的可复现问题。节点启停现在使用正确的布尔方向，满足后端二次确认与幂等契约；时间线超过 200 条时显式报告截断状态。

## 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `admin/session_online.go` | modify | 多取一条时间线记录并返回截断元数据 |
| `web/src/components/NodeStatusMatrix.vue` | modify | 修复启停方向、请求契约、操作互斥和主题颜色 |
| `web/src/components/NodeStatusMatrix.test.ts` | new | 覆盖异常分组、抽屉和禁用请求契约 |
| `web/src/components/QueuePerspectivePanel.vue` | modify | 移除状态背景的硬编码颜色 fallback |
| `CHANGELOG.md` | modify | 登记本次审计修复 |

## 为什么这样做

原节点按钮在未禁用状态下仍发送 `enabled: true`，且缺少后端强制要求的 `X-Confirm` 和 `reason`，导致禁用操作无效或被拒绝。幂等键也必须与 correlation ID 对齐，才能命中后端现有审计查询。时间线原先固定 `LIMIT 200`，调用方无法区分完整结果与截断结果。

## 验证结果

- `GOFLAGS=-mod=mod go build ./...`：通过。
- `GOFLAGS=-mod=mod go test ./admin/...`：通过。
- `GOFLAGS=-mod=mod go vet ./admin/...`：通过。
- `pnpm exec vitest run src/components/NodeStatusMatrix.test.ts src/components/QueuePerspectivePanel.test.ts`：4/4 通过。
- `pnpm exec vue-tsc --noEmit`：通过。
- `pnpm run build`：通过。
- 本地页面 `http://127.0.0.1:5174`：HTTP 200；browser-use 实测桌面视口 `1675x907` 正常渲染且无横向溢出，截图保存为 `ui-verify-node-audit-login-20260815.png`。

## 遗留与风险

- 隔离浏览器没有管理员认证态，未实际触发节点启停 API；请求契约由组件测试验证。
- Go vendor 目录与 `go.mod` 当前不同步，因此验证使用 `GOFLAGS=-mod=mod`，本次未改依赖或 vendor。

## 下一步建议

- 在有管理员认证态的本地或测试环境复测节点禁用、启用和立即测试三个操作，并留存浏览器截图。
