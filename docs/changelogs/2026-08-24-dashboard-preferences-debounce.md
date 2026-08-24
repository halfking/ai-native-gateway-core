# 2026-08-24 — dashboard 偏好 debounce 持久化 (LP7)

## 背景

dashboard `/live-stream` 实时偏好（groupBy / swimlane mode / 多维过滤器 / 队列透视面板）
每次 `writeLiveStreamPreferences()` 都同步触发 `localStorage.setItem`。高频变更（拖动泳道、开关过滤器）
会瞬时堆积 JSON 序列化 + localStorage IO，浏览器主线程延迟 20-50ms/次。

LP6 `useChatSessions` 已验证 debounce pattern，LP7 复用同模板。

## 改动

### `web/src/composables/liveStreamPreferences.ts`
- **In-memory cache 是真相源** — `currentPreferences` + `currentPreferencesKey` 双字段，user 切换时 cache 自动失效
- **300ms debounce** — `schedulePersist` + `flushPersist` 复用 LP6 模板（snapshot short-circuit）
- **Lifecycle flush 三重保险** — `visibilitychange`（hidden）+ `pagehide` + `beforeunload`
- **try/catch 错误降级** — `setItem` 抛错（隐私模式 / QuotaExceededError）时不阻断 UI，in-memory 仍保留最新值
- **模块级 `onScopeDispose` cleanup** — `try` catch 兜底（模块加载时无 active scope），listener 持久到 page unload
- **`_resetPersistState()` test-only hook** — 重置模块级 debounce 状态，便于 test 隔离

### 测试
- `liveStreamPreferences.test.ts` — 13 测试覆盖 debounce / snapshot short-circuit / 错误降级 / lifecycle flush / timer reset
- `useLiveStreamFilters.test.ts` / `useSwimLane.test.ts` / `QueuePerspectivePanel.test.ts` — beforeEach 调 `_resetPersistState()`；consumer 测试断言前调 `flushPersist()`

## 验证

- `pnpm vitest run`: 536 tests passed (79 files)
- `pnpm vue-tsc --noEmit`: 0 errors
- `go build ./...`: clean
- `wc -l`: 300 行（满足 rule 43 ≤ 300 行约束）

## 风险

- 模块级 `onScopeDispose` 注册失败（catch 静默）— listeners 持续到 page unload。生产环境可接受（listener 只 3 个，page 关闭时 GC 自动清理）
- consumer 测试需要 `_resetPersistState` + `flushPersist` 才能跑通 — 已修复 3 个测试文件

## 回滚

`git revert <commit>` 即可，所有改动都集中在 `liveStreamPreferences.ts` + 测试。