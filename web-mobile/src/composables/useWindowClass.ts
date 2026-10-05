import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { Ref } from 'vue'

// useWindowClass — 四档窗口 SSOT 的 TS 镜像（UI规范 01 §2）。
// CSS token（styles/theme.css --app-bp-*）无法被 media query 引用，数值
// 必须双镜像：改一处必改另一处；本文件断言见 useWindowClass.spec.ts。
// JS 判移动只走 useWindowClass / currentWindowClass()，禁止新造 px 常量。

export type WindowClass = 'compact' | 'medium' | 'expanded' | 'large'

export const BREAKPOINT_MEDIUM_PX = 600
export const BREAKPOINT_EXPANDED_PX = 960
export const BREAKPOINT_LARGE_PX = 1280

const MEDIA_QUERY_MEDIUM = `(min-width: ${BREAKPOINT_MEDIUM_PX}px)`
const MEDIA_QUERY_EXPANDED = `(min-width: ${BREAKPOINT_EXPANDED_PX}px)`
const MEDIA_QUERY_LARGE = `(min-width: ${BREAKPOINT_LARGE_PX}px)`

function classify(medium: boolean, expanded: boolean, large: boolean): WindowClass {
  if (large) return 'large'
  if (expanded) return 'expanded'
  if (medium) return 'medium'
  return 'compact'
}

/** 一次性判定（SSR 安全：无 window 回落 compact）。 */
export function currentWindowClass(): WindowClass {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return 'compact'
  return classify(
    window.matchMedia(MEDIA_QUERY_MEDIUM).matches,
    window.matchMedia(MEDIA_QUERY_EXPANDED).matches,
    window.matchMedia(MEDIA_QUERY_LARGE).matches,
  )
}

export interface UseWindowClass {
  windowClass: Readonly<Ref<WindowClass>>
}

/** setup 期即取真实档位（避免首帧跳档），挂 3 条 min-width 监听。 */
export function useWindowClass(): UseWindowClass {
  const windowClass = ref<WindowClass>(currentWindowClass())

  onMounted(() => {
    const mqMedium = window.matchMedia(MEDIA_QUERY_MEDIUM)
    const mqExpanded = window.matchMedia(MEDIA_QUERY_EXPANDED)
    const mqLarge = window.matchMedia(MEDIA_QUERY_LARGE)
    const update = () => {
      windowClass.value = classify(mqMedium.matches, mqExpanded.matches, mqLarge.matches)
    }
    for (const mq of [mqMedium, mqExpanded, mqLarge]) {
      if (typeof mq.addEventListener === 'function') {
        mq.addEventListener('change', update)
      } else {
        // Safari 14 以下 addEventListener 缺失 — 传统监听兜底（14 §5）
        ;(mq as unknown as { addListener(fn: () => void): void }).addListener(update)
      }
    }
    update()
    onBeforeUnmount(() => {
      for (const mq of [mqMedium, mqExpanded, mqLarge]) {
        if (typeof mq.removeEventListener === 'function') {
          mq.removeEventListener('change', update)
        } else {
          ;(mq as unknown as { removeListener(fn: () => void): void }).removeListener(update)
        }
      }
    })
  })

  return { windowClass }
}
