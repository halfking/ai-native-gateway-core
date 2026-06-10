<script setup lang="ts">
/**
 * ModelPicker — two-level dropdown (family → version) for selecting models.
 *
 * Single mode (default):
 *   <ModelPicker v-model="modelName" :allow-free-text="true" placeholder="..." />
 *
 * Multi mode (used for featured_models, etc.):
 *   <ModelPicker v-model="modelArray" mode="multi" />
 *
 * Data source: /api/routing/available-models (cached after first load).
 * Featured versions are marked with ★ and listed before non-featured.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  getAvailableModels,
  type AvailableFamily,
  type AvailableVersion,
  type AvailableModelsResponse,
} from '../api'

type Mode = 'single' | 'multi'

const props = withDefaults(defineProps<{
  modelValue: string | string[]
  mode?: Mode
  allowFreeText?: boolean
  placeholder?: string
  disabled?: boolean
}>(), {
  mode: 'single',
  allowFreeText: true,
  placeholder: '选择模型…',
  disabled: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: string | string[]]
}>()

// ── Shared module-level cache so multiple pickers share one fetch ───────
let _cache: AvailableModelsResponse | null = null
let _inflight: Promise<AvailableModelsResponse> | null = null
function clearAvailableModelsCache() {
  _cache = null
  _inflight = null
}

async function loadCached(): Promise<AvailableModelsResponse> {
  if (_cache) return _cache
  if (_inflight) return _inflight
  _inflight = getAvailableModels()
    .then((r) => { _cache = r; return r })
    .finally(() => { _inflight = null })
  return _inflight
}

const families = ref<AvailableFamily[]>([])
const unmapped = ref<string[]>([])
const loading  = ref(false)
const loadErr  = ref('')
const open     = ref(false)
const search   = ref('')
const freeText = ref('')

async function refreshModels(force = false) {
  if (force) clearAvailableModelsCache()
  loading.value = true
  loadErr.value = ''
  try {
    const data = await loadCached()
    families.value = data.families || []
    unmapped.value = data.unmapped || []
  } catch (e: unknown) {
    loadErr.value = e instanceof Error ? e.message : '加载模型失败'
  } finally {
    loading.value = false
  }
}

function onModelsUpdated() {
  void refreshModels(true)
}

onMounted(async () => {
  window.addEventListener('llm-gateway:models-updated', onModelsUpdated)
  await refreshModels()
})

onBeforeUnmount(() => {
  window.removeEventListener('llm-gateway:models-updated', onModelsUpdated)
})

// ── Single mode helpers ────────────────────────────────────────────────
const singleValue = computed(() =>
  typeof props.modelValue === 'string' ? props.modelValue : ''
)

// ── Multi mode helpers ─────────────────────────────────────────────────
const multiValues = computed<string[]>(() =>
  Array.isArray(props.modelValue) ? props.modelValue : []
)

function pickPreferredName(v: AvailableVersion): string {
  return v.canonical_name
}

function isChosen(name: string): boolean {
  if (props.mode === 'multi') return multiValues.value.includes(name)
  return singleValue.value === name
}

function pick(name: string) {
  if (props.mode === 'multi') {
    const cur = new Set(multiValues.value)
    if (cur.has(name)) cur.delete(name)
    else cur.add(name)
    emit('update:modelValue', Array.from(cur))
    freeText.value = ''
    search.value = ''
  } else {
    emit('update:modelValue', name)
    open.value = false
    search.value = ''
  }
}

function removeChip(raw: string) {
  if (props.mode !== 'multi') return
  emit('update:modelValue', multiValues.value.filter((m) => m !== raw))
}

function clear() {
  if (props.mode === 'multi') emit('update:modelValue', [])
  else emit('update:modelValue', '')
  freeText.value = ''
}

// ── Free-text submission ───────────────────────────────────────────────
function submitFreeText() {
  const v = freeText.value.trim()
  if (!v) return
  const first = filteredFamilies.value.flatMap((f) => sortedVersions(f.versions))[0]
  if (first) {
    pick(pickPreferredName(first))
    return
  }
  if (props.mode === 'multi') {
    if (!multiValues.value.includes(v)) {
      emit('update:modelValue', [...multiValues.value, v])
    }
    freeText.value = ''
  } else {
    emit('update:modelValue', v)
    open.value = false
    freeText.value = ''
  }
}

// keep freeText in sync with external single value (so user can edit)
watch(() => props.modelValue, (v) => {
  if (props.mode === 'single' && typeof v === 'string' && v !== freeText.value) {
    freeText.value = v
  }
}, { immediate: true })

// ── Filtered families based on search ──────────────────────────────────
const filteredFamilies = computed<AvailableFamily[]>(() => {
  const q = (props.mode === 'multi' && props.allowFreeText ? freeText.value : search.value).trim().toLowerCase()
  if (!q) return families.value
  return families.value
    .map((f) => {
      const matchesFamily = (
        f.id.toLowerCase().includes(q) ||
        f.display_name.toLowerCase().includes(q) ||
        (f.vendor || '').toLowerCase().includes(q)
      )
      const versions = f.versions.filter((v) =>
        matchesFamily ||
        v.canonical_name.toLowerCase().includes(q) ||
        v.display_name.toLowerCase().includes(q) ||
        (v.raw_names || []).some((r) => r.toLowerCase().includes(q)) ||
        (v.aliases || []).some((a) => a.toLowerCase().includes(q))
      )
      return { ...f, versions }
    })
    .filter((f) => f.versions.length > 0)
})

// Sort: featured first within each family
function sortedVersions(vs: AvailableVersion[]): AvailableVersion[] {
  return [...vs].sort((a, b) => {
    const af = a.featured || false
    const bf = b.featured || false
    if (af !== bf) return af ? -1 : 1
    return a.canonical_name.localeCompare(b.canonical_name)
  })
}

function toggle() {
  if (props.disabled) return
  open.value = !open.value
}

function onInputFocus() {
  open.value = true
}

function closeOnBlur(e: FocusEvent) {
  const next = e.relatedTarget as Node | null
  const wrapper = (e.currentTarget as HTMLElement)
  if (next && wrapper.contains(next)) return
  open.value = false
}
</script>

<template>
  <div class="model-picker" @focusout="closeOnBlur" tabindex="-1">
    <!-- Trigger -->
    <div class="mp-trigger" :class="{ open, disabled }">
      <!-- Multi-mode chips -->
      <template v-if="mode === 'multi'">
        <div class="mp-chips">
          <span v-for="v in multiValues" :key="v" class="mp-chip">
            {{ v }}
            <button type="button" class="mp-chip-x" @click.stop="removeChip(v)" :disabled="disabled">×</button>
          </span>
          <input
            v-if="allowFreeText"
            v-model="freeText"
            class="mp-chip-input"
            :placeholder="multiValues.length ? '' : placeholder"
            :disabled="disabled"
            @keyup.enter="submitFreeText"
            @focus="onInputFocus"
          />
          <button
            v-else
            type="button"
            class="mp-open-btn"
            :disabled="disabled"
            @click="toggle"
          >
            {{ multiValues.length ? `已选 ${multiValues.length}` : placeholder }}
          </button>
        </div>
      </template>

      <!-- Single-mode input/button -->
      <template v-else>
        <input
          v-if="allowFreeText"
          v-model="freeText"
          class="mp-single-input"
          :placeholder="placeholder"
          :disabled="disabled"
          @focus="onInputFocus"
          @keyup.enter="submitFreeText"
        />
        <button
          v-else
          type="button"
          class="mp-open-btn"
          :disabled="disabled"
          @click="toggle"
        >
          {{ singleValue || placeholder }}
        </button>
      </template>

      <button
        v-if="(mode === 'multi' ? multiValues.length : singleValue)"
        type="button"
        class="mp-clear"
        :disabled="disabled"
        title="清空"
        @click.stop="clear"
      >×</button>
      <button
        type="button"
        class="mp-caret"
        :disabled="disabled"
        @click.stop="toggle"
        :aria-label="open ? '收起' : '展开'"
      >▾</button>
    </div>

    <!-- Dropdown panel -->
    <div v-if="open" class="mp-panel">
      <div class="mp-search">
        <input
          v-model="search"
          placeholder="搜索厂商 / 模型 / 别名…"
          @keyup.escape="open = false"
        />
        <button type="button" class="mp-refresh" :disabled="loading" title="刷新模型列表" @click="refreshModels(true)">刷新</button>
      </div>

      <div v-if="loading" class="mp-status">加载中…</div>
      <div v-else-if="loadErr" class="mp-status mp-err">{{ loadErr }}</div>
      <div v-else-if="!filteredFamilies.length" class="mp-status">
        无匹配模型<span v-if="search"> · 关键词「{{ search }}」</span>
      </div>

      <div v-else class="mp-families">
        <div v-for="fam in filteredFamilies" :key="fam.id" class="mp-family">
          <div class="mp-family-title">
            <span>{{ fam.display_name }}</span>
            <span class="mp-vendor">{{ fam.vendor }}</span>
          </div>
          <div class="mp-versions">
            <button
              v-for="v in sortedVersions(fam.versions)"
              :key="v.canonical_name"
              type="button"
              class="mp-version"
              :class="{ chosen: isChosen(pickPreferredName(v)) }"
              :title="`${v.canonical_name} · ${v.provider_count} 个供应商 · ${v.raw_names.join(', ')}`"
              @click="pick(pickPreferredName(v))"
            >
              <span class="mp-star" v-if="v.featured">★</span>
              <span class="mp-name">{{ v.display_name || v.canonical_name }}</span>
              <span class="mp-pcount">×{{ v.provider_count }}</span>
            </button>
          </div>
        </div>
      </div>

      <div v-if="unmapped.length" class="mp-unmapped" :title="unmapped.join('\n')">
        ⚠ {{ unmapped.length }} 个未归类原始模型
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-picker { position: relative; width: 100%; }

.mp-trigger {
  display: flex; align-items: center; gap: 4px;
  border: 1px solid var(--border); border-radius: var(--radius);
  background: var(--card); padding: 4px 6px; min-height: 36px;
}
.mp-trigger.open { border-color: var(--accent); }
.mp-trigger.disabled { opacity: 0.6; cursor: not-allowed; }

.mp-single-input, .mp-chip-input {
  flex: 1; min-width: 120px;
  border: 0; outline: none; background: transparent;
  color: var(--text); font: inherit; padding: 4px 6px;
}
.mp-open-btn {
  flex: 1; text-align: left; border: 0; background: transparent;
  color: var(--text); cursor: pointer; padding: 4px 6px; font: inherit;
}
.mp-open-btn:disabled { cursor: not-allowed; }

.mp-chips { display: flex; flex-wrap: wrap; gap: 4px; flex: 1; align-items: center; }
.mp-chip {
  display: inline-flex; align-items: center; gap: 4px;
  background: rgba(96, 165, 250, 0.15); color: var(--accent);
  border: 1px solid var(--border); border-radius: 999px;
  padding: 2px 8px; font-size: 0.85em;
}
.mp-chip-x {
  border: 0; background: transparent; color: inherit;
  cursor: pointer; font-size: 1em; line-height: 1; padding: 0 2px;
}

.mp-clear, .mp-caret {
  border: 0; background: transparent; color: var(--muted);
  cursor: pointer; padding: 4px 6px; font-size: 0.9em;
}
.mp-caret { font-size: 0.8em; }
.mp-clear:hover, .mp-caret:hover { color: var(--text); }

.mp-panel {
  position: absolute; top: calc(100% + 4px); left: 0; right: 0;
  background: var(--card); border: 1px solid var(--border);
  border-radius: var(--radius); box-shadow: 0 10px 30px rgba(0,0,0,0.35);
  z-index: 50; max-height: 440px; overflow: hidden;
  display: flex; flex-direction: column;
}
.mp-search { display: flex; gap: 6px; padding: 8px; border-bottom: 1px solid var(--border); }
.mp-search input {
  flex: 1; min-width: 0; border: 1px solid var(--border); border-radius: var(--radius);
  background: var(--bg); color: var(--text); padding: 6px 8px; font: inherit;
}
.mp-refresh {
  border: 1px solid var(--border); border-radius: var(--radius);
  background: var(--card); color: var(--text); padding: 6px 10px;
  cursor: pointer; font: inherit; white-space: nowrap;
}
.mp-refresh:disabled { opacity: 0.6; cursor: wait; }
.mp-status { padding: 16px; color: var(--muted); font-size: 0.9em; text-align: center; }
.mp-err { color: var(--danger); }

.mp-families { overflow-y: auto; flex: 1; padding: 4px 0; }
.mp-family { padding: 6px 8px; }
.mp-family + .mp-family { border-top: 1px solid var(--border); }
.mp-family-title {
  display: flex; justify-content: space-between; align-items: baseline;
  font-size: 0.78em; text-transform: uppercase; letter-spacing: 0.04em;
  color: var(--muted); padding: 4px 6px;
}
.mp-vendor { font-size: 0.9em; opacity: 0.7; }

.mp-versions { display: flex; flex-direction: column; gap: 2px; }
.mp-version {
  display: flex; align-items: center; gap: 8px;
  background: transparent; border: 0; color: var(--text);
  padding: 6px 8px; border-radius: var(--radius); cursor: pointer;
  text-align: left; font: inherit;
}
.mp-version:hover { background: rgba(96, 165, 250, 0.10); }
.mp-version.chosen { background: rgba(96, 165, 250, 0.20); color: var(--accent); }
.mp-star { color: var(--warning); font-size: 0.9em; width: 1em; }
.mp-name { flex: 1; }
.mp-pcount { color: var(--muted); font-size: 0.82em; }

.mp-unmapped {
  padding: 6px 12px; border-top: 1px solid var(--border);
  color: var(--warning); font-size: 0.82em; cursor: help;
}
</style>
