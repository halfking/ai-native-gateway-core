<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import { useRoute } from 'vue-router'
import { store } from '@/store'

const route = useRoute()
const pluginId = computed(() => route.params.pluginId as string)
const page = computed(() => {
  const p = route.params.page
  return Array.isArray(p) ? p.join('/') : p
})
// iframe src 指向插件 web 资源（Gateway 反代 /plugins/<id>/）。
const src = computed(() => `/plugins/${pluginId.value}/`)
// 用 hash 把上下文传给 iframe（不携带 JWT）。
const hash = computed(() => {
  const params = new URLSearchParams({
    page: page.value || '',
    tenant: store.userInfo?.tenant_id || '',
    locale: store.locale || 'zh-CN',
  })
  return '#' + params.toString()
})

// P6: degraded fallback —— iframe 加载失败或超时则展示提示信息。
// 跨域 iframe 不会可靠地触发 onerror，因此 5s 超时是主要判定手段。
// 这是尽力而为的 UX；权威降级状态在 plugin-nav。
const loadFailed = ref(false)
let loadTimer: number | null = null

function onLoad() {
  if (loadTimer !== null) {
    clearTimeout(loadTimer)
    loadTimer = null
  }
  loadFailed.value = false
}
function markFailed() {
  loadFailed.value = true
}

onMounted(() => {
  // 5s 未成功加载 → 视为降级。
  loadTimer = window.setTimeout(markFailed, 5000)
})
onBeforeUnmount(() => {
  if (loadTimer !== null) {
    clearTimeout(loadTimer)
    loadTimer = null
  }
})
</script>

<template>
  <div class="plugin-mount">
    <div v-if="loadFailed" class="plugin-degraded">
      <p>插件 {{ pluginId }} 暂时不可用（未能加载）。</p>
      <p class="muted">可能原因：插件未启动、进程已退出、或网络中断。请稍后重试或联系管理员。</p>
    </div>
    <iframe
      v-show="!loadFailed"
      :src="src + hash"
      :title="`plugin-${pluginId}`"
      class="plugin-iframe"
      @load="onLoad"
      @error="markFailed"
    />
  </div>
</template>

<style scoped>
.plugin-mount { height: calc(100vh - 56px); width: 100%; }
.plugin-iframe { width: 100%; height: 100%; border: 0; }
.plugin-degraded { padding: 4rem 2rem 2rem; color: #666; text-align: center; }
.plugin-degraded .muted { color: #999; font-size: 0.9em; margin-top: 0.5rem; }
</style>
