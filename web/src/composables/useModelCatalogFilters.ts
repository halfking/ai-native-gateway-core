import { computed, ref, type Ref } from 'vue'
import { matchesModelCatalogSearch } from '../utils/modelCatalog'

export interface CatalogFilterStatusOption {
  value: string
  label: string
}

export interface UseModelCatalogFiltersOptions<T> {
  items: Ref<T[]>
  getVendor: (item: T) => string
  getCanonicalName: (item: T) => string
  getDisplayName: (item: T) => string
  /** Extra strings for search (family, tags, modality, …) */
  getSearchExtras?: (item: T) => string[]
  /** Client-side status filter; omit when status is server-driven only */
  matchExtra?: (item: T, extraFilter: string) => boolean
}

export function useModelCatalogFilters<T>(options: UseModelCatalogFiltersOptions<T>) {
  const pickedModel = ref('')
  const filterVendor = ref('')
  const extraFilter = ref('')
  const textSearch = ref('')

  const vendorOptions = computed(() => {
    const set = new Set<string>()
    for (const item of options.items.value) {
      const v = options.getVendor(item)?.trim()
      if (v) set.add(v)
    }
    if (filterVendor.value) set.add(filterVendor.value)
    return [...set].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  })

  const filtered = computed(() => {
    let rows = options.items.value
    const vendor = filterVendor.value.trim()
    if (vendor) {
      rows = rows.filter((item) => options.getVendor(item) === vendor)
    }
    const extra = extraFilter.value.trim()
    if (extra && options.matchExtra) {
      rows = rows.filter((item) => options.matchExtra!(item, extra))
    }
    const pick = pickedModel.value
    const text = textSearch.value
    if (!pick.trim() && !text.trim()) return rows
    return rows.filter((item) =>
      matchesModelCatalogSearch(
        options.getCanonicalName(item),
        options.getDisplayName(item),
        options.getVendor(item),
        pick,
        text,
        options.getSearchExtras?.(item) ?? [],
      ),
    )
  })

  const hasActiveFilters = computed(() =>
    Boolean(
      pickedModel.value.trim() ||
      filterVendor.value.trim() ||
      extraFilter.value.trim() ||
      textSearch.value.trim(),
    ),
  )

  function clearFilters() {
    pickedModel.value = ''
    filterVendor.value = ''
    extraFilter.value = ''
    textSearch.value = ''
  }

  return {
    pickedModel,
    filterVendor,
    extraFilter,
    textSearch,
    vendorOptions,
    filtered,
    hasActiveFilters,
    clearFilters,
  }
}
