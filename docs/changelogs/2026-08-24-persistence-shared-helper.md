# 2026-08-24 — `persistenceShared.ts` 共享 debounce + lifecycle flush 原语 (LP9)

> **LP9 (2026-08-24)** — 把 LP6 / LP7 / LP8 三处独立的 debounce / lifecycle / cleanup
> 代码合并到 `web/src/composables/persistenceShared.ts`，三个调用方共享同一份实现。

## 背景

LP6 (2026-08-24)、LP7 (2026-08-24)、LP8 (2026-08-24) 在不同时间点分别引入相同的持久化
原语栈：

| 模块 | 引入时间 | 持久化对象 |
|------|---------|-----------|
| `useChatSessions.ts` (LP6) | 2026-08-24 | 全树 `ChatSession[]` |
| `liveStreamPreferences.ts` (LP7) | 2026-08-24 | dashboard `/live-stream` 偏好 |
| `usePersistedValue.ts` (LP8) | 2026-08-24 | 6 个单值偏好 (locale / sidebar / device seed / ...) |

三处实现各自包含：
- `setTimeout` debounce 计时器 + 清理逻辑
- `lastPersistedSnapshot` 字符串缓存 + short-circuit 判断
- `visibilitychange` / `pagehide` / `beforeunload` 三个 listener
- `onScopeDispose` 清理（部分 module-level 调用方降级为 page-lifetime listener）
- try/catch 错误降级

代码审计发现三处实现存在以下细微差异：
- listener 名称 (`handleVisibilityFlush` vs `handleLifecycleFlush`)
- short-circuit 命中条件 (`payload === lastPersistedSnapshot` vs `snapshot === lastPersistedSnapshot`)
- immediate-mode 默认值（LP8 通过 `immediate: true` 显式支持，LP6/LP7 默认 debounce）
- 暴露给调用方的 alias (`api.flushPersist` vs `module.flushPersist`)

差异虽小但增加了审计面：每个新持久化入口都可能重新踩同样的坑（漏 listener、忘
short-circuit 缓存清理、scope dispose 错误使用 try/catch 绕过 dev-only warning）。

LP9 把这套原语抽到 `persistenceShared.ts`，三个调用方 + 未来新增的持久化入口共享
一份实现：

```typescript
// persistenceShared.ts (LP9)
export const PERSIST_DEBOUNCE_MS = 300
export const LIFECYCLE_FLUSH_EVENTS = ['visibilitychange', 'pagehide', 'beforeunload'] as const
export type WriteCallback = () => boolean | void

export function installLifecycleFlush(flush: WriteCallback): () => void
export function installScopeCleanup(flush: WriteCallback, removeListeners: () => void): void
export function createDebouncedFlush<T>(opts: CreateDebouncedFlushOptions<T>): DebouncedFlushHandle
```

## 变更内容

### `web/src/composables/persistenceShared.ts` (新增)

三个导出：

1. **`installLifecycleFlush(flush)`** — 注册三个 lifecycle listener，返回 cleanup
   函数。`visibilitychange` 仅在 `document.visibilityState === 'hidden'` 时触发，
   `pagehide` / `beforeunload` 始终触发。SSR 安全（`typeof window === 'undefined'` 早返）。

2. **`installScopeCleanup(flush, removeListeners)`** — Vue effect scope 销毁时
   flush + 移除 listener。模块级调用方（`getCurrentScope()` 为 null）被自动跳过，
   无 dev-only warning（与 LP6 原 `try { onScopeDispose } catch {}` 行为一致）。

3. **`createDebouncedFlush<T>(opts)`** — 完整 debounce + snapshot short-circuit 流水线：
   - `getSnapshot()` 返回 `null` → 跳过本次 write
   - `serialize()` 抛错 → 返回 `false`，保留 `lastPersistedSnapshot`
   - `write()` 返回 `false` → 不更新 cache，返回 `false`
   - `write()` 抛错 → 自动 catch 返回 `false`
   - `cancel()` 同时清 timer + short-circuit cache（module-level test isolation 必需）
   - `immediate: true` 跳过 timer，每次 `schedule()` 同步 flush

