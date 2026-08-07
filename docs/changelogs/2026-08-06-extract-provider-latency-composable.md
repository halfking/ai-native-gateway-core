# 2026-08-06 — 抽取 useProviderLatency composable

## 背景

上一轮抽取 `useLiveStreamUrl` 时，`onMounted` 里剩下的**供应商 HTTP 延时轮询**成了组件里
最后一处需要生命周期钩子的内联逻辑（~38 行）：

- `providerLatencyMap` ref（`{ [providerCode]: latency_ms }`）
- `latencyTimer` setInterval（5 分钟一次，与 NodeProbeWorker 探测节奏对齐）
- `refreshProviderLatency()`（仅 provider 维度拉取 + code/name 双 key map 构建 + 静默失败）
- `watch(groupBy)` 切到 provider 维度时立即拉取
- `onMounted`（首拉 + 启动轮询）/ `onUnmounted`（清理定时器）

抽取后，组件顶层不再需要 `onMounted` / `onUnmounted` / `watch` 这三个 vue 生命周期/响应式
API（`useLiveStreamUrl` 已在 composable 内自管 onMounted），import 里 `onMounted`、
`onUnmounted`、`watch`、`fetchProviderLatency` 全部可以移除。

## 修复

### `web/src/composables/useProviderLatency.ts`（新建，62 行）

```ts
export interface UseProviderLatencyOptions {
  groupBy: Ref<GroupByDimension>
}

export function useProviderLatency(options: UseProviderLatencyOptions) {
  const { groupBy } = options
  const providerLatencyMap = ref<Record<string, number>>({})
  let timer: ReturnType<typeof setInterval> | null = null

  async function refreshProviderLatency() {
    if (groupBy.value !== 'provider') return
    try {
      const res = await fetchProviderLatency()
      const map: Record<string, number> = {}
      for (const e of res.entries || []) {
        if (e.latency_ms <= 0) continue
        if (e.provider_code) map[e.provider_code] = e.latency_ms
        if (e.provider_name) map[e.provider_name] = e.latency_ms
      }
      providerLatencyMap.value = map
    } catch {
      // 接口可能在新探测模式未启用时不存在，静默失败
    }
  }

  watch(groupBy, (g) => {
    if (g === 'provider') void refreshProviderLatency()
  })

  onMounted(() => {
    void refreshProviderLatency()
    timer = setInterval(() => void refreshProviderLatency(), 5 * 60 * 1000)
  })

  onUnmounted(() => {
    if (timer) { clearInterval(timer); timer = null }
  })

  return { providerLatencyMap, refreshProviderLatency }
}
```

设计要点（沿用 useLiveStreamFilters / useLiveStreamUrl 的抽取范式）：
1. **依赖注入**：只接收 `groupBy: Ref<GroupByDimension>`，与 `useSwimLane` 解耦组合；
   API 调用直接走 `fetchProviderLatency()`（可 mock）
2. **职责单一**：只管延时拉取 + 轮询生命周期 + 维度联动，不做 UI 展示
3. **行为不变**：provider 维度 guard / code+name 双 key / 非正延时过滤 / 静默失败 /
   5 分钟轮询 / groupBy 联动时序与旧内联实现完全一致
4. **生命周期自管**：`onMounted` 首拉 + 轮询、`onUnmounted` 清理定时器都在 composable 内，
   组件不再需要生命周期钩子

### `web/src/composables/useProviderLatency.test.ts`（新建，10 用例）

| 测试用例 | 验证内容 |
|---|---|
| `onMounted: does NOT fetch when initial groupBy is not provider` | 初始 model 维度 → 不拉取 |
| `onMounted: fetches immediately when initial groupBy is provider` | 初始 provider 维度 → 首拉 + map 正确 |
| `map uses provider_code primary + provider_name fallback` | 双 key 同时写入（openai + OpenAI） |
| `skips entries with non-positive latency_ms` | 0 / 负值条目被过滤 |
| `silently fails when fetch rejects` | 接口报错 → map 保持空，不抛错 |
| `groupBy switch to provider triggers immediate fetch` | 切到 provider → 立即拉取 |
| `groupBy switch away from provider does not re-fetch` | 切走 → 不再拉取 |
| `polling: refreshProviderLatency runs on 5-minute interval` | 假时钟推进 5 分钟 → 触发一次轮询 |
| `unmount clears the polling timer` | unmount 后推进时间 → 不再触发 |
| `polling skips fetch when groupBy is not provider` | 轮询触发时 guard 拦截非 provider 维度 |

