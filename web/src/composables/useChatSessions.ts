import { computed, onScopeDispose, ref, watch } from 'vue'
import { store } from '../store'
import { addTokenUsage, emptyTokenUsage, type TokenUsage } from './useChatCompletions'
import {
  createDebouncedFlush,
  installLifecycleFlush,
  installScopeCleanup,
  PERSIST_DEBOUNCE_MS,
} from './persistenceShared'

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
  /** Model selected when this user message was sent */
  requestedModel?: string
  /** Per-reply token usage (assistant messages) */
  usage?: TokenUsage
  /** Canonical model used for this assistant reply */
  resolvedModel?: string
  /** Track C client-side resume (2026-06-21): true when this assistant
   *  reply was reconstructed from the gateway pending-response cache
   *  rather than a fresh upstream call. UI may render a "已从缓存恢复"
   *  badge to make it obvious to the user. */
  resumed?: boolean
}

export type ChatResponseMode = 'stream' | 'chat'

export interface ChatSessionSettings {
  mode: ChatResponseMode
  systemPrompt: string
  temperature: number
  maxTokens: number
  topP: number
  presencePenalty: number
  frequencyPenalty: number
  stop: string[]
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
  /** Per-session response mode + sampling params (v3) */
  settings: ChatSessionSettings
  createdAt: number
  updatedAt: number
}

const TITLE_MAX_LEN = 24
const STORAGE_VERSION = 3
const STORAGE_VERSION_PREV = 2
const STORAGE_VERSION_LEGACY = 1

export function defaultChatSessionSettings(): ChatSessionSettings {
  return {
    mode: 'stream',
    systemPrompt: '',
    temperature: 0.7,
    maxTokens: 2048,
    topP: 1,
    presencePenalty: 0,
    frequencyPenalty: 0,
    stop: [],
  }
}

export function normalizeChatSessionSettings(
  raw?: Partial<ChatSessionSettings> | null,
): ChatSessionSettings {
  const d = defaultChatSessionSettings()
  if (!raw || typeof raw !== 'object') return d
  const mode = raw.mode === 'chat' ? 'chat' : 'stream'
  const stop = Array.isArray(raw.stop)
    ? raw.stop.filter((s): s is string => typeof s === 'string' && s.length > 0)
    : []
  return {
    mode,
    systemPrompt: typeof raw.systemPrompt === 'string' ? raw.systemPrompt : d.systemPrompt,
    temperature: clampNum(raw.temperature, 0, 2, d.temperature),
    maxTokens: clampNum(raw.maxTokens, 1, 128_000, d.maxTokens),
    topP: clampNum(raw.topP, 0, 1, d.topP),
    presencePenalty: clampNum(raw.presencePenalty, -2, 2, d.presencePenalty),
    frequencyPenalty: clampNum(raw.frequencyPenalty, -2, 2, d.frequencyPenalty),
    stop,
  }
}

function clampNum(v: unknown, min: number, max: number, fallback: number): number {
  if (typeof v !== 'number' || !Number.isFinite(v)) return fallback
  return Math.min(max, Math.max(min, v))
}
// 2026-08-24 LP6: coalesce whole-tree localStorage writes into a single
// flush PERSIST_DEBOUNCE_MS (300ms, defined in persistenceShared.ts) after
// the last mutation. Long enough to absorb a burst of streaming-token /
// model-switch updates, short enough that user-initiated actions (close
// tab, switch session) don't feel laggy. LP9 (2026-08-24) moved the
// debounce timer / lifecycle listeners / scope-cleanup plumbing into
// persistenceShared.ts so LP6, LP7 and LP8 share one implementation.

// Pending-cache resume key prefix (2026-06-21, Track C client-side resume).
// Holds the most-recent gw_session_id used per (user, task) so that after
// an IDE crash or browser refresh the chatCompletion() entry can call
// GET /v1/sessions/{sid}/pending-response to recover the cached body
// without re-running the LLM. Scoped per-user so a multi-account browser
// doesn't leak cache references across identities.
const PERSISTED_SID_KEY_PREFIX = 'llmgw_persisted_sid:'

