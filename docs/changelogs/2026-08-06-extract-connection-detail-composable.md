# 2026-08-06 — 抽取 useConnectionDetail composable（Task B 收尾）

## 背景

`LiveRequestStreamV2.vue` 经前 5 个 composable 抽取后，剩余最后一块纯 UI 状态——
**连接详情弹窗**：

```ts
const showConnectionDetail = ref(false)
const isAdmin = computed(() => isSuperAdmin())

// 切换连接详情弹窗
function toggleConnectionDetail() {
  if (isAdmin.value) {
    showConnectionDetail.value = !showConnectionDetail.value
    if (!showConnectionDetail.value) {
      isEditingUrl.value = false
    }
  }
}
```

依赖面分析：
- `showConnectionDetail` ref
- `toggleConnectionDetail` 依赖 `isAdmin` computed + `isEditingUrl`（来自 `useLiveStreamUrl`）
- `isAdmin` 同时在模板独立使用（`v-if="isAdmin"` 渲染编辑按钮），不能整体移走

## 修复

### `web/src/composables/useConnectionDetail.ts`（新建）

```ts
export interface UseConnectionDetailOptions {
  isAdmin: Ref<boolean>
  isEditingUrl: Ref<boolean>
}

export function useConnectionDetail(options: UseConnectionDetailOptions) {
  const { isAdmin, isEditingUrl } = options

  const showConnectionDetail = ref(false)

  function toggleConnectionDetail() {
    if (isAdmin.value) {
      showConnectionDetail.value = !showConnectionDetail.value
      if (!showConnectionDetail.value) {
        isEditingUrl.value = false
      }
    }
  }

  return { showConnectionDetail, toggleConnectionDetail }
}
```

设计要点：
1. **依赖注入**：`isAdmin` / `isEditingUrl` 均为 Ref 注入，composable 不持有组件上下文，
   可独立单测
2. **只暴露被使用的 API（YAGNI）**：模板的关闭按钮是直接赋值
   `showConnectionDetail = false`（不重置编辑态），因此不额外暴露 `closeConnectionDetail`，
   避免无消费者的死 API
3. **行为不变**：非管理员点击无效；关闭弹窗时同步退出编辑态，与旧内联一致

### `web/src/composables/useConnectionDetail.test.ts`（新建，4 用例）

| 测试用例 | 验证内容 |
|---|---|
| `initial state: popup hidden` | 初始 showConnectionDetail 为 false |
| `toggleConnectionDetail opens the popup for admin` | 管理员点击展开 |
| `toggleConnectionDetail closes popup and exits edit mode for admin` | 关闭 + `isEditingUrl` 复位 |
| `toggleConnectionDetail does nothing for non-admin` | 非管理员无效果 |

### `web/src/components/LiveRequestStreamV2.vue` 接入

```diff
+import { useConnectionDetail } from '../composables/useConnectionDetail'
  ...
 const { providerLatencyMap } = useProviderLatency({ groupBy })
-const showConnectionDetail = ref(false)
 const isAdmin = computed(() => isSuperAdmin())
+// 2026-08-06: 连接详情弹窗状态抽到 useConnectionDetail composable
+const { showConnectionDetail, toggleConnectionDetail } =
+  useConnectionDetail({ isAdmin, isEditingUrl })
  ...
-// 切换连接详情弹窗
-function toggleConnectionDetail() {
-  if (isAdmin.value) { ... }
-}
```

关键约束——**声明顺序（TDZ）**：
- `isEditingUrl` 来自 `useLiveStreamUrl`，后者在组件中位于 `useConnectionDetail` 之后
- `const` 有暂时性死区，若在 `useLiveStreamUrl` 之前调用 `useConnectionDetail`
  会报 `Cannot access 'isEditingUrl' before initialization`
- 因此 `useConnectionDetail` 调用必须置于 `useLiveStreamUrl` 解构之后

`isAdmin` computed 保留在组件（模板 `v-if="isAdmin"` 独立使用）；
`import { ref }` 仍被 `filterDialog` 使用，无需移除。

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| composable 测试 | `npx vitest run src/composables/useConnectionDetail.test.ts` | **4/4 通过** |
| 全量测试 | `cd web && npx vitest run` | **35 文件 / 213 测试全绿**（+4 vs 上次 209） |
| 类型检查 | `cd web && npx vue-tsc --noEmit` | **0 errors** (exit 0) |
| 构建 | `cd web && npx vite build` | **8.76s 成功** |

## 改动清单

```
web/src/composables/useConnectionDetail.ts          (新建)
web/src/composables/useConnectionDetail.test.ts     (新建, 4 用例)
web/src/components/LiveRequestStreamV2.vue          |  -14 / +9（1128 → 1125 行）
CHANGELOG.md                                         |  +15
docs/changelogs/2026-08-06-extract-connection-detail-composable.md (新建)
```

## Task B 收尾

`LiveRequestStreamV2.vue` 已抽取 6 个 composable：

| composable | 职责 | 依赖注入 |
|---|---|---|
| `useLiveStreamFilters` | 状态/模型/供应商/Agent 过滤 + 排序 | store |
| `useLiveStreamUrl` | SSE URL 持久化 + 编辑状态机 | connection / reconnect / t |
| `useProviderLatency` | 供应商延时轮询 | groupBy |
| `useEmergencyDiagnostic` | 应急诊断弹窗状态 | 无 |
| `useIncidentDiagnosis` | RouteIncidentDrawer 工作台 | onOpenRequest |
| `useConnectionDetail` | 连接详情弹窗 | isAdmin / isEditingUrl |

组件从 **1425 → 1125 行（-300 行）**。剩余内联块均为模板展示型 computed
（connectionLabel / dimensionLabel / bufferCount / windowCount / isColdStart），
与渲染强耦合且无独立副作用，抽取价值趋近于零。

## 遗留与风险

- 无破坏性改动：模板引用名全部不变，行为完全等价
- TDZ 顺序是本次抽取的唯一陷阱，已在详档记录避免回归

## 反模式避免

- rule 37 原则 2（简洁优先）：不暴露无消费者的 `closeConnectionDetail`
- rule 37 原则 3（精准修改）：只动连接详情块，未触碰其它逻辑
- rule 43（最小补丁 + UTF-8 + ≤300 行）：composable 与测试各一次 write 完成
