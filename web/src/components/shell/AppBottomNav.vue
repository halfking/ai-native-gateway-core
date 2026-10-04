<script setup lang="ts">
/**
 * AppBottomNav — compact 底栏（docs/UI规范/00 §5.2 · H2，参考规范 02 §3）。
 *
 * 对标 iOS TabBar / Material Bottom Navigation，要点：
 * - **仅 compact 档渲染**。medium 及以上不渲染，桌面视觉零回归。
 * - **≤5 席**。超出的进「更多」（唤起侧栏抽屉），绝不做第二行底栏。
 * - 每席 ≥44px 触控高；compact 输入字号规则不适用于图标。
 * - 当前项 `aria-current="page"`。
 * - 席位与侧栏走**同一套权限过滤** —— 隐藏 UI 不等于鉴权，门禁在后端，
 *   但也不能因为在底栏就绕过角色过滤。
 *
 * 不做：第二行 Tab、把语言/主题/用户塞进底栏、用底栏承载写操作。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useWindowClass } from '../../composables/useWindowClass'
import { isNavItemActive, type NavItem } from '../../config/appNav'
import { useAppNav } from '../../composables/useAppNav'

const emit = defineEmits<{
  (e: 'navigate', item: NavItem): void
  (e: 'more'): void
}>()

const { t } = useI18n()
const route = useRoute()
const { isCompact } = useWindowClass()
// 复用侧栏同一份数据与权限过滤（useAppNav 内部已做 visibleNavItems 过滤 +
// 激活态 path 解析）。**不要在这里重新推导可见性** —— 复制一遍过滤逻辑，
// 底栏与侧栏就会在角色变更后不一致。
const { navPrimaryResolved, navLabel } = useAppNav()

/** 底栏最多 5 席：3 个 primary + 「更多」。超出部分不渲染，不做第二行。 */
const MAX_PRIMARY = 3

const primarySeats = computed(() => navPrimaryResolved.value.slice(0, MAX_PRIMARY))

function isActive(item: NavItem, exact?: boolean): boolean {
  return isNavItemActive(item.path, route.path, exact)
}

function go(item: NavItem): void {
  emit('navigate', item)
}
</script>

<template>
  <!--
    仅 compact 渲染。放在 v-if 而不是 CSS 隐藏，是为了让「桌面 DOM 不变」
    这条红线可被测试直接断言（medium+ 档 mount 后 querySelector 返回 null）。
  -->
  <nav
    v-if="isCompact"
    class="app-bottom-nav"
    :aria-label="t('hyper.bottomNav.ariaLabel')"
    data-testid="app-bottom-nav"
  >
    <button
      v-for="seat in primarySeats"
      :key="seat.item.path"
      type="button"
      class="app-bottom-nav__seat"
      :aria-current="isActive(seat.item, seat.item.exact) ? 'page' : undefined"
      :data-active="isActive(seat.item, seat.item.exact) ? 'true' : undefined"
      :data-nav-path="seat.item.path"
      @click="go(seat.item)"
    >
      <span class="app-bottom-nav__icon" aria-hidden="true">{{ seat.item.icon }}</span>
      <span class="app-bottom-nav__label">{{ navLabel(seat.item.labelKey, seat.item.label) }}</span>
    </button>

    <!-- 「更多」不是路由：它唤起侧栏抽屉，不建立 Tab 栈 -->
    <button
      type="button"
      class="app-bottom-nav__seat"
      data-testid="app-bottom-nav-more"
      :aria-label="t('hyper.bottomNav.more')"
      @click="emit('more')"
    >
      <span class="app-bottom-nav__icon" aria-hidden="true">⋯</span>
      <span class="app-bottom-nav__label">{{ t('hyper.bottomNav.more') }}</span>
    </button>
  </nav>
</template>

<style scoped>
/*
 * 触控下限 44px 是 WCAG 2.5.5 与 iOS 44pt 的共同基线。
 * 底栏整体高度 = 内容 + env(safe-area-inset-bottom)，
 * 由 App.vue 在内容区补等量底垫，最后一条内容才不会被挡住。
 *
 * z-index 分层：底栏 30 < 账户 Sheet 50 < 抽屉 100。
 * 「更多」打开抽屉后必须仍能点到抽屉里的菜单，所以底栏不能压过抽屉。
 * 顺序由 `gates.spec.ts` 的「弹层层级」用例守住，别只改这一处注释。
 */
.app-bottom-nav {
  position: sticky;
  bottom: 0;
  z-index: 30;
  display: flex;
  align-items: stretch;
  background: var(--kx-surface);
  border-top: 1px solid var(--kx-border);
  padding-bottom: env(safe-area-inset-bottom, 0px);
}

.app-bottom-nav__seat {
  flex: 1 1 0;
  min-width: 0;
  /* R1（2026-10-04 吸收）：本专题**新建**的触控控件一律 ≥48 CSS px，
     44px 是**存量控件**的下限、不是新标准。此前这里是 44 —— 同一专题的
     `CardList` 触控目标已经是 48，属于自相矛盾而非刻意取舍。
     本组件整体带 `v-if="isCompact"`，桌面 DOM 零影响。 */
  min-height: 48px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  padding: 6px 4px;
  background: none;
  border: 0;
  color: var(--kx-muted);
  font: inherit;
  cursor: pointer;
  /* 交互控件不参与浏览器默认手势判定之外的拦截 */
  touch-action: manipulation;
}

.app-bottom-nav__seat[data-active='true'] {
  color: var(--kx-primary);
}

.app-bottom-nav__seat:focus-visible {
  outline: 2px solid var(--kx-primary);
  outline-offset: -2px;
}

.app-bottom-nav__icon {
  font-size: 18px;
  line-height: 1;
}

.app-bottom-nav__label {
  font-size: 11px;
  line-height: 1.2;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