function persistedSidStorageKey(): string {
  const uid = store.userInfo?.id
  if (uid != null) return `${PERSISTED_SID_KEY_PREFIX}user:${uid}`
  if (store.apiKey) return `${PERSISTED_SID_KEY_PREFIX}apikey:${store.apiKey.slice(0, 12)}`
  return `${PERSISTED_SID_KEY_PREFIX}anon`
}

/**
 * Persist the latest gw_session_id for the given task id so that on
 * reload / reconnect we can attempt a cache resume. Pass null to clear.
 *
 * Best-effort: localStorage may be disabled (private mode, quota); failures
 * are silently ignored because the cache resume is itself a best-effort
 * optimisation — losing persistence just means we fall through to a
 * normal request on next chatCompletion().
 */
export function persistLastGwSessionId(taskId: string, sid: string | null): void {
  if (!taskId) return
  try {
    const key = `${persistedSidStorageKey()}:task:${taskId}`
    if (sid) localStorage.setItem(key, sid)
    else localStorage.removeItem(key)
  } catch {
    /* localStorage unavailable / quota exceeded */
  }
}

/**
 * Load the most recently persisted gw_session_id for this task id, if any.
 * Returns null when nothing has been persisted (first-time user, or cleared).
 */
export function loadPersistedGwSessionId(taskId: string): string | null {
  if (!taskId) return null
  try {
    return localStorage.getItem(`${persistedSidStorageKey()}:task:${taskId}`)
  } catch {
    return null
  }
}

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

