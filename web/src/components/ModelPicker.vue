<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  getAvailableModels,
  type AvailableVersion,
  type AvailableModelsResponse,
  type PopularModel,
} from '../api'

type Mode = 'single' | 'multi'

const props = withDefaults(defineProps<{
  modelValue: string | string[]
  mode?: Mode
  placeholder?: string
  disabled?: boolean
  /** 厂商分组预览条数，超出显示「更多…」 */
  vendorPreviewLimit?: number
  title?: string
}>(), {
  mode: 'single',
  placeholder: '选择模型…',
  disabled: false,
  vendorPreviewLimit: 8,
  title: '选择模型',
})

const emit = defineEmits<{
  'update:modelValue': [value: string | string[]]
}>()

let _cache: AvailableModelsResponse | null = null
let _inflight: Promise<AvailableModelsResponse> | null = null

async function loadCached(): Promise<AvailableModelsResponse> {
  if (_cache) return _cache
  if (_inflight) return _inflight
  _inflight = getAvailableModels()
    .then((r) => { _cache = r; return r })
    .finally(() => { _inflight = null })
  return _inflight
}

const popular = ref<PopularModel[]>([])
const vendorGroups = ref<{ vendor: string; versions: AvailableVersion[] }[]>([])
const loading = ref(false)
const loadErr = ref('')

const mainOpen = ref(false)
const vendorOpen = ref<{ vendor: string; versions: AvailableVersion[] } | null>(null)
const draft = ref<Set<string>>(new Set())

const isMulti = computed(() => props.mode === 'multi')

const singleValue = computed(() =>
  typeof props.modelValue === 'string' ? props.modelValue : ''
)

const multiValues = computed<string[]>(() =>
  Array.isArray(props.modelValue) ? props.modelValue : []
)

const triggerLabel = computed(() => {
  if (isMulti.value) {
    if (!multiValues.value.length) return ''
    return `已选 ${multiValues.value.length} 个模型`
  }
  return singleValue.value
})

function displayName(v: { canonical_name: string; display_name?: string }): string {
  return v.display_name || v.canonical_name
}

function dedupeVersions(versions: AvailableVersion[]): AvailableVersion[] {
  const seen = new Set<string>()
  const out: AvailableVersion[] = []
  for (const v of versions) {
    const key = v.canonical_name.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(v)
  }
  out.sort((a, b) => a.canonical_name.localeCompare(b.canonical_name))
  return out
}

function buildVendorGroups(families: AvailableModelsResponse['families']) {
  const map = new Map<string, AvailableVersion[]>()
  for (const fam of families || []) {
    const vendor = fam.vendor || fam.display_name || '其他'
    const cur = map.get(vendor) || []
    cur.push(...(fam.versions || []))
    map.set(vendor, cur)
  }
  return Array.from(map.entries())
    .map(([vendor, versions]) => ({ vendor, versions: dedupeVersions(versions) }))
    .filter((g) => g.versions.length > 0)
    .sort((a, b) => a.vendor.localeCompare(b.vendor, 'zh-CN'))
}

