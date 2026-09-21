/**
 * filter-types.ts — FilterBar 的声明式定义类型。
 * 独立成文件以便视图与组件共享（script setup 不能对外导出类型）。
 */
export type FilterDefinition = {
  key: string
  type: 'select' | 'search' | 'daterange'
  /** 字段标签（已翻译字符串） */
  label?: string
  placeholder?: string
  /** select 选项（字符串数组或 {label,value}） */
  options?: (string | { label: string; value: string })[]
  /** search 类型提供建议词时改用 FilterInput 自动补全 */
  suggestions?: string[]
  /** daterange：区间两端绑定的 filters 键 */
  fromKey?: string
  toKey?: string
  fromLabel?: string
  toLabel?: string
}
