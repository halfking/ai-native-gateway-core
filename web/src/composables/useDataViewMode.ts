/**
 * useDataViewMode — 表格/卡片视图模式（docs/UI规范/00 §5.2 · H3，参考规范 03 §2）。
 *
 * ## 产品规则（本 composable 是它的实现，不是它的可选实现）
 *
 * | 档位 | 默认 | 能否切换 | 是否写偏好 |
 * | --- | --- | --- | --- |
 * | compact (<768) | **卡片，强制** | 否（不渲染切换钮） | **否** |
 * | medium 及以上 | 表格 | 是：表格 ↔ 卡片 | 是，localStorage 跨页共用 |
 *
 * 「compact 强制」之所以做成**类型层**而不是模板里 `v-if` 掉切换钮：
 * 只隐藏按钮的话，调用方仍可能在别处调 `setMode('table')`，把横滚表格
 * 留在 320px 屏上 —— 那是规范明令禁止的降级。所以 `setMode` 在 compact 下
 * **直接 return**，且不写存储。
 *
 * ## 不变的部分
 *
 * 卡片与表格共用同一套 query / 分页 / 筛选。切换视图模式**不重新打接口**，
 * 只换呈现。这是本 composable 存在的全部理由；若某个页面在切换时 refetch，
 * 那是页面违反了约定，应改页面。
 */
import { computed, ref, watch, type ComputedRef, type Ref } from 'vue'
import { useWindowClass } from './useWindowClass'

export type DataViewMode = 'table' | 'cards'

/** 与本仓既有 `llmgw_*` 命名一致。 */
export const DATA_VIEW_MODE_KEY = 'llmgw_data_view_mode'

function readStored(): DataViewMode | null {
  if (typeof localStorage === 'undefined') return null
  try {
    const v = localStorage.getItem(DATA_VIEW_MODE_KEY)
    return v === 'cards' || v === 'table' ? v : null
  } catch {
    // 隐私模式 / 配额满：退化为内存偏好，不影响功能
    return null
  }
}

function writeStored(mode: DataViewMode): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(DATA_VIEW_MODE_KEY, mode)
  } catch {
    // 忽略：内存偏好仍生效
  }
}

export interface UseDataViewModeOptions {
  /** 桌面默认模式。规范要求默认表格。 */
  defaultMode?: DataViewMode
  /**
   * 外部初始值（例如从查询参数 `?view=cards` 推导）。
   * 提供时优先于存储与默认值。
   */
  initial?: Ref<DataViewMode | null>
}

export interface UseDataViewModeResult {
  /** 用户偏好（持久化的那一份）。compact 下**不代表实际渲染模式**。 */
  preference: Ref<DataViewMode>
  /** 实际应渲染的模式：compact 恒为 cards。 */
  effective: ComputedRef<DataViewMode>
  /** 是否处于 compact 档。 */
  isCompact: ComputedRef<boolean>
  /**
   * 是否**允许**用户切换。compact 为 false ——
   * 切换钮应据此 `v-if`，而不是「渲染了但点了没反应」。
   */
  canSwitch: ComputedRef<boolean>
  /**
   * 设置模式。**compact 下直接 return**（不生效、不写存储）。
   * 返回是否真的接受了这次设置，便于调用方在测试与调试里确认。
   */
  setMode(next: DataViewMode): boolean
}

export function useDataViewMode(opts: UseDataViewModeOptions = {}): UseDataViewModeResult {
  const { isCompact } = useWindowClass()
  const defaultMode: DataViewMode = opts.defaultMode ?? 'table'

  // 存储不可用时先用内存默认，读到值再覆盖
  const preference = ref<DataViewMode>(readStored() ?? defaultMode)

  const canSwitch = computed(() => !isCompact.value)
  const effective = computed<DataViewMode>(() => (isCompact.value ? 'cards' : preference.value))

  function setMode(next: DataViewMode): boolean {
    // ★ compact 强制卡片。这条早退是本文件的核心约束，不是可选优化。
    if (isCompact.value) return false
    if (preference.value === next) return true
    preference.value = next
    writeStored(next)
    return true
  }

  // 外部初始值（如 ?view=cards）只在非 compact 生效。
  if (opts.initial) {
    watch(
      opts.initial,
      (v) => {
        if (v && !isCompact.value) setMode(v)
      },
      { immediate: true },
    )
  }

  return { preference, effective, isCompact, canSwitch, setMode }
}

/** 仅供测试：复位模块级无状态（本实现无模块级状态，保留是为了对称与可测）。 */
export function _resetDataViewModeForTests(): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.removeItem(DATA_VIEW_MODE_KEY)
  } catch {
    // 忽略
  }
}
