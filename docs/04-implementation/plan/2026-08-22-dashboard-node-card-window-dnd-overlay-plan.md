# 2026-08-22 — Dashboard 节点卡片迷你窗口 / 拖拽手柄 / 浮层 z-index

分支：`feature/dashboard-node-card-window-dnd-overlay`（跟踪 `origin/main`）

## 背景

承接 `2026-08-20-dashboard-overview-node-optimization-plan.md` 的节点卡片体验缺口，并**纠正 D3 z-index 误判**。

## N1 — batch entries + 卡片迷你窗口

- `POST /api/credentials/sliding-window/batch` 支持 `include_entries` / `entry_limit`
  - 默认 `include_entries=false`（响应不含 `entries`，兼容旧调用）
  - `true` 时 `entry_limit` 默认 24，硬顶 `slidingWindowBatchMaxEntryRet=48`
- 前端 `getSlidingWindowBatch` 兼容旧签名（第二参为 `number` minutes）与 options 对象
- 队列透视卡片底部只读 `.qp-node-window` / `.qp-node-window-cell`（`ok` / `bad`），`CARD_ENTRY_LIMIT=24`

## N2 — 拖拽手柄

- 卡片本体不再 `draggable`（避免点卡片打开详情时误拖）
- 仅 `.qp-node-card-drag-handle` 在 `canReorder` 时 `draggable` + `is-enabled`
- `@click.stop.prevent`；`@dragstart.stop` / `@dragend.stop`；drop 仍在 wrap 上

## N3 — 浮层 z-index（纠正旧 D3）

**旧误判（2026-08-20/21 D3）**：以为 `RequestLogDrawer` 已有 `z-index: 9999`，高于 `NodeDetailDrawer`（3000/3001），「无需再改」。

**事实**：全局 `.drawer-backdrop` 为 `z-index: 100`；RequestLogDrawer 根节点用的就是该 class。文档里提到的 9999/10000 仅属于附件 lightbox，不是抽屉本体。嵌套在 NodeDetailDrawer 内打开时会被盖住。

**修复**：

- `stackLevel?: 'default' | 'nested'`（默认 `default`）
- `nested` → `.drawer-backdrop--nested { z-index: 3200; }`
- `NodeDetailDrawer` 内嵌传 `stack-level="nested"`

## 验证

```bash
go test ./admin/ -run 'SlidingWindowBatch' -count=1
cd web && pnpm exec vitest run \
  src/components/QueuePerspectivePanel.test.ts \
  src/components/RequestLogDrawer.test.ts \
  src/components/NodeDetailDrawer.test.ts
pnpm exec vite build
```

**LOCAL_VERIFIED（2026-08-22）**
- Go `SlidingWindowBatch`：pass
- vitest 上述 3 文件：**40/40** pass
- `vite build`：pass
- **REAL_DEPENDENCY_VERIFIED**：待部署后在 `llmgo`/`llm` 验证嵌套浮层与拖拽
