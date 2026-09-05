# Fix: Dashboard live-stream swimlane rendering direction

Date: 2026-07-23
Branch: fix/swimlane-rendering-direction
Author: AI Agent

## Symptom

Dashboard "实时数据流"（live stream V2）的 swimlane（泳道）中，色块
的视觉方向与后端数据顺序相反：

- **当前（bug）**: 最左侧色块 = 最新请求
- **期望**: 最右侧色块 = 最新请求，新数据追加到右尾

设计与代码（`docs/DASHBOARD_V2_VERIFICATION.md §1.7`、`docs/DASHBOARD_V2_TECHNICAL_DESIGN.md §2.2`）
明确泳道要求 FIFO：append 到 tail，超出容量时 `shift` 头部最旧。
后端 `admin/live_stream_redis_store_snapshot_fix.go` 也是按 ASC
时间戳排序（newest 在 tail）。

## Root Cause

`web/src/components/SwimLane.vue` 的 `visibleRequests` computed 在渲染时
对 lane.requests 做了 `.reverse()`，把后端 ASC 数组反转成"最左 = 新"，
导致视觉方向与数据顺序相反。

```ts
// 改前
if (requests.length <= max) return [...requests].reverse()
return requests.slice(requests.length - max).reverse()
```

`handleEmergencyDiagnose` 内部依赖 `props.lane.requests.reverse().find()`
定位"最近"的失败请求 — 依赖的是已经"反转后"的数组；
实际只要"按 ASC 顺序从 tail 往前找"就能拿到最近，行为不需改动。

CSS 注释 `.swim-lane__tiles`（"最新请求在左边"）也误导后续维护。

## Fix

最小补丁，去掉 `.reverse()`，使渲染方向与后端 ASC 时间戳顺序一致：

```ts
// 改后
if (requests.length <= max) return [...requests]
return requests.slice(requests.length - max)
```

注释同步修正（`visibleRequests` + `.swim-lane__tiles`），
准确描述方向契约。

## Files Changed

| File | Change |
|---|---|
| `web/src/components/SwimLane.vue` | 去掉 `visibleRequests` 的 `.reverse()`；更新 CSS 注释 |
| `VERSION`, `version.json`, `web/public/version.json`, `web/public/menu-config.json` | 自动 rebuild 产物 |

## Verification

| Check | Result |
|---|---|
| `vue-tsc --noEmit` | exit 0 ✅ |
| `vite build` | success ✅ |
| pre-existing unit tests | 6 failures pre-existed (i18n key 缺失，与本改动无关) ✅ |
| 浏览器实测（rule 11 §6） | 待部署到 154 后由人类执行 browser-use 验证 |

## Risk

- 低风险：纯前端渲染方向修正，不动 API/数据契约
- 后端不变，CSS 不动布局
- 唯一行为变化：泳道色块视觉方向

## Rollback

revert `web/src/components/SwimLane.vue` 单文件即可回退。
