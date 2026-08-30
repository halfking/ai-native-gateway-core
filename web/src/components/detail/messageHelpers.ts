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

export function contentToPlainText(content: unknown): string {
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
      return contentToPlainText(msgs[i].content).trim()
    }
  }
  return ''
}

/** First user message text from a chat body (per-turn user instruction). */
export function firstUserPrompt(body: unknown): string {
  const msgs = extractMessagesFromBody(body)
  for (const m of msgs) {
    if (String(m.role || '') === 'user') {
      return contentToPlainText(m.content).trim()
    }
  }
  return ''
}

/** Last user message text from a chat body (current-turn user instruction). */
export function lastUserPrompt(body: unknown): string {
  const msgs = extractMessagesFromBody(body)
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (String(msgs[i].role || '') === 'user') {
      return contentToPlainText(msgs[i].content).trim()
    }
  }
  return ''
}

export interface TurnMessageSources {
  /** Request-side messages for the turn (system + user + tool). */
  requestMessages: Record<string, unknown>[]
  /** Response-side messages for the turn (assistant + tool). */
  responseMessages: Record<string, unknown>[]
}

/**
 * Resolve the request-side message list for a single session turn.
 *
 * The V2 writer only persists incremental request_delta (the messages the
 * client added this turn), while outbound_body is the full assembled prompt
 * the gateway actually sent to the model (system + history + this turn's
 * user + tools). Showing only request_delta for turn 2+ would hide the
 * system prompt and earlier context — that's the bug this helper fixes:
 * we prefer outbound_body when it carries messages, fall back to
 * request_delta only when outbound is empty/unavailable.
 *
 * response_delta is always kept separate so the caller can render it as the
 * turn's assistant reply block instead of pretending it was part of the
 * request payload.
 */
export function pickTurnRequestMessages(
  requestDelta: unknown,
  outboundBody: unknown,
): Record<string, unknown>[] {
  const outboundMsgs = extractMessagesFromBody(outboundBody)
  if (outboundMsgs.length) return outboundMsgs
  return extractMessagesFromBody(requestDelta)
}

/**
 * Build the per-turn message sources in one call. Useful for the
 * SessionTurnsSyncPane V2 path, which always has both request_delta and
 * response_delta available.
 */
export function splitTurnMessages(
  requestDelta: unknown,
  responseDelta: unknown,
  outboundBody: unknown,
): TurnMessageSources {
  return {
    requestMessages: pickTurnRequestMessages(requestDelta, outboundBody),
    responseMessages: extractMessagesFromBody(responseDelta),
  }
}

/**
 * Pick the user instruction summary for a single session turn.
 *
 * Prefer the LAST user message in outbound_body so the card reflects the
 * current turn's actual question, even when request_delta was attachment-
 * only or otherwise empty. Fall back to the FIRST user message in
 * request_delta so older turns without outbound still surface their prompt.
 */
export function summarizeTurnUserInstruction(
  requestDelta: unknown,
  outboundBody: unknown,
): { text: string; full: string; truncated: boolean } {
  const outboundMsgs = extractMessagesFromBody(outboundBody)
  for (let i = outboundMsgs.length - 1; i >= 0; i--) {
    if (String(outboundMsgs[i].role || '') === 'user') {
      const full = contentToPlainText(outboundMsgs[i].content).trim()
      const { text, truncated } = previewText(full, 6)
      return { text, full, truncated }
    }
  }
  const full = firstUserPrompt(requestDelta)
  const { text, truncated } = previewText(full, 6)
  return { text, full, truncated }
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
  /** Backed by the V2 turn_no column when available; falls back to `number`. */
  turnNo?: number
  /** V2 request_id; used to align the selected card with the current route. */
  requestId?: string | null
  messages: Record<string, unknown>[]
  /** Request-side messages used to build the de-duplicated all-turn view. */
  requestMessages?: Record<string, unknown>[]
  /** Response-side messages used to build the de-duplicated all-turn view. */
  responseMessages?: Record<string, unknown>[]
  /** Incremental request + response messages for chronological all-turn view. */
  allTurnMessages?: Record<string, unknown>[]
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
    const full = userMsg ? contentToPlainText(userMsg.content) : ''
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
      const t = contentToPlainText(msg.content)
      if (t.trim()) return t.trim()
    }
    if (typeof last.text === 'string') return last.text.trim()
  }
  if (o.message && typeof o.message === 'object') {
    const t = contentToPlainText((o.message as Record<string, unknown>).content)
    if (t.trim()) return t.trim()
  }
  if (Array.isArray(o.content)) {
    const t = contentToPlainText(o.content)
    if (t.trim()) return t.trim()
  }
  const msgs = extractMessagesFromBody(parsed)
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (String(msgs[i].role || '') === 'assistant') {
      return contentToPlainText(msgs[i].content).trim()
    }
  }
  return ''
}
