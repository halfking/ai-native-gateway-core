<script setup lang="ts">
/**
 * AppModal — 通用弹层（方案 §4.5.7，2026-09-13）。
 *
 * 复用全局 .modal-overlay/.modal 类保持视觉零回归，scoped 只补增量。
 * 统一解决存量手写弹层的共性问题：ESC 关闭、body 滚动锁定（引用计数，
 * 嵌套弹层不误解锁）、焦点圈闭与关闭后焦点返还。
 *
 * 响应式：isSmall（<480）或 fullscreen 属性时自动全屏（顶部关闭栏 +
 * 底部 footer 吸底）；768px 以下宽度收敛为 min(92vw, size)。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBreakpoint } from '../../composables/useBreakpoint'
import { useFocusTrap } from '../../composables/useFocusTrap'
import { lockBodyScroll, unlockBodyScroll } from '../../composables/useScrollLock'
import { isTopmostOverlayLayer, nextOverlayLayerId, popOverlayLayer, pushOverlayLayer } from '../../composables/useOverlayStack'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    title?: string
    /** 面板宽度档位（max-width 映射）：sm=480 / md=640 / lg=860 */
    size?: 'sm' | 'md' | 'lg'
    /** 点击遮罩是否关闭，默认 true */
    closeOnMask?: boolean
    /** ESC 是否关闭，默认 true；门控类弹窗（协议确认等）置 false 强制显式选择 */
    escClose?: boolean
    /** 全屏模式（isSmall <480 时自动生效），顶部关闭栏 + 底部 footer 吸底 */
    fullscreen?: boolean
    /** 是否渲染右上角/顶栏关闭按钮 */
    closable?: boolean
    /** 透传给 #footer 插槽的确认按钮禁用态 */
    disabledConfirm?: boolean
    /** 追加到面板元素上的自定义类（保留旧面板皮肤时使用，如 login-modal） */
    panelClass?: string
    /** 叠放模式：遮罩 z-index 提到 110（复用全局 .modal-overlay-stacked），用于弹层叠弹层 */
    stacked?: boolean
  }>(),
  {
    title: '',
    size: 'md',
    closeOnMask: true,
    escClose: true,
    fullscreen: false,
    closable: true,
    disabledConfirm: false,
    panelClass: '',
    stacked: false,
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  close: []
}>()

defineSlots<{
  default?: () => unknown
  /** 作用域参数：disabledConfirm 透传、close 请求关闭 */
  footer?: (props: { disabledConfirm: boolean; close: () => void }) => unknown
}>()

const { t } = useI18n()
const { isSmall } = useBreakpoint()
const resolvedFullscreen = computed(() => props.fullscreen || isSmall.value)

const panelRef = ref<HTMLElement | null>(null)
const trap = useFocusTrap(panelRef)
// 叠层栈：ESC 只关最顶层弹层（audit R20 2026-09-13），避免 stacked 场景
// 一次按键把抽屉+弹层全部关闭。
const overlayId = nextOverlayLayerId()
onBeforeUnmount(() => popOverlayLayer(overlayId))

function requestClose(): void {
  emit('update:modelValue', false)
  emit('close')
}

function onMaskClick(): void {
  if (props.closeOnMask) requestClose()
}

function onDocumentKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    // escClose=false 时 ESC 不关闭（焦点圈闭仍生效），供门控类弹窗使用；
    // 仅栈顶弹层响应 ESC（useOverlayStack，嵌套弹层安全）
    if (!props.escClose) return
    if (!isTopmostOverlayLayer(overlayId)) return
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
      pushOverlayLayer(overlayId)
      lockBodyScroll()
      document.addEventListener('keydown', onDocumentKeydown)
      // immediate 首跑时 v-if 的面板尚未渲染，nextTick 后再圈焦
      void nextTick(() => trap.activate())
    } else {
      popOverlayLayer(overlayId)
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
      class="modal-overlay app-modal"
      :class="{
        'app-modal--fullscreen': resolvedFullscreen,
        'modal-overlay-stacked': stacked,
      }"
      role="presentation"
      @click.self="onMaskClick"
    >
      <div
        ref="panelRef"
        class="modal app-modal__panel"
        :class="[`app-modal__panel--${size}`, panelClass]"
        role="dialog"
        aria-modal="true"
        :aria-label="title || undefined"
        @click.stop
      >
        <header v-if="title || (resolvedFullscreen && closable)" class="app-modal__header">
          <h3 v-if="title" class="app-modal__title">{{ title }}</h3>
          <button
            v-if="resolvedFullscreen && closable"
            type="button"
            class="btn btn-ghost btn-sm app-modal__close"
            :aria-label="t('common.button.close')"
            @click="requestClose"
          >
            ✕
          </button>
        </header>

        <div class="app-modal__body">
          <slot />
        </div>

        <footer v-if="$slots.footer" class="app-modal__footer">
          <slot name="footer" :disabled-confirm="disabledConfirm" :close="requestClose" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* 桌面增量：宽度档位与头部/底部布局，不改全局 .modal 视觉 */
.app-modal__panel--sm { max-width: 480px; }
.app-modal__panel--md { max-width: 640px; }
.app-modal__panel--lg { max-width: 860px; }

.app-modal__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--kx-space-2);
  margin-bottom: var(--kx-space-4);
}
.app-modal__header .app-modal__title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}
.app-modal__close {
  flex-shrink: 0;
  min-width: 32px;
  padding: 4px 8px;
}
.app-modal__footer {
  display: flex;
  gap: var(--kx-space-2);
  justify-content: flex-end;
  margin-top: var(--kx-space-4);
}

/* 全屏模式（isSmall <480 或 fullscreen）：顶部关闭栏 + 底部 footer 吸底 */
.app-modal--fullscreen {
  align-items: stretch;
  padding: 0;
}
.app-modal--fullscreen .app-modal__panel {
  display: flex;
  flex-direction: column;
  width: 100%;
  min-width: 0;
  max-width: none;
  max-height: 100vh;
  max-height: 100dvh;
  border-radius: 0;
  padding: 0;
}
.app-modal--fullscreen .app-modal__header {
  flex-shrink: 0;
  margin-bottom: 0;
  padding: var(--kx-space-3) var(--kx-space-4);
  border-bottom: 1px solid var(--border);
}
.app-modal--fullscreen .app-modal__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--kx-space-4);
}
.app-modal--fullscreen .app-modal__footer {
  flex-shrink: 0;
  margin-top: 0;
  padding: var(--kx-space-3) var(--kx-space-4);
  padding-bottom: calc(var(--kx-space-3) + env(safe-area-inset-bottom));
  border-top: 1px solid var(--border);
  background: var(--card);
}

/* 768px 以下：宽度收敛为 min(92vw, size)，避免贴边 */
@media (max-width: 768px) {
  .app-modal__panel--sm { max-width: min(92vw, 480px); }
  .app-modal__panel--md { max-width: min(92vw, 640px); }
  .app-modal__panel--lg { max-width: min(92vw, 860px); }
}
</style>
