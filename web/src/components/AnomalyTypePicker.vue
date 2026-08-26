<script setup lang="ts">
import { computed, ref } from 'vue'

export interface AnomalyTypeOption {
  value: string
  label: string
  description?: string
}

const props = withDefaults(defineProps<{
  modelValue: string
  options: AnomalyTypeOption[]
  placeholder?: string
  disabled?: boolean
  title?: string
}>(), {
  placeholder: '选择异常类型…',
  disabled: false,
  title: '选择异常类型',
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const open = ref(false)
const search = ref('')

const selectedLabel = computed(() => {
  const found = props.options.find((o) => o.value === props.modelValue)
  return found?.label || ''
})

const filteredOptions = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter((o) => {
    return o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q)
  })
})

function openDialog() {
  if (props.disabled) return
  open.value = true
  search.value = ''
}

function closeDialog() {
  open.value = false
}

function pick(value: string) {
  emit('update:modelValue', value)
  closeDialog()
}

function clearValue() {
  emit('update:modelValue', '')
}
</script>

<template>
  <div class="type-picker" :class="{ disabled }">
    <button type="button" class="tp-trigger" :disabled="disabled" @click="openDialog">
      <span v-if="selectedLabel" class="tp-value">{{ selectedLabel }}</span>
      <span v-else class="tp-placeholder">{{ placeholder }}</span>
      <span class="tp-actions">
        <button
          v-if="modelValue"
          type="button"
          class="tp-clear"
          :disabled="disabled"
          title="清空"
          @click.stop="clearValue"
        >×</button>
        <span class="tp-caret">▾</span>
      </span>
    </button>

    <Teleport to="body">
      <div v-if="open" class="tp-overlay" @click.self="closeDialog">
        <div class="tp-dialog" role="dialog" :aria-label="title" @click.stop>
          <header class="tp-header">
            <h3 class="tp-title">{{ title }}</h3>
            <button type="button" class="tp-close" aria-label="关闭" @click="closeDialog">×</button>
          </header>

          <div class="tp-toolbar">
            <input v-model="search" class="tp-search" placeholder="搜索异常类型" />
          </div>

          <div class="tp-body">
            <div v-if="!filteredOptions.length" class="tp-status">没有匹配的异常类型</div>
            <div v-else class="tp-list">
              <button
                v-for="option in filteredOptions"
                :key="option.value || '__all__'"
                type="button"
                class="tp-item"
                :class="{ chosen: modelValue === option.value }"
                @click="pick(option.value)"
              >
                <span class="tp-name">{{ option.label }}</span>
                <span v-if="option.description" class="tp-meta">{{ option.description }}</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.type-picker { width: 100%; }
.type-picker.disabled { opacity: 0.6; pointer-events: none; }
.tp-trigger {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-height: 36px;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-subtle);
  color: var(--text);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.tp-trigger:not(:disabled):hover { border-color: var(--accent); }
.tp-placeholder { color: var(--muted); flex: 1; }
.tp-value { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tp-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.tp-caret { color: var(--muted); font-size: 0.85em; }
.tp-clear {
  border: 0;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  font-size: 1.1em;
  line-height: 1;
  padding: 0 4px;
}
.tp-clear:hover { color: var(--text); }
.tp-overlay {
  position: fixed;
  inset: 0;
  z-index: 1300;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 16px;
}
.tp-dialog {
  width: min(640px, 100%);
  max-height: min(82vh, 720px);
  display: flex;
  flex-direction: column;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.35);
  overflow: hidden;
}
.tp-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.tp-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}
.tp-close {
  border: 0;
  background: transparent;
  color: var(--muted);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 0 4px;
}
.tp-close:hover { color: var(--text); }
.tp-toolbar { padding: 12px 16px 0; }
.tp-search {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg);
  color: var(--text);
  padding: 8px 10px;
}
.tp-body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 16px 16px;
}
.tp-status {
  text-align: center;
  color: var(--muted);
  padding: 32px 16px;
}
.tp-list {
  display: grid;
  grid-template-columns: 1fr;
  gap: 8px;
}
.tp-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg);
  color: var(--text);
  padding: 10px 12px;
  text-align: left;
  cursor: pointer;
}
.tp-item:hover {
  border-color: var(--accent);
  background: rgba(96, 165, 250, 0.08);
}
.tp-item.chosen {
  border-color: var(--accent);
  background: rgba(96, 165, 250, 0.18);
  color: var(--accent);
}
.tp-name {
  font-size: 13px;
  font-weight: 600;
}
.tp-meta {
  font-size: 12px;
  color: var(--muted);
}
</style>
