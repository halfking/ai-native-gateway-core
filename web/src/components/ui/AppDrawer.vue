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
 *
 * ## 拖拽关闭（规范 12 §1 / §2，2026-10-04）
 *
 * 决策交给 `lib/shell/hyper/dragDismiss` 的纯状态机，本组件只做三件事：
 * 量尺寸、接 DOM 事件、把 offset 写进 transform。**方向锁与速度判定不在本文件**
 * ——它们一旦写在这里就只能靠真机手感验证，而手感是最难回归的那一类。
 *
 * ### 起手区域就是 §1 的争用仲裁
 *
 * | 方向 | 可起手区域 | 让给谁 |
 * | --- | --- | --- |
 * | `bottom` | 仅 `.app-drawer__grip` 与 `.app-drawer__header` | `.app-drawer__body` 归**内容滚动**（§1 优先级 3） |
 * | `right` | 整个面板 | 面板内无横向滚动，无冲突 |
 *
 * 底部 sheet 若允许从正文起手，用户想滚动列表却误关了弹层——这是最招致差评的一类。
 *
 * ### 关闭方向从**实测位置**推导，不从 `direction` prop 推
 *
 * 右侧面板用 `inset-inline-end` 定位，**dir=rtl 下它在屏幕左侧**，此时「向右拖」是错的。
 * ⇒ 用 `getBoundingClientRect()` 与视口中线比较来定符号，RTL 自动正确。
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBreakpoint } from '../../composables/useBreakpoint'
import { useFocusTrap } from '../../composables/useFocusTrap'
import { lockBodyScroll, unlockBodyScroll } from '../../composables/useScrollLock'
import { DragDismiss } from '../../lib/shell/hyper/dragDismiss'
import {
  isTopmostOverlayLayer,
  nextOverlayLayerId,
  popOverlayLayer,
  pushOverlayLayer,
  setOverlayLayerClose,
} from '../../composables/useOverlayStack'

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
    /** 是否允许拖拽关闭（规范 12 §2），默认 true。关掉后退化为纯点击/ESC 关闭 */
    draggable?: boolean
  }>(),
  {
    title: '',
    width: 'min(33vw, 520px)',
    direction: 'auto',
    closeOnMask: true,
    closable: true,
    draggable: true,
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  close: []
}>()

defineSlots<{
  default?: () => unknown
  footer?: () => unknown
}>()

const { t } = useI18n()
const { isTablet } = useBreakpoint()

const resolvedDirection = computed<'right' | 'bottom'>(() => {
  if (props.direction !== 'auto') return props.direction
  return isTablet.value ? 'right' : 'bottom'
})

const panelRef = ref<HTMLElement | null>(null)
const trap = useFocusTrap(panelRef)

/**
 * 跟手位移写进 transform。
 * 跟手阶段**没有 transition**（否则浏览器补间 ⇒ 不跟手）；
 * 只有松手后的回弹/退场段才启用，且 200ms（规范 12 §5「层进出 200–300ms」）。
 * 位移为 0 时不写 transform，避免空值样式压掉 CSS 里的其它声明。
 */
const dragStyle = computed(() => {
  if (dragOffset.value === 0 && !isDragging.value) return undefined
  const axis = resolvedDirection.value === 'bottom' ? 'translateY' : 'translateX'
  return { transform: `${axis}(${dragOffset.value}px)` }
})

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
    if (!isTopmostOverlayLayer(overlayId)) return
    e.stopPropagation()
    requestClose()
    return
  }
  trap.trapTab(e)
}

// ---- 拖拽关闭（规范 12 §1 / §2） ------------------------------------------------

