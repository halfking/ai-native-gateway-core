# 2026-08-06 — 抽取 useLiveStreamUrl composable

## 背景

上一轮把过滤器状态抽到 `useLiveStreamFilters` 后，`LiveRequestStreamV2.vue` 还有另一块
可独立的内联逻辑：**SSE endpoint URL 管理**（~120 行）。

该块职责单一且高度自包含：
- `STORAGE_KEY` / `ENDPOINT_PATH` 常量
- `defaultStreamUrl`（`window.location.origin` 派生）
- `streamUrl` / `isEditingUrl` / `editUrlValue` 三个 ref
- `onMounted` 从 localStorage 恢复自定义地址
- `watch(defaultStreamUrl)` 跟随 window.location 变化
- `startEditUrl` / `saveUrl` / `resetUrl` / `cancelEditUrl` 四个编辑函数
- `testConnection`（基于连接状态弹 alert）
- `reconnectStream` 依赖（保存/重置后立即重连）

### 附带发现的死代码

组件内还有一对互相引用的 `buildFinalUrl` / `liveUrl`：

```ts
// 把 string → URL 转换成一个 EventSource 可用的最终地址
function buildFinalUrl(url: string): string {
  // ... ?token= 降级逻辑
}

// 暴露给 store 的"当前目标 URL"：
const liveUrl = computed(() => buildFinalUrl(streamUrl.value))
```

排查结果：
- `buildFinalUrl` 的**唯一调用方**是 `liveUrl`（组件模板零引用）
- `liveUrl` 在组件模板 / 事件 / 其它文件**零消费**
- store 端 `liveStreamStore.ts` 通过 `getCustomEndpoint()` 直接读 localStorage，
  并在 `buildUrl(getCustomEndpoint() || ENDPOINT)` 内自行处理 `?token=` 降级（line ~755-760）

结论：`buildFinalUrl` / `liveUrl` 是历史遗留的冗余复制（曾想给 store 暴露 setter，
后改用 store 读 localStorage 方案，组件内的这对就没人消费了）。抽取时一并移除，
不把死代码带进新 composable。

## 修复

### `web/src/composables/useLiveStreamUrl.ts`（新建，100 行）

```ts
export interface UseLiveStreamUrlOptions {
  connection: Ref<ConnectionState>
  reconnect: () => void
  t: (key: string, named?: Record<string, unknown>) => string
}

const STORAGE_KEY = 'llmgw_sse_endpoint'
const ENDPOINT_PATH = '/api/admin/live-stream'

export function useLiveStreamUrl(options: UseLiveStreamUrlOptions) {
  const { connection, reconnect, t } = options

  const defaultStreamUrl = computed(() => `${window.location.origin}${ENDPOINT_PATH}`)
  const streamUrl = ref('')
  const isEditingUrl = ref(false)
  const editUrlValue = ref('')

  // 挂载时读取 localStorage 中管理员保存的自定义地址
  onMounted(() => {
    streamUrl.value = readSavedUrl() || defaultStreamUrl.value
  })

  // 如果用户修改了 window.location（多 tab 测试），默认地址也跟着变
  watch(defaultStreamUrl, (cur) => {
    if (!readSavedUrl()) streamUrl.value = cur
  })

  function startEditUrl() {
    editUrlValue.value = streamUrl.value
    isEditingUrl.value = true
  }

  // 保存 URL —— 立刻用新地址重连 SSE
  function saveUrl() {
    const url = editUrlValue.value.trim()
    if (url) {
      streamUrl.value = url
      try {
        localStorage.setItem(STORAGE_KEY, url)
      } catch {
        /* ignore */
      }
      reconnect()
    }
    isEditingUrl.value = false
  }

  // 重置为默认 URL
  function resetUrl() {
    streamUrl.value = defaultStreamUrl.value
    try {
      localStorage.removeItem(STORAGE_KEY)
    } catch {
      /* ignore */
    }
    isEditingUrl.value = false
    reconnect()
  }

  function cancelEditUrl() {
    isEditingUrl.value = false
  }

  // 测试 SSE 连接
  function testConnection() {
    if (connection.value === 'open') {
      window.alert(t('dashboard.liveStream.sseTestOk', { url: streamUrl.value }))
    } else {
      window.alert(t('dashboard.liveStream.sseTestFail', { status: connection.value, url: streamUrl.value }))
    }
  }

  return { defaultStreamUrl, streamUrl, isEditingUrl, editUrlValue,
           startEditUrl, saveUrl, resetUrl, cancelEditUrl, testConnection }
}
```

设计要点：
1. **依赖注入**：`connection` / `reconnect` / `t` 全部由调用方传入，composable 无 i18n / store
   直接依赖，可独立单测（不引入 liveStreamStore 的顶层副作用）
2. **类型安全**：`ConnectionState` 用 `import type`（编译期擦除，测试 import 不触发 store 的
   DOM 监听器副作用）
3. **职责单一**：只管 URL 状态 + localStorage + 重连触发，不做 UI 展示
4. **行为不变**：编辑状态机 / localStorage 读写 / 重连时序与旧内联实现完全一致

### `web/src/composables/useLiveStreamUrl.test.ts`（新建，11 用例）

