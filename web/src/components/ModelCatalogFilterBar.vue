<script setup lang="ts">
import ModelPicker from './ModelPicker.vue'
import type { CatalogFilterStatusOption } from '../composables/useModelCatalogFilters'

withDefaults(defineProps<{
  pickedModel: string
  filterVendor: string
  extraFilter?: string
  textSearch?: string
  vendorOptions: string[]
  count: number
  pickerTitle?: string
  pickerPlaceholder?: string
  vendorLabel?: string
  statusOptions?: CatalogFilterStatusOption[] | null
  statusLabel?: string
  statusSelectClass?: string
  showTextSearch?: boolean
  textSearchPlaceholder?: string
  showClear?: boolean
}>(), {
  extraFilter: '',
  textSearch: '',
  pickerTitle: '选择标准模型',
  pickerPlaceholder: '搜索标准模型…',
  vendorLabel: '全部厂家',
  statusOptions: null,
  statusLabel: '全部',
  statusSelectClass: 'cf-source',
  showTextSearch: false,
  textSearchPlaceholder: 'family / 标签…',
  showClear: true,
})

const emit = defineEmits<{
  'update:pickedModel': [value: string]
  'update:filterVendor': [value: string]
  'update:extraFilter': [value: string]
  'update:textSearch': [value: string]
  clear: []
  statusChange: [value: string]
}>()

function onPickedModel(v: string | string[]) {
  emit('update:pickedModel', typeof v === 'string' ? v : (v[0] ?? ''))
}

function onVendor(e: Event) {
  emit('update:filterVendor', (e.target as HTMLSelectElement).value)
}

function onExtra(e: Event) {
  const value = (e.target as HTMLSelectElement).value
  emit('update:extraFilter', value)
  emit('statusChange', value)
}

function onText(e: Event) {
  emit('update:textSearch', (e.target as HTMLInputElement).value)
}
</script>

<template>
  <div class="compact-filter-bar model-catalog-filter-bar">
    <div class="cf-grow" style="min-width:200px">
      <ModelPicker
        :model-value="pickedModel"
        :placeholder="pickerPlaceholder"
        :title="pickerTitle"
        @update:model-value="onPickedModel"
      />
    </div>
    <select
      class="cf-select"
      :value="filterVendor"
      @change="onVendor"
    >
      <option value="">{{ vendorLabel }}</option>
      <option v-for="v in vendorOptions" :key="v" :value="v">{{ v }}</option>
    </select>
    <select
      v-if="statusOptions && statusOptions.length"
      class="cf-select"
      :class="statusSelectClass"
      :value="extraFilter"
      @change="onExtra"
    >
      <option value="">{{ statusLabel }}</option>
      <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
        {{ opt.label }}
      </option>
    </select>
    <input
      v-if="showTextSearch"
      class="cf-input cf-grow"
      type="search"
      :value="textSearch"
      :placeholder="textSearchPlaceholder"
      @input="onText"
    />
    <button
      v-if="showClear"
      type="button"
      class="btn btn-ghost btn-sm"
      @click="emit('clear')"
    >
      清空
    </button>
    <span class="cf-meta">共 {{ count }} 个</span>
  </div>
</template>