/**
 * ⚠️ 这三个 `let` 必须**声明在下方 watch 之前**。
 *
 * 实测踩到的 TDZ bug：`watch(..., { immediate: true })` 在 setup 期就执行回调，
 * 而回调的 `else` 分支（`modelValue` 初值为 false）会调 `resetDrag()`，
 * 它读 `settleFallback` —— 若该 `let` 声明在 watch 之后，就是
 * `ReferenceError: Cannot access 'settleFallback' before initialization`。
 *
 * 为什么本仓的拖拽测试**没抓到**（`AppDrawer.drag.test.ts` 8/8 全绿）：
 * 那 8 条全部以 `modelValue: true` 起手 ⇒ 只走 `open` 分支 ⇒ 从不调 `resetDrag()`。
 * 是全量回归里 `UsersView.test.ts` / `RequestLogDrawer.test.ts`
 * 以关闭态挂载才暴露出来。
 * ⇒ **覆盖要按分支数，不要按「我想测的那条路」**。
 */
let drag: DragDismiss | null = null
let pendingOutcome: 'close' | 'cancel' | null = null
let settleFallback: ReturnType<typeof setTimeout> | null = null

/** 跟手位移（px，沿主轴）。直接写进 panel 的 transform。 */
const dragOffset = ref(0)
/** 跟手中：关掉 transition 才能 1:1 跟手，否则浏览器会补间。 */
const isDragging = ref(false)
/** 松手后的回弹/退场段：此时要有 transition。 */
const isSettling = ref(false)

watch(
  () => props.modelValue,
  (open) => {
    if (typeof document === 'undefined') return
    if (open) {
      pushOverlayLayer(overlayId)
      // Hyper 返回仲裁需要**真实**关闭动作；不登记的话 Android 系统返回
      // 会消费返回但层还在（假成功）。见 composables/useOverlayStack 头注。
      setOverlayLayerClose(overlayId, requestClose)
      lockBodyScroll()
      document.addEventListener('keydown', onDocumentKeydown)
      // immediate 首跑时 v-if 的面板尚未渲染，nextTick 后再圈焦
      void nextTick(() => trap.activate())
    } else {
      popOverlayLayer(overlayId)
      document.removeEventListener('keydown', onDocumentKeydown)
      trap.deactivate()
      unlockBodyScroll()
      // 关闭后必须清干净：否则下次打开会带着上次的位移出场。
      resetDrag()
    }
  },
  { immediate: true },
)

function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function resetDrag(): void {
  if (settleFallback) {
    clearTimeout(settleFallback)
    settleFallback = null
  }
  drag?.cancel()
  drag = null
  pendingOutcome = null
  dragOffset.value = 0
  isDragging.value = false
  isSettling.value = false
}

/** bottom sheet 只允许从把手/头部起手；正文归内容滚动（§1 优先级 3）。 */
function isGrabTarget(target: EventTarget | null): boolean {
  if (resolvedDirection.value !== 'bottom') return true
  const el = target as HTMLElement | null
  if (!el || typeof el.closest !== 'function') return false
  return !!el.closest('.app-drawer__grip, .app-drawer__header')
}

function onGrabPointerDown(e: PointerEvent): void {
  if (!props.draggable) return
  // 只接管主键；右键/中键留给浏览器（右键菜单等）
  if (e.pointerType === 'mouse' && e.button !== 0) return
  if (!isGrabTarget(e.target)) return
  const panel = panelRef.value
  if (!panel) return

  const rect = panel.getBoundingClientRect()
  const isBottom = resolvedDirection.value === 'bottom'
  const viewportWidth = window.innerWidth
  // 关闭方向用**实测位置**定：RTL 下右侧面板其实在屏幕左侧，符号必须反过来。
  const onRightHalf = rect.left + rect.width / 2 > viewportWidth / 2
  const closeDirection: 1 | -1 = isBottom ? 1 : onRightHalf ? 1 : -1

  drag = new DragDismiss(
    {
      axis: isBottom ? 'y' : 'x',
      closeDirection,
      panelSize: isBottom ? rect.height : rect.width,
    },
    {
      onProgress: (offset) => {
        dragOffset.value = offset
        isDragging.value = true
      },
      onSettle: (outcome, snapshot) => finishSettle(outcome, snapshot.offset),
    },
  )

  if (!drag.start(e.clientX, e.clientY, e.timeStamp, viewportWidth)) {
    drag = null
    return
  }
  isSettling.value = false
}