async function refreshModels(force = false) {
  if (force) {
    _cache = null
    _inflight = null
  }
  loading.value = true
  loadErr.value = ''
  try {
    const data = await loadCached()
    popular.value = data.popular || []
    vendorGroups.value = buildVendorGroups(data.families)
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

function syncDraftFromValue() {
  draft.value = new Set(isMulti.value ? multiValues.value : [])
}

function openMain() {
  if (props.disabled) return
  syncDraftFromValue()
  vendorOpen.value = null
  mainOpen.value = true
  if (!popular.value.length && !vendorGroups.value.length) {
    void refreshModels()
  }
}

function closeMain() {
  mainOpen.value = false
  vendorOpen.value = null
}

function openVendor(group: { vendor: string; versions: AvailableVersion[] }) {
  vendorOpen.value = group
}

function closeVendor() {
  vendorOpen.value = null
}

function isSelected(name: string): boolean {
  if (isMulti.value) return draft.value.has(name)
  return singleValue.value === name
}

function emitSingle(name: string) {
  emit('update:modelValue', name)
}

function pickSingle(name: string) {
  emitSingle(name)
  vendorOpen.value = null
  mainOpen.value = false
}

function toggleDraft(name: string) {
  const next = new Set(draft.value)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  draft.value = next
}

function onPick(name: string) {
  if (isMulti.value) {
    toggleDraft(name)
    return
  }
  pickSingle(name)
}

function confirmMulti() {
  emit('update:modelValue', Array.from(draft.value).sort((a, b) => a.localeCompare(b)))
  closeMain()
}

function cancelMulti() {
  closeMain()
}

function clearSingle() {
  emitSingle('')
}

function removeMultiChip(name: string) {
  emit('update:modelValue', multiValues.value.filter((m) => m !== name))
}

function previewVersions(versions: AvailableVersion[]) {
  return versions.slice(0, props.vendorPreviewLimit)
}

function hasMore(versions: AvailableVersion[]) {
  return versions.length > props.vendorPreviewLimit
}

watch(() => props.modelValue, () => {
  if (!mainOpen.value) syncDraftFromValue()
}, { deep: true })
</script>

<template>
  <div class="model-picker" :class="{ disabled }">
    <template v-if="isMulti">
      <div class="mp-trigger mp-trigger--multi" @click="openMain">
        <div v-if="multiValues.length" class="mp-chips" @click.stop>
          <span v-for="v in multiValues" :key="v" class="mp-chip">
            {{ v }}
            <button type="button" class="mp-chip-x" :disabled="disabled" @click.stop="removeMultiChip(v)">×</button>
          </span>
        </div>
        <span v-else class="mp-placeholder">{{ placeholder }}</span>
        <button type="button" class="mp-open-btn" :disabled="disabled" @click.stop="openMain">
          {{ multiValues.length ? '编辑' : '选择' }}
        </button>
      </div>
    </template>

    <template v-else>
      <button type="button" class="mp-trigger" :disabled="disabled" @click="openMain">
        <span v-if="triggerLabel" class="mp-value">{{ triggerLabel }}</span>
        <span v-else class="mp-placeholder">{{ placeholder }}</span>
        <span class="mp-actions">
          <button
            v-if="singleValue"
            type="button"
            class="mp-clear"
            :disabled="disabled"
            title="清空"
            @click.stop="clearSingle"
          >×</button>
          <span class="mp-caret">▾</span>
        </span>
      </button>
    </template>

    <!-- 主图层 -->
    <Teleport to="body">
      <div v-if="mainOpen" class="mp-overlay" @click.self="isMulti ? cancelMulti() : closeMain()">
        <div class="mp-dialog" role="dialog" :aria-label="title" @click.stop>
          <header class="mp-header">
            <h3 class="mp-title">{{ title }}</h3>
            <button type="button" class="mp-close" aria-label="关闭" @click="isMulti ? cancelMulti() : closeMain()">×</button>
          </header>

          <div class="mp-body">
            <div v-if="loading" class="mp-status">加载中…</div>
            <div v-else-if="loadErr" class="mp-status mp-err">{{ loadErr }}</div>
            <template v-else>
              <section v-if="popular.length" class="mp-section">
                <h4 class="mp-section-title">热门模型</h4>
                <div class="mp-grid">
                  <button
                    v-for="m in popular"
                    :key="'pop-' + m.canonical_name"
                    type="button"
                    class="mp-model"
                    :class="{ chosen: isSelected(m.canonical_name) }"
                    @click="onPick(m.canonical_name)"
                  >
                    <span class="mp-star">★</span>
                    <span class="mp-model-name">{{ displayName(m) }}</span>
                    <span v-if="m.count != null" class="mp-badge">{{ m.count }}</span>
                  </button>
                </div>
              </section>

              <section
                v-for="group in vendorGroups"
                :key="group.vendor"
                class="mp-section"
              >
                <h4 class="mp-section-title">{{ group.vendor }}</h4>
                <div class="mp-grid">
                  <button
                    v-for="v in previewVersions(group.versions)"
                    :key="group.vendor + '-' + v.canonical_name"
                    type="button"
                    class="mp-model"
                    :class="{ chosen: isSelected(v.canonical_name) }"
                    @click="onPick(v.canonical_name)"
                  >
                    <span v-if="v.featured" class="mp-star">★</span>
                    <span class="mp-model-name">{{ displayName(v) }}</span>
                  </button>
                  <button
                    v-if="hasMore(group.versions)"
                    type="button"
                    class="mp-model mp-more"
                    @click="openVendor(group)"
                  >
                    更多… ({{ group.versions.length }})
                  </button>
                </div>
              </section>
            </template>
          </div>

          <footer v-if="isMulti" class="mp-footer">
            <span class="mp-footer-hint">已选 {{ draft.size }} 个</span>
            <div class="mp-footer-actions">
              <button type="button" class="btn btn-ghost btn-sm" @click="cancelMulti">取消</button>
              <button type="button" class="btn btn-primary btn-sm" @click="confirmMulti">确认</button>
            </div>
          </footer>
        </div>
      </div>

      <!-- 厂商全量图层 -->
      <div v-if="vendorOpen" class="mp-overlay mp-overlay--nested" @click.self="closeVendor">
        <div class="mp-dialog mp-dialog--vendor" role="dialog" :aria-label="vendorOpen.vendor" @click.stop>
          <header class="mp-header">
            <h3 class="mp-title">{{ vendorOpen.vendor }}</h3>
            <button type="button" class="mp-close" @click="closeVendor">×</button>
          </header>
          <div class="mp-body">
            <div class="mp-grid">
              <button
                v-for="v in vendorOpen.versions"
                :key="'full-' + v.canonical_name"
                type="button"
                class="mp-model"
                :class="{ chosen: isSelected(v.canonical_name) }"
                @click="onPick(v.canonical_name)"
              >
                <span v-if="v.featured" class="mp-star">★</span>
                <span class="mp-model-name">{{ displayName(v) }}</span>
              </button>
            </div>
          </div>
          <footer v-if="isMulti" class="mp-footer mp-footer--compact">
            <button type="button" class="btn btn-ghost btn-sm" @click="closeVendor">返回</button>
          </footer>
          <footer v-else class="mp-footer mp-footer--compact">
            <button type="button" class="btn btn-ghost btn-sm" @click="closeVendor">返回</button>
          </footer>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.model-picker { width: 100%; }
.model-picker.disabled { opacity: 0.6; pointer-events: none; }

.mp-trigger {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-height: 36px;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--card);
  color: var(--text);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.mp-trigger--multi {
  flex-wrap: wrap;
  cursor: default;
}
.mp-trigger:not(:disabled):hover { border-color: var(--accent); }

.mp-placeholder { color: var(--muted); flex: 1; }
.mp-value { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mp-actions { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
.mp-caret { color: var(--muted); font-size: 0.85em; }
.mp-clear {
  border: 0; background: transparent; color: var(--muted);
  cursor: pointer; font-size: 1.1em; line-height: 1; padding: 0 4px;
}
.mp-clear:hover { color: var(--text); }

.mp-chips { display: flex; flex-wrap: wrap; gap: 4px; flex: 1; min-width: 0; }
.mp-chip {
  display: inline-flex; align-items: center; gap: 4px;
  background: rgba(96, 165, 250, 0.12); color: var(--accent);
  border: 1px solid var(--border); border-radius: 999px;
  padding: 2px 8px; font-size: 12px; max-width: 100%;
}
.mp-chip-x { border: 0; background: transparent; cursor: pointer; color: inherit; padding: 0 2px; }
.mp-open-btn {
  border: 1px solid var(--border); border-radius: var(--radius);
  background: var(--bg); color: var(--text); padding: 4px 10px;
  font-size: 12px; cursor: pointer; flex-shrink: 0;
}

.mp-overlay {
  position: fixed; inset: 0; z-index: 1300;
  background: rgba(0, 0, 0, 0.5);
  display: flex; align-items: center; justify-content: center;
  padding: 24px 16px;
}
.mp-overlay--nested { z-index: 1310; }

.mp-dialog {
  width: min(720px, 100%);
  max-height: min(85vh, 720px);
  display: flex; flex-direction: column;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.35);
  overflow: hidden;
}
.mp-dialog--vendor { width: min(640px, 100%); }

.mp-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 14px 16px; border-bottom: 1px solid var(--border); flex-shrink: 0;
}
.mp-title { margin: 0; font-size: 16px; font-weight: 600; }
.mp-close {
  border: 0; background: transparent; color: var(--muted);
  font-size: 22px; line-height: 1; cursor: pointer; padding: 0 4px;
}
.mp-close:hover { color: var(--text); }

.mp-body {
  flex: 1; overflow-y: auto; padding: 12px 16px 16px;
}
.mp-status { text-align: center; color: var(--muted); padding: 32px 16px; }
.mp-err { color: var(--danger); }

.mp-section + .mp-section { margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--border); }
.mp-section-title {
  margin: 0 0 10px; font-size: 12px; font-weight: 600;
  color: var(--muted); text-transform: uppercase; letter-spacing: 0.04em;
}

.mp-grid {
  display: flex; flex-wrap: wrap; gap: 8px;
}
.mp-model {
  display: inline-flex; align-items: center; gap: 6px;
  border: 1px solid var(--border); border-radius: 999px;
  background: var(--bg); color: var(--text);
  padding: 6px 12px; font-size: 13px; cursor: pointer;
  max-width: 100%;
}
.mp-model:hover { border-color: var(--accent); background: rgba(96, 165, 250, 0.08); }
.mp-model.chosen {
  border-color: var(--accent);
  background: rgba(96, 165, 250, 0.18);
  color: var(--accent);
}
.mp-model.mp-more {
  border-style: dashed; color: var(--muted); font-size: 12px;
}
.mp-star { color: var(--warning); font-size: 12px; }
.mp-model-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mp-badge {
  font-size: 11px; color: var(--muted);
  border: 1px solid var(--border); border-radius: 999px; padding: 0 6px;
}

.mp-footer {
  display: flex; align-items: center; justify-content: space-between;
  gap: 12px; padding: 12px 16px; border-top: 1px solid var(--border);
  flex-shrink: 0; background: var(--card);
}
.mp-footer--compact { justify-content: flex-end; }
.mp-footer-hint { font-size: 12px; color: var(--muted); }
.mp-footer-actions { display: flex; gap: 8px; }
</style>
