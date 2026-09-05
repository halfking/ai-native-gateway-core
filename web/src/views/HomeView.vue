<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElAlert } from 'element-plus'
import { store } from '../store'
import { probeMaintainAvailable } from '../config/edition'
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
const maintainUnavailable = ref(false)

// 未登录态先探测 Maintain；不可用时留在 Gateway 登录页，避免整页互踢。
onMounted(async () => {
  if (!isLoggedIn.value && !showLoginOverlay.value) {
    const available = await probeMaintainAvailable()
    if (available && typeof window !== 'undefined') {
      window.location.replace('/maintain/home')
    } else {
      maintainUnavailable.value = true
      await router.replace({ path: '/', query: { login: '1' } })
    }
  }
})
</script>

<template>
  <DashboardView v-if="isLoggedIn" />
  <template v-else>
    <el-alert
      v-if="maintainUnavailable"
      class="maintain-unavailable-alert"
      type="warning"
      :closable="false"
      title="运维平台暂时不可用，已保留在网关登录页。"
    />
    <LandingView />
  </template>
</template>