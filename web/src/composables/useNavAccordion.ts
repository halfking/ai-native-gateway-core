import { ref, watch, type Ref } from 'vue'
import type { NavGroup } from '../config/appNav'
import { isNavItemActive } from '../config/appNav'

/** Group id containing the active route, or null. */
export function findGroupForPath(groups: NavGroup[], path: string): string | null {
  for (const g of groups) {
    if (g.items.some((item) => isNavItemActive(item.path, path))) {
      return g.id
    }
  }
  return null
}

/**
 * Sidebar nav accordion: at most one group expanded; default all collapsed.
 * On / (总览) all groups stay collapsed. Other routes auto-expand their group.
 */
export function useNavAccordion(groups: Ref<NavGroup[]>, currentPath: Ref<string>) {
  const expandedGroupId = ref<string | null>(null)

  watch(
    [currentPath, groups],
    ([path]) => {
      if (path === '/') {
        expandedGroupId.value = null
        return
      }
      const gid = findGroupForPath(groups.value, path)
      if (gid) expandedGroupId.value = gid
    },
    { immediate: true },
  )

  function toggleGroup(id: string) {
    expandedGroupId.value = expandedGroupId.value === id ? null : id
  }

  function isGroupExpanded(id: string): boolean {
    return expandedGroupId.value === id
  }

  function groupHasActive(id: string): boolean {
    const g = groups.value.find((x) => x.id === id)
    if (!g) return false
    return g.items.some((item) => isNavItemActive(item.path, currentPath.value))
  }

  return { expandedGroupId, toggleGroup, isGroupExpanded, groupHasActive }
}
