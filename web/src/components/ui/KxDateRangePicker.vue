<script setup lang="ts">
// KxDateRangePicker.vue — 全站统一时间范围选择组件（2026-09-30 看板重构轮）。
// 面板形态对齐参考图 2：预设网格（2 列）+ 开始/结束日期输入 + 应用按钮。
// 基于 Element Plus el-popover + el-date-picker 组装，主题走现有 --kx-*/--el-* 令牌桥。
//
// 契约：
// - v-model 仅在「应用」时提交（避免半选状态触发请求）；instant 模式供表单场景即时生效。
// - 预设点击 = 立即回填输入框，仍需应用生效。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElDatePicker, ElPopover } from 'element-plus'
import type { KxDatePrecision, KxDateRange, KxDateRangePreset } from './kx-date-types'
import { makeDateRangePresets, rangeSpanDays, shortRangeLabel } from './kxDatePresets'

const props = withDefaults(
  defineProps<{
    modelValue: KxDateRange | null
    /** 页面可裁剪/扩展预设；不传用默认 8 项 */
    presets?: KxDateRangePreset[]
    precision?: KxDatePrecision
    /** 跨度上限（天），超限禁用应用 */
    maxSpanDays?: number
    /** 表单场景：无应用按钮，预设/日期变更即时提交 */
    instant?: boolean
    disabled?: boolean
    /** 触发器宽度撑满容器 */
    block?: boolean
  }>(),
  {
    precision: 'date',
    maxSpanDays: 0,
    instant: false,
    disabled: false,
    block: false,
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: KxDateRange]
  apply: [value: KxDateRange]
}>()

const { t } = useI18n()

const panelOpen = ref(false)
const draftStart = ref('')
const draftEnd = ref('')

const presets = computed(() => props.presets ?? makeDateRangePresets(props.precision))

const valueFormat = computed(() => (props.precision === 'datetime' ? 'YYYY-MM-DD HH:mm' : 'YYYY-MM-DD'))
const displayFormat = computed(() => (props.precision === 'datetime' ? 'YYYY/MM/DD HH:mm' : 'YYYY/MM/DD'))

const matchedPreset = computed<KxDateRangePreset | null>(() => {
  const value = props.modelValue
  if (!value) return null
  for (const preset of presets.value) {
    const r = preset.resolve()
    if (r.start === value.start && r.end === value.end) return preset
  }
  return null
})

const triggerLabel = computed(() => {
  const value = props.modelValue
  if (!value) return t('common.dateRange.title')
  const presetLabel = matchedPreset.value ? t(matchedPreset.value.labelKey) : t('common.dateRange.custom')
  return `${presetLabel}  ${shortRangeLabel(value)}`
})

const draftInvalid = computed(() => {
  if (!draftStart.value || !draftEnd.value) return false
  if (Date.parse(draftEnd.value.replace(' ', 'T')) < Date.parse(draftStart.value.replace(' ', 'T'))) {
    return 'endBeforeStart'
  }
  return null
})

const draftTooLong = computed(() => {
  if (!draftStart.value || !draftEnd.value || draftInvalid.value) return false
  return props.maxSpanDays > 0 && rangeSpanDays({ start: draftStart.value, end: draftEnd.value }) > props.maxSpanDays
})

const canApply = computed(() => !!draftStart.value && !!draftEnd.value && !draftInvalid.value && !draftTooLong.value)

watch(panelOpen, (open) => {
  if (open) {
    draftStart.value = props.modelValue?.start ?? ''
    draftEnd.value = props.modelValue?.end ?? ''
  }
})

function isPresetActive(preset: KxDateRangePreset): boolean {
  if (!draftStart.value || !draftEnd.value) return false
  const r = preset.resolve()
  return r.start === draftStart.value && r.end === draftEnd.value
}

function selectPreset(preset: KxDateRangePreset) {
  const r = preset.resolve()
  draftStart.value = r.start
  draftEnd.value = r.end
  if (props.instant) {
    commit(r)
    panelOpen.value = false
  }
}

function onDraftChange() {
  if (!props.instant) return
  if (draftStart.value && draftEnd.value && canApply.value) {
    commit({ start: draftStart.value, end: draftEnd.value })
  }
}

function commit(range: KxDateRange) {
  emit('update:modelValue', range)
  emit('apply', range)
}

function applyDraft() {
  if (!canApply.value) return
  commit({ start: draftStart.value, end: draftEnd.value })
  panelOpen.value = false
}
</script>

