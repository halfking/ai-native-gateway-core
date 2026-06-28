<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getProviders, type Provider } from '../api'

const props = withDefaults(defineProps<{
  modelValue: string
  placeholder?: string
  disabled?: boolean
  title?: string
}>(), {
  placeholder: '选择供应商…',
  disabled: false,
  title: '选择供应商',
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const open = ref(false)
const loading = ref(false)
const loadErr = ref('')
const search = ref('')
const providers = ref<Provider[]>([])

const triggerLabel = computed(() => props.modelValue || '')

const filteredProviders = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return providers.value
  return providers.value.filter((p) => {
    const name = (p.display_name || '').toLowerCase()
    const code = (p.catalog_code || '').toLowerCase()
    return name.includes(q) || code.includes(q)
  })
})

async function loadProviders() {
  loading.value = true
  loadErr.value = ''
  try {
    providers.value = await getProviders()
  } catch (e: unknown) {
    loadErr.value = e instanceof Error ? e.message : '加载供应商失败'
  } finally {
    loading.value = false
  }
}

function openDialog() {
  if (props.disabled) return
  open.value = true
  search.value = ''
  if (!providers.value.length) {
    void loadProviders()
  }
}

function closeDialog() {
  open.value = false
}

function pickProvider(provider: Provider) {
  emit('update:modelValue', provider.catalog_code || provider.display_name || '')
  closeDialog()
}

function clearValue() {
  emit('update:modelValue', '')
}

onMounted(() => {
  void loadProviders()
})
</script>

<template>
  <div class="provider-picker" :class="{ disabled }">
    <button type="button" class="pp-trigger" :disabled="disabled" @click="openDialog">
      <span v-if="triggerLabel" class="pp-value">{{ triggerLabel }}</span>
      <span v-else class="pp-placeholder">{{ placeholder }}</span>
      <span class="pp-actions">
        <button
          v-if="modelValue"
          type="button"
          class="pp-clear"
          :disabled="disabled"
          title="清空"
          @click.stop="clearValue"
        >×</button>
        <span class="pp-caret">▾</span>
      </span>
    </button>

    <Teleport to="body">
      <div v-if="open" class="pp-overlay" @click.self="closeDialog">
        <div class="pp-dialog" role="dialog" :aria-label="title" @click.stop>
          <header class="pp-header">
            <h3 class="pp-title">{{ title }}</h3>
            <button type="button" class="pp-close" aria-label="关闭" @click="closeDialog">×</button>
          </header>

          <div class="pp-toolbar">
            <input v-model="search" class="pp-search" placeholder="搜索供应商名称或代码" />
          </div>

          <div class="pp-body">
            <div v-if="loading" class="pp-status">加载中…</div>
            <div v-else-if="loadErr" class="pp-status pp-err">{{ loadErr }}</div>
            <div v-else-if="!filteredProviders.length" class="pp-status">没有匹配的供应商</div>
            <div v-else class="pp-list">
              <button
                v-for="provider in filteredProviders"
                :key="provider.id"
                type="button"
                class="pp-item"
                :class="{ chosen: modelValue === (provider.catalog_code || provider.display_name) }"
                @click="pickProvider(provider)"
              >
                <span class="pp-name">{{ provider.display_name }}</span>
                <span class="pp-meta">{{ provider.catalog_code || '—' }}</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.provider-picker { width: 100%; }
.provider-picker.disabled { opacity: 0.6; pointer-events: none; }
.pp-trigger {
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
.pp-trigger:not(:disabled):hover { border-color: var(--accent); }
.pp-placeholder { color: var(--muted); flex: 1; }
.pp-value { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pp-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.pp-caret { color: var(--muted); font-size: 0.85em; }
.pp-clear {
  border: 0;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  font-size: 1.1em;
  line-height: 1;
  padding: 0 4px;
}
.pp-clear:hover { color: var(--text); }
.pp-overlay {
  position: fixed;
  inset: 0;
  z-index: 1300;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 16px;
}
.pp-dialog {
  width: min(680px, 100%);
  max-height: min(82vh, 720px);
  display: flex;
  flex-direction: column;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.35);
  overflow: hidden;
}
.pp-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  border-bottom: 1px solid var(--border);
}
.pp-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}
.pp-close {
  border: 0;
  background: transparent;
  color: var(--muted);
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  padding: 0 4px;
}
.pp-close:hover { color: var(--text); }
.pp-toolbar { padding: 12px 16px 0; }
.pp-search {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg);
  color: var(--text);
  padding: 8px 10px;
}
.pp-body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 16px 16px;
}
.pp-status {
  text-align: center;
  color: var(--muted);
  padding: 32px 16px;
}
.pp-err { color: var(--danger); }
.pp-list {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.pp-item {
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
.pp-item:hover {
  border-color: var(--accent);
  background: rgba(96, 165, 250, 0.08);
}
.pp-item.chosen {
  border-color: var(--accent);
  background: rgba(96, 165, 250, 0.18);
  color: var(--accent);
}
.pp-name {
  font-size: 13px;
  font-weight: 600;
}
.pp-meta {
  font-size: 12px;
  color: var(--muted);
}
@media (max-width: 900px) {
  .pp-list {
    grid-template-columns: 1fr;
  }
}
</style>
