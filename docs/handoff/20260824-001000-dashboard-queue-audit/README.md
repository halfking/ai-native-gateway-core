# Handoff — Dashboard 队列优化审计（2026-08-24）

**分支**：`main` @ 合并 `fix/model-group-node-priority-sort`  
**生产基线**：build **#1693+**

---

## 审计结论

| 项 | 状态 | 备注 |
|---|---|---|
| 泳道 FIFO 左旧右新 | ✅ 已合 main | `SwimLaneTrack flex-start` + `buildLiveStreamLanes ASC` |
| 按模型分组节点按序号排序 | ✅ 本次修复 | `sortRoutingCandidates` / `orderNodesByRoutingCandidates` |
| resolve priority ★ + credential_label | ✅ 已合 main | cfedb41b8 推广 credential_label |
| PATCH candidate-binding `priority` | ✅ 已合 main | 6edd7bd87 |
| inflight protect env | ✅ 已合 main | d58c7312a |
| 生产 browser 复测（泳道 + 队列 ★） | ⚠️ 待部署后 | 154 build 需 ≥ 本次 merge（1693+） |
| 节点抽屉 priority 开关 UI | ✅ 已合 main | 33c834132 |

---

## 下一迭代（可选）

### P2 — 队列卡片 label fallback

live SSE 无 `credential_label` 时仍显示 `Provider · #N`；可优先用 resolve candidate label **或** `useCredentialLabels` 缓存（`nodeTitle` 已接缓存 fallback，待合）。

### P3 — 154 license restricted mode

`/api/admin/request-logs/top-models` 503 影响模型范围首次加载。

---

## 新会话提示词（复制即用）

```
llm-gateway-go Dashboard 队列 — 2026-08-24 审计后后续。

已完成合 main：
- 泳道 FIFO 左旧右新
- 按模型分组节点按 manual_priority 正序（非 credential_id）
- credential_label 全视图 + PATCH priority

待选：
1. REAL_DEPENDENCY_VERIFIED：154 部署后 browser 复测泳道方向 + 队列 ★ + 节点序号
2. 队列卡片 credential_label fallback（消除 Provider · #N）
3. 节点抽屉 priority 开关 UI

参考：docs/handoff/20260824-001000-dashboard-queue-audit/README.md
```
