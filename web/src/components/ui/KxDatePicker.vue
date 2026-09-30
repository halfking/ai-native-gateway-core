<script setup lang="ts">
// KxDatePicker.vue — 单值日期选择（date / datetime / month），KxDateRangePicker 的同族组件。
// 2026-09-30 统一日历轮：供表单行内编辑（过期时间/单日过滤/月选择）等单值场景，
// 与范围组件共享同一视觉体系（EP 令牌桥），替代裸 input[type=date/month] 与 datetime-local。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElDatePicker } from 'element-plus'

const props = withDefaults(
  defineProps<{
    modelValue: string
    type?: 'date' | 'datetime' | 'month'
    placeholder?: string
    disabled?: boolean
    clearable?: boolean
  }>(),
  { type: 'date', clearable: true },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
  change: [value: string]
}>()

const { t } = useI18n()

const valueFormat = computed(() => {
  if (props.type === 'month') return 'YYYY-MM'
  if (props.type === 'datetime') return 'YYYY-MM-DD HH:mm'
  return 'YYYY-MM-DD'
})

const displayFormat = computed(() => {
  if (props.type === 'month') return 'YYYY/MM'
  if (props.type === 'datetime') return 'YYYY/MM/DD HH:mm'
  return 'YYYY/MM/DD'
})

const fallbackPlaceholder = computed(() => t('common.dateRange.pickDate'))

function onInput(v: string | null) {
  const next = v ?? ''
  emit('update:modelValue', next)
  emit('change', next)
}
</script>

<template>
  <ElDatePicker
    :model-value="modelValue || undefined"
    :type="type"
    :value-format="valueFormat"
    :format="displayFormat"
    :placeholder="placeholder ?? fallbackPlaceholder"
    :disabled="disabled"
    :clearable="clearable"
    class="kx-date-picker"
    @update:model-value="onInput"
  />
</template>

<style scoped>
.kx-date-picker {
  width: 100%;
}
</style>
