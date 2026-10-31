<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { fetchAvailableModels, type AvailableVersion, type ModelFamily, type PopularModel } from '@/api/models'
import { t } from '@/i18n'
import AppSheet from '@/components/common/AppSheet.vue'
import { formatApiError } from '@/utils/errors'
import {
  modelDisplayName,
  popularKeys,
  projectFamilies,
  projectPopularModels,
  versionDisplayName,
} from '@/utils/availableModels'

const props = withDefaults(defineProps<{
  modelValue: string | string[]
  mode?: 'single' | 'multi'
  title?: string
  placeholder?: string
  disabled?: boolean
}>(), {
  mode: 'single',
  disabled: false,
})
const emit = defineEmits<{ 'update:modelValue': [value: string | string[]] }>()
const open = ref(false)
const search = ref('')
const debouncedSearch = ref('')
const loading = ref(false)
const error = ref('')
const families = ref<ModelFamily[]>([])
const popular = ref<PopularModel[]>([])
const loaded = ref(false)
const draft = ref<Set<string>>(new Set())
let debounceTimer: ReturnType<typeof setTimeout> | undefined

const isMulti = computed(() => props.mode === 'multi')
const singleValue = computed(() => typeof props.modelValue === 'string' ? props.modelValue : '')
const multiValues = computed(() => Array.isArray(props.modelValue)
  ? [...new Set(props.modelValue.filter((value): value is string => typeof value === 'string'))]
  : [])

watch(search, (value) => {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => { debouncedSearch.value = value }, 150)
})

watch(() => [props.modelValue, props.mode], ([value]) => {
  if (isMulti.value && !Array.isArray(value)) emit('update:modelValue', [])
  else if (isMulti.value && Array.isArray(value)) {
    const cleaned = [...new Set(value.filter((entry): entry is string => typeof entry === 'string'))]
    if (cleaned.length !== value.length) emit('update:modelValue', cleaned)
  } else if (!isMulti.value && Array.isArray(value)) emit('update:modelValue', value.find((entry) => typeof entry === 'string') ?? '')
  if (!open.value) draft.value = new Set(multiValues.value)
}, { deep: true, immediate: true })

onBeforeUnmount(() => { if (debounceTimer) clearTimeout(debounceTimer) })

const popularModels = computed(() => {
  const needle = debouncedSearch.value.trim().toLowerCase()
  return popular.value.filter((model) => !needle ||
    `${model.canonical_name} ${model.display_name ?? ''}`.toLowerCase().includes(needle))
})

const filteredFamilies = computed(() => {
  const needle = debouncedSearch.value.trim().toLowerCase()
  // 已在「常用模型」里出现过的模型不再在家族分组里重复列一遍
  // （与桌面 projectAvailableModels 的「一个模型只出现一次」合并语义一致）。
  const exclude = popularKeys(popularModels.value)
  return projectFamilies({ families: families.value }, exclude).map((family) => {
    const seen = new Set<string>()
    return { ...family, versions: family.versions.filter((version) => {
      const canonical = version.canonical_name.toLowerCase()
      if (seen.has(canonical)) return false
      seen.add(canonical)
      return !needle ||
        `${family.vendor} ${version.canonical_name} ${version.display_name} ${(version.aliases ?? []).join(' ')}`.toLowerCase().includes(needle)
    }) }
  }).filter((family) => family.versions.length > 0)
})

const triggerLabel = computed(() => isMulti.value
  ? (multiValues.value.length ? t('modelPicker.selectedCount', { count: multiValues.value.length }) : '')
  : singleValue.value)

async function load(force = false): Promise<void> {
  if (loading.value || (loaded.value && !force)) return
  loading.value = true
  error.value = ''
  try {
    const response = await fetchAvailableModels()
    families.value = response.families ?? []
    // ★ 2026-10-28：popular 的线上契约是 Go popularModelEntry 对象数组
    // （{canonical_name, display_name, source}，admin/routing.go:2702）——
    // 此前按 string[] 直接消费：插值把整个对象渲染成 JSON 字样（用户看到的
    // 「路由检查选模型是一列 JSON」），点选还会把对象塞进 v-model。
    // 现在统一走 utils/availableModels 的投影：显示 display_name、
    // 选中仍产出 canonical_name（查询接口只认 canonical）。
    popular.value = projectPopularModels(response)
    loaded.value = true
  } catch (reason) {
    error.value = reason instanceof Error ? formatApiError(reason) : t('modelPicker.loadFailed')
  } finally {
    loading.value = false
  }
}

function choose(name: string): void {
  if (isMulti.value) {
    const next = new Set(draft.value)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    draft.value = next
    return
  }
  emit('update:modelValue', name)
  open.value = false
}

