<script setup lang="ts">
// AppDrawer — 全局导航抽屉（UI规范 02 §3：compact 左滑入 + scrim + Esc；
// 路由变化自动收起由宿主 HyperApp 处理）。
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useHyperOverlay } from '@/hyper'
import { DRAWER_NAV, isNavItemActive, navItemsFor } from '@/config/appNav'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import AppIcon from '@/components/common/AppIcon.vue'

const props = defineProps<{ modelValue: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; account: [] }>()

const route = useRoute()
const auth = useAuthStore()
const panelRef = ref<HTMLElement | null>(null)

/**
 * ★ 2026-10-06：按角色过滤。此前 v-for 直接铺 DRAWER_NAV 全量，
 *   于是 tenant_admin 会看到 /integrity（后端 h.superAdmin，必然 403）。
 *   导航是权限问题的上游 —— 在这一层挡住，而不是让用户点进去吃报错。
 */
const drawerItems = computed(() => navItemsFor(DRAWER_NAV, auth.role))

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

// 路由跳转后收抽屉（02 §3 closeIfCompact 语义）—— 这是**正常路径**的收口。
// 它唯一够不着的是「点的是当前所在路由」那一档（fullPath 不变，不触发），
// 由下面的 onNavClick 补，理由与踩过的坑见那里。
watch(
  () => route.fullPath,
  () => {
    if (props.modelValue) emit('update:modelValue', false)
  },
)

/**
 * 点导航项后收起抽屉 —— **只补 watcher 够不着的那一种情况**。
 *
 * 收口本来有两条，职责必须分清：
 *   ① `watch(() => route.fullPath)`：路由真的变了才触发。这是**正常路径**。
 *   ② 本函数：只补「点的是当前所在路由」——此时 fullPath **不变**，① 根本不会
 *      触发，抽屉会留在屏幕上（2026-10-11 模拟器实测：人已经在 /providers 时
 *      再点「供应商」，抽屉不关，后续点击全打在抽屉上）。
 *
 * ⚠️ 曾经试过「无条件在 click 里收起」，两条一起收 —— **实测是功能回归**：
 * iOS 26 模拟器上点「供应商」抽屉是关了，人却停在总览，
 * `/api/admin/providers` 一个请求都没发。RouterLink 的 navigate() 与收口在
 * 同一个 click 上，抢跑就把导航整段丢了；随后 dismissOverlay 判定标记仍是栈顶
 * 再 history.back()（runtime.ts:277），连历史一起弹回。
 * 推迟到 nextTick 也**没有**用（seq=2542 实测仍然停在总览）——
 * 所以正确的做法不是排序，是**别在需要导航时插手**：下面这个 route.path === to
 * 的守卫就是「不需要导航」的充要条件。
 */
function onNavClick(to: string): void {
  if (route.path === to) emit('update:modelValue', false)
}

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
              v-for="item in drawerItems"
              :key="item.key"
              :to="item.to"
              class="drawer__link"
              :class="{ 'drawer__link--active': isNavItemActive(item, route.path) }"
              @click="onNavClick(item.to)"
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
  /* 横屏刘海/手势侧边：面板整体右移一个 inset（10 §4.6.61）。
     遮罩 `.drawer__scrim` 仍是 inset:0 满屏，点外面照样关。 */
  left: var(--app-safe-left);
  width: min(300px, 82vw);
  /* 两侧 inset 都算进宽度上限：左锚面板若只让左边，窄横屏下 300px 宽的面板
     会顶进右侧手势带（10 §4.6.61）。竖屏 inset 恒 0 ⇒ 与改前逐像素相同。 */
  max-width: calc(100vw - var(--app-safe-left) - var(--app-safe-right));
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
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
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
