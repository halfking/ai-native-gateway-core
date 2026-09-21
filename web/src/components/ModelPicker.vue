<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getAvailableModels,
  type AvailableVersion,
  type AvailableModelsResponse,
  type PopularModel,
} from '../api'

const { t } = useI18n()

type Mode = 'single' | 'multi'

const props = withDefaults(defineProps<{
  modelValue: string | string[]
  mode?: Mode
  placeholder?: string
  disabled?: boolean
  /** 厂商分组预览条数，超出显示「更多…」 */
  vendorPreviewLimit?: number
  title?: string
  /** multi 模式下触发器是否用紧凑计数样式（单行「已选 N」，不铺 chips）。
   *  工具栏内联场景用；默认 false 保持原有的 chips 展示。 */
  compact?: boolean
}>(), {
  mode: 'single',
  placeholder: '选择模型…',
  disabled: false,
  vendorPreviewLimit: 8,
  title: '选择模型',
  compact: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: string | string[]]
}>()

let _cache: AvailableModelsResponse | null = null
let _inflight: Promise<AvailableModelsResponse> | null = null
let _cacheGeneration = 0

async function loadCached(): Promise<AvailableModelsResponse> {
  if (_cache) return _cache
  if (_inflight) return _inflight
  const generation = _cacheGeneration
  const request = getAvailableModels().then((r) => {
    if (generation === _cacheGeneration) _cache = r
    return r
  })
  _inflight = request
  request.then(
    () => { if (_inflight === request) _inflight = null },
    () => { if (_inflight === request) _inflight = null },
  )
  return request
}

const popular = ref<PopularModel[]>([])
const vendorGroups = ref<{ vendor: string; versions: AvailableVersion[] }[]>([])
const loading = ref(false)
const loadErr = ref('')
let modelsGeneration = 0

const mainOpen = ref(false)
const vendorOpen = ref<{ vendor: string; versions: AvailableVersion[] } | null>(null)
const draft = ref<Set<string>>(new Set())

const isMulti = computed(() => props.mode === 'multi')

// R48 §七 + R46 #10 收尾：ModelPicker 的 [value] 守卫。
// 旧实现直接 trust props.modelValue 联合类型 string|string[]，外部传错
// （如 multi 模式收到 string、或 single 模式收到 string[]）会触发 silent
// 吞值（filter 返空、emit 后路由接收空集合）。新增 safeValue 统一收敛：
//   - single 模式 → string（数组/非字符串 → ''）
//   - multi 模式  → string[]（非数组/含非字符串 → []）
// 同时 watch props.modelValue 漂移 → emit 一次 safe value，让父组件归位。
const safeValue = computed<string | string[]>(() => {
  if (isMulti.value) {
    if (Array.isArray(props.modelValue)) {
      // 过滤掉非字符串元素（防御异常输入）
      return props.modelValue.filter((m): m is string => typeof m === 'string')
    }
    return [] as string[]
  }
  // single 模式
  if (typeof props.modelValue === 'string') {
    return props.modelValue
  }
  if (Array.isArray(props.modelValue) && props.modelValue.length > 0 && typeof props.modelValue[0] === 'string') {
    // 兜底：父组件意外传了数组但要求 single——取第一个非空字符串
    return props.modelValue[0]
  }
  return ''
})

const singleValue = computed(() =>
  typeof safeValue.value === 'string' ? safeValue.value : ''
)

const multiValues = computed<string[]>(() =>
  Array.isArray(safeValue.value) ? safeValue.value : []
)

// 守卫 watch：props.modelValue 与 safeValue 不一致时立即 emit 一次纠正值，
// 防止路由上游长期使用错误形态数据。
watch(() => props.modelValue, (current) => {
  if (isMulti.value) {
    if (Array.isArray(current)) {
      // 检查是否有非字符串元素 → 纠正
      const cleaned = current.filter((m): m is string => typeof m === 'string')
      if (cleaned.length !== current.length) {
        emit('update:modelValue', cleaned)
        return
      }
    } else if (current !== undefined && current !== null) {
      // multi 模式收到非数组 → 强制归位为空数组
      emit('update:modelValue', [] as string[])
    }
  } else {
    if (Array.isArray(current)) {
      // single 模式收到数组 → 归位为第一个或空
      emit('update:modelValue', current[0] ?? '')
    }
  }
}, { deep: true })

const triggerLabel = computed(() => {
  if (isMulti.value) {
    if (!multiValues.value.length) return ''
    // R50: 硬编码中文改 i18n（与 461dd883c 同族收尾）——8 个非 zh locale
    // 此前显示中文。
    return t('dashboard.modelsSelectedCount', { n: multiValues.value.length })
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
    _cacheGeneration++
    _cache = null
    _inflight = null
  }
  const generation = ++modelsGeneration
  loading.value = true
  loadErr.value = ''
  try {
    const data = await loadCached()
    if (generation !== modelsGeneration) return
    popular.value = data.popular || []
    vendorGroups.value = buildVendorGroups(data.families)
  } catch (e: unknown) {
    if (generation === modelsGeneration) {
      loadErr.value = e instanceof Error ? e.message : '加载模型失败'
    }
  } finally {
    if (generation === modelsGeneration) loading.value = false
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
    <template v-if="isMulti && compact">
      <button type="button" class="mp-trigger" :disabled="disabled" @click="openMain">
        <span v-if="multiValues.length" class="mp-value">已选 {{ multiValues.length }} 个模型</span>
        <span v-else class="mp-placeholder">{{ placeholder }}</span>
        <span class="mp-actions">
          <span v-if="multiValues.length" class="mp-badge">{{ multiValues.length }}</span>
          <span class="mp-caret">▾</span>
        </span>
      </button>
    </template>

    <template v-else-if="isMulti">
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
  background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent);
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
  background: var(--overlay-strong);
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
  box-shadow: 0 20px 50px var(--overlay-medium);
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
.mp-model:hover { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 15%, transparent); }
.mp-model.chosen {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 15%, transparent);
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
