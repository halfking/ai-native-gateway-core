/**
 * Light polling composable.
 *
 * Polling is visibility-aware: a hidden tab performs no fetches and no
 * reactive writes. When it becomes visible, one fetch rebuilds the view from
 * the server's authoritative state rather than replaying skipped ticks.
 */
import { onBeforeUnmount, ref, watch, type Ref } from 'vue'

export function usePolling<T>(
  source: Ref<T>,
  fetcher: () => Promise<T>,
  opts: { intervalMs?: number; immediate?: boolean } = {},
): { trigger: () => Promise<void>; stop: () => void } {
  const intervalMs = opts.intervalMs ?? 60_000
  const internal = ref(source.value)
  let timer: ReturnType<typeof setInterval> | null = null
  let inFlight = false
  let active = true

  function isPageVisible() {
    return typeof document === 'undefined' || !document.hidden
  }

  async function run() {
    if (!active || !isPageVisible() || inFlight) return
    inFlight = true
    try {
      const next = await fetcher()
      // If visibility changed while the request was in flight, skip the
      // reactive write. Visibility restoration will obtain a fresh snapshot.
      if (active && isPageVisible()) internal.value = next
    } catch {
      /* swallow; caller surfaces error */
    } finally {
      inFlight = false
    }
  }

  function start() {
    if (timer) return
    timer = setInterval(run, intervalMs)
  }

  function stop() {
    if (timer) clearInterval(timer)
    timer = null
  }

  function onVisibilityChange() {
    if (isPageVisible()) void run()
  }

  if (opts.immediate !== false) {
    void run()
  }
  start()

  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibilityChange)
  }
  onBeforeUnmount(() => {
    active = false
    stop()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', onVisibilityChange)
    }
  })
  watch(source, () => stop())

  return { trigger: run, stop }
}
