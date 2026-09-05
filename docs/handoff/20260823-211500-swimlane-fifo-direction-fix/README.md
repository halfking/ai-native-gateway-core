# Handoff — 泳道 FIFO 方向修复（左旧右新）

**日期**：2026-08-23  
**分支**：`fix/swimlane-fifo-direction`  
**生产基线**：build **#1692**（修复前）

---

## 问题

「实时请求流 → 按供应商/原厂/模型」泳道应 **左旧右新**，新 tile 从右尾进入；生产上 tile 全部挤在泳道**右侧**（`flex-end` 右锚定），左侧大片空白。

## 根因

| 层 | 问题 |
|---|---|
| 后端 `buildLiveStreamLanes` | DESC + `firstTiles`（与前端 ASC 双契约） |
| 前端 `SwimLaneTrack.vue` | `justify-content: flex-end` + `margin-left: auto` |

数据序在生产上已是 ASC（MiniMax _lane：17:10:11 → 20:49:34 自左向右递增），视觉锚定错误。

## 修复

1. **Go**：`buildLiveStreamLanes` → ASC；`firstTiles` → `lastTiles`
2. **Vue**：`SwimLaneTrack` → `flex-start`，去掉 `margin-left: auto`

## REAL_DEPENDENCY_VERIFIED（生产 build #1692，修复前）

| 项 | 结果 | 证据 |
|---|---|---|
| 按供应商泳道可打开 | ✅ | browser-use 登录后切换「按供应商」 |
| 数据时间序 ASC | ✅ | MiniMax 20 tile：17:10:11 … 20:49:34 左→右 |
| 视觉左起右进 | ❌ | `flex-end`；首 tile `left=1354` |
| CSS 注入 `flex-start` 后 | ✅ | 首 tile `left=222`（track 起点），条带从左向右铺开 |

截图：`/tmp/dashboard-provider-lanes.png`（修复前）、`/tmp/dashboard-provider-lanes-fixed.png`（注入验证）

## 部署后复测清单

1. 首 tile 贴近泳道左缘（非右对齐）
2. 新请求从右尾进入；满窗时最左被挤出
3. 按原厂 / 按模型 同一轨道组件

## 验证命令

```bash
go test ./admin/ -run 'BuildLiveStreamLanes|LastTiles|SelectiveTrim' -count=1
cd web && pnpm exec vitest run src/components/SwimLaneTrack.test.ts
```
