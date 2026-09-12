/**
 * useViewportSegments — 折叠屏视口分段 composable（方案 §4.6，2026-09-13）。
 *
 * 特性检测 window.viewport.segments（Viewport Segments API）与
 * navigator.devicePosture（Device Posture API）：resize 与姿态变化时更新；
 * 不支持的浏览器 supported=false、segments 恒为空数组、isSpanning 恒 false
 * （零风险降级，消费方无需额外兜底）。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'

interface ViewportSegmentsApi {
  viewport?: { segments?: DOMRect[] }
}

function readSegments(): DOMRect[] {
  if (typeof window === 'undefined') return []
  const api = window as unknown as ViewportSegmentsApi
  const segs = api.viewport?.segments
  return Array.isArray(segs) ? segs : []
}

const segments = ref<DOMRect[]>(readSegments())
const supported = ref(
  typeof window !== 'undefined' &&
    'viewport' in (window as unknown as Record<string, unknown>) &&
    Array.isArray((window as unknown as ViewportSegmentsApi).viewport?.segments),
)

const isSpanning = computed(() => segments.value.length > 1)

function update(): void {
  segments.value = readSegments()
  if (!supported.value && typeof window !== 'undefined' && 'viewport' in (window as unknown as Record<string, unknown>)) {
    supported.value = true
  }
}

// 姿态监听（支持的浏览器才有 devicePosture）
let stopPosture: (() => void) | null = null

export function useViewportSegments() {
  let stopResize: (() => void) | null = null

  onMounted(() => {
    if (typeof window === 'undefined') return
    window.addEventListener('resize', update)
    stopResize = () => window.removeEventListener('resize', update)
    update()

    const posture = (navigator as unknown as { devicePosture?: { addEventListener?: (t: string, cb: () => void) => void; removeEventListener?: (t: string, cb: () => void) => void } }).devicePosture
    if (posture?.addEventListener) {
      const cb = () => update()
      posture.addEventListener('change', cb)
      stopPosture = () => posture.removeEventListener?.('change', cb)
    }
  })

  onUnmounted(() => {
    stopResize?.()
    stopResize = null
    stopPosture?.()
    stopPosture = null
  })

  return { supported, segments, isSpanning }
}
