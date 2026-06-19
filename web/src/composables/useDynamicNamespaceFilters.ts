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
  /**
   * Return the model's family id (e.g. 'anthropic-claude'). Used as a fallback
   * when matching `family:<id>` tags: if the tag isn't in `getTags(item)` but
   * `getFamily(item) === <id>`, the item still matches. Also injected as a
   * virtual tag into the `family` namespace tag-counts so the advanced
   * namespace filter shows the correct count even when
   * `models_canonical.tags` doesn't contain `family:<id>` (2026-06-20
   * incident: id=103 claude-sonnet-4 had family='anthropic' but tags='{}').
   */
  getFamily?: (item: T) => string | null | undefined
}

function tagNamespace(tag: string): string {
  const idx = tag.indexOf(':')
  return idx >= 0 ? tag.slice(0, idx) : 'other'
}

export function useDynamicNamespaceFilters<T>(options: UseDynamicNamespaceFiltersOptions<T>) {
  function virtualFamilyTag(item: T): string | null {
    if (!options.getFamily) return null
    const family = options.getFamily(item)
    if (!family) return null
    return `family:${family}`
  }

  function matchesTags(item: T, tags: string[]): boolean {
    const itemTags = options.getTags(item)
    const virtFamily = virtualFamilyTag(item)
    return tags.every((tag) => {
      if (itemTags.includes(tag)) return true
      if (tag.startsWith('family:') && virtFamily === tag) return true
      return false
    })
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
      // For the family namespace, also count virtual `family:<id>` tags
      // derived from item.family so the advanced filter shows the right
      // count for models whose tags column is empty (or missing the
      // family entry). The virtual tag is *not* added to itemTags itself,
      // so it only affects the count display, not the per-item match.
      if (namespace === 'family') {
        const virt = virtualFamilyTag(item)
        if (virt && !seen.has(virt)) {
          seen.add(virt)
          counts.set(virt, (counts.get(virt) ?? 0) + 1)
        }
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