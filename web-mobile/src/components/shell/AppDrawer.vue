<script setup lang="ts">
// AppDrawer — 全局导航抽屉（UI规范 02 §3：compact 左滑入 + scrim + Esc；
// 路由变化自动收起由宿主 HyperApp 处理）。
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperOverlay } from '@/hyper'
import { DRAWER_NAV, isNavItemActive } from '@/config/appNav'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; account: [] }>()

const route = useRoute()
const panelRef = ref<HTMLElement | null>(null)

const overlay = useHyperOverlay({
  presentation: 'modal',
  title: () => t('nav.more'),
  onClose: () => emit('update:modelValue', false),
})

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      overlay.present()
      void nextTick(() => {
        panelRef.value?.querySelector<HTMLElement>('a, button')?.focus()
      })
    } else {
      overlay.release()
    }
  },
)

// 路由跳转后收抽屉（02 §3 closeIfCompact 语义）
watch(
  () => route.fullPath,
  () => {
    if (props.modelValue) emit('update:modelValue', false)
  },
)

onBeforeUnmount(() => overlay.release())
</script>

<template>
  <Teleport to="body">
    <Transition name="drawer">
      <div v-if="modelValue" class="drawer" role="dialog" aria-modal="true" :aria-label="t('nav.more')">
        <div class="drawer__scrim" @mousedown="overlay.close()" />
        <div ref="panelRef" class="drawer__panel">
          <div class="drawer__brand">
            <span class="drawer__app">{{ t('app.name') }}</span>
            <span class="drawer__sub">{{ t('app.subtitle') }}</span>
          </div>

          <nav class="drawer__nav">
            <RouterLink
              v-for="item in DRAWER_NAV"
              :key="item.key"
              :to="item.to"
              class="drawer__link"
              :class="{ 'drawer__link--active': isNavItemActive(item, route.path) }"
            >
              <AppIcon :name="item.icon" :size="20" />
              <span>{{ t(item.titleKey) }}</span>
            </RouterLink>
          </nav>

          <div class="drawer__footer">
            <button type="button" class="drawer__link drawer__link--action" @click="emit('account')">
              <AppIcon name="user" :size="20" />
              <span>{{ t('nav.account') }}</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.drawer {
  position: fixed;
  inset: 0;
  z-index: var(--app-z-overlay);
}

.drawer__scrim {
  position: absolute;
  inset: 0;
  background: rgb(10 18 32 / 45%);
}

.drawer__panel {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: min(300px, 82vw);
  display: flex;
  flex-direction: column;
  background: var(--app-surface);
  box-shadow: var(--app-shadow-drawer);
  padding: calc(var(--app-space-4) + var(--app-safe-top)) var(--app-space-3) calc(var(--app-space-4) + var(--app-safe-bottom));
}

.drawer__brand {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 0 var(--app-space-2) var(--app-space-4);
  border-bottom: 1px solid var(--app-border-subtle);
  margin-bottom: var(--app-space-2);
}

.drawer__app {
  font-size: 1rem;
  font-weight: 600;
}

.drawer__sub {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.drawer__nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.drawer__link {
  display: flex;
  align-items: center;
  gap: var(--app-space-3);
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border-radius: var(--app-radius);
  color: var(--app-text);
  text-decoration: none;
  font-size: 0.9375rem;
  border: none;
  background: transparent;
  cursor: pointer;
  text-align: left;
}

.drawer__link:active,
.drawer__link--active {
  background: var(--app-primary-soft);
  color: var(--kx-primary);
}

.drawer__footer {
  margin-top: auto;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: var(--app-space-2);
}

.drawer-enter-active,
.drawer-leave-active {
  transition: opacity 220ms ease;
}

.drawer-enter-active .drawer__panel,
.drawer-leave-active .drawer__panel {
  transition: transform 260ms cubic-bezier(0.2, 0, 0, 1);
}

.drawer-enter-from,
.drawer-leave-to {
  opacity: 0;
}

.drawer-enter-from .drawer__panel,
.drawer-leave-to .drawer__panel {
  transform: translateX(-70%);
}

@media (prefers-reduced-motion: reduce) {
  .drawer-enter-active,
  .drawer-leave-active {
    transition: opacity 100ms ease;
  }
  .drawer-enter-from .drawer__panel,
  .drawer-leave-to .drawer__panel {
    transform: none;
  }
}
</style>
