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
  <KxDateRangePicker
    :model-value="props.modelValue"
    :presets="presets"
    :max-span-days="92"
    :disabled="props.disabled"
    @update:model-value="emit('update:modelValue', $event)"
    @apply="emit('apply', $event)"
  />
</template>