function onGrabPointerMove(e: PointerEvent): void {
  if (!drag) return
  if (drag.move(e.clientX, e.clientY, e.timeStamp)) {
    // 已被手势消费：阻止文本选区与滚动接管
    e.preventDefault()
  }
}

function onGrabPointerUp(): void {
  if (!drag) return
  drag.end()
  drag = null
}

function onGrabPointerCancel(): void {
  // 中断即复位（规范 12 §2）：来电/系统手势抢走 ⇒ 立即回中性态
  drag?.cancel()
  drag = null
  isDragging.value = false
  dragOffset.value = 0
}

/** 退场需要走完位移才能卸载（v-if），所以把「关」推迟到过渡结束。 */
function finishSettle(outcome: 'close' | 'cancel', offset: number): void {
  const panel = panelRef.value
  const rect = panel?.getBoundingClientRect()
  const isBottom = resolvedDirection.value === 'bottom'
  const size = isBottom ? (rect?.height ?? 0) : (rect?.width ?? 0)

  pendingOutcome = outcome
  isDragging.value = false
  isSettling.value = true

  if (prefersReducedMotion()) {
    // 减少动态效果：不做补间，直接落终态（规范 12 §4）
    if (outcome === 'close') {
      resetDrag()
      requestClose()
    } else {
      resetDrag()
    }
    return
  }

  dragOffset.value = outcome === 'close' ? (size || offset) : 0
  // 兜底：元素被 display:none / 后台标签页时 transitionend 可能不触发，
  // 没有兜底就会卡在「面板停在半路且 modelValue 仍为 true」。
  if (settleFallback) clearTimeout(settleFallback)
  settleFallback = setTimeout(() => completeSettle(), 400)
}

function completeSettle(): void {
  const outcome = pendingOutcome
  resetDrag()
  if (outcome === 'close') requestClose()
}

function onPanelTransitionEnd(e: TransitionEvent): void {
  if (e.target !== panelRef.value) return
  if (!isSettling.value) return
  completeSettle()
}

onBeforeUnmount(() => {
  if (settleFallback) clearTimeout(settleFallback)
  drag = null
})

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
        :class="{
          'app-drawer__panel--dragging': isDragging,
          'app-drawer__panel--settling': isSettling,
          'app-drawer__panel--bottom': resolvedDirection === 'bottom',
        }"
        role="dialog"
        aria-modal="true"
        :aria-label="title || undefined"
        :style="dragStyle"
        @click.stop
        @pointerdown="onGrabPointerDown"
        @pointermove="onGrabPointerMove"
        @pointerup="onGrabPointerUp"
        @pointercancel="onGrabPointerCancel"
        @transitionend="onPanelTransitionEnd"
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

/* ---- 拖拽关闭（规范 12 §1 / §2） ---------------------------------------- */
/* 起手区域：把手 + 头部。touch-action:none 阻止浏览器先接管滚动，
   否则 pointermove 会被 cancel，手势根本起不来。 */
.app-drawer--bottom .app-drawer__grip,
.app-drawer--bottom .app-drawer__header {
  touch-action: none;
}
/* 右侧面板整面可起手（面板内无横向滚动，无争用） */
.app-drawer--right .app-drawer__panel {
  touch-action: none;
}

/* 跟手：1:1，无补间；同时禁掉选区，避免拖拽时划出文字 */
.app-drawer__panel--dragging {
  transition: none !important;
  animation: none !important;
  user-select: none;
}
/* 松手后的回弹 / 退场：200ms，规范 12 §5 的「层进出 200–300ms」下界 */
.app-drawer__panel--settling {
  transition: transform 200ms ease-out;
  animation: none !important;
  user-select: none;
}

@media (prefers-reduced-motion: reduce) {
  .app-drawer__panel--settling {
    transition: none;
  }
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
