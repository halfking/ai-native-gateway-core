# Handoff — Dashboard 队列优化后续

**日期**：2026-08-23  
**前置**：`ada1b62f` + 审计修复已合入 `main`（selective trim / priority routing / 节点名称）

---

## 已完成（main）

| 项 | 状态 |
|---|---|
| 按处理队列：resolve 序排序 + ★ 优先标志 | ✅ |
| 泳道 FIFO 左→右 + Redis lifecycle-aware 挤出 | ✅ |
| 节点名称（credential_label SSE） | ✅ |
| migration 561 `cmb.priority` | ✅（154 已 repair） |
| 审计 P1：rank 视觉序号、pipeline 错误日志、main 队列测试 | ✅ |

---

## 待办（下一迭代）

### P1 — REAL_DEPENDENCY_VERIFIED（必做）

在 **https://llm.kxpms.cn/dashboard** 实测：

1. 实时流 → **按处理队列**：同模型节点顺序与 `/api/routing/resolve?model=` 一致；优先节点有 ★
2. **按供应商/原厂/模型**：新 tile 从右进入；completed 从左侧被挤出；<2h 的 in_progress 不被挤掉
3. 节点卡片/抽屉标题为 **凭据 label**，非 `#ID`

```bash
# 部署后 smoke
curl -sS -H "Authorization: Bearer $TOKEN" \
  'https://llm.kxpms.cn/api/routing/resolve?model=YOUR_MODEL' | jq '.candidates[] | {credential_id, priority, manual_priority, credential_label}'
```

### P2 — priority 标志写 API

`PATCH /api/routing/candidate-binding/{id}` 增加 `priority?: boolean`，与 `manual_priority` 同级维护。当前只能 SQL 写 `cmb.priority`。

### P2 — 保护窗口环境变量接线

`LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS` → `liveStreamInflightProtectDuration`（`admin/live_stream_queue_trim.go`），与 `RequestSurvivalInteractiveDeadlineSeconds` 文档对齐。

### P3 — NodeStatusMatrix / RequestTile 统一 label

矩阵弹窗、泳道 tile 的凭据名仍走 `useCredentialLabels` 缓存；SSE `credential_label` 已可用，可进一步减少 fallback `#ID`。

---

## 新会话提示词（复制即用）

```
继续 llm-gateway-go Dashboard 队列优化后续（handoff 2026-08-23）。

已完成合 main：
- live stream selective trim（保护 fresh in_progress <2h）
- resolve priority 排序 + 队列卡片 ★ + credential_label
- 审计修复：rank 视觉序号、main 队列 trim 测试

请执行：
1. REAL_DEPENDENCY_VERIFIED：browser-use 打开 https://llm.kxpms.cn/dashboard 实时流，验证按处理队列排序/★/名称，以及泳道 FIFO 挤出行为
2. （可选）PATCH candidate-binding 支持 priority bool
3. （可选）接线 LLM_GATEWAY_LIVE_STREAM_INFLIGHT_PROTECT_SECONDS

参考：
- docs/changelogs/2026-08-23-live-stream-trim-priority-routing.md
- docs/changelogs/2026-08-23-dashboard-queue-audit-fixes.md
- admin/live_stream_queue_trim.go
- web/src/components/QueuePerspectivePanel.vue
```

---

## 验证命令

```bash
go test ./admin/ -run 'SelectiveTrim|Priority|Trim' -count=1
go test ./admin/ -run 'LiveStream' -count=1
cd web && pnpm exec vitest run src/utils/queueNodeCards.test.ts src/components/QueuePerspectivePanel.test.ts
```
