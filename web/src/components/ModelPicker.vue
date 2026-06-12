<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  getAvailableModels,
  type AvailableFamily,
  type AvailableVersion,
  type AvailableModelsResponse,
  type PopularModel,
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
const popular = ref<PopularModel[]>([])
const unmapped = ref<string[]>([])
const loading = ref(false)
const loadErr = ref('')
const open = ref(false)
const freeText = ref('')
const triggerRef = ref<HTMLElement | null>(null)
const panelStyle = ref<Record<string, string>>({})

async function refreshModels(force = false) {
  if (force) clearAvailableModelsCache()
  loading.value = true
  loadErr.value = ''
  try {
    const data = await loadCached()
    families.value = data.families || []
    popular.value = data.popular || []
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

function updatePanelPosition() {
  const el = triggerRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const width = Math.max(rect.width, 320)
  const maxHeight = Math.min(440, window.innerHeight - rect.bottom - 16)
  panelStyle.value = {
    position: 'fixed',
    top: `${rect.bottom + 4}px`,
    left: `${rect.left}px`,
    width: `${width}px`,
    maxHeight: `${Math.max(maxHeight, 200)}px`,
    zIndex: '1201',
  }
}

async function openPanel() {
  if (props.disabled) return
  open.value = true
  await nextTick()
  updatePanelPosition()
}

function closePanel() {
  open.value = false
}

function toggle() {
  if (open.value) closePanel()
  else void openPanel()
}

function onWindowChange() {
  if (open.value) updatePanelPosition()
}

onMounted(async () => {
  window.addEventListener('llm-gateway:models-updated', onModelsUpdated)
  window.addEventListener('resize', onWindowChange)
  window.addEventListener('scroll', onWindowChange, true)
  await refreshModels()
})

onBeforeUnmount(() => {
  window.removeEventListener('llm-gateway:models-updated', onModelsUpdated)
  window.removeEventListener('resize', onWindowChange)
  window.removeEventListener('scroll', onWindowChange, true)
})

const singleValue = computed(() =>
  typeof props.modelValue === 'string' ? props.modelValue : ''
)

const multiValues = computed<string[]>(() =>
  Array.isArray(props.modelValue) ? props.modelValue : []
)

const query = computed(() => freeText.value.trim().toLowerCase())

function displayName(v: AvailableVersion | PopularModel): string {
  return ('display_name' in v && v.display_name) ? v.display_name : v.canonical_name
}

function matchesQuery(name: string, display: string, extras: string[] = []): boolean {
  const q = query.value
  if (!q) return true
  if (name.toLowerCase().includes(q)) return true
  if (display.toLowerCase().includes(q)) return true
  return extras.some((x) => x.toLowerCase().includes(q))
}

function isChosen(name: string): boolean {
  if (props.mode === 'multi') return multiValues.value.includes(name)
  return singleValue.value === name
}

function emitSingle(value: string) {
  emit('update:modelValue', value)
}

function pick(name: string) {
  if (props.mode === 'multi') {
    const cur = new Set(multiValues.value)
    if (cur.has(name)) cur.delete(name)
    else cur.add(name)
    emit('update:modelValue', Array.from(cur))
    freeText.value = ''
  } else {
    emitSingle(name)
    freeText.value = name
    closePanel()
  }
}

function removeChip(raw: string) {
  if (props.mode !== 'multi') return
  emit('update:modelValue', multiValues.value.filter((m) => m !== raw))
}

function clear() {
  if (props.mode === 'multi') emit('update:modelValue', [])
  else emitSingle('')
  freeText.value = ''
}

function commitFreeText() {
  const v = freeText.value.trim()
  if (!v) return
  if (props.mode === 'multi') {
    if (!multiValues.value.includes(v)) {
      emit('update:modelValue', [...multiValues.value, v])
    }
    freeText.value = ''
  } else {
    emitSingle(v)
    closePanel()
  }
}

function onInputChange() {
  if (!open.value) void openPanel()
}

function onInputKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    closePanel()
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    commitFreeText()
  }
}

