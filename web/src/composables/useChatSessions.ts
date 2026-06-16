import { computed, ref, watch } from 'vue'
import { store } from '../store'
import { addTokenUsage, emptyTokenUsage, type TokenUsage } from './useChatCompletions'

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
  /** Model selected when this user message was sent */
  requestedModel?: string
  /** Per-reply token usage (assistant messages) */
  usage?: TokenUsage
  /** Canonical model used for this assistant reply */
  resolvedModel?: string
}

export interface ChatSession {
  id: string
  taskId: string
  gwSessionId: string | null
  /** API key id that owns gwSessionId; mismatch → discard gw session */
  apiKeyId: number | null
  title: string
  /** Optional LLM-generated session summary */
  summary?: string
  messages: ChatMessage[]
  /** User-selected model (auto or canonical_name) */
  model: string
  /** Last actually invoked canonical model (updated each reply) */
  lastResolvedModel?: string | null
  /** Cumulative token usage for this session */
  usage?: TokenUsage
  createdAt: number
  updatedAt: number
}

const TITLE_MAX_LEN = 24
const STORAGE_VERSION = 2
const STORAGE_VERSION_LEGACY = 1

function storageKey(version: number = STORAGE_VERSION): string {
  const uid = store.userInfo?.id
  const tag = `llmgw_chat_v${version}`
  if (uid != null) return `${tag}:user:${uid}`
  if (store.apiKey) return `${tag}:apikey:${store.apiKey.slice(0, 12)}`
  return `${tag}:anon`
}

function legacyStorageKeys(): string[] {
  const suffix = (tag: string) => {
    const uid = store.userInfo?.id
    if (uid != null) return `${tag}:user:${uid}`
    if (store.apiKey) return `${tag}:apikey:${store.apiKey.slice(0, 12)}`
    return `${tag}:anon`
  }
  const tag = `llmgw_chat_v${STORAGE_VERSION_LEGACY}`
  // Common legacy shapes: scoped to current identity, plus any orphan keys
  // (handles logout / API key rotation between versions).
  const keys = new Set<string>([suffix(tag)])
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && k.startsWith(`${tag}:`)) keys.add(k)
    }
  } catch {
    // ignore (e.g. privacy mode)
  }
  return [...keys]
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
    usage: raw.usage ?? emptyTokenUsage(),
    lastResolvedModel: raw.lastResolvedModel ?? null,
  }
}

function loadAll(): ChatSession[] {
  const readKey = (key: string): ChatSession[] | null => {
    try {
      const raw = localStorage.getItem(key)
      if (!raw) return null
      const parsed = JSON.parse(raw) as ChatSession[]
      return Array.isArray(parsed) ? parsed.map(normalizeSession) : null
    } catch {
      return null
    }
  }

  const migrated = readKey(storageKey(STORAGE_VERSION))
  if (migrated && migrated.length > 0) return migrated

  // v1 → v2 migration: read legacy keys, write to v2, then remove legacy entries.
  const legacy: ChatSession[] = []
  for (const key of legacyStorageKeys()) {
    const data = readKey(key)
    if (data) legacy.push(...data)
  }
  if (legacy.length > 0) {
    try {
      localStorage.setItem(storageKey(STORAGE_VERSION), JSON.stringify(legacy))
      for (const key of legacyStorageKeys()) localStorage.removeItem(key)
    } catch {
      // best-effort: even if migration write fails, keep using in-memory data
    }
  }
  return legacy
}

function saveAll(sessions: ChatSession[]) {
  const newKey = storageKey(STORAGE_VERSION)
  localStorage.setItem(newKey, JSON.stringify(sessions))
  // Defensive: drop any v1 entries that may still exist (e.g. from a partial
  // migration before this code shipped).
  for (const k of legacyStorageKeys()) {
    if (k !== newKey) localStorage.removeItem(k)
  }
}

