<script setup lang="ts">
// ActionOverflowMenu.vue — 动作区溢出菜单（2026-10-27 · 第 25 增量）
//
// ★ 为什么需要它：
//   impeccable `distill.md`：「Clear hierarchy: ONE primary action, few secondary
//   actions, **everything else tertiary or hidden**」；
//   `craft-floor.md`（Coverage）：「every brief requirement present and findable
//   within seconds」。
//   ⇒ 低频动作**不能删**（那是丢需求），但**不该与高频动作同排同重**（那是找不着）。
//   ⇒ 收进一个**可见的「更多」**按钮里：入口可见、项有标签 ⇒ 仍可被找到。
//
// ★ 为什么菜单本体放在 `<Teleport to="body">`：
//   ① 不被父级的 overflow / stacking context 裁切；
//   ② 判据（`src/design/actionDensity.ts`）把 Teleport 内的动作区判为**浮层**，
//      逐项弹出、不构成同屏竞争 ⇒ 菜单里的 7 项不会把「动作区」判成超阈值。
//      ★ 这不是「给自己开后门」—— 浮层里的项**同时只显示一个**，是事实。
//
// ★ 「更多」这个文案**只写一次**：本组件内部固定用 `common.button.more`，
//   调用方不传文案 ⇒ 8 个语种各一份，不许在页面里各写各的。
//
// ★ 用法：
//   <ActionOverflowMenu :items="[
//       { key: 'bootstrap', label: t('freePool.bootstrap') },
//       { key: 'oauth', label: t('freePool.bridgeOAuth') },
//     ]" @select="onOverflowSelect" />
//
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CaretBottom } from '@element-plus/icons-vue'

export interface ActionOverflowItem {
  /** 稳定标识；@select 回传 */
  key: string
  /** 可见文案（由调用方用 t() 给出） */
  label: string
  /** 禁用（仍显示，但不可点） */
  disabled?: boolean
  /** 危险动作（破坏性） */
  danger?: boolean
  /**
   * 解释性 tooltip。
   * ★ 收进菜单的动作原本常常带 `:title`（说明这个动作会做什么）⇒
   *   折叠之后 tooltip 是**唯一**还能说明白的渠道 ⇒ 不能丢。
   */
  title?: string
}

const props = withDefaults(
  defineProps<{
    items: ActionOverflowItem[]
    /** 触发器额外的类名（默认继承全局 .btn） */
    triggerClass?: string
    /** 触发器额外属性（data-testid 等） */
    triggerAttrs?: Record<string, string>
  }>(),
  { triggerClass: '', triggerAttrs: () => ({}) },
)

const emit = defineEmits<{ select: [key: string] }>()

const { t } = useI18n()

const open = ref(false)
const menuStyle = ref<Record<string, string>>({})
const triggerRef = ref<HTMLElement | null>(null)
const menuRef = ref<HTMLElement | null>(null)
/** 当前键盘焦点落在第几项（-1 = 无） */
const activeIndex = ref(-1)

function syncPosition() {
  const el = triggerRef.value
  if (!el || typeof window === 'undefined') return
  const rect = el.getBoundingClientRect()
  // 右对齐到触发器右缘；下方放不下就翻到上方
  const below = window.innerHeight - rect.bottom
  const flip = below < 240 && rect.top > below
  menuStyle.value = {
    position: 'fixed',
    top: flip ? `${Math.max(8, rect.top - 8)}px` : `${rect.bottom + 6}px`,
    right: `${Math.max(8, window.innerWidth - rect.right)}px`,
    left: 'auto',
    zIndex: '1001',
    transform: flip ? 'translateY(-100%)' : 'none',
  }
}

function toggle() {
  open.value = !open.value
  activeIndex.value = -1
  if (open.value) nextTick(() => syncPosition())
}

function close() {
  open.value = false
  activeIndex.value = -1
}

function selectItem(item: ActionOverflowItem) {
  if (item.disabled) return
  close()
  emit('select', item.key)
}

function onTriggerKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    if (!open.value) {
      open.value = true
      nextTick(() => syncPosition())
    }
    activeIndex.value = e.key === 'ArrowDown' ? 0 : props.items.length - 1
    focusActive()
  } else if (e.key === 'Escape' && open.value) {
    e.preventDefault()
    close()
  }
}

function focusActive() {
  nextTick(() => {
    const nodes = menuRef.value?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')
    nodes?.[activeIndex.value]?.focus()
  })
}

