<script setup lang="ts">
// AppTopbar — 目标 Hyper 顶栏（06 §7）：「返回/根级导航 + 当前标题 + 账户」。
// 标题真源 = Hyper.title（TitleResolver 优先级链）。48px + safe-top。
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { Hyper } from '@/hyper'
import { isRootRoute } from '@/config/appNav'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'
import type { TopbarAction } from './topbarActions'

const props = withDefaults(
  defineProps<{
    onMenu: () => void
    onAccount: () => void
    /**
     * 2026-10-10：弹窗/drawer/account sheet 打开时，导航栏左边那一颗按钮
     * 视觉上变成「叉叉」，但沿用原 click —— 用户预期它关弹窗，而不是再
     * 触发抽屉。`onCloseOverlay` 由父组件根据自身状态判断后回调：未传则
     * 走原来的菜单/返回路径，行为不变。覆盖优先级：
     *   onCloseOverlay 存在 && (drawerOpen || accountOpen)  → 关弹窗
     *   showBack                                         → 后退
     *   否则                                              → 打开抽屉（菜单）
     */
    onCloseOverlay?: () => void
    overlayActive?: boolean
    actions?: TopbarAction[]
  }>(),
  { actions: () => [], onCloseOverlay: undefined, overlayActive: false },
)

const route = useRoute()
const title = computed(() => Hyper.title.value.title)
const showBack = computed(() => !isRootRoute(route.path))
const showClose = computed(() => Boolean(props.onCloseOverlay) && props.overlayActive)
const pageActions = computed(() => props.actions)

function handleLeftClick(): void {
  if (showClose.value && props.onCloseOverlay) {
    props.onCloseOverlay()
    return
  }
  if (showBack.value) {
    Hyper.back()
    return
  }
  props.onMenu()
}

function actionLabel(action: TopbarAction): string {
  return typeof action.label === 'function' ? action.label() : action.label
}

function actionDisabled(action: TopbarAction): boolean {
  return typeof action.disabled === 'function' ? action.disabled() : action.disabled === true
}
</script>

<template>
  <header class="topbar">
    <div class="topbar__side">
      <button
        v-if="showClose"
        type="button"
        class="topbar__btn"
        id="topbar-close-overlay"
        :aria-label="t('common.close')"
        @click="handleLeftClick"
      >
        <AppIcon name="close" :size="22" />
      </button>
      <button
        v-else-if="showBack"
        type="button"
        class="topbar__btn"
        id="topbar-back"
        :aria-label="t('common.back')"
        @click="handleLeftClick"
      >
        <AppIcon name="back" :size="22" />
      </button>
      <button
        v-else
        id="topbar-menu"
        type="button"
        class="topbar__btn"
        :aria-label="t('nav.more')"
        @click="handleLeftClick"
      >
        <AppIcon name="menu" :size="22" />
      </button>
    </div>

    <h1 id="topbar-title" class="topbar__title" aria-live="polite">{{ title }}</h1>

    <div class="topbar__side topbar__side--end">
      <button
        v-for="action in pageActions"
        :key="action.key"
        type="button"
        class="topbar__btn topbar__action"
        :id="`topbar-action-${action.key}`"
        :aria-label="actionLabel(action)"
        :title="actionLabel(action)"
        :disabled="actionDisabled(action)"
        @click="action.onClick"
      >
        <AppIcon v-if="action.icon" :name="action.icon" :size="22" />
        <span v-else class="topbar__action-label">{{ actionLabel(action) }}</span>
      </button>
      <button id="topbar-account" type="button" class="topbar__btn" :aria-label="t('nav.account')" @click="onAccount">
        <AppIcon name="user" :size="22" />
      </button>
    </div>
  </header>
</template>

<style scoped>
.topbar {
  position: sticky;
  top: 0;
  z-index: var(--app-z-header);
  display: flex;
  align-items: center;
  gap: var(--app-space-1);
  height: calc(var(--app-header-height) + var(--app-safe-top));
  padding: var(--app-safe-top) var(--app-space-1) 0;
  background: var(--app-header-bg);
  backdrop-filter: blur(14px);
  border-bottom: 1px solid var(--app-header-border);
  flex-shrink: 0;
}

.topbar__side {
  display: flex;
  min-width: 48px;
}

.topbar__side--end {
  justify-content: flex-end;
}

.topbar__btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  border: none;
  background: transparent;
  color: var(--app-text);
  border-radius: var(--app-radius);
  cursor: pointer;
}

.topbar__btn:active {
  background: var(--app-surface-muted);
}

.topbar__btn:disabled {
  opacity: 0.45;
  cursor: default;
}

.topbar__action-label {
  font-size: 0.75rem;
  line-height: 1.1;
  text-align: center;
}

.topbar__title {
  /* 2026-10-10：原 `flex:1; min-width:0; text-align:center; white-space:nowrap`
     在窄屏（iPhone 6s / 6s Plus）配合 6 字节中文字符串把标题硬截成
     「请求旅程队列」一类看不到后半截，用户扫不到当前所在页。
     现允许两行（左对齐 + overflow-wrap），既扩出宽度又不顶破细音尾巴。 */
  flex: 1 1 auto;
  min-width: 0;
  margin: 0;
  text-align: left;
  font-size: 0.95rem;
  font-weight: 600;
  line-height: 1.25;
  overflow-wrap: anywhere;
  word-break: break-word;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