/** Display label for session model (handles auto → resolved, multi-model). */
export function formatSessionModelLabel(
  session: Pick<ChatSession, 'model' | 'lastResolvedModel' | 'messages'>,
  displayNames?: Map<string, string>,
): string {
  const resolved = session.lastResolvedModel
  const display = (name: string) => displayNames?.get(name) || name

  const used = new Set<string>()
  for (const m of session.messages ?? []) {
    if (m.requestedModel) used.add(m.requestedModel)
    if (m.resolvedModel) used.add(m.resolvedModel)
  }

  if (session.model === 'auto') {
    if (used.size > 1 && resolved) {
      return `自动 · 多模型 (最近 ${display(resolved)})`
    }
    return resolved ? `自动 → ${display(resolved)}` : '自动路由'
  }

  if (used.size > 1) {
    return `多模型 (当前 ${display(session.model)})`
  }
  return display(session.model)
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
      lastResolvedModel: null,
      usage: emptyTokenUsage(),
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

  function updateSession(
    id: string,
    patch: Partial<
      Pick<
        ChatSession,
        | 'messages'
        | 'model'
        | 'gwSessionId'
        | 'apiKeyId'
        | 'taskId'
        | 'title'
        | 'summary'
        | 'lastResolvedModel'
        | 'usage'
      >
    >,
  ) {
    const s = sessions.value.find((x) => x.id === id)
    if (!s) return
    if (patch.messages !== undefined) s.messages = patch.messages
    if (patch.model !== undefined) s.model = patch.model
    if (patch.gwSessionId !== undefined) s.gwSessionId = patch.gwSessionId
    if (patch.apiKeyId !== undefined) s.apiKeyId = patch.apiKeyId
    if (patch.taskId !== undefined) s.taskId = patch.taskId
    if (patch.title !== undefined) s.title = patch.title
    if (patch.summary !== undefined) s.summary = patch.summary
    if (patch.lastResolvedModel !== undefined) s.lastResolvedModel = patch.lastResolvedModel
    if (patch.usage !== undefined) s.usage = patch.usage
    s.updatedAt = Date.now()
    persist()
  }

  function deleteSession(id: string): ChatSession | null {
    const idx = sessions.value.findIndex((s) => s.id === id)
    if (idx < 0) return null
    const removed = sessions.value[idx]
    sessions.value = sessions.value.filter((s) => s.id !== id)
    if (activeId.value === id) {
      activeId.value = sessions.value[0]?.id ?? null
      if (!activeId.value) createSession()
    }
    persist()
    return removed
  }

  function accumulateUsage(
    patch: Partial<Pick<ChatSession, 'messages' | 'model' | 'gwSessionId' | 'apiKeyId' | 'taskId' | 'title' | 'summary' | 'lastResolvedModel'>>,
    delta: TokenUsage | null | undefined,
  ) {
    const s = activeSession.value
    if (!s) return
    Object.assign(s, patch)
    if (delta) {
      s.usage = addTokenUsage(s.usage ?? emptyTokenUsage(), delta)
    }
    s.updatedAt = Date.now()
    if (s.messages.length > 0 && s.title === '新对话') {
      s.title = titleFromFirstUserMessage(s.messages)
    }
    persist()
  }

  function updateActive(
    patch: Partial<
      Pick<
        ChatSession,
        | 'messages'
        | 'model'
        | 'gwSessionId'
        | 'apiKeyId'
        | 'taskId'
        | 'title'
        | 'summary'
        | 'lastResolvedModel'
        | 'usage'
      >
    >,
  ) {
    const s = activeSession.value
    if (!s) return
    if (patch.messages !== undefined) s.messages = patch.messages
    if (patch.model !== undefined) s.model = patch.model
    if (patch.gwSessionId !== undefined) s.gwSessionId = patch.gwSessionId
    if (patch.apiKeyId !== undefined) s.apiKeyId = patch.apiKeyId
    if (patch.taskId !== undefined) s.taskId = patch.taskId
    if (patch.title !== undefined) s.title = patch.title
    if (patch.summary !== undefined) s.summary = patch.summary
    if (patch.lastResolvedModel !== undefined) s.lastResolvedModel = patch.lastResolvedModel
    if (patch.usage !== undefined) s.usage = patch.usage
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
    updateSession,
    deleteSession,
    updateActive,
    accumulateUsage,
    setGwSessionId,
    resetGatewayBinding,
    ensureSessionApiKey,
    clearAllGwSessionIds,
    titleFromFirstUserMessage,
    formatSessionModelLabel,
  }
}
