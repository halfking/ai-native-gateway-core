<script setup lang="ts">
// AppTopbar — 目标 Hyper 顶栏（06 §7）：「返回/根级导航 + 当前标题 + 账户」。
// 标题真源 = Hyper.title（TitleResolver 优先级链）。48px + safe-top。
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { Hyper } from '@/hyper'
import { isRootRoute } from '@/config/appNav'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

defineProps<{ onMenu: () => void; onAccount: () => void }>()

const route = useRoute()
const title = computed(() => Hyper.title.value.title)
const showBack = computed(() => !isRootRoute(route.path))
</script>

<template>
  <header class="topbar">
    <div class="topbar__side">
      <button
        v-if="showBack"
        type="button"
        class="topbar__btn"
        :aria-label="t('common.back')"
        @click="Hyper.back()"
      >
        <AppIcon name="back" :size="22" />
      </button>
      <button v-else type="button" class="topbar__btn" :aria-label="t('nav.more')" @click="onMenu">
        <AppIcon name="menu" :size="22" />
      </button>
    </div>

    <h1 class="topbar__title" aria-live="polite">{{ title }}</h1>

    <div class="topbar__side topbar__side--end">
      <button type="button" class="topbar__btn" :aria-label="t('nav.account')" @click="onAccount">
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

.topbar__title {
  flex: 1;
  min-width: 0;
  text-align: center;
  font-size: 1rem;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
