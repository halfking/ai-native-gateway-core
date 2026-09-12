<script setup lang="ts">
/**
 * FilterBar.vue — 声明式筛选栏（2026-09-13，方案 §4.5.4）。
 *
 * 声明 definitions（select / search / daterange）+ v-model:filters，
 * 内部整合 FilterInput（search 提供建议词时）与 ActiveFilterChips。
 *
 * 响应式行为（方案口径）：
 * - <1024（useBreakpoint().isMobile）：控件纵向堆叠 + 搜索按钮全宽；
 * - <768（isSmall）：默认折叠为「筛选（N）」展开面板，N 为已生效条件数；
 *   折叠入口文案走 common.button.filter 词条。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import FilterInput from '../FilterInput.vue'
import ActiveFilterChips from '../ActiveFilterChips.vue'
import type { FilterChip } from '../../composables/useFilterChips'
import { useFilterChips } from '../../composables/useFilterChips'
import { useBreakpoint } from '../../composables/useBreakpoint'
import type { FilterDefinition } from './filter-types'

const props = defineProps<{
  definitions: FilterDefinition[]
  modelValue: Record<string, string>
  /** 搜索按钮加载态 */
  loading?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, string>]
  search: []
  clear: []
}>()

const { t } = useI18n()
const { isMobile, isSmall } = useBreakpoint()

const expanded = ref(false)

function setValue(key: string, v: string) {
  emit('update:modelValue', { ...props.modelValue, [key]: v })
}

const activeCount = computed(() => chips.value.length)

function makeChip(key: string, label: string, value: string): FilterChip {
  return {
    key,
    label: `${label}: ${value}`,
    onRemove: () => setValue(key, ''),
  }
}

const chips = useFilterChips(() =>
  props.definitions.flatMap((def) => {
    if (def.type === 'daterange') {
      const fk = def.fromKey ?? `${def.key}From`
      const tk = def.toKey ?? `${def.key}To`
      const fv = props.modelValue[fk]?.trim()
      const tv = props.modelValue[tk]?.trim()
      return [
        fv ? makeChip(fk, def.fromLabel ?? def.label ?? fk, fv) : null,
        tv ? makeChip(tk, def.toLabel ?? def.label ?? tk, tv) : null,
      ]
    }
    const v = props.modelValue[def.key]?.trim()
    return v ? [makeChip(def.key, def.label ?? def.key, v)] : null
  }),
)

function onSelectOptions(def: FilterDefinition): { label: string; value: string }[] {
  return (def.options ?? []).map((o) => (typeof o === 'string' ? { label: o, value: o } : o))
}

function onSearch() {
  emit('search')
}
</script>

<template>
  <div class="filter-bar" :class="{ 'filter-bar--stacked': isMobile }">
    <!-- <768 折叠入口（>=768 由 CSS 隐藏） -->
    <button
      v-if="isSmall"
      type="button"
      class="btn btn-ghost btn-sm filter-bar__toggle"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      {{ t('common.button.filter') }}（{{ activeCount }}）
      <span class="filter-bar__chevron" :class="{ 'filter-bar__chevron--open': expanded }" aria-hidden="true">▾</span>
    </button>

    <!-- <768 折叠面板：v-if 销毁/重建（控件值由 v-model 承载，无状态丢失） -->
    <div v-if="!isSmall || expanded" class="filter-bar__panel">
      <div class="filter-bar__fields">
        <template v-for="def in definitions" :key="def.key">
          <label v-if="def.type === 'select'" class="filter-bar__field">
            <span v-if="def.label" class="filter-bar__label">{{ def.label }}</span>
            <select
              class="input filter-bar__control"
              :value="modelValue[def.key] ?? ''"
              @change="setValue(def.key, ($event.target as HTMLSelectElement).value)"
            >
              <option v-if="def.placeholder" value="">{{ def.placeholder }}</option>
              <option v-for="o in onSelectOptions(def)" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
          </label>

          <label v-else-if="def.type === 'daterange'" class="filter-bar__field filter-bar__field--range">
            <span v-if="def.fromLabel" class="filter-bar__label">{{ def.fromLabel }}</span>
            <input
              type="datetime-local"
              class="input filter-bar__control"
              :value="modelValue[def.fromKey ?? `${def.key}From`] ?? ''"
              @change="setValue(def.fromKey ?? `${def.key}From`, ($event.target as HTMLInputElement).value)"
            />
            <span v-if="def.toLabel" class="filter-bar__label">{{ def.toLabel }}</span>
            <input
              type="datetime-local"
              class="input filter-bar__control"
              :value="modelValue[def.toKey ?? `${def.key}To`] ?? ''"
              @change="setValue(def.toKey ?? `${def.key}To`, ($event.target as HTMLInputElement).value)"
            />
          </label>

          <label v-else class="filter-bar__field">
            <span v-if="def.label" class="filter-bar__label">{{ def.label }}</span>
            <FilterInput
              v-if="def.suggestions?.length"
              class="filter-bar__control"
              :model-value="modelValue[def.key] ?? ''"
              :options="def.suggestions"
              :placeholder="def.placeholder"
              @update:model-value="(v: string) => setValue(def.key, v)"
            />
            <input
              v-else
              type="text"
              class="input filter-bar__control"
              :placeholder="def.placeholder"
              :value="modelValue[def.key] ?? ''"
              @input="setValue(def.key, ($event.target as HTMLInputElement).value)"
              @keyup.enter="onSearch"
            />
          </label>
        </template>

        <div class="filter-bar__actions">
          <button type="button" class="btn btn-primary btn-sm filter-bar__search" :disabled="loading" @click="onSearch">
            {{ t('common.button.search') }}
          </button>
          <button
            v-if="activeCount > 0"
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="loading"
            @click="emit('update:modelValue', Object.fromEntries(Object.keys(modelValue).map((k) => [k, '']))); emit('clear')"
          >
            {{ t('common.button.clear') }}
          </button>
        </div>
      </div>

      <ActiveFilterChips class="filter-bar__chips" :chips="chips" />
    </div>
  </div>
</template>

<style scoped>
.filter-bar {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-2);
}

.filter-bar__fields {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: var(--kx-space-2) var(--kx-space-3);
}

.filter-bar__field {
  display: inline-flex;
  align-items: center;
  gap: var(--kx-space-2);
  min-width: 0;
}
.filter-bar__field--range {
  flex-wrap: wrap;
}
.filter-bar__label {
  font-size: 12px;
  color: var(--muted, var(--text-muted));
  white-space: nowrap;
  font-weight: 600;
}
.filter-bar__control {
  min-width: 0;
}

.filter-bar__actions {
  display: inline-flex;
  gap: var(--kx-space-2);
  margin-inline-start: auto;
}

/* <1024：控件纵向堆叠 + 搜索按钮全宽（布局决策口径与 AppTopbar 一致） */
@media (max-width: 1023px) {
  .filter-bar__fields {
    flex-direction: column;
    align-items: stretch;
  }
  .filter-bar__actions {
    margin-inline-start: 0;
  }
  .filter-bar__search {
    width: 100%;
  }
}

/* >=768 隐藏折叠入口（<768 由 v-if 渲染） */
@media (min-width: 769px) {
  .filter-bar__toggle {
    display: none;
  }
}
</style>
