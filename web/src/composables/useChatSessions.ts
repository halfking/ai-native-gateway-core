import { computed, ref, watch } from 'vue'
import { store } from '../store'

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
}

export interface ChatSession {
  id: string
  taskId: string
  gwSessionId: string | null
  /** API key id that owns gwSessionId; mismatch → discard gw session */
  apiKeyId: number | null
  title: string
  messages: ChatMessage[]
  model: string
  createdAt: number
  updatedAt: number
}

const TITLE_MAX_LEN = 24
const STORAGE_VERSION = 1

function storageKey(): string {
  const uid = store.userInfo?.id
  if (uid != null) return `llmgw_chat_v${STORAGE_VERSION}:user:${uid}`
  if (store.apiKey) return `llmgw_chat_v${STORAGE_VERSION}:apikey:${store.apiKey.slice(0, 12)}`
  return `llmgw_chat_v${STORAGE_VERSION}:anon`
}

function newTaskId(): string {
  return `chat-web-${crypto.randomUUID()}`
}

function newSessionId(): string {
  return crypto.randomUUID()
}

function titleFromFirstUserMessage(messages: ChatMessage[]): string {
  const first = messages.find((m) => m.role === 'user' && m.content.trim())
  if (!first) return '新对话'
  const t = first.content.trim().replace(/\s+/g, ' ')
  return t.length <= TITLE_MAX_LEN ? t : `${t.slice(0, TITLE_MAX_LEN)}…`
}

function normalizeSession(raw: ChatSession): ChatSession {
  return {
    ...raw,
    apiKeyId: raw.apiKeyId ?? null,
    gwSessionId: raw.apiKeyId == null && raw.gwSessionId ? null : (raw.gwSessionId ?? null),
  }
}

function loadAll(): ChatSession[] {
  try {
    const raw = localStorage.getItem(storageKey())
    if (!raw) return []
    const parsed = JSON.parse(raw) as ChatSession[]
    return Array.isArray(parsed) ? parsed.map(normalizeSession) : []
  } catch {
    return []
  }
}

function saveAll(sessions: ChatSession[]) {
  localStorage.setItem(storageKey(), JSON.stringify(sessions))
}

export function useChatSessions() {
  const sessions = ref<ChatSession[]>(loadAll())
  const activeId = ref<string | null>(sessions.value[0]?.id ?? null)

  const activeSession = computed(() =>
    sessions.value.find((s) => s.id === activeId.value) ?? null,
  )

  function persist() {
    saveAll(sessions.value)
  }

  function createSession(model = 'auto'): ChatSession {
    const now = Date.now()
    const session: ChatSession = {
      id: newSessionId(),
      taskId: newTaskId(),
      gwSessionId: null,
      apiKeyId: null,
      title: '新对话',
      messages: [],
      model,
      createdAt: now,
      updatedAt: now,
    }
    sessions.value = [session, ...sessions.value]
    activeId.value = session.id
    persist()
    return session
  }

  function ensureActive(model = 'auto'): ChatSession {
    if (activeSession.value) return activeSession.value
    return createSession(model)
  }

  function switchSession(id: string) {
    if (sessions.value.some((s) => s.id === id)) {
      activeId.value = id
    }
  }

  /** Archive current chat and start a fresh session (Clear). */
  function startNewSession(model = 'auto'): ChatSession {
    const current = activeSession.value
    if (current && current.messages.length > 0) {
      current.title = titleFromFirstUserMessage(current.messages)
      current.updatedAt = Date.now()
      persist()
    }
    return createSession(model)
  }

  function updateActive(
    patch: Partial<Pick<ChatSession, 'messages' | 'model' | 'gwSessionId' | 'apiKeyId' | 'taskId' | 'title'>>,
  ) {
    const s = activeSession.value
    if (!s) return
    if (patch.messages !== undefined) s.messages = patch.messages
    if (patch.model !== undefined) s.model = patch.model
    if (patch.gwSessionId !== undefined) s.gwSessionId = patch.gwSessionId
    if (patch.apiKeyId !== undefined) s.apiKeyId = patch.apiKeyId
    if (patch.taskId !== undefined) s.taskId = patch.taskId
    if (patch.title !== undefined) s.title = patch.title
    s.updatedAt = Date.now()
    if (s.messages.length > 0 && s.title === '新对话') {
      s.title = titleFromFirstUserMessage(s.messages)
    }
    persist()
  }

  function setGwSessionId(gwSessionId: string, apiKeyId?: number | null) {
    updateActive({
      gwSessionId,
      ...(apiKeyId !== undefined ? { apiKeyId } : {}),
    })
  }

  /** Drop gateway session binding when API key changes or ownership mismatches. */
  function resetGatewayBinding(apiKeyId: number | null) {
    const s = activeSession.value
    if (!s) return
    if (s.apiKeyId === apiKeyId && s.gwSessionId) return
    updateActive({
      apiKeyId,
      gwSessionId: null,
      taskId: newTaskId(),
    })
  }

  function ensureSessionApiKey(apiKeyId: number): { gwSessionId: string | null; taskId: string } {
    const s = activeSession.value ?? ensureActive()
    if (s.apiKeyId != null && s.apiKeyId !== apiKeyId) {
      updateActive({ apiKeyId, gwSessionId: null, taskId: newTaskId() })
    } else if (s.apiKeyId == null) {
      updateActive({ apiKeyId })
    }
    const current = activeSession.value!
    return { gwSessionId: current.gwSessionId, taskId: current.taskId }
  }

  /** Drop gateway session bindings when switching API key. */
  function clearAllGwSessionIds() {
    let changed = false
    for (const s of sessions.value) {
      if (s.gwSessionId || s.apiKeyId != null) {
        s.gwSessionId = null
        s.apiKeyId = null
        s.taskId = newTaskId()
        changed = true
      }
    }
    if (changed) persist()
  }

  // Reload when user identity changes (login/logout).
  watch(
    () => [store.userInfo?.id, store.apiKey] as const,
    () => {
      sessions.value = loadAll()
      activeId.value = sessions.value[0]?.id ?? null
      if (!activeId.value) createSession()
    },
  )

  if (!activeId.value && sessions.value.length === 0) {
    createSession()
  }

  return {
    sessions,
    activeId,
    activeSession,
    createSession,
    ensureActive,
    switchSession,
    startNewSession,
    updateActive,
    setGwSessionId,
    resetGatewayBinding,
    ensureSessionApiKey,
    clearAllGwSessionIds,
    titleFromFirstUserMessage,
  }
}