function beginSelection(): void {
  if (props.disabled) return
  draft.value = new Set(multiValues.value)
  open.value = true
}

function confirmMulti(): void {
  emit('update:modelValue', [...draft.value].sort((a, b) => a.localeCompare(b)))
  open.value = false
}

function isSelected(name: string): boolean {
  return isMulti.value ? draft.value.has(name) : singleValue.value === name
}

watch(open, (value) => { if (value) void load() })

function displayName(version: AvailableVersion): string {
  return versionDisplayName(version)
}
</script>

<template>
  <button class="model-picker-trigger" type="button" :disabled="disabled" @click="beginSelection">
    {{ triggerLabel || placeholder || t('modelPicker.trigger') }}
  </button>
  <AppSheet v-model="open" presentation="sheet" :title="title || t('modelPicker.title')">
    <div class="model-picker">
      <input v-model="search" class="model-picker__search" type="search" :placeholder="t('modelPicker.search')" />
      <p v-if="error" class="model-picker__error" role="alert">
        {{ error }} <button type="button" class="text-button" @click="load(true)">{{ t('common.retry') }}</button>
      </p>
      <p v-if="loading" class="model-picker__muted">{{ t('common.loading') }}</p>
      <template v-else>
        <section v-if="popularModels.length" class="model-picker__group">
          <h3>{{ t('modelPicker.popular') }}</h3>
          <button v-for="model in popularModels" :key="`popular-${model.canonical_name}`" type="button" class="model-picker__item" :aria-pressed="isSelected(model.canonical_name)" @click="choose(model.canonical_name)">
                       <!-- 显示友好名（display_name），但选中产出 canonical_name ——
 查询接口（/api/routing/resolve）只认 canonical。 -->
            <span>
              <strong>{{ modelDisplayName(model) }}</strong>
              <small v-if="modelDisplayName(model) !== model.canonical_name">{{ model.canonical_name }}</small>
            </span>
            <span v-if="isSelected(model.canonical_name)" class="model-picker__selected">{{ t('modelPicker.selected') }}</span>
          </button>
        </section>
        <section v-for="family in filteredFamilies" :key="family.id" class="model-picker__group">
          <h3>{{ family.vendor || family.display_name }}</h3>
          <button v-for="version in family.versions" :key="version.canonical_name" type="button" class="model-picker__item" :aria-pressed="isSelected(version.canonical_name)" @click="choose(version.canonical_name)">
            <span><strong>{{ displayName(version) }}</strong><small>{{ version.canonical_name }}</small></span>
            <span v-if="isSelected(version.canonical_name)" class="model-picker__selected">{{ t('modelPicker.selected') }}</span>
          </button>
        </section>
        <p v-if="loaded && !popularModels.length && !filteredFamilies.length" class="model-picker__muted">{{ t('common.empty') }}</p>
      </template>
      <footer v-if="isMulti" class="model-picker__footer">
        <span>{{ t('modelPicker.selectedCount', { count: draft.size }) }}</span>
        <button type="button" class="btn" @click="open = false">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn--primary" @click="confirmMulti">{{ t('common.apply') }}</button>
      </footer>
    </div>
  </AppSheet>
</template>

<style scoped>
.model-picker-trigger {
  flex: 0 0 auto;
  max-width: 8rem;
  min-width: 4rem;
  min-height: 48px;
  overflow: hidden;
  padding: 0 0.65rem;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-primary);
  font: inherit;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.model-picker { display: flex; flex-direction: column; gap: var(--app-space-3); }
.model-picker__search { min-height: 48px; padding: 0 var(--app-space-3); border: 1px solid var(--app-border); border-radius: var(--app-radius); background: var(--app-surface); color: var(--app-text); font: inherit; }
.model-picker__group { display: flex; flex-direction: column; gap: var(--app-space-1); }
.model-picker__group h3 { margin: var(--app-space-2) 0; color: var(--app-text-muted); font-size: 0.75rem; font-weight: 700;}
.model-picker__item { display: flex; align-items: center; justify-content: space-between; gap: var(--app-space-2); min-height: 48px; padding: var(--app-space-2) var(--app-space-3); border: 1px solid var(--app-border-subtle); border-radius: var(--app-radius); background: var(--app-surface); color: var(--app-text); text-align: start; }
.model-picker__item > span:first-child { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.model-picker__item small { color: var(--app-text-muted); overflow-wrap: anywhere; }
.model-picker__selected { color: var(--app-primary); font-size: 0.75rem; }
.model-picker__error { color: var(--app-danger); }
.model-picker__muted { color: var(--app-text-muted); text-align: center; }
.model-picker__footer { position: sticky; bottom: 0; display: flex; align-items: center; justify-content: flex-end; gap: var(--app-space-2); padding-top: var(--app-space-2); background: var(--app-surface); }
</style>