测试技巧：
- `vi.mock('../api/provider-probe')` mock `fetchProviderLatency`
- `@vue/test-utils` 挂载 harness 组件激活 `onMounted` / `onUnmounted` / `watch`
- `vi.useFakeTimers()` + `vi.advanceTimersByTime()` 验证轮询节奏与清理
- **异步断言用 `flushPromises()`**（不是 `nextTick`）——`refreshProviderLatency` 是 async，
  需要冲刷微任务队列才能看到 map 更新（这也是本测试与 useLiveStreamUrl 测试的一个差异点）

### `web/src/components/LiveRequestStreamV2.vue` 接入

```diff
-import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
+import { ref, computed } from 'vue'
  ...
-import { fetchProviderLatency } from '../api/provider-probe'
+import { useProviderLatency } from '../composables/useProviderLatency'

-// 2026-07-23: 供应商 HTTP 延时（子项②）。仅在 provider 维度展示。
-// providerLatencyMap: { [providerCode(字符串)]: latency_ms }
-// key 用 provider_code（不是 provider_id），因为 lane.id = req.ProviderCode
-const providerLatencyMap = ref<Record<string, number>>({})
-let latencyTimer = ...
-async function refreshProviderLatency() { ... }
-watch(groupBy, (g) => { ... })
+// 2026-08-06: 供应商 HTTP 延时轮询抽到 useProviderLatency composable
+const { providerLatencyMap } = useProviderLatency({ groupBy })

- onMounted(() => { ... 首拉 + setInterval ... })
- onUnmounted(() => { ... clearInterval ... })
```

- 移除 ~38 行内联延时逻辑 + 空的 `onMounted` / `onUnmounted` 生命周期块
- import 层面同时清掉 `onMounted` / `onUnmounted` / `watch` / `fetchProviderLatency`（4 个不再使用）
- 模板层引用名 `providerLatencyMap` 保持不变（`SwimLane :avg-latency-ms` 绑定无感）
- 组件从 1183 → 1145 行

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| composable 测试 | `npx vitest run src/composables/useProviderLatency.test.ts` | **10/10 通过** |
| 全量测试 | `cd web && npx vitest run` | **32 文件 / 200 测试全绿**（+10 vs 上次 190） |
| 类型检查 | `cd web && npx vue-tsc --noEmit` | **0 errors** (exit 0) |
| i18n 审计 | `cd web && node scripts/i18n-audit.mjs` | `✅ 0 missing`（随测试运行，见 keys_referenced.test） |
| 构建 | `cd web && npx vite build` | **8.90s 成功** |

## 改动清单

```
web/src/composables/useProviderLatency.ts          (新建, 62 行)
web/src/composables/useProviderLatency.test.ts     (新建, 180 行, 10 用例)
web/src/components/LiveRequestStreamV2.vue         |  -52 / +4（1183 → 1145 行）
CHANGELOG.md                                       |  +19
docs/changelogs/2026-08-06-extract-provider-latency-composable.md (新建)
```

## 遗留与风险

- 无破坏性改动：模板引用名 `providerLatencyMap` 完全不变，只是实现换成 composable 解构
- `refreshProviderLatency` 仍从 composable 导出（当前组件未用，保留为公共 API 供未来
  手动刷新场景 / 测试；若确认无人消费可按 rule 09 §5.2 流程标记 KEEP 或移除）
- 这是第三个抽出的 composable（filters → url → providerLatency），`LiveRequestStreamV2.vue`
  已从 1425 → 1145 行。剩余可继续抽取的内联块：应急诊断弹窗状态（~20 行，UI 状态 + 事件
  转发，依赖 `isSuperAdmin` / emit）与连接详情弹窗状态（~15 行，UI 开关），均已没有
  composable 级生命周期依赖，抽取收益递减，建议后续按需再做

## 反模式避免

- 严格按 rule 37 原则 3「精准修改」：只动延时轮询块 + 生命周期钩子，SSE 连接 / 泳道渲染 /
  过滤器 / 应急诊断 / URL 管理均未触碰
- 严格按 rule 43「最小补丁 + UTF-8 + ≤300 行」：composable 与测试各一次 write 完成
- 严格按 rule 09 §5.2「死代码处理四步流程」：本轮没有删除逻辑代码，只是移动进 composable；
  空 `onMounted`/`onUnmounted` 块随抽取一并移除（已确认无其它逻辑依赖它们）
- 沿用前两个 composable 的抽取范式：依赖注入 + type-only import + harness 挂载测试 +
  flushPromises 处理 async 断言
