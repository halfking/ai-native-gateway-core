// sessionDetailExport.ts — build + download full session markdown from detail page.
import { getRequestLogDetail, getRequestLogs, type RequestLogDetail } from '../api/logs'
import { fetchSessionTurnsTree, type SessionTurnTreeItem } from '../api/sessionTurnsTree'
import { getSessionSnapshot } from '../api/sessions_v2'
import {
  extractAssistantReply,
  extractLastUserPrompt,
} from '../components/detail/messageHelpers'

export interface SessionExportInput {
  sessionId: string
  title?: string
  summary?: string
  totalTurns?: number
  totalCostUsd?: number
}

function safeFilename(raw: string): string {
  const s = raw.replace(/[^\w\u4e00-\u9fff.-]+/g, '_').replace(/_+/g, '_').slice(0, 80)
  return s || 'session'
}

function clip(s: string, max = 8000): string {
  if (s.length <= max) return s
  return `${s.slice(0, max)}\n…(截断)`
}

function pickBodies(log: RequestLogDetail | null): { user: string; reply: string } {
  if (!log) return { user: '', reply: '' }
  const user =
    extractLastUserPrompt(log.request_body)
    || String(log.request_preview || '').trim()
  const reply =
    extractAssistantReply(log.response_body)
    || String(log.response_preview || '').trim()
  return { user, reply }
}

async function loadLogMap(
  requestIds: string[],
): Promise<Map<string, RequestLogDetail>> {
  const map = new Map<string, RequestLogDetail>()
  const unique = [...new Set(requestIds.filter(Boolean))]
  const concurrency = 4
  let i = 0
  async function worker() {
    while (i < unique.length) {
      const idx = i++
      const id = unique[idx]
      try {
        map.set(id, await getRequestLogDetail(id))
      } catch {
        /* keep missing; preview fallback below */
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, unique.length) }, () => worker()))
  return map
}

/** Build markdown for an entire gateway session (turns + Q&A). */
export async function buildSessionDetailMarkdown(input: SessionExportInput): Promise<string> {
  const sid = input.sessionId.trim()
  if (!sid) throw new Error('缺少 sessionId')

  let snap: Record<string, unknown> = {}
  try {
    snap = await getSessionSnapshot(sid)
  } catch { /* optional */ }

  const title =
    input.title
    || (typeof snap.title === 'string' ? snap.title : '')
    || sid
  const summary =
    input.summary
    || (typeof snap.summary === 'string' ? snap.summary : '')
  const totalTurns =
    input.totalTurns
    ?? (typeof snap.total_turns === 'number' ? snap.total_turns : undefined)
  const totalCost =
    input.totalCostUsd
    ?? (typeof snap.total_cost_usd === 'number' ? snap.total_cost_usd : undefined)

  let turns: SessionTurnTreeItem[] = []
  try {
    const tree = await fetchSessionTurnsTree(sid, { limit: 100 })
    turns = tree.turns || []
  } catch {
    turns = []
  }

  // Preview fallback from request_logs list (chrono ascending).
  const previewById = new Map<string, { req?: string; res?: string }>()
  try {
    const logs = await getRequestLogs({ gw_session_id: sid, chrono: true, page_size: 100 })
    for (const row of logs.items || []) {
      previewById.set(row.request_id, {
        req: row.request_preview || undefined,
        res: row.response_preview || undefined,
      })
    }
  } catch { /* optional */ }

  const logMap = await loadLogMap(turns.map((t) => t.request_id))

  const lines: string[] = []
  lines.push(`# ${title}`)
  lines.push('')
  lines.push(`- Session ID: \`${sid}\``)
  lines.push(`- 导出时间: ${new Date().toLocaleString()}`)
  if (totalTurns != null) lines.push(`- 轮次数: ${totalTurns}`)
  if (totalCost != null) lines.push(`- 总费用: $${Number(totalCost).toFixed(4)}`)
  if (typeof snap.last_model === 'string' && snap.last_model) {
    lines.push(`- 最近模型: ${snap.last_model}`)
  }
  if (typeof snap.last_provider === 'string' && snap.last_provider) {
    lines.push(`- 最近供应商: ${snap.last_provider}`)
  }
  lines.push('')

  if (summary.trim()) {
    lines.push('## 会话摘要')
    lines.push('')
    lines.push(summary.trim())
    lines.push('')
  }

  lines.push('## 对话轮次')
  lines.push('')

  if (!turns.length) {
    lines.push('_暂无轮次数据_')
  } else {
    for (const t of turns) {
      const detail = logMap.get(t.request_id) || null
      const bodies = pickBodies(detail)
      const prev = previewById.get(t.request_id)
      const user = bodies.user || prev?.req || ''
      const reply = bodies.reply || prev?.res || ''
      const model = t.model || detail?.outbound_model || detail?.client_model || '—'
      const lat = t.latency == null ? '—' : `${t.latency}ms`

      lines.push(`### Turn #${t.turn_number}`)
      lines.push('')
      lines.push(`- request_id: \`${t.request_id}\``)
      lines.push(`- status: **${t.status || '—'}**`)
      lines.push(`- model: ${model}`)
      lines.push(`- latency: ${lat}`)
      lines.push('')
      lines.push('**用户**')
      lines.push('')
      lines.push(user ? clip(user) : '_（无用户内容）_')
      lines.push('')
      lines.push('**助手**')
      lines.push('')
      lines.push(reply ? clip(reply) : '_（无回复）_')
      lines.push('')
    }
  }

  return lines.join('\n')
}

export async function downloadSessionDetailMarkdown(input: SessionExportInput): Promise<void> {
  const md = await buildSessionDetailMarkdown(input)
  const day = new Date().toISOString().slice(0, 10)
  const name = `session-${safeFilename(input.title || input.sessionId)}-${day}.md`
  const blob = new Blob([md], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}