| 测试用例 | 验证内容 |
|---|---|
| `onMounted: uses default URL when localStorage is empty` | 空 localStorage → streamUrl = origin + path |
| `onMounted: restores admin-saved URL from localStorage` | 有保存值 → 恢复自定义地址 |
| `editing: startEditUrl enters edit mode with copy of streamUrl` | startEditUrl → editUrlValue = streamUrl, isEditingUrl = true |
| `saveUrl: persists URL, reconnects, exits edit mode` | 保存 → localStorage 写入 + reconnect×1 + 退出编辑 |
| `saveUrl: empty URL does NOT persist or reconnect` | 空白值 → 不写 storage、不重连、退出编辑 |
| `resetUrl: removes localStorage entry, resets to default, reconnects` | 重置 → storage 清除 + 回到默认 + reconnect×1 |
| `cancelEditUrl: exits edit mode without persisting` | 取消 → 不保存、不重连 |
| `testConnection: alerts OK message when connection is open` | open → alert OK + t 调用正确 |
| `testConnection: alerts FAIL message with status when connection is not open` | 非 open → alert FAIL + t 调用正确 |
| `watch(defaultStreamUrl): follows default when no saved URL` | 无保存值 → 跟随默认（覆盖分支） |
| `watch(defaultStreamUrl): saved URL is NOT overwritten by default changes` | 有保存值 → 默认变化不覆盖 |

测试用 `@vue/test-utils` 挂载 harness 组件激活 `onMounted` / `watch` 生命周期；
`window.alert` 用 `vi.spyOn` mock；`makeT` 构造假翻译函数验证 `t` 调用参数。

### `web/src/components/LiveRequestStreamV2.vue` 接入

```diff
+import { useLiveStreamUrl } from '../composables/useLiveStreamUrl'

-// SSE endpoint address - 可编辑
-// 行为：
-//  1) 默认值走 window.location.origin + ENDPOINT
-//  2) localStorage 里允许管理员保存一个自定义 URL（反向代理 / 内网穿透）
-//  3) 保存后立即关闭旧连接、打开新地址，刷新整个流
-const STORAGE_KEY = 'llmgw_sse_endpoint'
-const ENDPOINT_PATH = '/api/admin/live-stream'
-const defaultStreamUrl = computed(() => `${window.location.origin}${ENDPOINT_PATH}`)
-const streamUrl = ref('')
-const isEditingUrl = ref(false)
-const editUrlValue = ref('')
-const buildFinalUrl = ...
-const liveUrl = computed(...)
+// 2026-08-06: SSE endpoint URL 管理抽到 useLiveStreamUrl composable
+const { streamUrl, isEditingUrl, editUrlValue,
+        startEditUrl, saveUrl, resetUrl, cancelEditUrl, testConnection } =
+  useLiveStreamUrl({ connection, reconnect: reconnectStream, t })

 onMounted(() => {
-  // ... localStorage 恢复逻辑（进 composable 了）
   // 2026-07-23: 供应商延时轮询（5 分钟一次，与探测节奏对齐）
   void refreshProviderLatency()
   latencyTimer = setInterval(() => void refreshProviderLatency(), 5 * 60 * 1000)
 })
```

- 移除 ~120 行内联 URL 管理（含死代码 buildFinalUrl / liveUrl）
- `onMounted` 现在只负责供应商延时轮询（URL 恢复由 composable 自己的 onMounted 处理）
- `toggleConnectionDetail` / `showConnectionDetail` / `isAdmin` 保留在组件（UI 弹窗状态）
- `defaultStreamUrl` 组件内不再需要（composable 内部自用，resetUrl 已封装）
- 组件从 1273 → 1184 行

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| composable 测试 | `npx vitest run src/composables/useLiveStreamUrl.test.ts` | **11/11 通过** |
| 全量测试 | `cd web && npx vitest run` | **31 文件 / 190 测试全绿**（+11 vs 上次 179） |
| 类型检查 | `cd web && npx vue-tsc --noEmit` | **0 errors** (exit 0) |
| i18n 审计 | `cd web && node scripts/i18n-audit.mjs` | `✅ no missing keys` |
| 构建 | `cd web && npx vite build` | **8.76s 成功** |

## 改动清单

```
web/src/composables/useLiveStreamUrl.ts          (新建, 100 行)
web/src/composables/useLiveStreamUrl.test.ts     (新建, 11 用例)
web/src/components/LiveRequestStreamV2.vue       |  -103 / +13（1273 → 1184 行）
CHANGELOG.md                                     |  +26
docs/changelogs/2026-08-06-extract-live-stream-url-composable.md (新建)
```

## 遗留与风险

- 无破坏性改动：模板层引用名（streamUrl / isEditingUrl / editUrlValue / 4 个编辑函数 /
  testConnection）全部保持不变，只是实现换成了 composable 解构
- 死代码移除（buildFinalUrl / liveUrl）：已确认零消费 + store 端 `buildUrl` 有等价实现，
  属 rule 09 §5.2 类别 A（真死代码）。如老板想保留 token 降级逻辑的组件侧副本，可要求
  恢复，但建议以 store 端 `buildUrl` 为准（单一实现）

## 反模式避免

- 严格按 rule 37 原则 3「精准修改」：只动 URL 管理块，SSE 连接 / 泳道渲染 / 过滤器 /
  应急诊断 / 供应商延时轮询均未触碰
- 严格按 rule 43「最小补丁 + UTF-8 + ≤300 行」：composable 与测试各一次 write 完成
- 严格按 rule 09 §5.2「死代码处理四步流程」：buildFinalUrl / liveUrl 已溯源（历史
  遗留，store 改读 localStorage 后无人消费）、已分类（A 类）、移除原因写进本文档与
  commit message
- 沿用 `useLiveStreamFilters` 的抽取范式：依赖注入 + type-only import + harness 挂载测试
