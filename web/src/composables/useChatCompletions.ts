import { createGatewaySession } from '../api'

export interface ChatCompletionMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
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
}

export interface ChatCompletionResult {
  content: string
  gwSessionId: string | null
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

function parseSseDelta(line: string): string {
  const trimmed = line.trim()
  if (!trimmed.startsWith('data:')) return ''
  const payload = trimmed.slice(5).trim()
  if (!payload || payload === '[DONE]') return ''
  try {
    const obj = JSON.parse(payload)
    return obj?.choices?.[0]?.delta?.content ?? ''
  } catch {
    return ''
  }
}

/**
 * POST /v1/chat/completions with gateway session/task headers.
 * Uses streaming to avoid proxy/browser timeouts on model=auto (classifier + upstream).
 */
export async function chatCompletion(opts: ChatCompletionOptions): Promise<ChatCompletionResult> {
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

  const resumeHdr = resp.headers.get('X-Gw-Session-Id-Resume')
  if (resumeHdr) {
    gwSessionId = resumeHdr
  }

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
      return { content: content || raw.slice(0, 2000) || '（空响应）', gwSessionId }
    } catch {
      return { content: raw.slice(0, 2000) || '（空响应）', gwSessionId }
    }
  }

  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let content = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''
    for (const line of lines) {
      const delta = parseSseDelta(line)
      if (delta) {
        content += delta
        opts.onDelta?.(delta)
      }
    }
  }

  if (buffer.trim()) {
    const delta = parseSseDelta(buffer)
    if (delta) {
      content += delta
      opts.onDelta?.(delta)
    }
  }

  return { content: content || '（空响应）', gwSessionId }
}
