# 2026-08-06 — 抽取 useIncidentDiagnosis composable

## 背景

`LiveRequestStreamV2.vue` 还剩两块内联状态，其中**诊断工作台**（route-incident drawer，
2026-07-13 Phase 1 只读）是干净、有领域边界的一块：

```ts
// 2026-07-13: 诊断工作台 state
const activeIncidentId = ref<string | null>(null)
const activeIncidentPreview = ref<RouteIncident | null>(null)

function handleDiagnose(incidentId: string, preview: RouteIncident) {
  activeIncidentId.value = incidentId
  activeIncidentPreview.value = preview
}

function closeDiagnose() {
  activeIncidentId.value = null
  activeIncidentPreview.value = null
}

function handleRequestFromDrawer(requestId: string) {
  closeDiagnose()
  emit('openDetail', requestId)
}
```

依赖面分析：
- 2 个 ref（activeIncidentId / activeIncidentPreview）
- 3 个纯函数，唯一副作用是 `handleRequestFromDrawer` 里的 `emit('openDetail', requestId)`
- 无生命周期钩子、无 i18n、无 store 依赖

唯一需要处理的是 `emit` 副作用 —— 通过依赖注入 `onOpenRequest` 回调解决，composable 与
组件解耦。

## 修复

### `web/src/composables/useIncidentDiagnosis.ts`（新建，49 行）

```ts
export interface UseIncidentDiagnosisOptions {
  onOpenRequest: (requestId: string) => void
}

export function useIncidentDiagnosis(options: UseIncidentDiagnosisOptions) {
  const { onOpenRequest } = options

  const activeIncidentId = ref<string | null>(null)
  const activeIncidentPreview = ref<RouteIncident | null>(null)

  function handleDiagnose(incidentId: string, preview: RouteIncident) {
    activeIncidentId.value = incidentId
    activeIncidentPreview.value = preview
  }

  function closeDiagnose() {
    activeIncidentId.value = null
    activeIncidentPreview.value = null
  }

  function handleRequestFromDrawer(requestId: string) {
    closeDiagnose()
    onOpenRequest(requestId)
  }

  return { activeIncidentId, activeIncidentPreview,
           handleDiagnose, closeDiagnose, handleRequestFromDrawer }
}
```

设计要点：
1. **副作用注入**：`onOpenRequest` 是组件 `emit('openDetail')` 的包装，composable 不再持有
   Vue 组件上下文，可独立单测
2. **职责单一**：只管诊断工作台 open/close + 请求跳转编排，不做 drawer 渲染
3. **行为不变**：打开填充 / 关闭清空 / 从 drawer 跳转请求先关闭再触发回调，与旧内联一致

### `web/src/composables/useIncidentDiagnosis.test.ts`（新建，4 用例）

| 测试用例 | 验证内容 |
|---|---|
| `initial state: no active incident` | 初始 id / preview 均为 null |
| `handleDiagnose opens the drawer and fills incident id + preview` | 打开并填充（toStrictEqual 深比较） |
| `closeDiagnose clears the active incident` | 关闭后两个 ref 均置 null |
| `handleRequestFromDrawer closes the drawer then triggers onOpenRequest` | 先关闭再触发回调（1 次 + 参数正确） |

测试构造了完整的 `RouteIncident` fixture（route_key / state / failure_streak /
recovery_streak / first_failure_at / totals / version / created_at / updated_at），
不依赖任何 mock 框架，纯函数式断言。

### `web/src/components/LiveRequestStreamV2.vue` 接入

```diff
+import { useIncidentDiagnosis } from '../composables/useIncidentDiagnosis'
  ...
-// 2026-07-13: 诊断工作台 state
-const activeIncidentId = ref<string | null>(null)
-const activeIncidentPreview = ref<RouteIncident | null>(null)
-function handleDiagnose(...) { ... }
-function closeDiagnose() { ... }
-function handleRequestFromDrawer(requestId: string) {
-  closeDiagnose()
-  emit('openDetail', requestId)
-}
+// 2026-08-06: 诊断工作台（RouteIncidentDrawer）状态抽到 useIncidentDiagnosis composable
+const { activeIncidentId, activeIncidentPreview, handleDiagnose,
+        closeDiagnose, handleRequestFromDrawer } =
+  useIncidentDiagnosis({ onOpenRequest: (id) => emit('openDetail', id) })
```

- 移除 18 行内联诊断状态
- 模板引用名全部保持不变（`RouteIncidentDrawer` 的 2 个 prop + 2 个事件绑定无感；
  `@diagnose` 仍用 `RouteIncident` 类型标注箭头，`import type { RouteIncident }` 保留）
- 组件从 1135 → 1128 行

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| composable 测试 | `npx vitest run src/composables/useIncidentDiagnosis.test.ts` | **4/4 通过** |
| 全量测试 | `cd web && npx vitest run` | **34 文件 / 209 测试全绿**（+4 vs 上次 205） |
| 类型检查 | `cd web && npx vue-tsc --noEmit` | **0 errors** (exit 0) |
| i18n 审计 | `cd web && node scripts/i18n-audit.mjs` | `✅ 0 missing`（随测试运行，见 keys_referenced.test） |
| 构建 | `cd web && npx vite build` | **8.60s 成功** |

## 改动清单

```
web/src/composables/useIncidentDiagnosis.ts          (新建, 49 行)
web/src/composables/useIncidentDiagnosis.test.ts     (新建, ~70 行, 4 用例)
web/src/components/LiveRequestStreamV2.vue           |  -22 / +8（1135 → 1128 行）
CHANGELOG.md                                         |  +15
docs/changelogs/2026-08-06-extract-incident-diagnosis-composable.md (新建)
```

## 遗留与风险

- 无破坏性改动：模板引用名全部不变，`handleRequestFromDrawer` 的关闭→跳转时序不变
- 第五个抽出的 composable，`LiveRequestStreamV2.vue` 已从 1425 → 1128 行
- 剩余内联块只剩：连接详情弹窗状态（`showConnectionDetail` + `toggleConnectionDetail`，
  依赖 `isAdmin` computed + `isEditingUrl`）+ 连接状态标签（connectionLabel / connectionClass）。
  连接详情与 `useLiveStreamUrl` 的 `isEditingUrl` 交叉引用，抽取需同时注入 `isAdmin` 与
  `isEditingUrl` 两个 ref，收益最低，建议不抽（保持现状，组件已从 1425 → 1128 行完成瘦身）

## 反模式避免

- 严格按 rule 37 原则 3「精准修改」：只动诊断工作台块，连接详情 / 应急诊断 / URL /
  延时轮询均未触碰
- 严格按 rule 43「最小补丁 + UTF-8 + ≤300 行」：composable 与测试各一次 write 完成
- 沿用前四个 composable 的抽取范式：副作用（emit）通过回调注入，纯状态函数式断言
