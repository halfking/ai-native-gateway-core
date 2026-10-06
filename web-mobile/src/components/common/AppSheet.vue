<script setup lang="ts">
// AppSheet — 覆盖层基座（UI规范 03 §弹窗：compact 全屏/近全屏；06 §5.1
// history 标记由 Hyper runtime 负责；Esc/系统返回经 BackDispatcher 仲裁）。
// 业务关闭（受控）：调用返回的 close()；父组件直接置 modelValue=false 也
// 会同步清理注册与标记（幂等）。
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
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

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      lastFocused = document.activeElement
      overlay.present()
      void nextTick(() => {
        const focusable = panelRef.value?.querySelector<HTMLElement>(
          'input, select, textarea, button:not([aria-label="关闭"]), [tabindex]:not([tabindex="-1"])',
        )
        focusable?.focus()
      })
    } else {
      overlay.release()
      if (lastFocused instanceof HTMLElement) {
        lastFocused.focus()
        lastFocused = null
      }
    }
  },
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
        :class="`app-sheet--${presentation}`"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
      >
        <div class="app-sheet__scrim" @mousedown="requestClose" />
        <div
          ref="panelRef"
          class="app-sheet__panel"
          :class="`app-sheet__panel--${presentation}`"
          @keydown="onKeydownTab"
        >
          <header class="app-sheet__header">
            <h2 class="app-sheet__title">{{ title }}</h2>
            <button type="button" class="app-sheet__close" :aria-label="t('common.close')" @click="requestClose">
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
  left: 0;
  right: 0;
  bottom: 0;
  max-height: min(90dvh, 100vh);
  border-radius: var(--app-radius-lg) var(--app-radius-lg) 0 0;
  padding-bottom: var(--app-safe-bottom);
}

/* modal：居中卡片；compact 近全屏留 1rem 边（03 §4 居中 min(设计px, 100vw-2rem)） */
.app-sheet__panel--modal {
  inset: 1rem;
  inset-inline: max(1rem, calc(50% - 210px));
  top: max(3dvh, 1rem);
  margin: auto;
  width: auto;
  max-width: 420px;
  border-radius: var(--app-radius-lg);
}

@media (min-width: 600px) {
  .app-sheet__panel--sheet {
    left: auto;
    right: 0;
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
     ≥600px 时本面板是右侧抽屉（left:auto; right:0），<600px 时是贴底 sheet，
     两种形态的面板上沿都不在屏幕顶端，横屏刘海区也只在**左右**——那一侧由
     `.hyper-app` 的 padding-inline 单点消费。所以此处不消费 top 是正确取舍，
     而不是「忘了」。 */
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
