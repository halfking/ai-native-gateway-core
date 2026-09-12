<script setup lang="ts">
/**
 * AppDrawer — 通用抽屉（方案 §4.5.8，2026-09-13）。
 *
 * 封装存量 drawer-mask/drawer-panel 模式：
 * - direction="right"：右侧面板（宽度默认 min(33vw, 520px)，可配）；
 * - direction="bottom"：bottom sheet（宽度全宽，max-height 90dvh，
 *   顶部拖拽把手视觉）；
 * - direction="auto"（默认）：按 useBreakpoint().isTablet（>=768）右侧、
 *   <768 底部。
 * RTL：右侧定位使用逻辑属性 inset-inline-end，dir=rtl 下自动换到左侧。
 * 统一能力：遮罩点击关闭、ESC、body 滚动锁定（引用计数）、焦点圈闭与返还。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBreakpoint } from '../../composables/useBreakpoint'
import { useFocusTrap } from '../../composables/useFocusTrap'
import { lockBodyScroll, unlockBodyScroll } from '../../composables/useScrollLock'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    title?: string
    /** 右侧面板宽度（任意 CSS width 值），默认 min(33vw, 520px) */
    width?: string
    /** auto=按断点自动（>=768 右侧 / <768 bottom sheet） */
    direction?: 'auto' | 'right' | 'bottom'
    /** 点击遮罩是否关闭，默认 true */
    closeOnMask?: boolean
    /** 是否渲染头部关闭按钮 */
    closable?: boolean
  }>(),
  {
    title: '',
    width: 'min(33vw, 520px)',
    direction: 'auto',
    closeOnMask: true,
    closable: true,
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  close: []
}>()

const { t } = useI18n()
const { isTablet } = useBreakpoint()

const resolvedDirection = computed<'right' | 'bottom'>(() => {
  if (props.direction !== 'auto') return props.direction
  return isTablet.value ? 'right' : 'bottom'
})

const panelRef = ref<HTMLElement | null>(null)
const trap = useFocusTrap(panelRef)

function requestClose(): void {
  emit('update:modelValue', false)
  emit('close')
}

function onMaskClick(): void {
  if (props.closeOnMask) requestClose()
}

function onDocumentKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    e.stopPropagation()
    requestClose()
    return
  }
  trap.trapTab(e)
}

watch(
  () => props.modelValue,
  (open) => {
    if (typeof document === 'undefined') return
    if (open) {
      lockBodyScroll()
      document.addEventListener('keydown', onDocumentKeydown)
      // immediate 首跑时 v-if 的面板尚未渲染，nextTick 后再圈焦
      void nextTick(() => trap.activate())
    } else {
      document.removeEventListener('keydown', onDocumentKeydown)
      trap.deactivate()
      unlockBodyScroll()
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (typeof document !== 'undefined') {
    document.removeEventListener('keydown', onDocumentKeydown)
    if (props.modelValue) {
      trap.deactivate()
      unlockBodyScroll()
    }
  }
})
</script>

<template>
  <Teleport to="body">
    <div
      v-if="modelValue"
      class="app-drawer"
      :class="`app-drawer--${resolvedDirection}`"
      role="presentation"
      @click.self="onMaskClick"
    >
      <section
        ref="panelRef"
        class="app-drawer__panel"
        role="dialog"
        aria-modal="true"
        :aria-label="title || undefined"
        @click.stop
      >
        <div v-if="resolvedDirection === 'bottom'" class="app-drawer__grip" aria-hidden="true"></div>

        <header v-if="title || closable" class="app-drawer__header">
          <h3 v-if="title" class="app-drawer__title">{{ title }}</h3>
          <button
            v-if="closable"
            type="button"
            class="btn btn-ghost btn-sm app-drawer__close"
            :aria-label="t('common.button.close')"
            @click="requestClose"
          >
            ✕
          </button>
        </header>

        <div class="app-drawer__body">
          <slot />
        </div>

        <footer v-if="$slots.footer" class="app-drawer__footer">
          <slot name="footer" />
        </footer>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.app-drawer {
  position: fixed;
  inset: 0;
  z-index: 100;
  background: var(--overlay-medium);
}

.app-drawer__panel {
  position: fixed;
  display: flex;
  flex-direction: column;
  background: var(--card);
  box-shadow: 0 8px 24px var(--shadow-color-dark);
}

/* 右侧面板：逻辑属性 inset-inline-end，dir=rtl 自动换边 */
.app-drawer--right .app-drawer__panel {
  top: 0;
  bottom: 0;
  inset-inline-end: 0;
  width: v-bind(width);
  max-width: 90vw;
  border-inline-start: 1px solid var(--border);
  animation: app-drawer-slide-in 0.2s ease-out;
}

/* bottom sheet：全宽 + max-height 90dvh（fallback 90vh）+ 顶部把手 */
.app-drawer--bottom .app-drawer__panel {
  bottom: 0;
  inset-inline: 0;
  max-height: 90vh;
  max-height: 90dvh;
  border-top: 1px solid var(--border);
  border-radius: 16px 16px 0 0;
  padding-bottom: env(safe-area-inset-bottom);
  animation: app-drawer-slide-up 0.2s ease-out;
}

.app-drawer__grip {
  flex-shrink: 0;
  width: 40px;
  height: 4px;
  margin: 8px auto 0;
  border-radius: 999px;
  background: var(--border);
}

.app-drawer__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--kx-space-2);
  padding: var(--kx-space-4) var(--kx-space-4) var(--kx-space-3);
  border-bottom: 1px solid var(--border);
}
.app-drawer__header .app-drawer__title {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}
.app-drawer__close {
  flex-shrink: 0;
  min-width: 32px;
  padding: 4px 8px;
}

.app-drawer__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--kx-space-4);
}

.app-drawer__footer {
  flex-shrink: 0;
  display: flex;
  gap: var(--kx-space-2);
  justify-content: flex-end;
  padding: var(--kx-space-3) var(--kx-space-4);
  border-top: 1px solid var(--border);
}

@keyframes app-drawer-slide-in {
  from { transform: translateX(100%); }
  to { transform: translateX(0); }
}
@keyframes app-drawer-slide-up {
  from { transform: translateY(100%); }
  to { transform: translateY(0); }
}
</style>
