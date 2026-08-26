import { createGatewaySession, getPendingResponse } from '../api'
import { loadPersistedGwSessionId, persistLastGwSessionId } from './useChatSessions'

export interface ChatCompletionMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
}

export interface TokenUsage {
  promptTokens: number
  completionTokens: number
  totalTokens: number
}

export interface ChatCompletionOptions {
  apiKey: string
  model: string
  messages: ChatCompletionMessage[]
  taskId: string
  gwSessionId: string | null
  maxTokens?: number
  onDelta?: (text: string) => void
  /** Internal: one-shot retry after SESSION_FORBIDDEN */
  _sessionRetry?: boolean
  /**
   * Internal (Track C client-side resume, 2026-06-21): force the gateway
   * pending-response cache lookup before any new upstream call. Set by
   * the "恢复上次对话" UI button. Default off — auto-resume only fires
   * when the local last user message is a fresh retry/follow-up.
   */
  forceResumeFromCache?: boolean
}

export interface ChatCompletionResult {
  content: string
  gwSessionId: string | null
  usage: TokenUsage | null
  /** Canonical model actually used (from X-Gw-Auto-Decision or explicit selection) */
  resolvedModel: string | null
  /**
   * Track C client-side resume: true when this response was reconstructed
   * from the gateway pending-response cache (no upstream call). Lets the
   * UI show a "已从缓存恢复" badge.
   */
  resumed?: boolean
}

export class SessionForbiddenError extends Error {
  readonly code = 'SESSION_FORBIDDEN'

  constructor(message: string) {
    super(message)
    this.name = 'SessionForbiddenError'
  }
}

export function isSessionForbiddenError(e: unknown): boolean {
  if (e instanceof SessionForbiddenError) return true
  const msg = e instanceof Error ? e.message : String(e)
  return msg.includes('session not owned') || msg.includes('SESSION_FORBIDDEN')
}

export function emptyTokenUsage(): TokenUsage {
  return { promptTokens: 0, completionTokens: 0, totalTokens: 0 }
}

export function addTokenUsage(a: TokenUsage, b: TokenUsage | null | undefined): TokenUsage {
  if (!b) return { ...a }
  return {
    promptTokens: a.promptTokens + b.promptTokens,
    completionTokens: a.completionTokens + b.completionTokens,
    totalTokens: a.totalTokens + b.totalTokens,
  }
}

export function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${(n / 1_000).toFixed(1)}k`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function parseForbiddenFromResponse(status: number, raw: string): SessionForbiddenError | null {
  if (status !== 403) return null
  let msg = raw || `HTTP ${status}`
  let code: string | undefined
  try {
    const j = JSON.parse(raw)
    code = j?.error?.code as string | undefined
    const inner = j?.error?.message || j?.error || raw
    msg = typeof inner === 'string' ? inner : JSON.stringify(inner)
  } catch {
    msg = raw || msg
  }
  if (code === 'SESSION_FORBIDDEN' || msg.includes('session not owned')) {
    return new SessionForbiddenError(msg)
  }
  return null
}

const DEVICE_SEED_KEY = 'llmgw_device_seed'

function deviceSeed(): string {
  let seed = localStorage.getItem(DEVICE_SEED_KEY)
  if (!seed) {
    seed = crypto.randomUUID()
    localStorage.setItem(DEVICE_SEED_KEY, seed)
  }
  return seed
}

/** Skip assistant error bubbles so retries are not polluted. */
export function messagesForApi(messages: ChatCompletionMessage[]): ChatCompletionMessage[] {
  return messages.filter(
    (m) => !(m.role === 'assistant' && m.content.startsWith('错误：')),
  )
}

async function ensureGwSession(apiKey: string, taskId: string, existing: string | null): Promise<string | null> {
  if (existing) return existing
  try {
    const created = await createGatewaySession(apiKey, taskId)
    return created.session_id
  } catch {
    return null
  }
}

function buildHeaders(apiKey: string, taskId: string, gwSessionId: string | null): Record<string, string> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${apiKey}`,
    'X-Gw-Task-Id': taskId,
    'X-Device-Seed': deviceSeed(),
  }
  if (gwSessionId) {
    headers['X-Gw-Session-Id'] = gwSessionId
  }
  return headers
}

