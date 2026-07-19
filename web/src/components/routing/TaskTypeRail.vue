<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { TASK_TYPES } from '../../api-autoroute'

const props = defineProps<{
  modelValue: string
  counts: Record<string, number>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const { t } = useI18n()

const totalCount = computed(() =>
  Object.values(props.counts).reduce((sum, n) => sum + n, 0),
)

function select(key: string) {
  emit('update:modelValue', key)
}
</script>

<template>
  <aside class="task-rail" aria-label="task types">
    <button
      type="button"
      class="rail-item"
      :class="{ active: modelValue === '' }"
      :title="t('routingDefault.rail.allHint')"
      @click="select('')"
    >
      <span class="rail-icon">◇</span>
      <span class="rail-label">{{ t('routingDefault.rail.all') }}</span>
      <span v-if="totalCount" class="rail-badge">{{ totalCount }}</span>
    </button>
    <button
      v-for="task in TASK_TYPES"
      :key="task.key"
      type="button"
      class="rail-item"
      :class="{ active: modelValue === task.key }"
      :title="task.label"
      @click="select(task.key)"
    >
      <span class="rail-icon">{{ task.icon }}</span>
      <span class="rail-label">{{ task.label }}</span>
      <span v-if="counts[task.key]" class="rail-badge">{{ counts[task.key] }}</span>
    </button>
  </aside>
</template>

<style scoped>
.task-rail {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 148px;
  max-width: 180px;
  padding: 8px;
  border-right: 1px solid var(--border, #e5e7eb);
  overflow-y: auto;
}
.rail-item {
  display: grid;
  grid-template-columns: 28px 1fr auto;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 8px 10px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--text, #1f2937);
  text-align: left;
  cursor: pointer;
}
.rail-item:hover {
  background: var(--bg-muted, #f3f4f6);
}
.rail-item.active {
  background: var(--bg-accent-soft, #eef2ff);
  border-color: var(--border-accent, #c7d2fe);
}
.rail-icon {
  font-size: 16px;
  line-height: 1;
  text-align: center;
}
.rail-label {
  font-size: 13px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.rail-badge {
  min-width: 18px;
  padding: 0 6px;
  border-radius: 999px;
  background: var(--bg-muted, #e5e7eb);
  font-size: 11px;
  text-align: center;
}
@media (max-width: 860px) {
  .task-rail {
    flex-direction: row;
    max-width: none;
    min-width: 0;
    border-right: none;
    border-bottom: 1px solid var(--border, #e5e7eb);
    overflow-x: auto;
  }
  .rail-item {
    min-width: 110px;
  }
}
</style>
