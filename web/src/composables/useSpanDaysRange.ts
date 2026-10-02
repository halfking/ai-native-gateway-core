// useSpanDaysRange.ts — 把「只接受 days 整数」的页面接到 KxDateRangePicker。
// 预设档位与旧 select/radio 一致；应用后按含首尾日的跨度回写 days，自定义区间同样生效。
import { computed, type Ref } from 'vue'
import type { KxDateRange, KxDateRangePreset } from '../components/ui/kx-date-types'
import { rangeFromSpanDays, rangeSpanDays, spanDaysPreset } from '../components/ui/kxDatePresets'

export interface SpanDaysBucket {
  days: number
  labelKey: string
}

export function useSpanDaysRange(days: Ref<number>, buckets: SpanDaysBucket[]) {
  const presets = computed<KxDateRangePreset[]>(() =>
    buckets.map((bucket) => spanDaysPreset(bucket.days, bucket.labelKey)),
  )
  const rangeValue = computed(() => rangeFromSpanDays(Math.max(1, days.value || 1)))

  function applyRange(range: KxDateRange) {
    const span = rangeSpanDays(range)
    if (span >= 1) days.value = span
  }

  return { presets, rangeValue, applyRange }
}
