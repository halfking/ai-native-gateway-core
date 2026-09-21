# 请求详情全屏化 + 异步/路由全流程 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将统一请求/会话详情升级为全屏路由页，分区异步加载，并把调度瀑布（T0–T9）与 Attempts 对齐 `DispatchWaterfallDetail` SSOT，禁止打开时串行拉大 body / 全会话 compare。

**Architecture:** 全屏页是壳（路由 + 左导航 + 顶栏）；现有 `detail/*` 分区组件复用；瀑布 UI 复用 `waterfallTimeline` + `DispatchWaterfallTrack`（抽共享子块，不重造）；`useRequestDetailLoader` 分阶段并行 + AbortController；后端补单请求 waterfall 查询与 session-compare 的 `request_id` 过滤。

**Tech Stack:** Vue 3 + TypeScript（web）、Go admin/domains（unified + dispatch waterfall + session-compare）、现有 `GET /api/admin/request-detail` / logs / dispatch APIs。

**Baseline:** `main` @ `eb7878684`（handoff 后已 pull）。工作区另有无关 migration 改动（580/578），**本 feature 分支开工前 stash，勿混入**。

---

## 0. 确认项（实施前锁定）

| # | 决策点 | 建议默认 | 备选 |
|---|---|---|---|
| C1 | 路由路径 | `/request-detail/:requestId`（auth 同 request-logs） | `/admin/request-detail/:id`（requiresSuper） |
| C2 | 抽屉去留 | 保留 `UnifiedRequestSessionDrawer` / `RequestLogDrawer` 作轻量嵌套入口；列表「打开详情」默认进全屏 | 抽屉仅嵌套，全屏为主 |
| C3 | 左导航分区 | 见 §1.2（8 区） | 合并「调度瀑布」+「路由与重试」为一区 |
| C4 | 瀑布数据源 | **新增** `GET /api/admin/dispatch/waterfall/request/{id}`（memory ring → DB merge，与列表同源） | 仅扩 unified meta（字段重复风险） |
| C5 | Attempts 展示 | Waterfall `attempts` 为主 SSOT；logs `routing_attempts` 作兜底/对照折叠 | 两套并列表格 |
| C6 | compare | 后端加 `?request_id=`；前端默认带当前 request | 仅前端 filter（大会话仍卡） |
| C7 | Journey 页 | 保留独立；全屏「流程 Trace」内嵌现有 trace，不吞掉 Journey | 全屏链到 Journey |

**建议全部采用「建议默认」；老板回复「确认」或逐条改 C1–C7 后开工。**

---

## 1. 信息架构（Fullscreen Shell）

### 1.1 顶栏

- 身份：`request_id` · session · status · `source`/`persistence` · result
- 模式切换：`单请求` | `会话轮次`（同现有 `DetailMode`）
- 动作：复制 ID、打开会话页、关闭（`router.back()` 或回 `/request-logs`）

### 1.2 左导航（固定分区）

| key | 标签 | 默认挂载数据 | 说明 |
|---|---|---|---|
| `overview` | 概览 | meta（omit_body） | 补齐 vendor/cred/tokens/cost 等 |
| `chat` | 对话 | bodies（二次） | 现有 `ConversationMessagesPanel` |
| `waterfall` | 调度瀑布 | waterfall-by-id | T0–T9 条 + 阶段表（复用 SSOT） |
| `attempts` | 路由与重试 | waterfall.attempts（缺则 logs.routing_attempts 映射） | 对齐 waterfall Attempts UI |
| `flow` | 流程 Trace | `GET …/trace`（可 `summary=1` 二期） | 现有 `FlowTimingPanel` |
| `compress` | 压缩脱敏 | session-compare **带 request_id** | 现有三栏 |
| `attachments` | 附件 | meta 计数 + 按需列表 | 一期可只显示计数/占位 |
| `raw` | 原始 JSON | 已缓存 payload | 懒渲染 |

会话模式：左时间线（`SessionTurnsSyncPane`）+ 右上述分面；点 turn **只换 `request_id`**，壳不整页销毁。

### 1.3 路由

- 新增：`web/src/views/RequestDetailFullscreenView.vue` + `router.ts` 条目
- 入口改造：`RequestLogsView` / SessionDetail 下钻 / Waterfall「打开详情」→ `router.push({ name: 'request-detail', params: { requestId } })`
- Query：`?tab=waterfall&mode=session-turns` 可选深链

