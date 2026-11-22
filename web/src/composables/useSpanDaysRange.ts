// useSpanDaysRange.ts — 把「只接受 days 整数」的页面接到 KxDateRangePicker。
// 预设档位与旧 select/radio 一致；应用后按含首尾日的跨度回写 days。
//
// ★ 2026-11-23 批判式审计轮（重要行为变更，务必读）：
//
//	此前 applyRange 把用户选的**任意**区间压成「跨度天数」再写进 days，
//	而 days-only 的后端把 days 解释为「相对现在/今天的滚动窗口」
//	（如 `ts >= NOW() - INTERVAL '24 hours'`）。
//	⇒ 用户选 2026-09-01 ~ 2026-09-10，实际拿到的是**最近 10 天**——
//	界面显示自定义区间，数据却是另一个窗口（静默错误答案，非量具问题）。
//
//	现在分成两个 composable，按**端点能力**选用：
//
//	  useSpanDaysRange —— 端点只收 days。
//	    ⚠️ 此时自定义区间的语义是「跨度天数」，**不是**用户选的那个历史区间。
//	    调用方若不打算改后端，就必须用 KxDateRangePicker 的 `presetsOnly`
//	    禁掉自定义入口，让用户只能选预设档位（⇒ 界面与数据口径一致）。
//
//	  useRangeDays —— 端点收 start/end（YYYY-MM-DD，系统时区）。
//	    预设档位走 days（保持后端缓存键/MV 选择/索引扫描等既有优化不变），
//	    **用户手选的历史区间原样透传**（customRange 非空时传 start/end）。
//
//	★ 为什么预设仍走 days：预设解析出的区间本身就是「相对今天」的窗口，
//	  与 days 语义天然一致 ⇒ 两者结果相同，走 days 不损失任何东西。
//	  只有用户手选的历史区间两者才分叉 ⇒ 只在自定义时透传 start/end。
import { computed, ref, type Ref } from 'vue'
import type { KxDateRange, KxDateRangePreset } from '../components/ui/kx-date-types'
import { rangeFromSpanDays, rangeSpanDays, spanDaysPreset } from '../components/ui/kxDatePresets'

export interface SpanDaysBucket {
  days: number
  labelKey: string
}

/** 把区间折叠成天数档位；非法区间（0 天）返回 0 表示「不改」。 */
function spanOf(range: KxDateRange): number {
  const span = rangeSpanDays(range)
  return span >= 1 ? span : 0
}

/** 区间是否命中某个预设档位（命中 ⇒ 与 days 语义等价，可走 days 快路）。 */
function matchesPreset(presets: KxDateRangePreset[], range: KxDateRange): boolean {
  return presets.some((p) => {
    const r = p.resolve()
    return r.start === range.start && r.end === range.end
  })
}

export function useSpanDaysRange(days: Ref<number>, buckets: SpanDaysBucket[]) {
  const presets = computed<KxDateRangePreset[]>(() =>
    buckets.map((bucket) => spanDaysPreset(bucket.days, bucket.labelKey)),
  )
  const rangeValue = computed(() => rangeFromSpanDays(Math.max(1, days.value || 1)))

  function applyRange(range: KxDateRange) {
    const span = spanOf(range)
    if (span >= 1) days.value = span
  }

  return { presets, rangeValue, applyRange }
}

// useRangeDays —— 面向「后端接受真实区间」的页面：同时持有 days 与真实区间。
export function useRangeDays(days: Ref<number>, buckets: SpanDaysBucket[]) {
  const presets = computed<KxDateRangePreset[]>(() =>
    buckets.map((bucket) => spanDaysPreset(bucket.days, bucket.labelKey)),
  )
  // 用户显式选的自定义区间（null = 当前仍在预设档位上）。
  const custom = ref<KxDateRange | null>(null)

  const rangeValue = computed<KxDateRange | null>(
    () => custom.value ?? rangeFromSpanDays(Math.max(1, days.value || 1)),
  )
  const customRange = computed<KxDateRange | null>(() => custom.value)

  function applyRange(range: KxDateRange) {
    const span = spanOf(range)
    if (span >= 1) days.value = span
    // 命中预设 ⇒ 回到 days 口径（清掉自定义标记）；否则记下真实区间。
    custom.value = matchesPreset(presets.value, range) ? null : { start: range.start, end: range.end }
  }

  /** 从 URL/外部注入一个自定义区间（如 board 下钻链接带入的 start/end）。 */
  function setCustomRange(range: KxDateRange | null) {
    custom.value = range ? { start: range.start, end: range.end } : null
    if (range) {
      const span = spanOf(range)
      if (span >= 1) days.value = span
    }
  }

  return { presets, rangeValue, customRange, applyRange, setCustomRange }
}
