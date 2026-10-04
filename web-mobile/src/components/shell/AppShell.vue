<script setup lang="ts">
// AppShell.vue — compact 应用框架（顶栏 + 滚动宿主 + 底栏）。
// 顶栏标题是全页唯一标题（TitleResolver 优先级链驱动）；返回席走 BackDispatcher
// 单一仲裁；账户固定顶栏头像（02 §5）；「更多」抽屉由底栏第 5 席唤起。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '../Icon.vue'
import AccountSheet from '../account/AccountSheet.vue'
import MoreSheet from './MoreSheet.vue'
import { backDispatcher } from '../../runtime/backDispatcher'
import { resolveTitle } from '../../runtime/titleResolver'
import { scrollHost } from '../../runtime/scrollHost'
import { useTitleStore } from '../../stores/titleStore'
import { t } from '../../i18n'

defineProps<{
  /** 底栏 5 席（登录页传 false 隐藏）。 */
  showTabbar?: boolean
}>()

const route = useRoute()
const router = useRouter()
const contentEl = ref<HTMLElement | null>(null)
const bd = backDispatcher()
const sh = scrollHost()
const titleStore = useTitleStore()

const accountOpen = ref(false)

const tabs = computed(() => [
  { key: '/', icon: 'overview', label: t('nav.overview') },
  { key: '/nodes', icon: 'nodes', label: t('nav.nodes') },
  { key: '/models', icon: 'models', label: t('nav.models') },
  { key: '/keys', icon: 'keys', label: t('nav.keys') },
])

const pageTitle = computed(() =>
  resolveTitle({
    registered: titleStore.registered,
    overlay: titleStore.overlay,
    inherited: titleStore.inherited,
    route: typeof route.meta.titleKey === 'string' ? t(route.meta.titleKey) : null,
    appName: t('app.name'),
  }),
)

const canBack = computed(() => route.path !== '/' && route.path !== '/login')

function onBack(): void {
  bd.dispatchBack(
    () => (window.history.state?.position ?? 0) > 0,
    () => router.back(),
  )
}

function onEsc(e: KeyboardEvent): void {
  if (e.key === 'Escape') onBack()
}

function goTab(key: string): void {
  router.push(key)
}

onMounted(() => {
  if (contentEl.value) sh.attach(contentEl.value)
  window.addEventListener('keydown', onEsc)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onEsc)
  sh.detach()
})
</script>

<template>
  <div class="m-shell">
    <header class="m-topbar">
      <button
        v-if="canBack"
        type="button"
        class="m-topbar__btn"
        :aria-label="t('common.close')"
        @click="onBack"
      >
        <Icon name="back" />
      </button>
      <div v-else class="m-topbar__btn" aria-hidden="true"></div>
      <h1 class="m-topbar__title">{{ pageTitle }}</h1>
      <div class="m-topbar__side">
        <slot name="action" />
        <button
          v-if="route.path !== '/login'"
          type="button"
          class="m-topbar__btn"
          :aria-label="t('account.title')"
          @click="accountOpen = true"
        >
          <Icon name="account" />
        </button>
      </div>
    </header>

    <main ref="contentEl" class="m-content" :data-scroll-host="route.path">
      <slot />
    </main>

    <nav v-if="showTabbar !== false && route.path !== '/login'" class="m-tabbar" aria-label="tabbar">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        type="button"
        class="m-tabbar__item"
        :class="{ 'is-active': tab.key === route.path }"
        @click="goTab(tab.key)"
      >
        <Icon :name="tab.icon" />
        <span>{{ tab.label }}</span>
      </button>
      <button
        type="button"
        class="m-tabbar__item"
        :class="{ 'is-active': titleStore.moreOpen }"
        @click="titleStore.openMore()"
      >
        <Icon name="more" />
        <span>{{ t('nav.more') }}</span>
      </button>
    </nav>

    <AccountSheet v-if="accountOpen" @close="accountOpen = false" />
    <MoreSheet v-if="titleStore.moreOpen" @close="titleStore.closeMore()" />
  </div>
</template>
