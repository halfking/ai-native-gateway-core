<script setup lang="ts">
// FocusLayer — 专注工作区（UI规范 07 §8/§9）。
// 单实例 Teleport：active=false 时内容原位渲染（disabled Teleport），active
// 时移动到 body portal 并加载全屏 chrome；背景根 inert（旧 WebView 降级
// aria-hidden + 指针屏蔽）；滚动锁引用计数配对。
import { onBeforeUnmount, ref, watch } from 'vue'
import { FocusMachine, Hyper, lockScroll } from '@/hyper'
import { t } from '@/i18n'
import AppIcon from './AppIcon.vue'

const props = withDefaults(
  defineProps<{
    active: boolean
    title?: string
  }>(),
  { title: undefined },
)

const emit = defineEmits<{ 'update:active': [value: boolean] }>()

// WKWebView on iOS 15 can retain the inline Teleport node's former document
// position when `disabled` flips to false. Keep the fullscreen geometry inline
// so the moved layer is pinned to the visual viewport immediately.
const focusLayerFrameStyle = {
  position: 'fixed',
  top: '0',
  right: '0',
  bottom: '0',
  left: '0',
  zIndex: '1010',
  display: 'flex',
  flexDirection: 'column',
} as const

let touchStart: { x: number; y: number; fromTable: boolean } | null = null

function onTouchStart(event: TouchEvent): void {
  const touch = event.changedTouches[0]
  if (!touch) return
  const target = event.target
  touchStart = {
    x: touch.clientX,
    y: touch.clientY,
    fromTable: target instanceof Element && target.closest('.table-scroll, table') !== null,
  }
}

function onTouchEnd(event: TouchEvent): void {
  const start = touchStart
  touchStart = null
  const touch = event.changedTouches[0]
  if (!start || !touch || start.fromTable) return

  const deltaX = touch.clientX - start.x
  const deltaY = touch.clientY - start.y
  if (deltaX < -90 && Math.abs(deltaX) > Math.abs(deltaY) * 1.3) emit('update:active', false)
}

const machine = new FocusMachine()

let overlayId: string | null = null
let releaseLocks: Array<() => void> = []
let releaseFocusScroll: (() => void) | null = null
let appRoot: HTMLElement | null = null
let prevInert = false
const focusBodyRef = ref<HTMLElement | null>(null)

function clearFocusScrollHost(): void {
  releaseFocusScroll?.()
  releaseFocusScroll = null
}

function findAppRoot(): HTMLElement | null {
  if (typeof document === 'undefined') return null
  return document.getElementById('hyper-app-root')
}

function setBackgroundInert(on: boolean): void {
  appRoot = appRoot ?? findAppRoot()
  if (!appRoot) return
  if (on) {
    prevInert = appRoot.inert
    appRoot.inert = true
    if (!appRoot.hasAttribute('aria-hidden')) appRoot.setAttribute('aria-hidden', 'true')
  } else {
    appRoot.inert = prevInert
    if (appRoot.getAttribute('aria-hidden') === 'true') appRoot.removeAttribute('aria-hidden')
    appRoot = null
  }
}

watch(
  () => props.active,
  (active) => {
    if (active) {
      void machine.enter(
        () => {
          clearFocusScrollHost()
          releaseFocusScroll = Hyper.scroll.register({
            id: 'focus-layer',
            axis: 'y',
            getEl: () => focusBodyRef.value,
          })
          // 登记为 focus 覆盖层（history 标记 + 标题 + BackDispatcher 可关）
          overlayId = `focus-${Math.random().toString(36).slice(2, 9)}`
          Hyper.presentOverlay({
            id: overlayId,
            presentation: 'focus',
            title: () => props.title,
            close: () => emit('update:active', false),
          })
          // 锁真实背景滚动宿主（不止 body）
          releaseLocks.push(lockScroll(document.documentElement))
          const main = document.getElementById('main-content')
          if (main) releaseLocks.push(lockScroll(main))
          setBackgroundInert(true)
        },
        () => {
          clearFocusScrollHost()
          if (overlayId) {
            Hyper.dismissOverlay(overlayId)
            overlayId = null
          }
          for (const fn of releaseLocks) fn()
          releaseLocks = []
          setBackgroundInert(false)
        },
      )
    } else {
      void machine.exit(() => {
        clearFocusScrollHost()
        if (overlayId) {
          Hyper.dismissOverlay(overlayId)
          overlayId = null
        }
        for (const fn of releaseLocks) fn()
        releaseLocks = []
        setBackgroundInert(false)
      })
    }
  },
  { flush: 'post' },
)

onBeforeUnmount(() => {
  if (overlayId) {
    Hyper.dismissOverlay(overlayId)
    overlayId = null
  }
  clearFocusScrollHost()
  for (const fn of releaseLocks) fn()
  releaseLocks = []
  setBackgroundInert(false)
  machine.reset()
})
</script>

<template>
  <Teleport to="body" :disabled="!active">
    <div
      class="focus-inline"
      :class="{ 'focus-layer': active }"
      :style="active ? focusLayerFrameStyle : undefined"
      :role="active ? 'dialog' : undefined"
      :aria-modal="active ? 'true' : undefined"
      :aria-label="active ? title : undefined"
      @touchstart.passive="onTouchStart"
      @touchend="onTouchEnd"
    >
      <header v-if="active" class="focus-layer__header">
        <button id="focus-layer-close" type="button" class="focus-layer__exit" :aria-label="t('common.close')" @click="emit('update:active', false)">
          <AppIcon name="close" :size="20" />
          <span>{{ t('common.exitFocus') }}</span>
        </button>
        <h2 class="focus-layer__title">{{ title }}</h2>
        <span class="focus-layer__spacer" />
      </header>
      <div ref="focusBodyRef" :class="active ? 'focus-layer__body' : 'focus-inline__body'">
        <slot />
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.focus-layer {
  position: fixed;
  inset: 0;
  z-index: calc(var(--app-z-overlay) + 10);
  display: flex;
  flex-direction: column;
  background: var(--app-bg);
  padding-top: var(--app-safe-top);
  padding-bottom: var(--app-safe-bottom);
  /* 横向 inset：fixed 满屏层不受壳根 padding-inline 约束（10 §4.6.61） */
  padding-left: var(--app-safe-left);
  padding-right: var(--app-safe-right);
}

.focus-layer__header {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  min-height: 48px;
  padding: var(--app-space-1) var(--app-space-2);
  border-bottom: 1px solid var(--app-border);
  background: var(--app-header-bg);
  backdrop-filter: blur(14px);
  flex-shrink: 0;
}

.focus-layer__exit {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: none;
  background: transparent;
  color: var(--app-primary);
  font-size: 0.875rem;
  font-weight: 500;
  border-radius: var(--app-radius);
  cursor: pointer;
}

.focus-layer__exit:active {
  background: var(--app-primary-soft);
}

.focus-layer__title {
  flex: 1;
  min-width: 0;
  font-size: 0.9375rem;
  font-weight: 600;
  line-height: 1.25;
  text-align: center;
  overflow-wrap: anywhere;
}

.focus-layer__spacer {
  width: 76px;
  flex-shrink: 0;
}

.focus-layer__body {
  flex: 1;
  min-height: 0;
  overflow: auto;
  -webkit-overflow-scrolling: touch;
  overscroll-behavior: contain;
  padding: var(--app-space-3);
}

.focus-inline {
  display: contents;
}

.focus-inline__body {
  display: contents;
}
</style>