function numField(obj: Record<string, unknown>, ...keys: string[]): number {
  for (const k of keys) {
    const v = obj[k]
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return 0
}

export function parseUsageFromObject(obj: unknown): TokenUsage | null {
  if (!obj || typeof obj !== 'object') return null
  const usage = (obj as Record<string, unknown>).usage ?? obj
  if (!usage || typeof usage !== 'object') return null
  const u = usage as Record<string, unknown>
  const promptTokens = numField(u, 'prompt_tokens', 'input_tokens')
  const completionTokens = numField(u, 'completion_tokens', 'output_tokens')
  let totalTokens = numField(u, 'total_tokens')
  if (totalTokens === 0 && (promptTokens > 0 || completionTokens > 0)) {
    totalTokens = promptTokens + completionTokens
  }
  if (promptTokens === 0 && completionTokens === 0 && totalTokens === 0) return null
  return { promptTokens, completionTokens, totalTokens }
}

/** Parse X-Gw-Auto-Decision for chosen canonical model (auto routing). */
export function parseAutoDecisionModel(header: string | null): string | null {
  if (!header) return null
  try {
    const j = JSON.parse(header) as { chosen_model?: string }
    const m = j.chosen_model?.trim()
    return m || null
  } catch {
    return null
  }
}

function parseSsePayload(line: string): { delta: string; usage: TokenUsage | null; model: string | null } {
  const trimmed = line.trim()
  if (!trimmed.startsWith('data:')) return { delta: '', usage: null, model: null }
  const payload = trimmed.slice(5).trim()
  if (!payload || payload === '[DONE]') return { delta: '', usage: null, model: null }
  try {
    const obj = JSON.parse(payload) as Record<string, unknown>
    const delta =
      (obj?.choices as Array<{ delta?: { content?: string } }> | undefined)?.[0]?.delta?.content ?? ''
    const usage = parseUsageFromObject(obj)
    const model = typeof obj.model === 'string' ? obj.model : null
    return { delta: delta || '', usage, model }
  } catch {
    return { delta: '', usage: null, model: null }
  }
}

function resolveModelName(
  requestedModel: string,
  autoHeader: string | null,
  streamModel: string | null,
): string | null {
  const fromAuto = parseAutoDecisionModel(autoHeader)
  if (fromAuto) return fromAuto
  if (requestedModel && requestedModel !== 'auto') return requestedModel
  if (streamModel && streamModel !== 'auto') return streamModel
  return null
}

/**
 * POST /v1/chat/completions with gateway session/task headers.
 * Uses streaming to avoid proxy/browser timeouts on model=auto (classifier + upstream).
 *
 * Track C client-side resume (2026-06-21): before issuing any new upstream
 * call, check the gateway pending-response cache for a recoverable entry
 * keyed by the most recently used gw_session_id for this task. If a
 * completed entry exists, replay it as if it had just streamed in —
 * returning the same ChatCompletionResult shape with `resumed: true`.
 * The original streaming hot path is unaffected when no cache is hit.
 */
export async function chatCompletion(opts: ChatCompletionOptions): Promise<ChatCompletionResult> {
  // ── Cache resume: best-effort, never blocks normal flow ─────────────
  // Triggered when:
  //   - caller explicitly asked for it (forceResumeFromCache), OR
  //   - the request looks like a reconnect (last message is a user turn,
  //     meaning a previous assistant reply was interrupted / never arrived)
  // The candidate session id is whatever is passed in, or whatever we
  // persisted locally for this task id from a prior session.
  const candidateSid = opts.gwSessionId ?? loadPersistedGwSessionId(opts.taskId)
  if (candidateSid && shouldTryCacheResume(opts)) {
    try {
      const cached = await getPendingResponse(candidateSid, opts.apiKey)
      if (cached.status === 'completed' && cached.body) {
        const resumed = replayCachedSseToResult(cached.body, opts.onDelta)
        if (resumed.content) {
          // Persist the resumed session id so a subsequent reload can
          // find it again. Do not overwrite when the cached id is the
          // same as the one we just consumed.
          persistLastGwSessionId(opts.taskId, candidateSid)
          return {
            content: resumed.content,
            gwSessionId: candidateSid,
            usage: resumed.usage,
            resolvedModel: null,
            resumed: true,
          }
        }
      }
      // failed / in_progress / not_found → fall through to a fresh
      // upstream call. We deliberately do NOT block on in_progress;
      // the user already triggered a new chatCompletion() and waiting
      // could feel like a hang.
    } catch {
      // getPendingResponse already swallows errors → never reaches here
    }
  }

  let gwSessionId = await ensureGwSession(opts.apiKey, opts.taskId, opts.gwSessionId)

  const body = {
    model: opts.model,
    messages: messagesForApi(opts.messages),
    max_tokens: opts.maxTokens ?? 2048,
    stream: true,
    metadata: {
      task_id: opts.taskId,
      session_id: gwSessionId ?? opts.taskId,
    },
  }

  const resp = await fetch('/v1/chat/completions', {
    method: 'POST',
    headers: buildHeaders(opts.apiKey, opts.taskId, gwSessionId),
    body: JSON.stringify(body),
  })

  // Persist the gw_session_id we just used so the next chatCompletion()
  // (e.g. after a refresh / new tab) can look it up for cache resume.
  // Done unconditionally on the success path; on HTTP errors the
  // capturer will have still written a `failed` entry to Redis, so
  // we want the id persisted regardless to enable the next retry to
  // short-circuit via getPendingResponse().
  persistLastGwSessionId(opts.taskId, gwSessionId)

  const resumeHdr = resp.headers.get('X-Gw-Session-Id-Resume')
  if (resumeHdr) {
    gwSessionId = resumeHdr
  }

  const autoDecisionHdr = resp.headers.get('X-Gw-Auto-Decision')

  if (!resp.ok) {
    const raw = await resp.text()
    const forbidden = parseForbiddenFromResponse(resp.status, raw)
    if (forbidden && !opts._sessionRetry) {
      return chatCompletion({ ...opts, gwSessionId: null, _sessionRetry: true })
    }
    if (forbidden) {
      throw forbidden
    }
    let msg = `HTTP ${resp.status}`
    try {
      const j = JSON.parse(raw)
      const inner = j?.error?.message || j?.error || raw
      msg = typeof inner === 'string' ? inner : JSON.stringify(inner)
    } catch {
      msg = raw || msg
    }
    throw new Error(msg)
  }

  const ct = resp.headers.get('Content-Type') ?? ''
  if (!ct.includes('text/event-stream') || !resp.body) {
    const raw = await resp.text()
    try {
      const data = JSON.parse(raw)
      const content = data?.choices?.[0]?.message?.content ?? ''
      const usage = parseUsageFromObject(data)
      const resolvedModel = resolveModelName(opts.model, autoDecisionHdr, data?.model ?? null)
      return {
        content: content || raw.slice(0, 2000) || '（空响应）',
        gwSessionId,
        usage,
        resolvedModel,
      }
    } catch {
      return {
        content: raw.slice(0, 2000) || '（空响应）',
        gwSessionId,
        usage: null,
        resolvedModel: resolveModelName(opts.model, autoDecisionHdr, null),
      }
    }
  }

  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let content = ''
  let latestUsage: TokenUsage | null = null
  let streamModel: string | null = null

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''
    for (const line of lines) {
      const { delta, usage, model } = parseSsePayload(line)
      if (model) streamModel = model
      if (usage) latestUsage = usage
      if (delta) {
        content += delta
        opts.onDelta?.(delta)
      }
    }
  }

  if (buffer.trim()) {
    const { delta, usage, model } = parseSsePayload(buffer)
    if (model) streamModel = model
    if (usage) latestUsage = usage
    if (delta) {
      content += delta
      opts.onDelta?.(delta)
    }
  }

  const resolvedModel = resolveModelName(opts.model, autoDecisionHdr, streamModel)

  return {
    content: content || '（空响应）',
    gwSessionId,
    usage: latestUsage,
    resolvedModel,
  }
}

