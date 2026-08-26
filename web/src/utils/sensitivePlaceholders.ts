// sensitivePlaceholders.ts — parse {SENSITIVE:type:index} tokens (align Go PlaceholderPattern).
export const SENSITIVE_PLACEHOLDER_RE = /\{SENSITIVE:([a-z_]+):(\d+)\}/g

export interface SensitivePlaceholder {
  placeholder: string
  type: string
  index: number
}

export function extractSensitivePlaceholders(text: string | null | undefined): SensitivePlaceholder[] {
  if (!text) return []
  const out: SensitivePlaceholder[] = []
  const seen = new Set<string>()
  const re = new RegExp(SENSITIVE_PLACEHOLDER_RE.source, 'g')
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    const placeholder = m[0]
    if (seen.has(placeholder)) continue
    seen.add(placeholder)
    out.push({ placeholder, type: m[1], index: Number(m[2]) })
  }
  return out
}

/** Split text into plain / placeholder segments for inline highlight rendering. */
export function splitSensitivePlaceholders(text: string): Array<{ kind: 'text' | 'ph'; value: string }> {
  if (!text) return []
  const parts: Array<{ kind: 'text' | 'ph'; value: string }> = []
  const re = new RegExp(SENSITIVE_PLACEHOLDER_RE.source, 'g')
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) parts.push({ kind: 'text', value: text.slice(last, m.index) })
    parts.push({ kind: 'ph', value: m[0] })
    last = m.index + m[0].length
  }
  if (last < text.length) parts.push({ kind: 'text', value: text.slice(last) })
  return parts.length ? parts : [{ kind: 'text', value: text }]
}

const MAX_PREVIEW = 256 * 1024

/** Truncate huge JSON/text so UI formatters do not freeze the main thread. */
export function truncateForPreview(text: string, max = MAX_PREVIEW): { text: string; truncated: boolean } {
  if (!text || text.length <= max) return { text: text || '', truncated: false }
  return { text: text.slice(0, max) + '\n…(truncated)', truncated: true }
}
