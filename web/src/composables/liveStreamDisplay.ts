// liveStreamDisplay — UI helpers for the swim lane.
//
// Pure data-shaping for the tiles. Lives in its own module so it
// can be unit-tested in isolation and reused by LiveRequestBlock
// (render) and LiveStreamLegend (label).
//
// 2026-07-03 v7: failure detail — added errorKindLabel() and
// errorKindBg() so the operator sees the failure mode directly on
// the tile (line 2 in failure mode shows "5xx" / "timeout" / etc.,
// not the model short label) without needing to hover.

/**
 * Reduce a model name to a single uppercase letter for the tile's
 * dominant visual cue (the family colour). Kept for callers that
 * only have room for one character. Same top-vendor priority as
 * `modelShortLabel`.
 */
export function modelGlyph(model: string | undefined | null): string {
  if (!model) return '?'
  const m = model.toLowerCase()
  if (m.startsWith('gpt-') || m.startsWith('o1-') || m.startsWith('o3-') || m.startsWith('o4-')) return 'G'
  if (m.startsWith('claude-')) return 'C'
  if (m.startsWith('qwen')) return 'Q'
  if (m.startsWith('glm-')) return 'M'
  if (m.startsWith('deepseek-')) return 'D'
  const m2 = (model || '').trim()
  if (!m2) return '?'
  const first = m2[0]
  return /[a-zA-Z0-9]/.test(first) ? first.toUpperCase() : '?'
}

/**
 * Reduce a provider code to a 2-4 letter *typical* short label.
 */
export function providerShortLabel(provider: string | undefined | null): string {
  if (!provider) return '???'
  const p = provider.toLowerCase().trim()
  if (!p) return '???'
  if (p === 'openai' || p.startsWith('openai-')) return 'OPEN'
  if (p === 'anthropic' || p.startsWith('anthropic-')) return 'ANTH'
  if (p === 'azure' || p.startsWith('azure-') || p.includes('azure-openai')) return 'AZR'
  if (p === 'google' || p.startsWith('google-') || p.startsWith('vertex')) return 'GGL'
  if (p === 'bedrock' || p.startsWith('bedrock-')) return 'BDR'
  if (p === 'cohere' || p.startsWith('cohere-')) return 'COH'
  if (p === 'mistral' || p.startsWith('mistral-')) return 'MST'
  if (p === 'qwen' || p.startsWith('qwen-')) return 'QWN'
  if (p === 'deepseek' || p.startsWith('deepseek-')) return 'DSK'
  if (p === 'zhipu' || p.startsWith('zhipu-') || p === 'glm' || p.startsWith('glm-')) return 'GLM'
  const stripped = p.replace(/[^a-z0-9]/gi, '')
  if (stripped.length >= 3) return stripped.slice(0, 3).toUpperCase()
  if (stripped.length > 0) return stripped.toUpperCase().padEnd(3, '?')
  return '???'
}

/**
 * Top-vendor family code shown on the second line of every tile.
 *
 *   - GPT / o1 / o3 / o4  → "GPT"
 *   - Claude-*            → "CLD"
 *   - Qwen*               → "QWN"
 *   - GLM-*               → "GLM"
 *   - Deepseek-*          → "DSK"
 *
 * Everything else returns "???".
 */
export function modelShortLabel(model: string | undefined | null): string {
  if (!model) return '???'
  const m = model.toLowerCase().trim()
  if (!m) return '???'
  if (m.startsWith('gpt-') || m.startsWith('o1-') || m.startsWith('o3-') || m.startsWith('o4-')) return 'GPT'
  if (m.startsWith('claude-')) return 'CLD'
  if (m.startsWith('qwen')) return 'QWN'
  if (m.startsWith('glm-')) return 'GLM'
  if (m.startsWith('deepseek-')) return 'DSK'
  return '???'
}

/**
 * Coarse status category.
 */
export type StatusCategory =
  | 'success'
  | 'in_progress'
  | 'failure_5xx'
  | 'failure_4xx'
  | 'failure_timeout'
  | 'failure_not_found'
  | 'failure_other'