---

## 2. 异步加载时序（硬约束）

```
open(requestId)
  │
  ├─ Phase A (阻塞首屏骨架，目标 < 300ms 感知) ── 并行、可取消
  │    ├─ GET /api/admin/request-detail/{id}?omit_body=1
  │    └─ GET /api/logs/{id}?omit_body=1
  │         → 渲染顶栏 + overview 骨架
  │
  ├─ Phase B (不阻塞 A；按可见 tab 优先) ── 并行
  │    ├─ GET …/waterfall/request/{id}     → waterfall + attempts
  │    ├─ GET …/trace（仅 tab=flow 或 prefetch 空闲）
  │    └─ bodies: unified 无 bodies 时再 GET logs 全量 / unified 不 omit
  │
  └─ Phase C (仅进入对应 tab / 会话模式时)
       ├─ session-compare?session_id=&request_id=   → compress
       └─ sessions/{id}/turns?limit=小               → session-turns
```

**禁止：**

- 打开时 `await` 大 body 再拉 compare / tree
- 默认拉全会话 compare（无 `request_id`）
- 从 `fetchDispatchWaterfall(limit)` 列表里 `find` 当详情数据源

**机制：** `AbortController` + `loadSeq`；切换 request/turn 递增 seq，旧响应丢弃；分区独立 `loading`/`error`（单区失败不拖垮整页）。

---

## 3. API 变更清单

| API | 变更 | 优先级 |
|---|---|---|
| `GET /api/admin/dispatch/waterfall/request/{id}` | **新增**；返回单条 `WaterfallRequest`（ring → DB，与 snapshot 同源字段） | P0 |
| `GET /api/admin/request-detail/{id}` | 可选扩展：`routing_summary` / `attempts_count` / T0–T9 指针（避免 N+1）；一期可只靠 waterfall-by-id | P1 |
| `GET /api/admin/session-compare` | 增加可选 `request_id`；SQL `AND rl.request_id = $n`；响应 turns ≤1 | P0 |
| `GET /api/admin/requests/{id}/trace` | 二期 `summary=1`；一期保持现状、按 tab 懒加载 | P2 |
| `GET /api/logs/{id}?omit_body=1` | 已有；loader 强制 Phase A 使用 | 已有 |
| Unified `omit_body` | 前端 Phase A **必须**传 | 前端修复 |

---

## 4. Waterfall 对齐矩阵（SSOT）

复用（勿重写语义）：

- `web/src/utils/waterfallTimeline.ts`：`WATERFALL_STAGES` / `layoutRows` / `stageDetailRows` / `formatAxisMs`
- `web/src/components/DispatchWaterfallTrack.vue`
- 抽 `RequestWaterfallPanel.vue`（或拆 `DispatchWaterfallDetail` 的 stage+attempts 为共享组件），props：`WaterfallRequest`

| 能力 | Waterfall Detail | 全屏目标 |
|---|---|---|
| T0–T9 条带 | ✓ | ✓ 同组件 |
| 阶段表（缺失「—」禁 0 冒充） | ✓ `stageDetailRows` | ✓ |
| routing_ms 数字行 | ✓ | ✓ |
| Attempts：attempt_no / cred / model / outcome / error_kind | ✓ | ✓ |
| latency 列 | waterfall 无显式 latency；logs RoutingAttempt 有 | attempts 区补 `latency_ms` 若来自 logs 映射 |
| Trace events | ✗（Journey/Flow） | 独立 `flow` 区 |
| Bodies / chat | ✗ | 独立 `chat` |

**适配：** `mapLogRoutingAttemptsToWaterfallAttempts`（仅兜底）；优先 API 返回的 `WaterfallAttempt`。

---

## 5. 文件规划

