// messageHelpers.ts — shared message extraction for detail panels.

export function formatJson(obj: unknown): string {
  if (obj == null) return '(无数据)'
  try {
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(obj)
  }
}

function summaryEnvelopeMessage(summary: Record<string, unknown> | undefined): Record<string, unknown>[] | null {
  if (!summary || typeof summary !== 'object') return null
  const bytes = typeof summary.bytes === 'number' ? summary.bytes : Number(summary.bytes || 0)
  const truncated = summary.head_truncated === true
  return [{
    role: 'gateway',
    content: truncated
      ? `[已摘要化: 原始 ${bytes} bytes, head 已截断]`
      : `[已摘要化: 原始 ${bytes} bytes]`,
  }]
}

export function extractMessagesFromBody(body: unknown): Record<string, unknown>[] {
  if (body == null) return []
  let parsed: unknown = body
  if (typeof parsed === 'string') {
    try { parsed = JSON.parse(parsed) } catch { return [] }
  }
  if (Array.isArray(parsed)) return parsed as Record<string, unknown>[]
  if (typeof parsed === 'object' && parsed !== null) {
    const o = parsed as Record<string, unknown>
    const summaryMessage = summaryEnvelopeMessage(o._gw_body_summary as Record<string, unknown> | undefined)
    if (summaryMessage) return summaryMessage
    if (Array.isArray(o.messages)) return o.messages as Record<string, unknown>[]
    if (Array.isArray(o.choices)) {
      const msgs: Record<string, unknown>[] = []
      for (const c of o.choices as Record<string, unknown>[]) {
        if (c.message) msgs.push(c.message as Record<string, unknown>)
      }
      return msgs
    }
    return [o]
  }
  return []
}

export function roleColor(role: string): string {
  switch (role) {
    case 'user': return 'var(--info)'
    case 'assistant': return 'var(--success)'
    case 'system': return 'var(--warning)'
    case 'tool': return 'var(--muted)'
    default: return 'inherit'
  }
}

export type RoleFilter = 'all' | 'system' | 'user' | 'tool' | 'assistant'

export function filterMessages(
  msgs: Record<string, unknown>[],
  filter: RoleFilter,
): Record<string, unknown>[] {
  if (filter === 'all') return msgs
  return msgs.filter((m) => String(m.role || '') === filter)
}

export function previewText(content: unknown, lines = 3): { text: string; truncated: boolean } {
  const raw = typeof content === 'string' ? content : formatJson(content)
  const parts = raw.split('\n')
  if (parts.length <= lines) return { text: raw, truncated: false }
  return { text: parts.slice(0, lines).join('\n'), truncated: true }
}

function contentToPlain(content: unknown): string {
  if (content == null) return ''
  if (typeof content === 'string') return content
  if (Array.isArray(content)) {
    const texts: string[] = []
    for (const part of content) {
      if (typeof part === 'string') {
        texts.push(part)
        continue
      }
      if (part && typeof part === 'object') {
        const o = part as Record<string, unknown>
        if (typeof o.text === 'string') texts.push(o.text)
        else if (typeof o.content === 'string') texts.push(o.content)
      }
    }
    return texts.filter(Boolean).join('\n')
  }
  if (typeof content === 'object') {
    const o = content as Record<string, unknown>
    if (typeof o.text === 'string') return o.text
  }
  return formatJson(content)
}

/** Last user message text from a chat request body (for overview turn card). */
export function extractLastUserPrompt(body: unknown): string {
  const msgs = extractMessagesFromBody(body)
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (String(msgs[i].role || '') === 'user') {
      return contentToPlain(msgs[i].content).trim()
    }
  }
  return ''
}

/**
 * deriveConversationTurns splits a flat chat message list into turns.
 *
 * A turn starts at a user message and ends right before the next user
 * message (or at the end of the conversation).  Leading system messages that
 * appear before any user message are attached to the first user turn, so each
 * turn reads as `system + user + assistant reply` — matching how the gateway
 * assembles the per-turn request before sending it to the model.
 *
 * Turns with no user message (e.g. trailing system-only blocks) are dropped,
 * because turns are defined by user instructions.
 */
export interface ConversationTurn {
  /** 0-based index within the derived turn list. */
  index: number
  /** 1-based turn number for display. */
  number: number
  messages: Record<string, unknown>[]
  /** Plain-text preview of the turn's leading user message (truncated). */
  userPreview: string
  /** Full plain-text of the turn's leading user message. */
  userPreviewFull: string
  /** Whether userPreview is truncated. */
  truncated: boolean
  /** Number of assistant replies within the turn. */
  assistantCount: number
}

export function deriveConversationTurns(
  messages: Record<string, unknown>[],
): ConversationTurn[] {
  const turns: ConversationTurn[] = []
  let lead: Record<string, unknown>[] = [] // trailing-system buffer before the first user
  let cur: Record<string, unknown>[] = []

  const flush = () => {
    if (cur.length === 0 && lead.length === 0) return
    const slice = lead.concat(cur)
    const hasUser = slice.some((m) => String(m.role || '') === 'user')
    if (!hasUser) {
      // No user yet — keep these system blocks for the next user turn.
      lead = slice
      cur = []
      return
    }
    const userMsg = slice.find((m) => String(m.role || '') === 'user')
    const full = userMsg ? contentToPlain(userMsg.content) : ''
    const { text, truncated } = previewText(full, 6)
    turns.push({
      index: turns.length,
      number: turns.length + 1,
      messages: slice,
      userPreview: text,
      userPreviewFull: full,
      truncated,
      assistantCount: slice.filter((m) => String(m.role || '') === 'assistant').length,
    })
    lead = []
    cur = []
  }

  for (const m of messages) {
    const role = String(m.role || '')
    if (role === 'user') {
      flush()
      cur.push(m)
    } else if (role === 'system') {
      // A system message mid-turn belongs to the current turn; before any user
      // it is a leading-system block deferred to the next user turn.
      if (cur.length === 0) lead.push(m)
      else cur.push(m)
    } else {
      cur.push(m)
    }
  }
  flush()
  return turns
}

/** Assistant / model reply text from response_body (choices or message). */
export function extractAssistantReply(body: unknown): string {
  if (body == null) return ''
  let parsed: unknown = body
  if (typeof parsed === 'string') {
    try { parsed = JSON.parse(parsed) } catch {
      return typeof parsed === 'string' ? parsed.trim() : ''
    }
  }
  if (typeof parsed !== 'object' || parsed === null) return String(parsed)

  const o = parsed as Record<string, unknown>
  if (Array.isArray(o.choices) && o.choices.length) {
    const last = o.choices[o.choices.length - 1] as Record<string, unknown>
    const msg = (last.message || last.delta) as Record<string, unknown> | undefined
    if (msg) {
      const t = contentToPlain(msg.content)
      if (t.trim()) return t.trim()
    }
    if (typeof last.text === 'string') return last.text.trim()
  }
  if (o.message && typeof o.message === 'object') {
    const t = contentToPlain((o.message as Record<string, unknown>).content)
    if (t.trim()) return t.trim()
  }
  if (Array.isArray(o.content)) {
    const t = contentToPlain(o.content)
    if (t.trim()) return t.trim()
  }
  const msgs = extractMessagesFromBody(parsed)
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (String(msgs[i].role || '') === 'assistant') {
      return contentToPlain(msgs[i].content).trim()
    }
  }
  return ''
}
