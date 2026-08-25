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
