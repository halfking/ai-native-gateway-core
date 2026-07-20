# 2026-07-21 — Smart 页图标可见 + 左侧工作类型 DB 同源

## 背景

`/routing-v2?tab=smart` 上：

1. **明细 / 删除按钮空白**：`TierGroupList` 用了 `<el-icon><View/></el-icon>`，
   但 `main.ts` 未注册 Element Plus / 未引入其 CSS，图标 SVG 无尺寸塌成空按钮。
2. **左侧列表错源**：`TaskTypeRail` 走 `useL1TaskTypes()`（L1 八分类），
   而 `work_type_config` 启用约 25 条；`task_default_routing.task_type` 实际存的是
   工作类型 key（如 `code_gen`），不是 L1。

## 变更

- `TierGroupList.vue`：去掉 `el-icon`；直接渲染 `View`/`Delete` 并固定 16px；
  图标按钮收到 `row-main` 右侧，尽量单行。
- 新增 `composables/useWorkTypes.ts`（模块级缓存 + inflight 去重）+ 单测。
- `TaskTypeRail.vue` / `RoutingDefaultsView.vue`：改用 `useWorkTypes()`。
- i18n：rail / needTask / subtitle 文案「任务类型」→「工作类型」。

## 验证

- `pnpm --dir web exec vitest run src/composables/useWorkTypes.test.ts`