watch(() => props.modelValue, (v) => {
  if (props.mode === 'single' && typeof v === 'string' && v !== freeText.value) {
    freeText.value = v
  }
}, { immediate: true })

const inputMatchesPopular = computed(() => {
  const q = query.value
  if (!q) return true
  return popular.value.some((m) =>
    matchesQuery(m.canonical_name, m.display_name)
  )
})

const displayedPopular = computed(() => {
  if (!inputMatchesPopular.value) return []
  return popular.value
})

const vendorGroups = computed(() => {
  const q = query.value
  const groups = new Map<string, { vendor: string; versions: AvailableVersion[] }>()
  for (const fam of families.value) {
    const vendor = fam.vendor || fam.display_name || '其他'
    const versions = fam.versions.filter((v) => {
      if (!q) return true
      return matchesQuery(
        v.canonical_name,
        v.display_name,
        [vendor, fam.display_name, ...(v.raw_names || []), ...(v.aliases || [])],
      )
    })
    if (!versions.length) continue
    const cur = groups.get(vendor) || { vendor, versions: [] }
    cur.versions.push(...versions)
    groups.set(vendor, cur)
  }
  return Array.from(groups.values())
    .map((g) => {
      g.versions.sort((a, b) => a.canonical_name.localeCompare(b.canonical_name))
      return g
    })
    .sort((a, b) => a.vendor.localeCompare(b.vendor))
})

function sortedVersions(vs: AvailableVersion[]): AvailableVersion[] {
  return [...vs].sort((a, b) => {
    const af = a.featured || false
    const bf = b.featured || false
    if (af !== bf) return af ? -1 : 1
    return a.canonical_name.localeCompare(b.canonical_name)
  })
}

const hasSuggestions = computed(() =>
  displayedPopular.value.length > 0 || vendorGroups.value.length > 0
)
</script>

