<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BoardPeriodPreset, BoardTimeRange } from '../../utils/boardTimeRange'
import { presetToDays } from '../../utils/boardTimeRange'

const props = defineProps<{
  modelValue: BoardTimeRange
}>()

const emit = defineEmits<{
  'update:modelValue': [value: BoardTimeRange]
  change: [value: BoardTimeRange]
}>()

const { t } = useI18n()

const presets: { id: BoardPeriodPreset; labelKey: string }[] = [
  { id: 'today', labelKey: 'dashboard.range.today' },
  { id: '7d', labelKey: 'dashboard.range.last7d' },
  { id: '30d', labelKey: 'dashboard.range.last30d' },
  { id: 'custom', labelKey: 'dashboard.board.rangeCustomTab' },
]

const customStart = ref(props.modelValue.start ?? '')
const customEnd = ref(props.modelValue.end ?? '')

const isCustom = computed(() => props.modelValue.preset === 'custom')

watch(
  () => props.modelValue,
  (v) => {
    if (v.preset === 'custom') {
      customStart.value = v.start ?? ''
      customEnd.value = v.end ?? ''
    }
  },
  { deep: true },
)

function emitRange(next: BoardTimeRange) {
  emit('update:modelValue', next)
  emit('change', next)
}

function selectPreset(preset: BoardPeriodPreset) {
  if (preset === 'custom') {
    const today = new Date().toISOString().slice(0, 10)
    const start = customStart.value || today
    const end = customEnd.value || today
    customStart.value = start
    customEnd.value = end
    emitRange({ preset: 'custom', days: diffDays(start, end), start, end })
    return
  }
  emitRange({ preset, days: presetToDays(preset) })
}

function applyCustom() {
  if (!customStart.value || !customEnd.value) return
  if (customEnd.value < customStart.value) return
  emitRange({
    preset: 'custom',
    days: diffDays(customStart.value, customEnd.value),
    start: customStart.value,
    end: customEnd.value,
  })
}

function diffDays(start: string, end: string): number {
  const a = Date.parse(`${start}T00:00:00Z`)
  const b = Date.parse(`${end}T00:00:00Z`)
  return Math.max(1, Math.round((b - a) / 86_400_000) + 1)
}
</script>

<template>
  <div class="bps">
    <div class="bps-pills" role="tablist" :aria-label="t('dashboard.board.rangePicker')">
      <button
        v-for="p in presets"
        :key="p.id"
        type="button"
        role="tab"
        class="bps-pill"
        :class="{ active: modelValue.preset === p.id }"
        :aria-selected="modelValue.preset === p.id"
        @click="selectPreset(p.id)"
      >
        {{ t(p.labelKey) }}
      </button>
    </div>
    <div v-if="isCustom" class="bps-custom">
      <label class="bps-date">
        <span>{{ t('dashboard.board.rangeFrom') }}</span>
        <input v-model="customStart" type="date" class="bps-input" @change="applyCustom" />
      </label>
      <span class="bps-sep">—</span>
      <label class="bps-date">
        <span>{{ t('dashboard.board.rangeTo') }}</span>
        <input v-model="customEnd" type="date" class="bps-input" @change="applyCustom" />
      </label>
    </div>
  </div>
</template>

<style scoped>
.bps {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
}
.bps-pills {
  display: inline-flex;
  padding: 3px;
  border-radius: 10px;
  border: 1px solid var(--border);
  background: color-mix(in srgb, var(--bg) 70%, transparent);
  gap: 2px;
}
.bps-pill {
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 600;
  padding: 6px 12px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s, box-shadow 0.15s;
  white-space: nowrap;
}
.bps-pill:hover {
  color: var(--text);
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.bps-pill.active {
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 14%, var(--card));
  box-shadow: 0 1px 2px var(--overlay-faint);
}
.bps-custom {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border-radius: 10px;
  border: 1px solid var(--border);
  background: var(--bg);
}
.bps-date {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--text-muted);
}
.bps-input {
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--card);
  color: var(--text);
  font-size: 12px;
  padding: 4px 8px;
}
.bps-sep {
  color: var(--text-muted);
  font-size: 12px;
}
@media (max-width: 720px) {
  .bps {
    flex-direction: column;
    align-items: stretch;
  }
  .bps-pills {
    width: 100%;
    justify-content: space-between;
  }
  .bps-custom {
    width: 100%;
    flex-wrap: wrap;
  }
}
</style>
