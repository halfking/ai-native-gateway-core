<script setup lang="ts">
// AppBottomNav — compact 档底栏（UI规范 02 §4：≤5 席、图标+文字、
// 安全区吸收；active = 主色 + 600 字重 + 图标上浮 1px）。
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { BOTTOM_NAV, isNavItemActive } from '@/config/appNav'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

defineProps<{ onMore: () => void }>()

const route = useRoute()
const items = computed(() => BOTTOM_NAV.map((item) => ({ ...item, label: t(item.titleKey), active: isNavItemActive(item, route.path) })))
const moreLabel = t('nav.more')
</script>

<template>
  <nav class="bottomnav" aria-label="bottom">
    <RouterLink
      v-for="item in items"
      :key="item.key"
      :to="item.to"
      class="bottomnav__item"
      :class="{ 'bottomnav__item--active': item.active }"
      :aria-current="item.active ? 'page' : undefined"
    >
      <AppIcon :name="item.icon" :size="22" />
      <span class="bottomnav__label">{{ item.label }}</span>
    </RouterLink>

    <button type="button" class="bottomnav__item" :aria-label="moreLabel" @click="onMore">
      <AppIcon name="menu" :size="22" />
      <span class="bottomnav__label">{{ moreLabel }}</span>
    </button>
  </nav>
</template>

<style scoped>
.bottomnav {
  position: fixed;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: var(--app-z-header);
  display: flex;
  background: var(--app-bottomnav-bg);
  backdrop-filter: blur(14px);
  border-top: 1px solid var(--app-bottomnav-border);
  padding-bottom: var(--app-safe-bottom);
}

.bottomnav__item {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  height: var(--app-bottomnav-height);
  color: var(--app-text-secondary);
  text-decoration: none;
  -webkit-tap-highlight-color: transparent;
  cursor: pointer;
  border: none;
  background: transparent;
}

.bottomnav__item--active {
  color: var(--kx-primary);
  font-weight: 600;
}

.bottomnav__item--active :deep(.app-icon) {
  transform: translateY(-1px);
}

.bottomnav__label {
  font-size: 0.6875rem;
  line-height: 1.2;
}

.bottomnav__item:active {
  color: var(--kx-primary);
}
</style>
