# 2026-08-24 — `usePersistedValue<T>` composable + 6 个 localStorage 入口统一

> **LP8 (2026-08-24)** — 前端 storage 持久化层统一，闭环 LP6 / LP7 模式到单值偏好场景。

## 背景

前端的 `localStorage.setItem / getItem` 调用分散在 N 处，各自实现持久化逻辑：

| 位置 | Key | 频率 | 现状缺陷 |
|------|-----|------|---------|
| `useChatCompletions.ts` | `llmgw_device_seed` | 每次 chat 请求 | 无 debounce / 无生命周期 flush |
| `useDashboardBoard.ts` | `llmgw_dashboard:board-range:*` | 每次 board 日期切换 | 无 debounce；silent fail on quota |
| `liveStreamStore.ts` | `llmgw_sse_endpoint` | 管理员手动设 | bare try/catch |
| `appNav.ts` | `llmgw_sidebar_collapsed` | sidebar toggle | bare try/catch |
| `i18n.ts` | `llmgw_locale` | 语言切换 | direct setItem |

每处都各自实现 try/catch + localStorage 同步写入，缺乏统一的：

1. **300ms debounce** —— burst mutation 合并为单次写入（LP6 / LP7 模式）
2. **lifecycle flush** —— `visibilitychange(hidden)` / `pagehide` / `beforeunload` 触发 flush
3. **snapshot short-circuit** —— 重复写入跳过 IO
4. **scope-aware cleanup** —— setup() 内调用时自动清理监听器；module-level 调用走 page lifetime
5. **统一错误降级** —— in-memory ref 始终权威，下次 mutation 重试

## 实施

### 新增 `web/src/composables/usePersistedValue.ts`

```typescript
export function usePersistedValue<T>(
  key: string,
  factory: () => T,
  opts?: PersistedValueOptions<T>,
): PersistedValueHandle<T>  // { value: Ref<T>, flush(): boolean, remove(): boolean }
```

**Options：**
- `serialize / deserialize` —— 默认 `JSON.stringify / JSON.parse`；可定制 `('0' | '1' / 'en' / 'zh-CN')`
- `immediate: true` —— 每次 mutation 同步写入（低频单写场景：sidebar collapse / locale / endpoint URL）
- `debounceMs` —— 默认 300ms

**返回 handle：**
- `value: Ref<T>` —— 立即从 localStorage 读取的 ref，in-memory 始终权威
- `flush(): boolean` —— 强制同步写入（立即触发，未到 debounce 也会清 timer + 写）
- `remove(): boolean` —— 清 key + 重置为 factory() 默认值

### 改造点（6 个）

| 文件 | Key | 类型 | opts |
|------|-----|------|------|
| `useChatCompletions.ts` | `llmgw_device_seed` | `string` (UUID) | 默认 JSON（debounce）|
| `useDashboardBoard.ts` | `llmgw_dashboard:board-range:*` | 对象 | 默认 JSON（debounce）|
| `liveStreamStore.ts` | `llmgw_sse_endpoint` | `string` (URL) | `immediate: true`, 裸字符串 |
| `appNav.ts` | `llmgw_sidebar_collapsed` | `boolean` | `immediate: true`, `'0'\|'1'` |
| `i18n.ts` | `llmgw_locale` | `string` | `immediate: true`, 裸字符串 |
| `useLiveStreamUrl.ts` | （继承 liveStreamStore） | — | — |

### 测试 (`usePersistedValue.test.ts`)

11 个测试 case，覆盖：
- factory fallback / 错误降级 / JSON round-trip
- burst mutation → 单次 setItem（debounce 300ms）
- `flush()` 同步写入
- `remove()` 清 key + 重置 ref
- immediate 模式绕过 debounce
- custom serialize/deserialize ('0'/'1' encoding)
- `pagehide` 监听器触发 flush
- module-init 调用（无 scope）走 page lifetime

## 验证

```
✓ pnpm vue-tsc --noEmit          (0 errors)
✓ pnpm vitest run src/composables/   (219/219 passed, +11 new)
✓ pnpm build                     (no warnings introduced)
✓ bash verify.sh --web           (PASS)
```

**端到端 (browser-use 实测)：**

1. 5/5 LP8 改造点的 localStorage 写入正确：
   - `llmgw_locale` = `zh-CN`
   - `llmgw_sidebar_collapsed` = `0`
   - `llmgw_sse_endpoint` = `""`
   - `llmgw_dashboard:board-range:legacy:default` = `{"preset":"today","days":1}`
   - `llmgw_device_seed` = null (chat activity 未触发，符合预期)

2. i18n locale 端到端：
   - `localStorage.setItem('llmgw_locale', 'en')` → `window.location.reload()`
   - `document.documentElement.lang === 'en'` ✓
   - 证明：`detectInitialLocale()` 从 localStorage 读到 'en' → `applyDocumentLocale('en')` 应用

## 设计权衡

**1. module-init vs setup() 调用**

`usePersistedValue` 同时支持 module-level（i18n.ts 在 import 时即调用）和 setup() 内调用（composable consumers）。差异：

- **module-level**：`getCurrentScope()` 返回 `undefined` → 跳过 `onScopeDispose`；监听器挂在 `window`，由 page unload 清理
- **setup() 内**：`onScopeDispose` 触发 `flush()` + `removeEventListener` 自动清理

代价：module-level 多挂 3 个监听器直到 page 关闭。代价忽略（监听器廉价，page lifetime 是实际 lifetime），换来调用灵活性。

**2. `immediate: true` vs 默认 debounce**

i18n.ts / appNav.ts / liveStreamStore.ts 都用 `immediate: true`：

- 语言切换 5 秒一次，不需要合并 burst
- sidebar collapse 是布尔 toggle，无 burst
- SSE endpoint 由管理员手动设，一次操作一次写入

useChatCompletions / useDashboardBoard 用默认 debounce：

- chat 请求可能连续触发 device seed 写入（很少，但 burst 可合并）
- dashboard board 切换可能用户连续点几次 date preset

**3. 序列化默认 JSON**

默认 `JSON.stringify` 统一所有对象的序列化。但 i18n locale 和 SSE endpoint 用 `serialize: (v) => v` 保留裸字符串，与旧实现兼容（避免破坏现有 localStorage 键格式）。

## 不变量

- **单一真相源**：localStorage 与 in-memory ref 是同一份数据的两个 view，in-memory 永远权威
- **snapshot short-circuit**：`lastPersistedSnapshot` 记忆上次写入内容，相等则跳过 setItem
- **error degradation**：`try/catch` 失败时 in-memory ref 不变，下次 mutation 重试，不抛错给业务调用方
- **lifecycle flush**：浏览器隐藏 / 关闭 tab 之前一定把 pending writes 落盘

## 影响范围

- **新文件**：`web/src/composables/usePersistedValue.ts`（254 行），`usePersistedValue.test.ts`（~280 行）
- **修改文件**：6 个 composable / config 文件
- **无新增依赖**
- **无 breaking change**：所有 localStorage key 保持原值原格式

## 后续

- LP9（下一批）：将 `useChatSessions` (LP6) / `liveStreamPreferences` (LP7) 也接入 `usePersistedValue` 进一步消除模式重复。当前它们已有自包含 debounce + lifecycle flush，是迁移候选但非强制。