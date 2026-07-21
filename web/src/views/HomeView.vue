<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { store } from '../store'
import DashboardView from './DashboardView.vue'
import LandingView from './LandingView.vue'

const route = useRoute()
const router = useRouter()

const isLoggedIn = computed(() => !!(store.jwtToken || store.apiKey || store.userInfo))

// 2026-07-21: 根路径 /
//  - 未登录: redirect 到 ai-native-maintain 首页 (/maintain/home)
//  - 已登录: 显示 DashboardView（总览）
// 仅当 query.login=1 时（被 App.vue 触发）才内嵌 LandingView（用于弹登录框）。
const showLoginOverlay = computed(() => route.query.login === '1' || route.query.login === 'true')

// 未登录态且非 /dashboard 入口：直接跳到 maintain SPA 首页。
onMounted(() => {
  if (!isLoggedIn.value && !showLoginOverlay.value) {
    if (typeof window !== 'undefined') {
      window.location.replace('/maintain/home')
    }
  }
})
</script>

<template>
  <DashboardView v-if="isLoggedIn" />
  <LandingView v-else-if="showLoginOverlay" />
  <!-- 未登录未触发 login overlay：已在外层 redirect，此处保留占位 -->
  <div v-else class="guest-redirect-hint">正在跳转至产品首页…</div>
</template>