# liveStreamPreferences debounced persistence (LP7)

- 日期：2026-08-24
- 类型：perf（行为保持型优化）
- 计划文档：`docs/04-implementation/plan/2026-08-24-request-body-storage-optimization-plan.md` (LP7)
- 阶段：llm-gateway 内存优化 第二阶段 — 前端 dashboard 偏好持久化

## 背景

`liveStreamPreferences` 是 dashboard `/live-stream` 页面偏好（filters / queue status / mode / groupBy / selectedLegends）的入口。每次 filter chip toggle、status filter apply、queue depth toggle 都立即同步写 `localStorage`，并伴随一次伴生的 legacy swimlane mode 写入（2 次 `setItem`）。在用户连续操作（开 5 个 filter chip）场景下产生 10 次同步 JSON 序列化 + 主线程 IO。

LP6 已为 `useChatSessions` 提供 debounce 模板（300ms coalesce + snapshot short-circuit + lifecycle flush + scope dispose + try/catch 错误降级），LP7 把同一模式应用到 dashboard 偏好，统一前端持久化风格。

## 变更内容

### `web/src/composables/liveStreamPreferences.ts`

1. **写入防抖 (300ms)**：所有 `writeLiveStreamPreferences` 调用改为 `schedulePersist()`，在 300ms 窗口内合并为一次 `flushPersist()`。
2. **Snapshot short-circuit**：`flushPersist()` 对比 `JSON.stringify(preferences)` 与 `lastPersistedSnapshot`；相同内容跳过写 IO，幂等写零成本。
4. **生命周期 flush**：注册 `visibilitychange`（hidden）、`pagehide`、`beforeunload` 监听器；用户在隐藏或关闭 tab 时立即同步 flush，避免丢状态。
5. **scope 自动清理**：模块顶层 `try { onScopeDispose(...) } catch {}` — catch 兼容 module-load 无 scope 场景；listener 在 page lifetime 内有效，scope 销毁时不强制清理（对齐 LP6 注释：listener 泄漏 bounded by page lifetime）。
6. **错误降级**：`persistLiveStreamPreferencesImmediate` 内部对 `localStorage.setItem` 包 `try/catch`，配额满或隐私模式不抛；in-memory state 保留，下次读仍返回最新值。
7. **In-memory cache key-tracked**：`currentPreferences` + `currentPreferencesKey` 双字段；切 user/tenant 时 cache 自动失效；`readLiveStreamPreferences` 检测到 `pendingKey !== key` 时 flush 旧用户待写，避免新用户写覆盖旧用户 pending。
8. **Test-only hook**：导出 `_resetPersistState()`（`_` 前缀标志非生产 API），测试 beforeEach 重置模块状态。

### 测试

- `web/src/composables/liveStreamPreferences.test.ts` — 重写为 13 测试：
  - 同步行为（默认值 / legacy 迁移 / 无效 mode / 用户隔离 / 字段合并）
  - LP7 debounce：5 次连写合并为 2 次 setItem、`flushPersist()` 立即落盘、setItem 抛错不挂、幂等写短路、beforeunload / pagehide / visibilitychange flush、multi-write timer 重置不早 flush。
- `web/src/composables/useLiveStreamFilters.test.ts` / `useSwimLane.test.ts` / `QueuePerspectivePanel.test.ts` — `beforeEach` 加 `_resetPersistState()`；断言读 storage 前 `flushPersist()`。

## 不变更

- 写入内容 schema 完全保持；既有 `liveStreamPreferencesStorageKey()` / `liveStreamPreferences` 字段签名不变
- 既有 `useLiveStreamFilters` / `useSwimLane` / `QueuePerspectivePanel` 生产代码不修改（已通过 `writeLiveStreamPreferences` 间接获益）

## 性能影响

- 5 次连写（典型场景：开 5 个 filter chip）：10 次 setItem → 2 次 setItem（-80%）
- 重复状态写（filter toggle 撤销等）：setItem → 0（snapshot 短路）
- 用户切 tab / 锁屏：lifecycle flush 立即落盘，不丢状态

## 验证

- `pnpm vitest run`：536 tests passed（13 LP7 新增 + 18 useLiveStreamFilters + 1 useSwimLane + 24 QueuePerspectivePanel + 其他既有）
- `pnpm vue-tsc --noEmit`：0 errors
- `go build ./...`：clean
- 字符数 / 行数：`liveStreamPreferences.ts` 300 lines / 11930 chars（恰好满足 rule 43 ≤ 300 行 / ≤ 12000 字符）