### `web/src/composables/persistenceShared.test.ts` (新增)

16 个单测覆盖：
- `PERSIST_DEBOUNCE_MS` 常量值
- `createDebouncedFlush`: 5 次 burst 合并为 1 次写入
- `createDebouncedFlush`: `flush()` 同步落盘并取消 timer
- `createDebouncedFlush`: `serialize()` 抛错返回 `false`
- `createDebouncedFlush`: `write()` 返回 `false` 返回 `false`
- `createDebouncedFlush`: `write()` 抛错自动 catch
- `createDebouncedFlush`: snapshot short-circuit 跳过幂等写
- `createDebouncedFlush`: `cancel()` 清 timer + cache
- `createDebouncedFlush`: `getSnapshot()` 返回 `null` 跳过 write
- `createDebouncedFlush`: `immediate: true` 同步写
- `installLifecycleFlush`: `pagehide` / `beforeunload` 始终 flush
- `installLifecycleFlush`: `visibilitychange` (hidden) flush
- `installLifecycleFlush`: `visibilitychange` (visible) 不 flush
- `installLifecycleFlush`: `remove()` 移除所有 listener
- `installScopeCleanup`: effectScope.stop() 触发 flush + remove
- `installScopeCleanup`: scope 外调用为 no-op

### `web/src/composables/usePersistedValue.ts`

移除内联的：
- `Windowish` / `Documentish` 类型本地别名
- `handleVisibilityFlush` / `handlePageHide` / `handleBeforeUnload` 三个 listener
- 直接的 `onScopeDispose` 调用 + `getCurrentScope()` 检查
- 手动 `lastPersistedSnapshot` / `persistTimer` 状态

替换为 helper 调用：
```typescript
const persist = createDebouncedFlush<T>({...})
const removeListeners = installLifecycleFlush(flush)
installScopeCleanup(flush, removeListeners)
```

外部 API (`PersistedValueHandle<T>` + `usePersistedValue<T>(key, factory, opts)`) 零改动。

### `web/src/composables/useChatSessions.ts`

删除 80+ 行持久化原语（计时器 + listener + scope dispose + `lastPersistedSnapshot`）。
本地 `persist()` / `flushPersist()` alias 保留以最小化调用方 diff。

### `web/src/composables/liveStreamPreferences.ts`

删除 60+ 行持久化原语。`flushPersist()` / `schedulePersist()` / `_resetPersistState()`
保留，`_resetPersistState()` 改为调 `persistHandle.cancel()`（helper 的 `cancel()` 现在
同时清 short-circuit cache，解决 test isolation 问题）。

## 兼容性

- **外部 API**: 三个 composable 的导出形状零变化（`usePersistedValue` / `useChatSessions`
  / `liveStreamPreferences` 的对外契约不变）
- **行为**: debounce window、short-circuit、lifecycle flush 时机完全对齐（已通过 50 个
  原测试 + 16 个新测试验证）
- **错误降级语义**: 保留 `localStorage.setItem` 抛错时静默失败、内存态为真相、下次
  flush 重试的行为
- **Module-level 调用方**: `liveStreamPreferences` 在 module init 时调用 helper，
  `installScopeCleanup` 自动 no-op，listener 跟随 page lifetime（与 LP6/LP7 原行为一致）

## 验证

- `pnpm vue-tsc --noEmit`: 0 errors
- `pnpm vitest run`: 553/555 pass（2 个失败为 baseline `i18n/parity.test.ts` 缺失
  `tabErrorDetail` 翻译 key，与 LP9 无关，已确认 baseline 同样失败）
- `pnpm build`: success
- LP6/LP7/LP8 原 9/13/12 测试全过；LP9 新增 16 测试全过；总计 50/50 共享持久化测试通过

## 后续

LP9 是收敛点不是终点。如果未来出现：
- 多个 storage 后端（sessionStorage / IndexedDB）：在 `createDebouncedFlush` 之上加一层
  backend adapter，不动 helper
- 跨 tab 同步（`storage` event 触发 flush）：扩展 `installLifecycleFlush` 加 `'storage'`
- Server-side persistence（cookies / server actions）：helper 已 SSR-safe，加 backend
  参数即可
