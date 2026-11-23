<script setup lang="ts">
// StatsDateRangePicker.vue — 统计类页面的**统一**时间范围选择器
// （2026-11-24 统一轮）。
//
// ★ 契约（一处定义，三处使用）：
//   ① 控件 = 全站统一的 KxDateRangePicker；
//   ② **固定档位**：今天 / 昨天 / 近 7 天 / 近 14 天 / 近 30 天 / 本月 / 上月
//      （= makeDateRangePresets('date')，与看板同一套）；
//   ③ **自定义起止**保留（端点已支持显式日历窗口，见 admin/dashboardapi 的
//      共享窗口层 dashboard_window.go ⇒ 显示的区间 = 数据窗口）。
//
// ★ 为什么抽成组件而不是每页各写一份：
//   本轮之前「看板」「会话与统计」「会话分析·用户」三处各写一套预设
//   （会话统计只有 4 个滚动档位且禁自定义；会话分析只有近 7/30 天两个档位）
//   ⇒ 同一个「日期范围」在三处指的不是同一段区间。
//   ⇒ 收敛到一个组件后：**改一次，三处同步**，且档位形态天然一致。
import { computed } from 'vue'
import KxDateRangePicker from '../ui/KxDateRangePicker.vue'
import { makeDateRangePresets } from '../ui/kxDatePresets'
import type { KxDateRange } from '../ui/kx-date-types'

const props = withDefaults(
  defineProps<{
    modelValue: KxDateRange | null
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:modelValue': [value: KxDateRange]
  apply: [value: KxDateRange]
}>()

// 固定档位：与看板页同一工厂（今天/昨天/近7/14/30天/本月/上月）。
const presets = computed(() => makeDateRangePresets('date'))
</script>

<template>
  <!-- ★★ 2026-11-24（统一轮 E2E 锚点）：包一层**只带 testid 的 inline-flex 壳**。
       · 为什么需要壳：E2E 要断言的是「**这一处用的是共享封装 StatsDateRangePicker**」，
         而不只是「页面上有个日期控件」——KxDateRangePicker 在别的页面也被直接使用。
       · 为什么不用「给组件传 attr」：KxDateRangePicker 的根元素就是那个**带 aria-label
         的按钮** ⇒ attr 落到按钮上 ⇒ 被 aria-label 遮蔽 ⇒ Maestro 的 `id:` 选不到。
       · 壳是 inline-flex ⇒ **不改布局**（原来按钮就是父容器里的那个 flex item）。
       · ★ 点壳的中心 = 点按钮 ⇒ E2E 可直接 `tapOn: id: stats-date-range-picker` 开面板。 -->
  <span class="stats-date-range-picker" data-testid="stats-date-range-picker">
    <KxDateRangePicker
      :model-value="props.modelValue"
      :presets="presets"
      :max-span-days="92"
      :disabled="props.disabled"
      @update:model-value="emit('update:modelValue', $event)"
      @apply="emit('apply', $event)"
    />
  </span>
</template>

<style scoped>
.stats-date-range-picker {
  display: inline-flex;
}
</style>