<template>
  <div class="model-picker" ref="triggerRef">
    <div class="mp-trigger" :class="{ open, disabled }">
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
            @input="onInputChange"
            @keydown="onInputKeydown"
            @focus="openPanel"
          />
          <button v-else type="button" class="mp-open-btn" :disabled="disabled" @click="toggle">
            {{ multiValues.length ? `已选 ${multiValues.length}` : placeholder }}
          </button>
        </div>
      </template>

      <template v-else>
        <input
          v-if="allowFreeText"
          v-model="freeText"
          class="mp-single-input"
          :placeholder="placeholder"
          :disabled="disabled"
          autocomplete="off"
          spellcheck="false"
          @input="onInputChange"
          @keydown="onInputKeydown"
          @focus="openPanel"
        />
        <button v-else type="button" class="mp-open-btn" :disabled="disabled" @click="toggle">
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
      <button type="button" class="mp-caret" :disabled="disabled" @click.stop="toggle" :aria-label="open ? '收起' : '展开'">▾</button>
    </div>

    <Teleport to="body">
      <div v-if="open" class="mp-backdrop" @click="closePanel" />
      <div v-if="open" class="mp-panel" :style="panelStyle" @mousedown.prevent>
        <div v-if="loading" class="mp-status">加载中…</div>
        <div v-else-if="loadErr" class="mp-status mp-err">{{ loadErr }}</div>
        <div v-else-if="!hasSuggestions" class="mp-status">
          无匹配模型<span v-if="query"> · 关键词「{{ freeText.trim() }}」</span>
          <div v-if="allowFreeText && query" class="mp-hint">按 Enter 使用输入值</div>
        </div>

        <div v-else class="mp-scroll">
          <div v-if="displayedPopular.length" class="mp-section">
            <div class="mp-section-title">热门模型</div>
            <div class="mp-versions">
              <button
                v-for="m in displayedPopular"
                :key="m.canonical_name"
                type="button"
                class="mp-version"
                :class="{ chosen: isChosen(m.canonical_name) }"
                @click="pick(m.canonical_name)"
              >
                <span class="mp-star">★</span>
                <span class="mp-name">{{ displayName(m) }}</span>
                <span v-if="m.count != null" class="mp-pill">{{ m.count }}</span>
                <span v-else class="mp-pill">{{ m.source === 'usage' ? '用量' : '策略' }}</span>
              </button>
            </div>
          </div>

          <div v-for="group in vendorGroups" :key="group.vendor" class="mp-section">
            <div class="mp-section-title">{{ group.vendor }}</div>
            <div class="mp-versions">
              <button
                v-for="v in sortedVersions(group.versions)"
                :key="v.canonical_name"
                type="button"
                class="mp-version"
                :class="{ chosen: isChosen(v.canonical_name) }"
                :title="`${v.canonical_name} · ${v.provider_count} 个供应商`"
                @click="pick(v.canonical_name)"
              >
                <span class="mp-star" v-if="v.featured">★</span>
                <span class="mp-name">{{ displayName(v) }}</span>
                <span class="mp-pcount">×{{ v.provider_count }}</span>
              </button>
            </div>
          </div>
        </div>

        <div v-if="unmapped.length" class="mp-unmapped" :title="unmapped.join('\n')">
          ⚠ {{ unmapped.length }} 个未归类原始模型
        </div>
      </div>
    </Teleport>
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
  flex: 1; min-width: 120px; border: 0; outline: none; background: transparent;
  color: var(--text); font: inherit; padding: 4px 6px;
}
.mp-open-btn {
  flex: 1; text-align: left; border: 0; background: transparent;
  color: var(--text); cursor: pointer; padding: 4px 6px; font: inherit;
}
.mp-chips { display: flex; flex-wrap: wrap; gap: 4px; flex: 1; align-items: center; }
.mp-chip {
  display: inline-flex; align-items: center; gap: 4px;
  background: rgba(96, 165, 250, 0.15); color: var(--accent);
  border: 1px solid var(--border); border-radius: 999px; padding: 2px 8px; font-size: 0.85em;
}
.mp-chip-x { border: 0; background: transparent; color: inherit; cursor: pointer; }
.mp-clear, .mp-caret { border: 0; background: transparent; color: var(--muted); cursor: pointer; padding: 4px 6px; }
.mp-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.35); z-index: 1200; }
.mp-panel {
  background: var(--card); border: 1px solid var(--border); border-radius: var(--radius);
  box-shadow: 0 10px 30px rgba(0,0,0,0.35); overflow: hidden; display: flex; flex-direction: column;
}
.mp-status { padding: 16px; color: var(--muted); font-size: 0.9em; text-align: center; }
.mp-err { color: var(--danger); }
.mp-hint { margin-top: 8px; font-size: 0.85em; }
.mp-scroll { overflow-y: auto; flex: 1; padding: 4px 0; }
.mp-section { padding: 6px 8px; }
.mp-section + .mp-section { border-top: 1px solid var(--border); }
.mp-section-title { font-size: 0.78em; color: var(--muted); padding: 4px 6px 6px; }
.mp-versions { display: flex; flex-direction: column; gap: 2px; }
.mp-version {
  display: flex; align-items: center; gap: 8px; background: transparent; border: 0;
  color: var(--text); padding: 6px 8px; border-radius: var(--radius); cursor: pointer; text-align: left; font: inherit;
}
.mp-version:hover { background: rgba(96, 165, 250, 0.10); }
.mp-version.chosen { background: rgba(96, 165, 250, 0.20); color: var(--accent); }
.mp-star { color: var(--warning); width: 1em; }
.mp-name { flex: 1; }
.mp-pcount, .mp-pill { color: var(--muted); font-size: 0.82em; }
.mp-pill { border: 1px solid var(--border); border-radius: 999px; padding: 0 6px; }
.mp-unmapped { padding: 6px 12px; border-top: 1px solid var(--border); color: var(--warning); font-size: 0.82em; }
</style>
