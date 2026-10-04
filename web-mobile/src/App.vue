<script setup lang="ts">
// App.vue — 根组件：popstate 接 BackDispatcher、路由标题席清空、水合 /api/auth/me。
import { onBeforeUnmount, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import AppShell from './components/shell/AppShell.vue'
import { backDispatcher } from './runtime/backDispatcher'
import { fetchMe } from './api/auth'
import { setUserInfo, isAuthenticated } from './api/_core'
import { nextSessionEpoch } from './runtime/epochs'

const route = useRoute()
const bd = backDispatcher()

async function hydrate(): Promise<void> {
  if (!isAuthenticated()) {
    try {
      const me = await fetchMe()
      const user = (me as { user?: unknown }).user ?? me
      setUserInfo(user as Record<string, unknown>)
    } catch {
      // 401 = 未登录，正常路径非错误（17 §5）；_core 已处理跳转。
    }
  }
}

function onPopState(): void {
  bd.handlePopState(() => {
    /* 路由 pop 由 vue-router 自己消费 history；此处只仲裁覆盖层。 */
  })
}

onMounted(() => {
  window.addEventListener('popstate', onPopState)
  void hydrate()
  nextSessionEpoch()
})

onBeforeUnmount(() => {
  window.removeEventListener('popstate', onPopState)
})
</script>

<template>
  <AppShell :show-tabbar="route.path !== '/login'">
    <RouterView />
  </AppShell>
</template>
