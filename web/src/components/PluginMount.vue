<script setup lang="ts">
import { computed } from 'vue'
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
</script>

<template>
  <div class="plugin-mount">
    <iframe :src="src + hash" :title="`plugin-${pluginId}`" class="plugin-iframe" />
  </div>
</template>

<style scoped>
.plugin-mount { height: calc(100vh - 56px); width: 100%; }
.plugin-iframe { width: 100%; height: 100%; border: 0; }
</style>
