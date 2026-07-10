<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { store } from '../store'

const router = useRouter()
const route = useRoute()

onMounted(async () => {
  // 2026-07-10: 不要在 /login route 直接 openLogin + replace '/' — 这
  // 会跟 App.vue 的 `?login=1` watcher 抢逻辑，导致 modal-overlay 拦截
  // 整个页面（用户报告「反复弹出登录窗然后刷新」）。改成统一让
  // router beforeEach 把 ?login=1 注入到 query，App.vue watcher 自动处理。
  // 如果用户已经登录（store.userInfo 或 cookie 探测成功），直接去 /
  const target = typeof route.query.redirect === 'string' && route.query.redirect.startsWith('/')
    ? { path: route.query.redirect, query: { login: '1' } }
    : { path: '/', query: { login: '1' } }
  await router.replace(target)
})
</script>

<template>
  <div />
</template>
