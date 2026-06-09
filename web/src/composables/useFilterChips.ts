import { computed } from 'vue'

export interface FilterChip {
  key: string
  label: string
  onRemove: () => void
  className?: string
}

export function useFilterChips(source: () => Array<FilterChip | null | undefined | false>) {
  return computed(() => source().filter(Boolean) as FilterChip[])
}