| 文件 | 职责 |
|---|---|
| Create `web/src/composables/useRequestDetailLoader.ts` | 分区加载、seq、abort、缓存 |
| Create `web/src/views/RequestDetailFullscreenView.vue` | 全屏壳 |
| Create `web/src/components/detail/RequestWaterfallPanel.vue` | 复用 timeline + track + attempts |
| Modify `web/src/components/detail/RequestOverviewPanel.vue` | meta 字段补齐 |
| Modify `web/src/components/detail/CompressionRedactionPanel.vue` | compare 带 `request_id` |
| Modify `web/src/components/detail/UnifiedRequestSessionDrawer.vue` | 改用 loader；打开路径可选跳全屏 |
| Modify `web/src/api/dispatch.ts` | `fetchWaterfallByRequestId` |
| Modify `web/src/api/sessionCompare.ts`（或现有 compare client） | `request_id` query |
| Modify `web/src/router.ts` + 入口跳转 | 全屏路由 |
| Create/Modify `cmd/gateway` + `domains/dispatch` | waterfall-by-id handler |
| Modify `admin/session_compare.go` | `request_id` filter + tests |
| Test：loader unit、waterfall-by-id、compare filter、overview 字段 | TDD |

单文件 ≤ 300 行；大壳拆子组件。

---

## 6. 实施任务拆解

### Task 1: 分支 + stash 无关改动

- [ ] `git stash push -u -m "wip: unrelated migration 580"`（或老板确认保留）
- [ ] `git checkout -b feature/request-detail-fullscreen`
- [ ] `git pull origin main` 已完成确认

### Task 2: 后端 waterfall-by-id（TDD）

- [ ] 写失败测试：未知 id → 404；ring 命中 → 200 `WaterfallRequest`
- [ ] 实现 lookup（先 memory ring，再现有 DB merge 路径）
- [ ] 注册路由 `GET /api/admin/dispatch/waterfall/request/{id}`
- [ ] Commit

### Task 3: session-compare `request_id` 过滤（TDD）

- [ ] 写测试：带 `request_id` 时 SQL/结果只含该 turn
- [ ] 改 `HandleCompare` + SQL
- [ ] Commit

### Task 4: `useRequestDetailLoader`（TDD）

- [ ] 单测：Phase A 并行 omit_body；切换 id 取消旧请求；不默认触发 compare
- [ ] 实现 composable
- [ ] Commit

### Task 5: `RequestWaterfallPanel` + 全屏壳

- [ ] 从 `DispatchWaterfallDetail` 抽共享展示（或 props 嵌入）
- [ ] `RequestDetailFullscreenView` + 路由 + 左导航
- [ ] Overview 字段补齐（vendor/cred/tokens/cost/finish_reason/… 有则显）
- [ ] Commit

### Task 6: 接线入口 + compress/compare

- [ ] 日志/会话/waterfall 打开 → 全屏
- [ ] Compression 传 `request_id`
- [ ] 抽屉保留兼容（可选「在全屏打开」）
- [ ] Commit

### Task 7: 验证 + browser 实测

- [ ] `go test` 相关包；`web` 单测 / typecheck
- [ ] browser-use：打开详情首屏不卡、切 tab 分区 loading、切 turn 不串 body+compare
- [ ] 对照线上 waterfall：`https://llm.kxpms.cn/dispatch/waterfall` 字段一致
- [ ] Commit + push（按用户要求）

---

## 7. 验收标准

1. 打开全屏详情：**首屏只依赖 Phase A**（omit_body meta），Network 无默认全量 body、无无过滤 compare。
2. `waterfall` / `attempts` 区与 `DispatchWaterfallDetail` 同阶段语义（合成/实测标签、缺失非 0）。
3. 切换 turn：旧请求 abort；新 meta 快；body/compare 按 tab 再拉。
4. session-compare 带 `request_id` 时响应 turns ≤ 1。
5. browser 实测有截图/状态证据；verification-before-completion 过关后再宣称完成。

---

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 旧请求不在 ring、DB 无完整 T0–T9 | waterfall 区空态 + 提示；attempts 回退 logs.routing_attempts |
| Journey 与 flow 重复 | 文案区分：flow=events；waterfall=队列阶段 |
| 无关 migration 脏文件 | stash，禁止混 commit |
| 本地无服务导致实测欠账 | 优先打线上 `llm.kxpms.cn`（已登录 browser real）或本地起 gateway |

---

## 9. 非目标（本期不做）

- 重写 Journey 全页
- Trace `summary=1`（记 P2）
- 附件完整浏览器（仅计数/占位即可）
- 推翻双模式（仍默认单请求）