function onMenuKeydown(e: KeyboardEvent) {
  const enabled = props.items.map((it, i) => (it.disabled ? -1 : i)).filter((i) => i >= 0)
  if (!enabled.length) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    const pos = enabled.indexOf(activeIndex.value)
    activeIndex.value = enabled[(pos + 1) % enabled.length]!
    focusActive()
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    const pos = enabled.indexOf(activeIndex.value)
    activeIndex.value = enabled[(pos - 1 + enabled.length) % enabled.length]!
    focusActive()
  } else if (e.key === 'Escape') {
    e.preventDefault()
    close()
    triggerRef.value?.focus()
  } else if (e.key === 'Home') {
    e.preventDefault()
    activeIndex.value = enabled[0]!
    focusActive()
  } else if (e.key === 'End') {
    e.preventDefault()
    activeIndex.value = enabled[enabled.length - 1]!
    focusActive()
  }
}

function onDocumentClick(event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (!target) return
  if (target.closest('.action-overflow')) return
  close()
}

function onWindowChange() {
  if (open.value) syncPosition()
}

onMounted(() => {
  document.addEventListener('click', onDocumentClick)
  window.addEventListener('resize', onWindowChange)
  window.addEventListener('scroll', onWindowChange, true)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocumentClick)
  window.removeEventListener('resize', onWindowChange)
  window.removeEventListener('scroll', onWindowChange, true)
})
</script>

<template>
  <div class="action-overflow">
    <button
      ref="triggerRef"
      type="button"
      class="btn action-overflow__trigger"
      :class="props.triggerClass"
      :aria-expanded="open"
      aria-haspopup="menu"
      data-testid="action-overflow-trigger"
      v-bind="props.triggerAttrs"
      @click.stop="toggle"
      @keydown="onTriggerKeydown"
    >
      {{ t('common.button.more') }}
      <span class="action-overflow__chevron" aria-hidden="true"><CaretBottom /></span>
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="menuRef"
        class="action-overflow__menu"
        :style="menuStyle"
        role="menu"
        data-testid="action-overflow-menu"
        @click.stop
        @keydown="onMenuKeydown"
      >
        <button
          v-for="item in items"
          :key="item.key"
          type="button"
          class="action-overflow__item"
          :class="{ 'action-overflow__item--danger': item.danger }"
          role="menuitem"
          :aria-disabled="item.disabled ? 'true' : undefined"
          :disabled="item.disabled"
          :title="item.title"
          @click="selectItem(item)"
        >
          {{ item.label }}
        </button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.action-overflow {
  display: inline-flex;
  position: relative;
}

.action-overflow__trigger {
  gap: 4px;
}

.action-overflow__chevron {
  display: inline-flex;
  font-size: 10px;
  opacity: 0.7;
  transition: transform 0.2s var(--kx-motion-ease-out);
  flex-shrink: 0;
}
.action-overflow__trigger[aria-expanded='true'] .action-overflow__chevron {
  transform: rotate(180deg);
}

.action-overflow__menu {
  min-width: 168px;
  padding: 4px;
  background: var(--kx-surface, var(--card));
  /* ★ 抬升**只声明一次**（craft-floor：「Declare elevation once, border or shadow」）：
     描边 + 宽软阴影叠在一起 = 「幽灵卡片」（V1-ghost-card）。
     ⇒ 浮层靠阴影读出「浮起来」，不另加描边。 */
  border-radius: var(--radius, 8px);
  box-shadow: var(--kx-shadow-md);
  display: flex;
  flex-direction: column;
  gap: 2px;
  animation: action-overflow-enter 0.14s var(--kx-motion-ease-out) both;
}

.action-overflow__item {
  display: block;
  width: 100%;
  padding: 8px 8px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--kx-text, var(--text));
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.2;
  text-align: start;
  white-space: nowrap;
  cursor: pointer;
  transition: background-color 0.15s var(--kx-motion-ease-out), color 0.15s var(--kx-motion-ease-out);
}
.action-overflow__item:hover:not(:disabled),
.action-overflow__item:focus-visible {
  background: var(--kx-primary-soft, var(--bg-hover));
  color: var(--kx-primary, var(--accent));
  outline: none;
}
.action-overflow__item:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.action-overflow__item--danger:hover:not(:disabled) {
  color: var(--kx-danger, var(--danger));
}

@keyframes action-overflow-enter {
  from { opacity: 0; transform: translateY(-4px); }
  to { opacity: 1; transform: translateY(0); }
}

/* ★ Element Plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。 */
.action-overflow__chevron > svg { width: 10px; height: 10px; }

/* 2026-10-27 第 26 增量：空间移动的 reduced-motion 路径（impeccable animate.md:77）。
   颜色 / 透明度反馈保留（那是「有意义的状态变化」），只把**空间移动**停下来。 */
@media (prefers-reduced-motion: reduce) {
  .action-overflow__chevron { transition: none; }
}
</style>
