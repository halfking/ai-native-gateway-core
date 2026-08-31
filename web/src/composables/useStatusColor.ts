import { computed, type ComputedRef, type Ref } from 'vue'

/**
 * 2026-08-31 (P2-9 audit): dot/text status-color mapping.
 *
 * The earlier inline map in RequestTile.vue used six hard-coded hex values,
 * which:
 *   - did not follow the active skin (亮/暗主题的语义色应该统一)
 *   - duplicated the status-bar mapping (statusBarColor) without sharing
 *     the semantic taxonomy
 *
 * The dot/text surface is the second visual language for status alongside
 * statusBarColor. statusBarColor differentiates inside 'failure' (timeout
 * vs 4xx vs 5xx vs no-route) using var(--warning) / var(--danger) /
 * var(--muted), but the dot surface only needs a 6-way bucket because a
 * single dot cannot convey error_kind granularity. Both surfaces now share
 * the same CSS variable namespace so future skin tweaks move both at once.
 *
 * Returned strings are CSS values usable in `background:` / `color:`.
 */
export function useStatusColor(): {
  statusColor: (status?: string | null, errorKind?: string | null) => string
} {
  function resolveStatusColor(
    status?: string | null,
    _errorKind?: string | null,
  ): string {
    if (!status) return 'var(--muted)'
    if (status === 'success') return 'var(--success)'
    if (status === 'in_progress') return 'var(--accent)'
    if (status === 'failure') return 'var(--danger)'
    if (status === 'cancelled' || status === 'canceled') return 'var(--warning)'
    if (status === 'idle') return 'var(--muted)'
    return 'var(--muted)'
  }
  return { statusColor: resolveStatusColor }
}

type StatusSource<T> = Ref<T> | ComputedRef<T> | (() => T)

function toGetter<T>(source: StatusSource<T>): () => T {
  return typeof source === 'function' ? source : () => source.value
}

/**
 * Reactive variant for <script setup> bindings that prefer a ComputedRef.
 * Used by RequestTile.vue where tile.status is already a reactive prop.
 */
export function useReactiveStatusColor(
  status: StatusSource<string | undefined | null>,
  errorKind?: StatusSource<string | undefined | null>,
): ComputedRef<string> {
  const { statusColor } = useStatusColor()
  const statusGetter = toGetter(status)
  const errorKindGetter = errorKind ? toGetter(errorKind) : () => undefined
  return computed(() => statusColor(statusGetter(), errorKindGetter()))
}
