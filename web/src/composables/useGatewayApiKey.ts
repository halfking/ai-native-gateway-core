import { onMounted, ref } from 'vue'
import type { ApiKey } from '../api'
import { getKeys, revealKey } from '../api'
import {
  store,
  setApiKey,
  getPreferredChatKeyId,
  setPreferredChatKeyId,
  clearPreferredChatKeyId,
} from '../store'
import { formatApiKeyLabel, isActiveApiKey, isNoCiphertextError } from '../utils/apiKey'

/** Resolve sk-* for /v1/* calls. JWT admin login does not populate store.apiKey. */
export function useGatewayApiKey() {
  const apiKey = ref(store.apiKey)
  const loading = ref(false)
  const error = ref('')
  const showPicker = ref(false)
  const candidateKeys = ref<ApiKey[]>([])
  const picking = ref(false)

  function useStoredApiKey(): string {
    apiKey.value = store.apiKey
    return store.apiKey
  }

  async function tryRevealKey(id: number): Promise<string | null> {
    try {
      const revealed = await revealKey(id)
      const key = revealed.api_key
      if (!key) return null
      apiKey.value = key
      setApiKey(key)
      setPreferredChatKeyId(id)
      error.value = ''
      showPicker.value = false
      return key
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e)
      if (isNoCiphertextError(msg)) return null
      throw e
    }
  }

  async function resolve(): Promise<string> {
    if (store.apiKey?.startsWith('sk-')) {
      return useStoredApiKey()
    }

    loading.value = true
    error.value = ''
    try {
      const keys = await getKeys()
      const active = keys.filter(isActiveApiKey)
      if (!active.length) {
        error.value = '没有可用的 API 密钥，请先在「API 密钥」页面创建或启用'
        showPicker.value = false
        candidateKeys.value = []
        apiKey.value = ''
        return ''
      }

      const preferredId = getPreferredChatKeyId()
      if (preferredId) {
        const preferred = active.find((k) => k.id === preferredId)
        if (preferred) {
          const key = await tryRevealKey(preferredId)
          if (key) return key
          clearPreferredChatKeyId()
        }
      }

      if (active.length === 1) {
        const key = await tryRevealKey(active[0].id)
        if (key) return key
        candidateKeys.value = active
        showPicker.value = true
        error.value =
          '当前密钥无法还原完整内容（历史密钥未保存密文）。请重新签发或选择其他密钥。'
        apiKey.value = ''
        return ''
      }

      candidateKeys.value = active
      showPicker.value = true
      apiKey.value = ''
      return ''
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '获取 API 密钥失败'
      apiKey.value = ''
      return ''
    } finally {
      loading.value = false
    }
  }

  async function selectKey(id: number): Promise<boolean> {
    picking.value = true
    error.value = ''
    try {
      const key = await tryRevealKey(id)
      if (!key) {
        error.value =
          '此密钥无法还原完整内容（创建时未保存密文）。请重新签发或选择其他密钥。'
        return false
      }
      return true
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '获取密钥失败'
      return false
    } finally {
      picking.value = false
    }
  }

  onMounted(() => {
    void resolve()
  })

  return {
    apiKey,
    loading,
    error,
    showPicker,
    candidateKeys,
    picking,
    resolve,
    selectKey,
    formatApiKeyLabel,
  }
}
