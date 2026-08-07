# 2026-08-06 — 抽取 useEmergencyDiagnostic composable

## 背景

`LiveRequestStreamV2.vue` 还剩一块完全自包含的内联 UI 状态：**应急诊断弹窗**（~20 行）。

```ts
// 应急诊断弹窗
const showEmergencyDiagnostic = ref(false)
const emergencyCredentialId = ref(0)
const emergencyModel = ref('')
const emergencyLaneName = ref('')

function handleEmergencyDiagnose(data: { credentialId: number; model: string; laneName: string }) {
  emergencyCredentialId.value = data.credentialId
  emergencyModel.value = data.model
  emergencyLaneName.value = data.laneName
  showEmergencyDiagnostic.value = true
}

function handleEmergencyClose() {
  showEmergencyDiagnostic.value = false
}

function handleEmergencyRecovered() {
  console.log('Credential recovered successfully')
}
```

与前三个 composable（filters / url / providerLatency）不同，该块**零依赖**：

- 不依赖 i18n / store / connection / emit
- 不依赖任何生命周期钩子
- 只是 4 个 ref + 3 个纯函数

抽取收益是「一致性 + 组件区域只剩解构行」，属于从 1425 行组件瘦身过程中的收尾型清理。

## 修复

### `web/src/composables/useEmergencyDiagnostic.ts`（新建，45 行）

```ts
export interface EmergencyDiagnosePayload {
  credentialId: number
  model: string
  laneName: string
}

export function useEmergencyDiagnostic() {
  const showEmergencyDiagnostic = ref(false)
  const emergencyCredentialId = ref(0)
  const emergencyModel = ref('')
  const emergencyLaneName = ref('')

  function handleEmergencyDiagnose(data: EmergencyDiagnosePayload) {
    emergencyCredentialId.value = data.credentialId
    emergencyModel.value = data.model
    emergencyLaneName.value = data.laneName
    showEmergencyDiagnostic.value = true
  }

  function handleEmergencyClose() {
    showEmergencyDiagnostic.value = false
  }

  function handleEmergencyRecovered() {
    // 恢复成功后，可以选择刷新泳道或显示通知
    console.log('Credential recovered successfully')
  }

  return { showEmergencyDiagnostic, emergencyCredentialId, emergencyModel,
           emergencyLaneName, handleEmergencyDiagnose, handleEmergencyClose,
           handleEmergencyRecovered }
}
```

设计要点：
1. **零依赖**：函数式 composable，无参数、无生命周期钩子，调用即得完整状态机
2. **类型安全**：`EmergencyDiagnosePayload` 接口显式定义泳道 `@emergency-diagnose` 事件入参形状
3. **行为不变**：打开填充 payload / 关闭仅隐藏（保留字段）/ recovered 回调日志，与旧内联实现一致
4. **可测性**：纯状态变更，无需挂载 harness 组件即可断言

### `web/src/composables/useEmergencyDiagnostic.test.ts`（新建，4 用例）

| 测试用例 | 验证内容 |
|---|---|
| `initial state: modal hidden, empty payload fields` | 初始 show=false，字段全空 |
| `handleEmergencyDiagnose fills payload and opens the modal` | 填充 payload + show=true |
| `handleEmergencyClose hides the modal but keeps payload fields` | 关闭只隐藏，字段保留（不重置） |
| `handleEmergencyRecovered logs a confirmation` | `vi.spyOn(console, 'log')` 断言日志 |

### `web/src/components/LiveRequestStreamV2.vue` 接入

```diff
+import { useEmergencyDiagnostic } from '../composables/useEmergencyDiagnostic'
  ...
-// 应急诊断弹窗
-const showEmergencyDiagnostic = ref(false)
-const emergencyCredentialId = ref(0)
-const emergencyModel = ref('')
-const emergencyLaneName = ref('')
-function handleEmergencyDiagnose(...) { ... }
-function handleEmergencyClose() { ... }
-function handleEmergencyRecovered() { ... }
+// 2026-08-06: 应急诊断弹窗状态抽到 useEmergencyDiagnostic composable
+const { showEmergencyDiagnostic, emergencyCredentialId, emergencyModel,
+        emergencyLaneName, handleEmergencyDiagnose, handleEmergencyClose,
+        handleEmergencyRecovered } = useEmergencyDiagnostic()
```

- 移除 20 行内联弹窗状态
- 模板层引用名全部保持不变（`EmergencyDiagnosticModal` 的 4 个 prop + 2 个事件绑定无感）
- 组件从 1145 → 1135 行

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| composable 测试 | `npx vitest run src/composables/useEmergencyDiagnostic.test.ts` | **4/4 通过** |
| 全量测试 | `cd web && npx vitest run` | **33 文件 / 205 测试全绿**（+4 vs 上次 200） |
| 类型检查 | `cd web && npx vue-tsc --noEmit` | **0 errors** (exit 0) |
| i18n 审计 | `cd web && node scripts/i18n-audit.mjs` | `✅ 0 missing`（随测试运行，见 keys_referenced.test） |
| 构建 | `cd web && npx vite build` | **8.67s 成功** |

## 改动清单

```
web/src/composables/useEmergencyDiagnostic.ts          (新建, 45 行)
web/src/composables/useEmergencyDiagnostic.test.ts     (新建, 47 行, 4 用例)
web/src/components/LiveRequestStreamV2.vue             |  -24 / +10（1145 → 1135 行）
CHANGELOG.md                                           |  +14
docs/changelogs/2026-08-06-extract-emergency-diagnostic-composable.md (新建)
```

## 遗留与风险

- 无破坏性改动：模板引用名全部不变，只是实现换成 composable 解构
- 第四个抽出的 composable，`LiveRequestStreamV2.vue` 已从 1425 → 1135 行
- 剩余内联块：连接详情弹窗状态（`showConnectionDetail` + `toggleConnectionDetail`，依赖
  `isAdmin` computed + `isEditingUrl`）+ 诊断工作台状态（`activeIncidentId` / `activeIncidentPreview`，
  依赖 emit `openDetail`）。这两块与既有 composable / 组件其它状态有交叉引用，
  抽取收益递减，建议作为后续任务单独评估
- ⚠️ 关联提醒：团队 48h 审计（`f8b10499e`，已 rebase 进 main）发现 `useLiveStreamFilters`
  抽取时 `applyAgentFilter` 丢失了 `.toLowerCase()` 归一化，已修复。这提醒我们：后续抽取
  必须逐行为比对行为等价，本轮的 `useEmergencyDiagnostic` 为纯状态拷贝，无此类风险

## 反模式避免

- 严格按 rule 37 原则 3「精准修改」：只动应急诊断弹窗块，连接详情 / 诊断工作台 / URL /
  延时轮询均未触碰
- 严格按 rule 43「最小补丁 + UTF-8 + ≤300 行」：composable 与测试各一次 write 完成
- 沿用前三个 composable 的抽取范式：纯状态用例直接调用函数式 composable 断言，
  无需 harness 挂载（生命周期钩子不在本块职责内）
