import { computed, type Ref } from 'vue'
import type { TagInfo, TagNamespaceGroup } from '../api'

export interface DynamicNamespaceTag extends TagInfo {
  disabled: boolean
}

export interface DynamicNamespaceGroup {
  namespace: string
  tags: DynamicNamespaceTag[]
}

interface UseDynamicNamespaceFiltersOptions<T> {
  items: Ref<T[]>
  namespaceGroups: Ref<TagNamespaceGroup[]>
  activeTags: Ref<string[]>
  search: Ref<string>
  vendor: Ref<string>
  getTags: (item: T) => string[]
  matchesSearch: (item: T, query: string) => boolean
  matchesVendor: (item: T, vendor: string) => boolean
  singleSelectNamespaces?: Set<string>
}

function tagNamespace(tag: string): string {
  const idx = tag.indexOf(':')
  return idx >= 0 ? tag.slice(0, idx) : 'other'
}

export function useDynamicNamespaceFilters<T>(options: UseDynamicNamespaceFiltersOptions<T>) {
  function matchesTags(item: T, tags: string[]): boolean {
    const itemTags = options.getTags(item)
    return tags.every((tag) => itemTags.includes(tag))
  }

  function filterItems(source: T[], filters: { vendor?: string; search?: string; tags?: string[] } = {}) {
    return source.filter((item) => (
      options.matchesVendor(item, filters.vendor ?? '') &&
      options.matchesSearch(item, filters.search ?? '') &&
      matchesTags(item, filters.tags ?? [])
    ))
  }

  function buildTagCounts(source: T[], namespace: string): Map<string, number> {
    const counts = new Map<string, number>()
    for (const item of source) {
      const seen = new Set<string>()
      for (const tag of options.getTags(item)) {
        if (tagNamespace(tag) !== namespace || seen.has(tag)) continue
        seen.add(tag)
        counts.set(tag, (counts.get(tag) ?? 0) + 1)
      }
    }
    return counts
  }

  function toggleTag(tag: string) {
    const namespace = tagNamespace(tag)
    if (options.activeTags.value.includes(tag)) {
      options.activeTags.value = options.activeTags.value.filter((item) => item !== tag)
      return
    }
    const retained = options.singleSelectNamespaces?.has(namespace)
      ? options.activeTags.value.filter((item) => tagNamespace(item) !== namespace)
      : options.activeTags.value
    options.activeTags.value = [...retained, tag]
  }

  const tagFiltered = computed(() => filterItems(options.items.value, { tags: options.activeTags.value }))
  const filteredByVendor = computed(() => filterItems(tagFiltered.value, { vendor: options.vendor.value }))
  const filtered = computed(() => filterItems(filteredByVendor.value, { search: options.search.value }))

  const namespaceOptions = computed<DynamicNamespaceGroup[]>(() => {
    const baseItems = filterItems(options.items.value, { vendor: options.vendor.value, search: options.search.value })
    return options.namespaceGroups.value.map((group) => {
      const retainedTags = options.activeTags.value.filter((tag) => tagNamespace(tag) !== group.namespace)
      const candidateItems = filterItems(baseItems, { tags: retainedTags })
      const counts = buildTagCounts(candidateItems, group.namespace)
      const tags = group.tags.map((tagInfo) => ({
        ...tagInfo,
        count: counts.get(tagInfo.tag) ?? 0,
        disabled: !counts.has(tagInfo.tag) && !options.activeTags.value.includes(tagInfo.tag),
      }))
      return { ...group, tags }
    })
  })

  return {
    filterItems,
    filtered,
    filteredByVendor,
    namespaceOptions,
    tagFiltered,
    tagNamespace,
    toggleTag,
  }
}