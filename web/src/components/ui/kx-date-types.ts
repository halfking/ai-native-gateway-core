// kx-date-types.ts — 全站统一日期选择组件族（KxDateRangePicker / KxDatePicker）的共享类型。
// 2026-09-30 看板重构轮：收敛此前 7 种互不兼容的日期输入写法（见 docs/design 效果图方案）。

/** 日期范围值。date 精度为 YYYY-MM-DD，datetime 精度为 YYYY-MM-DD HH:mm。 */
export interface KxDateRange {
  start: string
  end: string
}

export type KxDatePrecision = 'date' | 'datetime'

/** 预设项：id 稳定（可持久化），resolve 每次调用现算（相对日期）。 */
export interface KxDateRangePreset {
  id: string
  labelKey: string
  resolve: () => KxDateRange
}
