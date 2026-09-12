/**
 * useBreakpoint — 视口断点 composable（方案 §4.3，零依赖，接口对齐 VueUse 便于未来替换）。
 *
 * 模块级全局单例：三个 matchMedia 查询只在首次调用时绑定一次监听，
 * 所有组件共用同一份 readonly ref，避免重复监听。
 *
 * 命名口径（设计文档澄清）：
 * - isMobile = <1024，是"布局决策口径"（是否需要抽屉导航/全屏弹窗），非设备分类；
 * - isTablet = >=768；isSmall = <480（小屏微调档，不改变布局结构）；
 * - isDesktop = !isMobile（computed）。
 *
 * SSR / jsdom 安全：window 或 matchMedia 不存在时惰性初始化失败后保持默认 false，
 * 不抛错（组合式函数在测试环境中可直接调用）。
 */
import { computed, readonly, ref } from 'vue'
import { BREAKPOINTS } from '../config/breakpoints'

const isMobile = ref(false) // < desktop(1024)
const isTablet = ref(false) // >= tablet(768)
const isSmall = ref(false) // < small(480)

let initialized = false
let cleanup: (() => void) | null = null

function bind(): void {
  if (initialized) return
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
  initialized = true

  const mqlMobile = window.matchMedia(`(max-width: ${BREAKPOINTS.desktop - 1}px)`) // <1024
  const mqlTablet = window.matchMedia(`(min-width: ${BREAKPOINTS.tablet}px)`) // >=768
  const mqlSmall = window.matchMedia(`(max-width: ${BREAKPOINTS.small - 1}px)`) // <480

  const sync = () => {
    isMobile.value = mqlMobile.matches
    isTablet.value = mqlTablet.matches
    isSmall.value = mqlSmall.matches
  }
  sync()

  if (typeof mqlMobile.addEventListener === 'function') {
    mqlMobile.addEventListener('change', sync)
    mqlTablet.addEventListener('change', sync)
    mqlSmall.addEventListener('change', sync)
    cleanup = () => {
      mqlMobile.removeEventListener('change', sync)
      mqlTablet.removeEventListener('change', sync)
      mqlSmall.removeEventListener('change', sync)
    }
  }
}

export function useBreakpoint() {
  bind()
  return {
    isMobile: readonly(isMobile),
    isTablet: readonly(isTablet),
    isSmall: readonly(isSmall),
    isDesktop: computed(() => !isMobile.value),
  }
}

/** 仅供测试：解除单例绑定并复位懒初始化标记，使下一个用例重新绑定当前 matchMedia。 */
export function _resetForTests(): void {
  cleanup?.()
  cleanup = null
  initialized = false
  isMobile.value = false
  isTablet.value = false
  isSmall.value = false
}
