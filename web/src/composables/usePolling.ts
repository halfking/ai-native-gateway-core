/** Light polling composable (audit 2026-07-22 D). */
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

  async function run() {
    if (inFlight) return
    inFlight = true
    try {
      const next = await fetcher()
      internal.value = next
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

  if (opts.immediate !== false) {
    void run()
  }
  start()

  onBeforeUnmount(stop)
  watch(source, () => stop())

  return { trigger: run, stop }
}