// ── Cache resume helpers (Track C, 2026-06-21) ───────────────────────
// These functions reconstruct a ChatCompletionResult-shaped object from
// the cached SSE body returned by GET /v1/sessions/{sid}/pending-response.
// They reuse parseSsePayload so the replayed deltas match what the live
// stream would have produced.

/**
 * Decide whether to attempt a cache resume before issuing a new upstream
 * call. Conservative: only fires when the last message is a user turn
 * (i.e. the previous assistant reply was missing or interrupted). When
 * `forceResumeFromCache` is set the caller has explicitly opted in (e.g.
 * a "恢复上次对话" button) and we always try.
 */
export function shouldTryCacheResume(opts: ChatCompletionOptions): boolean {
  if (opts.forceResumeFromCache) return true
  const last = opts.messages[opts.messages.length - 1]
  return last?.role === 'user'
}

/**
 * Replay a cached SSE body. Calls onDelta for each parsed delta so the
 * UI bubbles update in real time, just like a live stream. Returns the
 * accumulated content and the last non-null usage found in the body.
 *
 * Tolerates malformed lines by skipping them (parseSsePayload swallows
 * JSON errors and returns an empty delta). Skips `[DONE]` sentinels.
 */
export function replayCachedSseToResult(
  sseBody: string,
  onDelta?: (text: string) => void,
): { content: string; usage: TokenUsage | null } {
  let content = ''
  let usage: TokenUsage | null = null
  for (const rawLine of sseBody.split('\n')) {
    const { delta, usage: lineUsage } = parseSsePayload(rawLine)
    if (lineUsage) usage = lineUsage
    if (delta) {
      content += delta
      onDelta?.(delta)
    }
  }
  return { content, usage }
}
