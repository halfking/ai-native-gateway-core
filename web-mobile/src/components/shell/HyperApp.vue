<script setup lang="ts">
// HyperApp — 应用壳（唯一布局）：顶栏 + 主滚动区 + compact 底栏 +
// 抽屉/账户 Sheet。headerless 路由（登录页）隐藏全部 chrome。
// DockCoordinator 的 effectiveTop 以本壳顶栏实测为准（07 §4）。
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useWindowClass } from '@/composables/useWindowClass'
import { useDeploySeqUpdate } from '@/hyper/update/useDeploySeqUpdate'
import AppTopbar from './AppTopbar.vue'
import AppBottomNav from './AppBottomNav.vue'
import AppDrawer from './AppDrawer.vue'
import AppAccountSheet from './AppAccountSheet.vue'
import AppUpdateBanner from './AppUpdateBanner.vue'

const route = useRoute()
const { windowClass } = useWindowClass()
const update = useDeploySeqUpdate()

const drawerOpen = ref(false)
const accountOpen = ref(false)

const headerless = computed(() => route.meta.headerless === true)
const showBottomNav = computed(() => !headerless.value && windowClass.value === 'compact')

// 部署序号检查（UI规范 18 §4）：headerless（登录页）同样检查——长期停在
// 登录页的设备也该收到更新提示；首次检查延迟晚于首屏落地。
onMounted(() => update.start())
</script>

<template>
  <div id="hyper-app-root" class="hyper-app" :class="{ 'hyper-app--bare': headerless }">
    <AppTopbar v-if="!headerless" @menu="drawerOpen = true" @account="accountOpen = true" />

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
