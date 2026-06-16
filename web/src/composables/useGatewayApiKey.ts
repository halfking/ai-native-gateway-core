import { onMounted, ref } from 'vue'
import { store } from '../store'
import { getKeys, revealKey } from '../api'

/** Resolve sk-* for /v1/* calls. JWT admin login does not populate store.apiKey. */
export function useGatewayApiKey() {
  const apiKey = ref(store.apiKey)
  const loading = ref(false)
  const error = ref('')

  async function resolve(): Promise<string> {
    if (store.apiKey) {
      apiKey.value = store.apiKey
      return apiKey.value
    }
    loading.value = true
    error.value = ''
    try {
      const keys = await getKeys()
      const active = keys.find((k) => k.enabled && k.status === 'active')
      if (!active) {
        error.value = '没有可用的 API 密钥，请先在「API 密钥」页面创建或启用'
        apiKey.value = ''
        return ''
      }
      const revealed = await revealKey(active.id)
      apiKey.value = revealed.api_key
      return apiKey.value
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '获取 API 密钥失败'
      apiKey.value = ''
      return ''
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    void resolve()
  })

  return { apiKey, loading, error, resolve }
}
