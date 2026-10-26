<script setup lang="ts">
// HyperApp — 应用壳（唯一布局）：顶栏 + 主滚动区 + compact 底栏 +
// 抽屉/账户 Sheet。headerless 路由（登录页）隐藏全部 chrome。
// DockCoordinator 的 effectiveTop 以本壳顶栏实测为准（07 §4）。
import { computed, onBeforeUnmount, onMounted, provide, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Hyper } from '@/hyper'
import { useWindowClass } from '@/composables/useWindowClass'
import { useDeploySeqUpdate } from '@/hyper/update/useDeploySeqUpdate'
import AppTopbar from './AppTopbar.vue'
import AppBottomNav from './AppBottomNav.vue'
import AppDrawer from './AppDrawer.vue'
import AppAccountSheet from './AppAccountSheet.vue'
import AppUpdateBanner from './AppUpdateBanner.vue'
import { createTopbarActionRegistry, TOPBAR_ACTIONS } from './topbarActions'

const route = useRoute()
const { windowClass } = useWindowClass()
const update = useDeploySeqUpdate()
const topbar = createTopbarActionRegistry()
const topbarActions = topbar.actions
provide(TOPBAR_ACTIONS, topbar)

const drawerOpen = ref(false)
const accountOpen = ref(false)

const headerless = computed(() => route.meta.headerless === true)
const showBottomNav = computed(() => !headerless.value && windowClass.value === 'compact')

// 2026-10-10：弹窗（drawer / 账户 sheet / AppSheet / FocusLayer）打开时，
// 顶栏左按钮要变 X 才能让用户关掉，而不是再触发一层抽屉。
//
// ★ 2026-10-17 真机踩坑：原先只看 drawerOpen/accountOpen，AppSheet 与
// FocusLayer 虽已登记进 Hyper.topOverlay，顶栏却仍显示菜单键；用户点
// 到「看起来像叉叉」的系统返回位（或期待顶栏 X）无效。必须把
// Hyper.topOverlay 一并算进 overlayActive，并从顶栏关闭顶层 Hyper overlay。
const overlayActive = computed(
  () => drawerOpen.value || accountOpen.value || Hyper.topOverlay.value != null,
)
function closeActiveOverlay(): void {
  // 优先关 Hyper 登记的最顶层（AppSheet / FocusLayer / 账户 Sheet 共用）。
  // 必须走 dismissOverlay：只调 handle.close() 不会从栈里摘掉登记，
  // 顶栏 X 会卡死在打开态（单测与真机同构）。
  const top = Hyper.topOverlay.value
  if (top) {
    Hyper.dismissOverlay(top.id)
    return
  }
  if (accountOpen.value) {
    accountOpen.value = false
    return
  }
  if (drawerOpen.value) {
    drawerOpen.value = false
  }
}

function onOpenAccountEvent(): void {
  accountOpen.value = true
}

// 在 setup 同步登记：/me 冷启动时子页 onMounted 早于本壳 onMounted，
// 若只在 onMounted 里 addEventListener 会丢掉首屏打开账户的事件。
if (typeof window !== 'undefined') {
  window.addEventListener('llmgw:open-account', onOpenAccountEvent)
}

// 部署序号检查（UI规范 18 §4）：headerless（登录页）同样检查——长期停在
// 登录页的设备也该收到更新提示；首次检查延迟晚于首屏落地。
onMounted(() => update.start())
onBeforeUnmount(() => {
  if (typeof window !== 'undefined') {
    window.removeEventListener('llmgw:open-account', onOpenAccountEvent)
  }
})
</script>

<template>
  <div id="hyper-app-root" class="hyper-app" :class="{ 'hyper-app--bare': headerless }">
    <AppTopbar
      v-if="!headerless"
      :actions="topbarActions"
      :overlay-active="overlayActive"
      :on-close-overlay="closeActiveOverlay"
      @menu="drawerOpen = true"
      @account="accountOpen = true"
    />

    <main id="main-content" class="hyper-app__main" :class="{ 'hyper-app__main--padded': showBottomNav }">
      <RouterView v-slot="{ Component }">
        <Transition name="page-fade" mode="out-in">
          <component :is="Component" :key="route.path" />
        </Transition>
      </RouterView>
    </main>

    <AppBottomNav v-if="showBottomNav" @more="drawerOpen = true" />
    <AppUpdateBanner />
    <AppDrawer v-model="drawerOpen" @account="accountOpen = true" />
    <AppAccountSheet v-model="accountOpen" />
  </div>
</template>

<style scoped>
.hyper-app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  height: 100dvh;
  min-height: 0;
  /* 横向 safe-area 的**流内**消费点（10 §4.6.32）：`--app-safe-left/right`
     此前只有声明、零消费者，于是横屏刘海 / 曲面屏 / 折叠屏侧边一律撞内容。
     放壳根而不是各页，是因为横屏下**流内**内容都可能被切到 —— 页内自补会漏。
     竖屏与桌面该值恒 0 ⇒ 本行不产生任何位移，桌面零回归。
     （box-sizing 已在 shared.css:8 全局 border-box，横向 padding 不外扩。）
     ⚠️ 订正（10 §4.6.61）：原文此处写的是「单点消费」，**说过了**。
     本壳根没有 transform/filter/contain，而抽屉 / Sheet / FocusLayer 是
     `position:fixed` + `Teleport to="body"`、底栏与更新条也是 fixed
     ⇒ 它们的包含块是**视口**，这行 padding 对它们**完全无效**。
     现在是「壳根管流内 + 各 fixed 浮层自己管自己」两处，缺一不可。 */
  padding-inline: var(--app-safe-left) var(--app-safe-right);
}

/* 100dvh 仅 iOS 15.4+；先 100vh fallback 再增强（14 §3） */
@supports (height: 100dvh) {
  .hyper-app {
    height: 100dvh;
  }
}

.hyper-app__main {
  flex: 1;
  min-height: 0;
  /* 页面自带滚动根（HyperList / PullRefreshContainer / .page）：
     main 只做布局容器不滚动——一个轴一个主宿主（07 §1）。 */
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.hyper-app__main--padded {
  padding-bottom: calc(var(--app-bottomnav-height) + var(--app-safe-bottom));
}

.page-fade-enter-active,
.page-fade-leave-active {
  transition: opacity 160ms ease;
}

.page-fade-enter-from,
.page-fade-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .page-fade-enter-active,
  .page-fade-leave-active {
    transition: none;
  }
}
</style>