export function statusCategory(
  status: string | undefined | null,
  errorKind: string | undefined | null,
): StatusCategory {
  if (status === 'success') return 'success'
  if (status === 'in_progress') return 'in_progress'
  if (status !== 'failure') return 'failure_other'
  if (!errorKind) return 'failure_other'
  const k = errorKind.toLowerCase()
  if (/(?<!upstream_)(?<!backend_)(?<!server_)timeout/.test(k)) return 'failure_timeout'
  if (/client_disconnect|\bdisconnect\b|network_reset|network_error|connection_reset|eof|cancelled|canceled/.test(k)) return 'failure_timeout'
  if (/(5xx|server|upstream|provider|overloaded|backend|internal)/.test(k)) return 'failure_5xx'
  if (/(4xx|auth|unauthor|forbidden|quota|rate|billing|payment|invalid)/.test(k)) return 'failure_4xx'
  if (/(not_found|model_not|\brout|no_route|resolve|policy|missing)/.test(k)) return 'failure_not_found'
  return 'failure_other'
}

/**
 * Tiny hex → rgba converter.
 */
export function hexToRgba(hex: string, alpha: number): string {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim())
  if (!m) return `rgba(139, 148, 158, ${alpha})`
  const n = parseInt(m[1], 16)
  const r = (n >> 16) & 0xff
  const g = (n >> 8) & 0xff
  const b = n & 0xff
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}

/**
 * Reduce a raw error_kind to a short, human-readable label that
 * fits on a single tile line (≤8 chars). Same priority order as
 * statusCategory() so the swim lane and the failure-detail drawer
 * stay in lock-step.
 *
 *   "upstream_5xx"     → "5xx"
 *   "client_disconnect"→ "disc"
 *   "timeout"          → "timeout"
 *   "rate_limit"       → "rate"
 *   "quota_exceeded"   → "quota"
 *   "auth_failed"      → "auth"
 *   "model_not_found"  → "no model"
 */
export function errorKindLabel(errorKind: string | undefined | null): string {
  if (!errorKind) return ''
  const k = errorKind.toLowerCase().trim()
  if (!k) return ''
  if (/(?<!upstream_)(?<!backend_)(?<!server_)timeout/.test(k)) return 'timeout'
  if (/client_disconnect|\bdisconnect\b|network_reset|network_error|connection_reset|eof|cancelled|canceled/.test(k)) return 'disc'
  if (/(5xx|server|upstream|provider|overloaded|backend|internal)/.test(k)) return '5xx'
  if (/(4xx|auth|unauthor|forbidden|quota|rate|billing|payment|invalid)/.test(k)) {
    if (/(quota|rate)/.test(k)) return 'rate'
    if (/(auth|unauthor|forbidden)/.test(k)) return 'auth'
    if (/(billing|payment)/.test(k)) return 'billing'
    return '4xx'
  }
  if (/(not_found|model_not|\brout|no_route|resolve|policy|missing)/.test(k)) return 'no model'
  return k.replace(/_/g, ' ').slice(0, 8)
}

/**
 * Error-kind → translucent tile background colour. The body stays
 * calm (22% alpha) but the operator can immediately group failures
 * by family without reading the text.
 *
 *  - timeout / disconnect / network → amber
 *  - 5xx / upstream / provider      → red
 *  - 4xx / auth / quota / rate      → yellow
 *  - not_found / routing            → purple
 *  - default                        → red
 */
export function errorKindBg(errorKind: string | undefined | null): string {
  if (!errorKind) return 'transparent'
  const k = errorKind.toLowerCase()
  if (/(timeout|disconnect|network|reset|eof|cancel)/.test(k)) return 'rgba(245, 158, 11, 0.22)'
  if (/(5xx|server|upstream|provider|overloaded|backend|internal)/.test(k)) return 'rgba(239, 68, 68, 0.22)'
  if (/(4xx|auth|unauthor|forbidden|quota|rate|billing|payment|invalid)/.test(k)) return 'rgba(251, 191, 36, 0.22)'
  if (/(not_found|model_not|\brout|no_route|resolve|policy|missing)/.test(k)) return 'rgba(167, 139, 250, 0.22)'
  return 'rgba(239, 68, 68, 0.22)'
}

/**
 * Format a date as HH:MM using the active locale.
 */
export function timeHHMM(ts: string | undefined | null, locale: string = 'en-US'): string {
  if (!ts) return '--:--'
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return '--:--'
  try {
    return d.toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit', hour12: false })
  } catch {
    return '--:--'
  }
}

/**
 * Format a latency in ms as the compact "1.2s" / "320ms" string.
 */
export function latencyLabel(latencyMs: number | null | undefined, isInProgress: boolean): string {
  if (isInProgress) return '…'
  if (latencyMs == null || !Number.isFinite(latencyMs)) return '—'
  if (latencyMs < 0) return '—'
  if (latencyMs < 1000) return `${Math.round(latencyMs)}ms`
  if (latencyMs < 10_000) return `${(latencyMs / 1000).toFixed(1)}s`
  return `${Math.round(latencyMs / 1000)}s`
}
