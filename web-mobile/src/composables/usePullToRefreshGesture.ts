import { computed, ref } from 'vue'
import { PTR_CONFIG, PullToRefreshMachine, isVerticalPull } from '@/hyper/scroll/pullToRefresh'
import { t } from '@/i18n'

// usePullToRefreshGesture — 07 §2 手势接线（07 §7 仲裁：单 owner、12px
// 方向锁、到顶容差 2px、单指）。返回可直接绑到滚动容器的监听器与
// visualOffset / label。刷新动作由调用方注入（HyperList→controller.refresh，
// 概览页→整页重查）。

export interface PullToRefreshGestureOptions {
  onRefresh: () => Promise<void>
  getScroller: () => HTMLElement | null
  enabled?: () => boolean
  /**
   * 列表是否正在追加加载。传入后下拉与 loadNext 双向互斥（移植自
   * feat/web-mobile-hyper）：不传则退回旧行为——只在 loadNext 侧单向拒绝。
   */
  isLoadingMore?: () => boolean
}

const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)'

export function usePullToRefreshGesture(opts: PullToRefreshGestureOptions) {
  const visualOffset = ref(0)
  const machine = new PullToRefreshMachine({
    onRefresh: opts.onRefresh,
    onSettled: () => {
      visualOffset.value = 0
    },
    isLoadingMore: opts.isLoadingMore,
    // 「减少动态效果」由本层直接读 matchMedia：判据是用户偏好而非调用方状态，
    // 机器层保留钩子只是为了可测（见 pullToRefresh.spec.ts）。
    reducedMotion: () =>
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia(REDUCED_MOTION_QUERY).matches,
  })

  let startX = 0
  let startY = 0
  let touchActive = false
  let owned = false

  function onTouchStart(ev: TouchEvent): void {
    if (machine.busy) return
    if (ev.touches.length !== 1) return
    const scroller = opts.getScroller()
    if (!scroller || scroller.scrollTop > PTR_CONFIG.startTolerancePx) return
    touchActive = true
    owned = false
    startX = ev.touches[0]?.clientX ?? 0
    startY = ev.touches[0]?.clientY ?? 0
  }

  function onTouchMove(ev: TouchEvent): void {
    if (!touchActive || ev.touches.length !== 1) return
    const touch = ev.touches[0]
    if (!touch) return
    const dx = touch.clientX - startX
    const dy = touch.clientY - startY
    const scroller = opts.getScroller()
    if (!scroller) return
    if (!owned) {
      if (!isVerticalPull(dx, dy)) {
        touchActive = false
        machine.abandon()
        visualOffset.value = 0
        return
      }
      if (
        dy > 0 &&
        scroller.scrollTop <= PTR_CONFIG.startTolerancePx &&
        (opts.enabled?.() ?? true) &&
        machine.begin()
      ) {
        owned = true
      }
    }
    if (owned) {
      ev.preventDefault()
      visualOffset.value = machine.move(dy)
    }
  }

  function onTouchEnd(): void {
    if (!touchActive) return
    touchActive = false
    if (owned) {
      owned = false
      machine.release()
      visualOffset.value = 0
    }
  }

  const label = computed(() => {
    const s = machine.state
    if (s === 'armed') return t('hyper.refreshArmed')
    if (s === 'refreshing') return t('hyper.refreshRefreshing')
    if (s === 'error') return t('common.refreshFailed')
    return t('hyper.refreshPulling')
  })

  return {
    machine,
    visualOffset,
    label,
    listeners: {
      touchstart: onTouchStart,
      touchmove: onTouchMove,
      touchend: onTouchEnd,
      touchcancel: onTouchEnd,
    },
    /** 绑定到容器（touchmove 需非 passive 才能 preventDefault）。 */
    bind(el: HTMLElement): () => void {
      el.addEventListener('touchstart', onTouchStart, { passive: true })
      el.addEventListener('touchmove', onTouchMove, { passive: false })
      el.addEventListener('touchend', onTouchEnd)
      el.addEventListener('touchcancel', onTouchEnd)
      return () => {
        el.removeEventListener('touchstart', onTouchStart)
        el.removeEventListener('touchmove', onTouchMove)
        el.removeEventListener('touchend', onTouchEnd)
        el.removeEventListener('touchcancel', onTouchEnd)
      }
    },
  }
}
