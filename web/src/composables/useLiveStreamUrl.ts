// useLiveStreamUrl — SSE endpoint URL 管理 composable
//
// 职责：
//  1) 默认值走 window.location.origin + ENDPOINT_PATH
//  2) localStorage 里允许管理员保存一个自定义 URL（反向代理 / 内网穿透）
//  3) 保存后立即用新地址重连（reconnect 由调用方注入）
//  4) 测试当前 SSE 连接状态（alert 提示）
//
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取（净减组件 ~120 行）。
// 历史说明：组件原有一对 buildFinalUrl / liveUrl（?token= 降级 + 暴露给 store），
// 但 store 通过 getCustomEndpoint() 直接读 localStorage（liveStreamStore.ts），
// 且 buildFinalUrl/liveUrl 在本组件零消费 —— 属冗余复制，抽取时一并移除。

import { ref, computed, watch, onMounted, type Ref } from 'vue'
import type { ConnectionState } from './liveStreamStore'
import { getCustomEndpoint, setCustomEndpoint } from './liveStreamStore'

export interface UseLiveStreamUrlOptions {
  /** 当前 SSE 连接状态（来自 useLiveStream） */
  connection: Ref<ConnectionState>
  /** 保存/重置新地址后立即重连（调用方注入 useLiveStream().reconnect） */
  reconnect: () => void
  /** i18n 翻译函数（组件注入 useI18n().t） */
  t: (key: string, named?: Record<string, unknown>) => string
}

const ENDPOINT_PATH = '/api/admin/live-stream'

export function useLiveStreamUrl(options: UseLiveStreamUrlOptions) {
  const { connection, reconnect, t } = options

  const defaultStreamUrl = computed(() => `${window.location.origin}${ENDPOINT_PATH}`)
  const streamUrl = ref('')
  const isEditingUrl = ref(false)
  const editUrlValue = ref('')

  // LP8 (2026-08-24): localStorage reads / writes for the SSE endpoint go
  // through liveStreamStore (which now uses usePersistedValue under the
  // hood). Read lazily on mount so a freshly-saved endpoint lands in the
  // composable's streamUrl ref immediately.
  function readStoredUrl(): string {
    return getCustomEndpoint() || ''
  }

  // 挂载时读取 localStorage 中管理员保存的自定义地址
  onMounted(() => {
    streamUrl.value = readStoredUrl() || defaultStreamUrl.value
  })

  // 如果用户修改了 window.location（多 tab 测试），默认地址也跟着变
  watch(defaultStreamUrl, (cur) => {
    if (!readStoredUrl()) streamUrl.value = cur
  })

  function startEditUrl() {
    editUrlValue.value = streamUrl.value
    isEditingUrl.value = true
  }

  // 保存 URL —— 立刻用新地址重连 SSE
  function saveUrl() {
    const url = editUrlValue.value.trim()
    if (url) {
      streamUrl.value = url
      setCustomEndpoint(url)
      reconnect()
    }
    isEditingUrl.value = false
  }

  // 重置为默认 URL
  function resetUrl() {
    streamUrl.value = defaultStreamUrl.value
    setCustomEndpoint('')
    isEditingUrl.value = false
    reconnect()
  }

  // 取消编辑
  function cancelEditUrl() {
    isEditingUrl.value = false
  }

  // 测试 SSE 连接
  function testConnection() {
    if (connection.value === 'open') {
      window.alert(t('dashboard.liveStream.sseTestOk', { url: streamUrl.value }))
    } else {
      window.alert(t('dashboard.liveStream.sseTestFail', { status: connection.value, url: streamUrl.value }))
    }
  }

  return {
    defaultStreamUrl,
    streamUrl,
    isEditingUrl,
    editUrlValue,
    startEditUrl,
    saveUrl,
    resetUrl,
    cancelEditUrl,
    testConnection,
  }
}
