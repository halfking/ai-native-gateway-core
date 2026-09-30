<script setup lang="ts">
// FailureReasons.vue — range-level failure bars. Click a row to filter the model table.
import { useI18n } from 'vue-i18n'
import type { ReasonChip } from './distTypes'
import { fmtInt } from './format'

const { t } = useI18n()

const props = defineProps<{
  items: ReasonChip[]
  active: string | null
}>()

const emit = defineEmits<{ pick: [code: string] }>()

function width(count: number): string {
  const max = props.items[0]?.count ?? 1
  if (!(max > 0)) return '0%'
  return `${(count / max) * 100}%`
}
</script>

<template>
  <div data-testid="failure-reasons">
    <button
      v-for="item in items"
      :key="item.code"
      type="button"
      class="reason"
      :class="{ on: active === item.code }"
      :data-reason="item.code"
      @click="emit('pick', item.code)"
    >
      <span class="code">{{ item.code }}</span>
      <span class="track"><span class="fill" :style="{ width: width(item.count) }" /></span>
      <span class="count">{{ fmtInt(item.count) }}</span>
    </button>
    <p v-if="items.length === 0" class="empty">{{ t('reports.noErrors') }}</p>
  </div>
</template>

<style scoped>
.reason {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  margin: 0 0 8px;
  padding: 4px 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  cursor: pointer;
  text-align: left;
}
.reason.on .code { border-color: var(--accent); color: var(--accent); }
.code {
  flex: none;
  min-width: 132px;
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 2px 8px;
  font-size: 12px;
  text-align: center;
}
.track {
  flex: 1;
  height: 6px;
  border-radius: 3px;
  background: var(--border);
  overflow: hidden;
}
.fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  background: var(--danger);
}
.count { flex: none; min-width: 48px; text-align: right; font-variant-numeric: tabular-nums; }
.empty { color: var(--text-muted, var(--el-text-color-secondary)); font-size: 12px; }
</style>
