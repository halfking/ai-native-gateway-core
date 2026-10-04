<script setup lang="ts">
// FocusLayer — 专注工作区（UI规范 07 §8/§9）。
// 单实例 Teleport：active=false 时内容原位渲染（disabled Teleport），active
// 时移动到 body portal 并加载全屏 chrome；背景根 inert（旧 WebView 降级
// aria-hidden + 指针屏蔽）；滚动锁引用计数配对。
import { onBeforeUnmount, watch } from 'vue'
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

const machine = new FocusMachine()

let overlayId: string | null = null
let releaseLocks: Array<() => void> = []
let appRoot: HTMLElement | null = null
let prevInert = false

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
)

onBeforeUnmount(() => {
  if (overlayId) {
    Hyper.dismissOverlay(overlayId)
    overlayId = null
  }
  for (const fn of releaseLocks) fn()
  releaseLocks = []
  setBackgroundInert(false)
  machine.reset()
})
</script>

<template>
  <Teleport to="body" :disabled="!active">
    <section
      v-if="active"
      class="focus-layer"
      role="dialog"
      aria-modal="true"
      :aria-label="title"
    >
      <header class="focus-layer__header">
        <button type="button" class="focus-layer__exit" @click="emit('update:active', false)">
          <AppIcon name="back" :size="20" />
          <span>{{ t('common.exitFocus') }}</span>
        </button>
        <h2 class="focus-layer__title">{{ title }}</h2>
        <span class="focus-layer__spacer" />
      </header>
      <div class="focus-layer__body">
        <slot />
      </div>
    </section>
    <div v-else class="focus-inline">
      <slot />
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
}

.focus-layer__header {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  height: 48px;
  padding-inline: var(--app-space-2);
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
  font-size: 0.9375rem;
  font-weight: 600;
  text-align: center;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
</style>
