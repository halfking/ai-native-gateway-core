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

function keyMatchesPrefix(fullKey: string, prefix: string): boolean {
  const bare = prefix.replace(/\*+$/, '')
  return bare.length > 0 && fullKey.startsWith(bare)
}

/** Resolve sk-* for /v1/* calls. JWT admin login does not populate store.apiKey. */
export function useGatewayApiKey() {
  const apiKey = ref(store.apiKey)
  const loading = ref(false)
  const error = ref('')
  const showPicker = ref(false)
  const showKeyModal = ref(false)
  const keyModalReason = ref<'session-forbidden' | 'manual'>('manual')
  const candidateKeys = ref<ApiKey[]>([])
  const picking = ref(false)
  const selectedKeyId = ref<number | null>(getPreferredChatKeyId())
  const selectedKeyMeta = ref<ApiKey | null>(null)

  function syncSelectedMeta(id: number | null) {
    selectedKeyMeta.value = id ? candidateKeys.value.find((k) => k.id === id) ?? null : null
  }

  async function loadActiveKeys(): Promise<ApiKey[]> {
    const keys = await getKeys()
    const active = keys.filter(isActiveApiKey)
    candidateKeys.value = active
    return active
  }

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
      selectedKeyId.value = id
      syncSelectedMeta(id)
      error.value = ''
      showPicker.value = false
      return key
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e)
      if (isNoCiphertextError(msg)) return null
      throw e
    }
  }

  /** Try preferred key first, then every active key in list order. */
  async function revealFirstAvailable(active: ApiKey[]): Promise<string> {
    const preferredId = getPreferredChatKeyId()
    const ordered: ApiKey[] = []
    if (preferredId) {
      const preferred = active.find((k) => k.id === preferredId)
      if (preferred) ordered.push(preferred)
    }
    for (const k of active) {
      if (!ordered.some((x) => x.id === k.id)) ordered.push(k)
    }

    for (const k of ordered) {
      const key = await tryRevealKey(k.id)
      if (key) return key
    }

    if (preferredId) clearPreferredChatKeyId()
    candidateKeys.value = active
    showPicker.value = true
    error.value =
      active.length === 1
        ? '当前密钥无法还原完整内容（历史密钥未保存密文）。请重新签发或选择其他密钥。'
        : '没有可自动还原的密钥，请从下方选择或重新签发。'
    apiKey.value = ''
    selectedKeyId.value = null
    return ''
  }

  function openPicker() {
    showPicker.value = true
    void loadActiveKeys()
  }

  function openKeyModal(reason: 'session-forbidden' | 'manual' = 'manual') {
    keyModalReason.value = reason
    showKeyModal.value = true
    void loadActiveKeys()
  }

  function closeKeyModal() {
    showKeyModal.value = false
  }

  async function resolve(): Promise<string> {
    loading.value = true
    error.value = ''
    try {
      const active = await loadActiveKeys()
      if (!active.length) {
        error.value = '没有可用的 API 密钥，请先在「API 密钥」页面创建或启用'
        showPicker.value = false
        apiKey.value = ''
        selectedKeyId.value = null
        return ''
      }

      if (store.apiKey?.startsWith('sk-')) {
        const match = active.find((k) => keyMatchesPrefix(store.apiKey, k.key_prefix))
        if (match) {
          selectedKeyId.value = match.id
          setPreferredChatKeyId(match.id)
          syncSelectedMeta(match.id)
          return useStoredApiKey()
        }
      }

      return revealFirstAvailable(active)
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : '获取 API 密钥失败'
      apiKey.value = ''
      selectedKeyId.value = null
      return ''
    } finally {
      loading.value = false
    }
  }

  async function selectKey(id: number): Promise<boolean> {
    if (id === selectedKeyId.value && apiKey.value?.startsWith('sk-')) {
      return true
    }

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
    showKeyModal,
    keyModalReason,
    candidateKeys,
    picking,
    selectedKeyId,
    selectedKeyMeta,
    resolve,
    selectKey,
    openPicker,
    openKeyModal,
    closeKeyModal,
    loadActiveKeys,
    formatApiKeyLabel,
  }
}
