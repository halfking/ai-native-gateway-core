<script setup lang="ts">
// AppSheet — 覆盖层基座（UI规范 03 §弹窗：compact 全屏/近全屏；06 §5.1
// history 标记由 Hyper runtime 负责；Esc/系统返回经 BackDispatcher 仲裁）。
// 业务关闭（受控）：调用返回的 close()；父组件直接置 modelValue=false 也
// 会同步清理注册与标记（幂等）。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useHyperOverlay } from '@/hyper'
import AppIcon from './AppIcon.vue'
import { t } from '@/i18n'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    presentation?: 'sheet' | 'modal'
    title?: string
    /** 阻止关闭（脏表单守卫）——返回 false 消费返回事件保持现状。 */
    beforeClose?: () => boolean | Promise<boolean>
  }>(),
  { presentation: 'sheet', title: undefined, beforeClose: undefined },
)

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const panelRef = ref<HTMLElement | null>(null)
let lastFocused: Element | null = null

const overlay = useHyperOverlay({
  presentation: props.presentation,
  title: () => props.title,
  beforeClose: props.beforeClose,
  onClose: () => {
    emit('update:modelValue', false)
  },
})
const isTopOverlay = overlay.isTop

// compact sheet：顶栏已显示 Hyper.title。若 sheet 头再画一遍标题 = 双标题
// （2026-10-17 6s-size Maestro 08 截图像素实证）。
// ★ 关闭钮必须留在 sheet 头：iOS WKWebView 对顶栏 icon 按钮经常不暴露
// aria-label 文本到 Maestro a11y 树（实测顶栏 ✕ 搜不到「关闭」），
// 去掉 sheet 关闭后 flow 08 直接 Element not found。modal / ≥600px 仍显示完整头。
const hideDuplicateTitle = computed(() => {
  if (props.presentation !== 'sheet') return false
  if (typeof window === 'undefined' || !window.matchMedia) return false
  return window.matchMedia('(max-width: 599px)').matches
})

function preferPanelFocus(): void {
  // ★ 永远不要自动 focus 业务按钮（如「解除手动停用」会立刻弹出 window.prompt）。
  // iOS 模拟器常报 pointer:fine / hover:hover，旧分支会落到第一个 button，
  // Maestro 二次打开详情时就卡在原生 prompt 上（08 二次关闭全红）。
  const closeButton = panelRef.value?.querySelector<HTMLElement>('.app-sheet__close')
  const target = closeButton ?? panelRef.value
  target?.focus()
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      lastFocused = document.activeElement
      // ⚠️ 打开弹层必须先把背景里还聚焦着的输入框**失焦**（2026-10-12 实测修复）。
      //
      // 供应商列表页顶部有个搜索框；点卡片打开详情弹层时它往往还保持 focus。
      // 只要还有 input 处于 focus 状态，iOS 就会：
      //   ① 让**自动填充条**（▲▼✓）一直浮在弹层上面不消失；
      //   ② 把落在页面上的点按判成「长按文本」→ 弹出「粘贴 / 自动填充」上下文菜单，
      //      这一下根本到不了 ✕ 按钮上。
      // 实测现象：模拟器上连点 ✕ 三次都关不掉弹窗，坐标点按弹出的直接是
      // 「Paste | AutoFill」浮层。**按钮本身没坏，是焦点没被弹层接管。**
      //
      // 只 blur 不 preventDefault：blur() 不阻止后续事件派发，
      // 也不会影响下面针对 panelRef 的主动 focus。
      if (lastFocused instanceof HTMLElement) lastFocused.blur()
      overlay.present()
      void nextTick(() => {
        preferPanelFocus()
      })
    } else {
      overlay.release()
      if (lastFocused instanceof HTMLElement) {
        lastFocused.focus()
        lastFocused = null
      }
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  overlay.release()
})

function requestClose(): void {
  void (async () => {
    if (props.beforeClose && !(await props.beforeClose())) return
    overlay.close()
  })()
}

function onScrimClick(): void {
  // Close after the complete tap, not on touch-down. Removing a nested modal
  // while a gesture is still in flight can retarget the same tap to its parent.
  if (!overlay.isTop.value) return
  requestClose()
}