export function normalizeSession(raw: ChatSession | Record<string, unknown>): ChatSession {
  const s = raw as ChatSession
  return {
    ...s,
    apiKeyId: s.apiKeyId ?? null,
    gwSessionId: s.apiKeyId == null && s.gwSessionId ? null : (s.gwSessionId ?? null),
    usage: s.usage ?? emptyTokenUsage(),
    lastResolvedModel: s.lastResolvedModel ?? null,
    settings: normalizeChatSessionSettings(
      (raw as { settings?: Partial<ChatSessionSettings> }).settings,
    ),
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

  const current = readKey(storageKey(STORAGE_VERSION))
  if (current && current.length > 0) return current

  // v2 → v3, then v1 → v3
  const fromPrev = readKey(storageKey(STORAGE_VERSION_PREV))
  const legacy: ChatSession[] = fromPrev ? [...fromPrev] : []
  if (legacy.length === 0) {
    for (const key of legacyStorageKeys()) {
      const data = readKey(key)
      if (data) legacy.push(...data)
    }
  }
  if (legacy.length > 0) {
    try {
      localStorage.setItem(storageKey(STORAGE_VERSION), JSON.stringify(legacy))
      try {
        localStorage.removeItem(storageKey(STORAGE_VERSION_PREV))
      } catch {
        /* ignore */
      }
      for (const key of legacyStorageKeys()) localStorage.removeItem(key)
    } catch {
      // best-effort migration
    }
  }
  return legacy
}

function saveAll(sessions: ChatSession[]) {
  const newKey = storageKey(STORAGE_VERSION)
  try {
    localStorage.setItem(newKey, JSON.stringify(sessions))
  } catch {
    /* drop the write; in-memory sessions.value remains the source of truth */
  }
  for (const k of [storageKey(STORAGE_VERSION_PREV), ...legacyStorageKeys()]) {
    if (k !== newKey) {
      try {
        localStorage.removeItem(k)
      } catch {
        /* ignore */
      }
    }
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

  // ─── Debounced persistence (LP6, 2026-08-24) ──────────────────────────
  // Why: `saveAll` JSON-serialises the whole session tree on every mutation
  // (typing, streaming token, model switch, usage tick). On a long reply the
  // synchronous localStorage.setItem can stall the main thread by tens of ms
  // and pushes the same payload N times before a stable state is reached.
  //
  // The strategy: coalesce mutation bursts into a single write 300ms after
  // the last change, and force-flush on lifecycle events so users never lose
  // state when they close the tab / background the page / unmount the view.
  // Errors (quota / private mode) are caught and degrade silently: the
  // in-memory `sessions` ref stays authoritative and the next flush retries.
  //
  // LP9 (2026-08-24): debounce timer + snapshot short-circuit + lifecycle
  // listeners + scope-aware cleanup are now provided by persistenceShared.ts.
  // saveAll() keeps the user-scoped key + legacy migration logic local to
  // this composable; only the persistence plumbing is shared.
  const persistHandle = createDebouncedFlush<ChatSession[]>({
    getSnapshot: () => sessions.value,
    serialize: (snapshot) => JSON.stringify(snapshot),
    write: (snapshot) => {
      saveAll(snapshot)
      return true
    },
    debounceMs: PERSIST_DEBOUNCE_MS,
  })

  // Lifecycle flush handlers — the only way to guarantee a write before the
  // page is torn down (visibilitychange covers mobile backgrounding where
  // beforeunload often doesn't fire).
  const removeListeners = installLifecycleFlush(persistHandle.flush)
  installScopeCleanup(persistHandle.flush, removeListeners)

  // Back-compat alias: every existing call site uses `persist()`. Switching
  // to `persistHandle.schedule()` keeps behaviour identical (debounced) while
  // preserving the local name to minimise the diff and the audit footprint.
  function persist() {
    persistHandle.schedule()
  }

  // Back-compat alias exposed on the returned API: tests and external callers
  // reach for `api.flushPersist()` to force a synchronous write.
  function flushPersist(): boolean {
    return persistHandle.flush()
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
      settings: defaultChatSessionSettings(),
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

  type SessionPatch = Partial<
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
      | 'settings'
    >
  >

  function applySessionPatch(s: ChatSession, patch: SessionPatch) {
    if (patch.messages !== undefined) s.messages = patch.messages
    if (patch.model !== undefined) s.model = patch.model
    if (patch.gwSessionId !== undefined) s.gwSessionId = patch.gwSessionId
    if (patch.apiKeyId !== undefined) s.apiKeyId = patch.apiKeyId
    if (patch.taskId !== undefined) s.taskId = patch.taskId
    if (patch.title !== undefined) s.title = patch.title
    if (patch.summary !== undefined) s.summary = patch.summary
    if (patch.lastResolvedModel !== undefined) s.lastResolvedModel = patch.lastResolvedModel
    if (patch.usage !== undefined) s.usage = patch.usage
    if (patch.settings !== undefined) {
      s.settings = normalizeChatSessionSettings({ ...s.settings, ...patch.settings })
    }
    s.updatedAt = Date.now()
  }

  function updateSession(id: string, patch: SessionPatch) {
    const s = sessions.value.find((x) => x.id === id)
    if (!s) return
    applySessionPatch(s, patch)
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
    patch: SessionPatch,
    delta: TokenUsage | null | undefined,
  ) {
    const s = activeSession.value
    if (!s) return
    applySessionPatch(s, patch)
    if (delta) {
      s.usage = addTokenUsage(s.usage ?? emptyTokenUsage(), delta)
    }
    if (s.messages.length > 0 && s.title === '新对话') {
      s.title = titleFromFirstUserMessage(s.messages)
    }
    persist()
  }

  function updateActive(patch: SessionPatch) {
    const s = activeSession.value
    if (!s) return
    applySessionPatch(s, patch)
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
    /** Force an immediate synchronous write of the session tree.
     *  Exposed for tests and for callers that need a guaranteed-on-return
     *  persistence (rare; the lifecycle listeners cover the common cases). */
    flushPersist,
  }
}
