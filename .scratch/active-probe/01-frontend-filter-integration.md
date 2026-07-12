# Frontend: 探测请求过滤器集成

**状态**: needs-info  
**优先级**: medium  
**预估工时**: 1-2 小时  
**依赖**: 后端 commit 2a5604651 已合并

---

## 背景

后端已实现错误触发的主动探测（commit 2a5604651），`admin/live_stream_sse.go` 的 `LiveRequest` 增加了三个字段：
- `IsProbe bool` — 是否为探测请求
- `ProbeOrigin string` — 探测来源（direct/gateway/scheduled）
- `ProbeAttempt int` — 第几轮探测

前端需要在实时请求流 dashboard 中：
1. 渲染探测请求的视觉标识（🛡️ 盾牌图标） ✅ 已完成（commit bf354e078）
2. 提供"仅探测"过滤器快速筛选 ✅ 已完成（commit 3e77a40ee）
3. 详情面板展示探测元数据 ✅ 已完成（commit 3e77a40ee）

---

## ✅ 已完成

### commit bf354e078 — 探测可视化 + 诊断按钮优化
- `RequestTile.vue` 色块第二行显示：`🛡️ DIRECT#1` / `🛡️ GW#2` / `🛡️ SCHED#3`
- `tooltipText` 优先显示探测元数据（来源 + 轮次）
- 诊断按钮优化：失败率 > 1/3 → 显示，连续 4 次成功 → 隐藏

### commit 3e77a40ee — 探测器 + 详情面板
- `LiveRequestStreamV2.vue` 增加「全部」「🛡️ 仅探测」filter buttons
- `filteredLanes` computed 过滤每个泳道的 requests
- `RequestLogDrawer.vue` 增加 probe-info 徽章（🛡️ + 来源 + 轮次）
- `logs.ts` RequestLogRow 增加 5 个探测元数据字段类型

---

## 验收标准（全部 ✅）

1. ✅ 实时流中探测请求带蓝色 🛡️ 盾牌图标
2. ✅ "仅探测" filter tab 正常工作
3. ✅ 详情面板展示探测来源和轮次
4. ✅ 无 TypeScript 类型错误（相关组件）
5. ✅ 无 console 警告/错误

---

## 相关文档

- 后端 commit: `2a5604651` (feat: 错误触发的主动探测)
- 前端 commit: `bf354e078` (feat: 探测请求可视化 + 诊断按钮优化)
- 前端 commit: `3e77a40ee` (feat: 探测过滤器 + 详情面板元数据展示)
- 设计文档: `docs/自检功能/04-error-triggered-probe-design.md`
- Changelog: `docs/changelogs/2026-07-13-error-triggered-probe.md`