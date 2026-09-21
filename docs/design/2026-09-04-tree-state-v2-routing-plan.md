# Tree-state 投影与 Sessions V2 路由契约统一 — 设计 Plan（2026-09-04）

**状态**：plan（未动代码）。目标引用 2026-09-02 handoff §5.6：`/api/admin/sessions/{id}/turns` 存在 tree/V2 历史契约并存，metadata tree 保持不返回正文；统一路由需单独设计迁移，先做 tree-state 投影与 V2 routing 契约。

## 1. 现状事实（调研结论，含行号）

### 路由遮蔽（最重要的结构性事实）
- `handler.go:1140` 精确注册 `mux.HandleFunc("/api/admin/sessions/{id}/turns", handleSessionTurnsTree)`，优先于 `:1080` 的子树 pattern `/api/admin/sessions/`。
- 因此 **GET `/turns` 实际由 tree 契约响应**；subrouter 里的 `case "turns"` → V2 `serveSessionTurnsList`（session_turns_v2.go:73）**HTTP 层不可达（死路由）**。该行为被 `session_turns_tree_test.go:237-258` 显式钉死。
- `/turns/{n}`、`/turns/bodies`、`/snapshot`、`/instant-summary` 走 subrouter → V2（活）。

### 两套契约的形状差异
| | tree（现役） | V2（被遮蔽） |
|---|---|---|
| 数据源 | `request_logs_with_current_month`（读时投影，无物化） | `session_turns` ⨝ `session_bodies` |
| turn 编号 | `ROW_NUMBER() OVER (ORDER BY ts, request_id)` 派生 | `session_turns.turn_no`（写时定序） |
| 正文 | **绝不返回**（metadata-only，文件头硬约束） | 返回 request/response delta + digest |
| 子请求 | `child_requests[]`（title/summary/sensitive_word/compression 分类） | 无对应物（只有 `parent_request_id` 过滤维度） |
| cursor | `turns|sid|tn|rid|mac` HMAC 独立格式 | JSON `cursorPayload` + HMAC |

### 已知偏差与孤儿
- **turn 编号双源不一致**：tree 派生 turn_number ≠ session_turns.turn_no（session_turns_tree.go:18-21 TODO 挂账）。SessionDetailPage 的 digest 流（tree turnNumber → TurnDigestDrawer → V2 `/turns/{n}`）已踩在此不一致上。
- `session_turn_snapshots`（migration 385，original/compressed/secured 三阶段快照 + TTL）：**孤儿表，全仓 Go 零读写**——tree-state 的物化设计从未接线。
- `/turns/{n}/attachments/{att}/url` 是 404 stub，但 `TurnDigestDrawer.vue:58` 已在调用。
- 无任何 HTTP 版本协商机制（无 Accept-Version / ?v= / /v2/ 前缀）；写侧 flag（sessions_v2.*）不控制 admin 路由。

### tree 契约消费方（迁移影响面）
1. `SessionTurnsTimeline.vue`（本体）→ `SessionDetailPage.vue`（生产会话详情页轮次列表）+ `DashboardViewV2.vue` → `SessionDrilldownPanel.vue`
2. `sessionDetailExport.ts`（导出：tree 拿骨架 + request_logs 拼正文——正文需求已旁路满足）
3. 前端 `listSessionTurns`（sessions_v2.ts，V2 列表 client）只剩测试引用

## 2. 设计决策

### D1：目标契约 = "V2 列表 + tree 投影作为 V2 响应的扩展字段"
不保留两个端点。统一后的 `GET /api/admin/sessions/{id}/turns` 返回 V2 形状（turn_no/ts/tokens/digest/cursor），并**内联 tree 投影**：

```
turns: [{ turn_no, ts, request_id, title, summary, digest, tokens...,
          child_requests: [{request_id, request_type, status, latency}] }]   // ← tree 投影内联
```

理由：child_requests（后台子任务可见性）是 tree 契约唯一不可替代的价值；V2 形状是唯一有 durable 排序（turn_no）的一方；正文继续由 `/turns/{n}`、`/turns/bodies` 按需取，**统一后的列表仍是 metadata-only**——保住"tree 不返回正文"的安全约束。