// 焦点困层（简化版 07 §9：Tab 循环限制在面板内）
function onKeydownTab(ev: KeyboardEvent): void {
  if (ev.key !== 'Tab' || !panelRef.value) return
  const nodes = Array.from(
    panelRef.value.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input, select, textarea, [tabindex]:not([tabindex="-1"])'),
  ).filter((el) => el.offsetParent !== null)
  if (nodes.length === 0) return
  const first = nodes[0] as HTMLElement
  const last = nodes[nodes.length - 1] as HTMLElement
  if (ev.shiftKey && document.activeElement === first) {
    ev.preventDefault()
    last.focus()
  } else if (!ev.shiftKey && document.activeElement === last) {
    ev.preventDefault()
    first.focus()
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="sheet">
      <div
        v-if="modelValue"
        class="app-sheet"
        :class="[`app-sheet--${presentation}`, { 'app-sheet--inactive': !isTopOverlay }]"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
      >
        <div class="app-sheet__scrim" @click.self="onScrimClick" />
        <div
          ref="panelRef"
          class="app-sheet__panel"
          :class="`app-sheet__panel--${presentation}`"
          tabindex="-1"
          @keydown="onKeydownTab"
        >
          <header class="app-sheet__header" :class="{ 'app-sheet__header--title-deferred': hideDuplicateTitle }">
            <h2 v-if="!hideDuplicateTitle" class="app-sheet__title">{{ title }}</h2>
            <span v-else class="app-sheet__title-spacer" aria-hidden="true" />
            <button id="app-sheet-close" type="button" class="app-sheet__close" :aria-label="t('common.close')" @click="requestClose">
              <AppIcon name="close" :size="20" />
            </button>
          </header>
          <div class="app-sheet__body">
            <slot />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.app-sheet {
  position: fixed;
  inset: 0;
  z-index: var(--app-z-overlay);
}

/* Only the top overlay may receive input; nested modal scrims must not close a parent. */
.app-sheet--inactive {
  pointer-events: none;
}

/* 确认框可嵌在账户 Sheet 等浮层中；它也必须盖过全屏专注层（+10）。 */
.app-sheet--modal {
  /* Keep this as a token value: iOS 15 WebKit drops calc() on integer z-index. */
  z-index: var(--app-z-modal);
}

.app-sheet__scrim {
  position: absolute;
  inset: 0;
  background: rgb(10 18 32 / 45%);
}

.app-sheet__panel {
  position: absolute;
  display: flex;
  flex-direction: column;
  background: var(--app-surface);
  box-shadow: var(--app-shadow-dialog);
  max-height: 100dvh;
  overflow: hidden;
}

/* sheet：底部滑入，头部圆角，max 90dvh（compact 主形态） */
.app-sheet__panel--sheet {
  /* 横向 inset 自己消费（10 §4.6.61）：本面板 fixed/Teleport，壳根 padding 管不到。
     实测未避让时抽屉链接有 18px 命中区落进 30px inset 带（图标被刘海压住）。 */
  left: var(--app-safe-left);
  right: var(--app-safe-right);
  bottom: 0;
  max-height: min(90dvh, 100vh);
  border-radius: var(--app-radius-lg) var(--app-radius-lg) 0 0;
  padding-bottom: var(--app-safe-bottom);
}

/* modal：居中卡片；compact 近全屏留 1rem 边（03 §4 居中 min(设计px, 100vw-2rem)） */
.app-sheet__panel--modal {
  inset: 1rem;
  /* 居中卡片两侧各让一个 inset：窄屏横屏时 1rem 边小于 inset，边缘会进手势区 */
  inset-inline-start: max(calc(1rem + var(--app-safe-left)), calc(50% - 210px));
  inset-inline-end: max(calc(1rem + var(--app-safe-right)), calc(50% - 210px));
  top: max(3dvh, 1rem);
  margin: auto;
  width: auto;
  max-width: 420px;
  border-radius: var(--app-radius-lg);
}

@media (min-width: 600px) {
  .app-sheet__panel--sheet {
    left: auto;
    right: var(--app-safe-right);
    top: 0;
    max-height: none;
    width: min(420px, 92vw);
    border-radius: var(--app-radius-lg) 0 0 var(--app-radius-lg);
  }
}

.app-sheet__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: var(--app-space-3) var(--app-space-4);
  /* 曾经这里有一行 `padding-top: calc(var(--app-space-3) + var(--app-safe-top) * 0)`
     —— 与上一行**逐字冗余**（`* 0` 使 safe-area 项恒为 0），却长得像在处理
     刘海。已删除并在此说明为什么**不需要** safe-top（10 §4.6.32）：
     ≥600px 时本面板是右侧抽屉（left:auto; right:var(--app-safe-right)），<600px 时是贴底 sheet，
     两种形态的面板上沿都不在屏幕顶端，横屏刘海区也只在**左右**。
     ⚠️ 订正（10 §4.6.61）：原文此处写「左右由 `.hyper-app` 的 padding-inline **单点消费**」
     —— 那是**错的**。本面板 `position:fixed` + `Teleport to="body"`，包含块是视口，
     壳根那点 padding 碰不到它（实测：抽屉链接 18px 命中区落进 30px inset 带）。
     横向 inset 现由各 fixed 浮层**自己**消费，见上。 */
  border-bottom: 1px solid var(--app-border-subtle);
  flex-shrink: 0;
}

.app-sheet__title {
  font-size: var(--app-section-title-size);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-sheet__title-spacer {
  flex: 1;
  min-width: 0;
}

.app-sheet__header--title-deferred {
  justify-content: flex-end;
}

.app-sheet__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  margin-right: calc((48px - 32px) / -2);
  border: none;
  background: transparent;
  color: var(--app-text-secondary);
  border-radius: var(--app-radius);
  cursor: pointer;
}

.app-sheet__close:active {
  background: var(--app-surface-muted);
}

.app-sheet__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  overscroll-behavior: contain;
  padding: var(--app-space-3) var(--app-space-4) var(--app-space-4);
}

/* 过渡（12 §4：Sheet 240–320ms；reduced-motion 取消位移） */
.sheet-enter-active,
.sheet-leave-active {
  transition: opacity 240ms ease;
}

.sheet-enter-active .app-sheet__panel--sheet,
.sheet-leave-active .app-sheet__panel--sheet {
  transition: transform 280ms cubic-bezier(0.2, 0, 0, 1);
}

.sheet-enter-from,
.sheet-leave-to {
  opacity: 0;
}

.sheet-enter-from .app-sheet__panel--sheet,
.sheet-leave-to .app-sheet__panel--sheet {
  transform: translateY(40%);
}

@media (prefers-reduced-motion: reduce) {
  .sheet-enter-active,
  .sheet-leave-active {
    transition: opacity 100ms ease;
  }
  .sheet-enter-from .app-sheet__panel--sheet,
  .sheet-leave-to .app-sheet__panel--sheet {
    transform: none;
  }
}
</style>
