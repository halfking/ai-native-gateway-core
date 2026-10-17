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

/**
 * ★ 2026-10-16：iPhone 6s / iOS 15.8.8 真机实测 + bg_004a30db 调查结论 —— 防御性切换。
 *
 * 现象：WKWebView 上 WDA 合成 tap（pointerMove→pointerDown→80ms pause→pointerUp）
 * 经常**不**触发 JS `click` 事件，于是 vue-router 5.2.0 的 `guardEvent` 整条
 * 链路不跑、`<a>` 默认导航随之触发；该默认导航走到原生 `decidePolicyFor` →
 * `NodeNavigationPlugin.shouldOverrideLoad`，被 `ShellOriginPolicy.allows()`
 * 拒（`url.path` 非 `/`/空/`/index.html`），返回 `true` 取消 WKWebView 的
 * 主帧导航 ⇒ `pushState` 始终不跑、路由纹丝不动。
 *
 * 根因是「`<a href>` 的默认导航」+「WKWebView tap 识别不完全匹配 W3C actions」
 * 的耦合。修法：路由入口改用 `<RouterLink custom>` 渲染成 `<a>` + 显式
 * `@click` 走 slot prop `navigate(e)`。**不能自己抢先 `e.preventDefault()`**：
 * vue-router 5.2.0 的 `navigate(e)` 第一件事是 `guardEvent(e)`，而 guardEvent
 * 对 `defaultPrevented` 的处理是直接 return（vue-router.cjs:2113）——抢先
 * preventDefault 等于把 push 链饿死，底栏点击全灭（2026-10-17 12h 审计
 * 订正 454740b35 的「双保险」顺序）。plain tap 时 guardEvent 自己会
 * preventDefault（:2119）再 `router.push`，原生导航/路由导航由它裁决；
 * 修饰键点击（metaKey/ctrlKey 新标签页）则交还 `<a>` 默认行为，同样正确。
 *
 * 样式对 `<a>` / `<button>` 都通用（已在原 CSS 中验过：border:none; background:
 * transparent; text-decoration:none 全部无害），UX 上 5 个底栏项视觉行为不变。
 */
// vue-router 5.2.0 的 RouterLink slot prop 实际签名为
//   navigate: (e?: MouseEvent) => Promise<void | NavigationFailure> | void
// 我们只在事件 handler 里消费它，不需要 NavigationFailure 的具体类型。
type RouterNavigate = (e?: MouseEvent) => Promise<unknown> | void
function safeNavigate(navigate: RouterNavigate, e: MouseEvent): void {
  // 只拦冒泡，不拦默认：preventDefault 由 guardEvent 在决定 push 时自己做。
  e.stopPropagation()
  // navigate() 内部已经做了 guardEvent + preventDefault + router.push。
  const result = navigate(e)
  if (result && typeof (result as Promise<unknown>).then === 'function') {
    void (result as Promise<unknown>).catch(() => undefined)
  }
}
</script>

<template>
  <nav class="bottomnav" aria-label="bottom">
    <RouterLink
      v-for="item in items"
      :key="item.key"
      :to="item.to"
      custom
      v-slot="{ href, navigate }"
    >
      <a
        :href="href"
        class="bottomnav__item"
        :class="{ 'bottomnav__item--active': item.active }"
        :aria-current="item.active ? 'page' : undefined"
        @click="safeNavigate(navigate, $event)"
      >
        <AppIcon :name="item.icon" :size="22" />
        <span class="bottomnav__label">{{ item.label }}</span>
      </a>
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
  /* 横向 inset 必须**自己**消费（10 §4.6.61）：本栏是 fixed，包含块是视口，
     壳根 `.hyper-app` 的 padding-inline 碰不到它。背景仍满宽（符合 HIG 的
     「背景延伸、内容避让」），只有热区被推入安全区。竖屏该值恒 0 ⇒ 零位移。 */
  padding-left: var(--app-safe-left);
  padding-right: var(--app-safe-right);
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