<template>
  <ElPopover
    v-model:visible="panelOpen"
    trigger="click"
    placement="bottom-start"
    :width="344"
    :disabled="disabled"
    popper-class="kx-dr-popper"
  >
    <template #reference>
      <button
        type="button"
        class="kx-dr-trigger"
        :class="{ 'kx-dr-trigger--block': block }"
        :disabled="disabled"
        :aria-label="t('common.dateRange.openAria')"
        aria-haspopup="dialog"
      >
        <svg class="kx-dr-trigger__icon" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <rect x="1.5" y="2.5" width="13" height="12" rx="2.5" stroke="currentColor" stroke-width="1.4" />
          <path d="M4 1v3M12 1v3M1.5 6h13" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
        </svg>
        <span class="kx-dr-trigger__label">{{ triggerLabel }}</span>
        <span class="kx-dr-trigger__caret" aria-hidden="true">▾</span>
      </button>
    </template>

    <div class="kx-dr-panel">
      <div class="kx-dr-panel__head">
        <span class="kx-dr-panel__title">{{ t('common.dateRange.title') }}</span>
      </div>
      <div v-if="presets.length" class="kx-dr-presets" role="listbox" :aria-label="t('common.dateRange.title')">
        <button
          v-for="preset in presets"
          :key="preset.id"
          type="button"
          class="kx-dr-preset"
          :class="{ active: isPresetActive(preset) }"
          :aria-selected="isPresetActive(preset)"
          @click="selectPreset(preset)"
        >
          {{ t(preset.labelKey) }}
        </button>
      </div>
      <div v-if="presets.length" class="kx-dr-divider" aria-hidden="true"></div>
      <div class="kx-dr-inputs">
        <div class="kx-dr-input">
          <label class="kx-dr-input__label">{{ t('common.dateRange.startDate') }}</label>
          <ElDatePicker
            v-model="draftStart"
            :type="precision === 'datetime' ? 'datetime' : 'date'"
            :value-format="valueFormat"
            :format="displayFormat"
            :clearable="false"
            :aria-label="t('common.dateRange.startDate')"
            style="width: 100%"
            @change="onDraftChange"
          />
        </div>
        <div class="kx-dr-input">
          <label class="kx-dr-input__label">{{ t('common.dateRange.endDate') }}</label>
          <ElDatePicker
            v-model="draftEnd"
            :type="precision === 'datetime' ? 'datetime' : 'date'"
            :value-format="valueFormat"
            :format="displayFormat"
            :clearable="false"
            :aria-label="t('common.dateRange.endDate')"
            style="width: 100%"
            @change="onDraftChange"
          />
        </div>
      </div>
      <div v-if="!instant" class="kx-dr-foot">
        <span v-if="draftInvalid === 'endBeforeStart'" class="kx-dr-hint kx-dr-hint--error">
          {{ t('common.dateRange.endBeforeStart') }}
        </span>
        <span v-else-if="draftTooLong" class="kx-dr-hint kx-dr-hint--error">
          {{ t('common.dateRange.spanTooLong', { n: maxSpanDays }) }}
        </span>
        <span v-else class="kx-dr-hint" aria-hidden="true"></span>
        <button type="button" class="kx-dr-apply" :disabled="!canApply" @click="applyDraft">
          {{ t('common.button.apply') }}
        </button>
      </div>
    </div>
  </ElPopover>
</template>

<style scoped>
.kx-dr-trigger {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  max-width: 100%;
  padding: 6px 12px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--card);
  color: var(--text);
  font-size: 12.5px;
  font-weight: 600;
  cursor: pointer;
  transition: border-color 0.15s;
}
.kx-dr-trigger:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
}
.kx-dr-trigger:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
.kx-dr-trigger--block {
  width: 100%;
}
.kx-dr-trigger__icon {
  width: 13px;
  height: 13px;
  flex-shrink: 0;
  color: var(--accent);
}
.kx-dr-trigger__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.kx-dr-trigger__caret {
  flex-shrink: 0;
  font-size: 10px;
  color: var(--text-muted);
}
</style>

<style>
/* 面板随 popper 挂 body，需全局样式（仍全令牌）。 */
.kx-dr-popper.kx-dr-popper {
  --el-popper-border-radius: 14px;
}
.kx-dr-panel {
  padding: 2px 4px;
}
.kx-dr-panel__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.kx-dr-panel__title {
  font-size: 12.5px;
  font-weight: 700;
  color: var(--text-muted);
}
.kx-dr-presets {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 7px;
}
.kx-dr-preset {
  border: 1px solid transparent;
  background: color-mix(in srgb, var(--text-muted) 9%, transparent);
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
  padding: 7px 0;
  border-radius: 8px;
  cursor: pointer;
  transition: border-color 0.15s, color 0.15s, background 0.15s;
}
.kx-dr-preset:hover {
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
}
.kx-dr-preset.active {
  background: var(--bg-subtle);
  color: var(--accent);
  border-color: color-mix(in srgb, var(--accent) 45%, transparent);
}
.kx-dr-divider {
  height: 1px;
  background: var(--border);
  margin: 12px 0;
}
.kx-dr-inputs {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.kx-dr-input__label {
  display: block;
  font-size: 11px;
  color: var(--text-muted);
  margin-bottom: 4px;
  font-weight: 600;
}
.kx-dr-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 13px;
}
.kx-dr-hint {
  font-size: 10.5px;
  color: var(--text-muted);
}
.kx-dr-hint--error {
  color: var(--danger);
}
.kx-dr-apply {
  border: 0;
  border-radius: 8px;
  padding: 6px 16px;
  font-size: 12px;
  font-weight: 700;
  background: var(--accent);
  color: var(--on-primary);
  cursor: pointer;
  transition: background 0.15s, opacity 0.15s;
}
.kx-dr-apply:hover:not(:disabled) {
  background: var(--accent-h);
}
.kx-dr-apply:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
@media (max-width: 480px) {
  .kx-dr-presets {
    grid-template-columns: 1fr 1fr;
  }
  .kx-dr-inputs {
    grid-template-columns: 1fr;
  }
}
</style>
