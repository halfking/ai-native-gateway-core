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
const src = computed(() => `/plugins/${pluginId.value}/`)
const hash = computed(() => {
  const params = new URLSearchParams({
    page: page.value || '',
    tenant: store.userInfo?.tenant_id || '',
    locale: store.locale || 'zh-CN',
  })
  return '#' + params.toString()
})

// P6/P8: degraded/failed fallback.
// First load timeout = degraded (recoverable). ≥FAILED_THRESHOLD consecutive
// failures (across route re-entries, via sessionStorage) = failed (terminal,
// needs human intervention — e.g. plugin exhausted auto-restart limit).
const FAILED_THRESHOLD = 3
const loadFailed = ref(false)
const isFailed = ref(false)
let loadTimer: number | null = null

function failureKey(): string {
  return `plugin-fail-count:${pluginId.value}`
}
function failureCount(): number {
  try { return Number(sessionStorage.getItem(failureKey()) || '0') } catch { return 0 }
}
function bumpFailure(): number {
  const n = failureCount() + 1
  try { sessionStorage.setItem(failureKey(), String(n)) } catch { /* ignore */ }
  return n
}
function resetFailure() {
  try { sessionStorage.removeItem(failureKey()) } catch { /* ignore */ }
}

function onLoad() {
  if (loadTimer !== null) { clearTimeout(loadTimer); loadTimer = null }
  loadFailed.value = false
  resetFailure()
}
function markFailed() {
  loadFailed.value = true
  const n = bumpFailure()
  isFailed.value = n >= FAILED_THRESHOLD
}

onMounted(() => {
  const n = failureCount()
  if (n >= FAILED_THRESHOLD) { isFailed.value = true; loadFailed.value = true }
  loadTimer = window.setTimeout(markFailed, 5000)
})
onBeforeUnmount(() => {
  if (loadTimer !== null) { clearTimeout(loadTimer); loadTimer = null }
})
</script>

<template>
  <div class="plugin-mount">
    <div v-if="loadFailed" class="plugin-degraded">
      <template v-if="isFailed">
        <p>插件 {{ pluginId }} 持续不可用（多次加载失败）。</p>
        <p class="muted">可能原因：插件进程反复崩溃已耗尽自动重启上限、或安装损坏。请联系管理员检查插件状态或重新安装。</p>
      </template>
      <template v-else>
        <p>插件 {{ pluginId }} 暂时不可用（未能加载）。</p>
        <p class="muted">可能原因：插件未启动、进程已退出、或网络中断。请稍后重试或联系管理员。</p>
      </template>
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