### D2：tree-state 投影 = 读时 JOIN，不物化新表
- child_requests 由 `request_logs_with_current_month` 按 `(tenant_id, parent_request_id)` 批量关联（复用 session_turns_tree.go 现有查询的子请求部分），关联键改用 V2 行的 `request_id`。
- **不接线 `session_turn_snapshots`**：其设计意图（三阶段快照审计）与本契约统一正交，且 session_bodies 的 delta 已覆盖正文需求。孤儿表处置（drop or 接线）另开独立决策，不在本 plan 范围。
- turn 编号以 `session_turns.turn_no` 为唯一真相；tree 的 ROW_NUMBER 派生编号废弃（消除双源不一致，TODO 销账）。

### D3：路由迁移 = 三阶段灰度（flag 驱动，不改前端 URL）
无 HTTP 版本协商 → 引入**服务端读 flag**（settings 热加载，与 sessions_v2.* 同机制）：
- `sessions_v2.turns_list_routing = tree | dual | v2`（默认 `tree`）
- `dual` 模式：tree 响应里附加 `v2_shadow: {turn_no, digest_available}` 供前端对账；V2 handler 挂在 `GET /turns?source=v2`（仅 dual 模式注册）供验证。
- 切换实现：handler.go:1140 的精确注册改为按 flag 分派到 tree 或新统一 handler；`session_turns_tree_test.go` 的路由特异性测试同步改写。
- 前端 `sessionTurnsTree.ts` 的消费组件逐步换到统一形状（`SessionTurnsTimeline.vue` 第一批；`sessionDetailExport.ts` 第二批）。

### D4：数据完备性兜底（未开 V2 写的环境）
V2 列表对"sessions_v2 未产出数据的会话"返回空 → 统一 handler 需**空结果时自动回退 tree 投影**（同 session_turns 查无行时走 request_logs 派生：ROW_NUMBER 桥接 turn_no 用 `(session_id, ts)` 对齐），并在响应带 `source: "v2" | "tree_fallback"`。这是灰度期间不断详情页的关键保险。

### D5：cursor 统一
统一契约只保留 V2 JSON cursor；tree 旧 cursor 在 fallback 路径中**不透传**（fallback 只服务第一页 + "重新以 tree 游标查询"由后端内部消化）。前端"加载更多"在 dual/v2 模式下用新 cursor。

## 3. 分步实施

| 步骤 | 内容 | 风险 |
|---|---|---|
| T1 | 统一 handler `serveSessionTurnsUnified`：V2 列表 + child_requests 内联投影 + 空/未启用回退（D1/D2/D4） | 中：SQL 双源 JOIN，需集成测试 |
| T2 | `sessions_v2.turns_list_routing` flag + 路由分派改造 + 路由特异性测试改写（D3） | 低 |
| T3 | dual 模式对账字段 + `?source=v2` 验证入口（D3） | 低 |
| T4 | 前端 `SessionTurnsTimeline.vue` 切统一形状（feature flag `VITE_SESSION_TURNS_UNIFIED`） | 中：组件重写数据层 |
| T5 | `sessionDetailExport.ts` 与 DrilldownPanel 跟进；删除死路由 `case "turns"` 分派；`session_turns_v2.go` 头注释修正 | 低 |
| T6 | （顺手）`/turns/{n}/attachments/{att}/url` 404 stub：要么实现签名 URL，要么前端移除调用——独立小决策 | 低 |

每步独立可合并；T1–T3 纯后端（pgxmock 集成测试沿用 sessionTurnsDB 缝），T4–T5 前端。

## 4. 明确不做

- 不动 `/api/admin/turns*` 家族（已是 V2）。
- 不做 HTTP Accept-Version 协商（flag 足够，URL 不变是硬约束）。
- 不在本 plan 内处置 `session_turn_snapshots` 孤儿表。
- 不收敛导出旁路（request_logs 拼正文）到 turns/bodies——导出走特权读，正文来源切换收益低。

## 5. 验收标准

1. flag=tree：行为与现状逐字节一致（现有 tree 测试全过，零回归）。
2. flag=dual：tree 响应带 v2_shadow 对账字段；`?source=v2` 返回统一形状 + child_requests。
3. flag=v2：`GET /turns` 返回统一形状；未开 V2 写的环境自动 tree_fallback 且 `source` 标注；列表不含正文。
4. turn_no 与 `/turns/{n}` 详情的 turn_no 一致（双源不一致 TODO 销账）。
5. pgxmock 集成测试覆盖：统一投影 JOIN、fallback 触发、cursor 循环、租户隔离（沿用 WithArgs 精确匹配模式）。
