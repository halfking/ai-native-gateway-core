import type { BoardPayload, BoardTrendPoint } from '../api/board'
import type { LiveRequest } from './liveStreamStore'
import type { BoardTimeRange } from '../utils/boardTimeRange'
import { isWithinBoardRange, alignToTrendBucket } from '../utils/boardTimeRange'

const UNKNOWN = '__unknown__'

export function isTerminalBoardRequest(req: LiveRequest): boolean {
  if (req.type === 'idle_marker') return false
  return req.status === 'success' || req.status === 'failure'
}

/** @deprecated use isWithinBoardRange */
export function isWithinBoardDays(ts: string | undefined, days: number): boolean {
  const endMs = Date.now()
  const startMs = endMs - days * 86_400_000
  if (!ts) return false
  const when = Date.parse(ts)
  return !Number.isNaN(when) && when >= startMs && when <= endMs
}

function num(v: number | null | undefined): number {
  return v ?? 0
}

function pieKey(raw: string | undefined): string {
  const k = (raw ?? '').trim()
  return k || UNKNOWN
}

function bumpPieItem(items: BoardPayload['pies']['models'], key: string, delta: {
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}) {
  const next = items.map((item) => ({ ...item }))
  const idx = next.findIndex((item) => item.key === key)
  if (idx >= 0) {
    const row = next[idx]
    next[idx] = {
      ...row,
      requests: (row.requests ?? 0) + delta.requests,
      tokens: (row.tokens ?? 0) + delta.tokens,
      credits: (row.credits ?? 0) + delta.credits,
      cost_usd: (row.cost_usd ?? 0) + delta.cost_usd,
    }
    return next
  }
  next.push({
    key,
    requests: delta.requests,
    tokens: delta.tokens,
    credits: delta.credits,
    cost_usd: delta.cost_usd,
  })
  return next
}

function trendBucketKey(ts: string, range: BoardTimeRange): string {
  return alignToTrendBucket(ts, range)
}

function bumpTrend(trends: BoardTrendPoint[], ts: string, range: BoardTimeRange, delta: {
  requests: number
  tokens: number
  credits: number
  cost_usd: number
}): BoardTrendPoint[] {
  const bucket = trendBucketKey(ts, range)
  const next = trends.map((p) => ({ ...p }))
  const idx = next.findIndex((p) => p.bucket === bucket)
  if (idx >= 0) {
    const row = next[idx]
    next[idx] = {
      ...row,
      requests: (row.requests ?? 0) + delta.requests,
      tokens: (row.tokens ?? 0) + delta.tokens,
      credits: (row.credits ?? 0) + delta.credits,
      cost_usd: (row.cost_usd ?? 0) + delta.cost_usd,
    }
    return next
  }
  next.push({
    bucket,
    requests: delta.requests,
    tokens: delta.tokens,
    credits: delta.credits,
    cost_usd: delta.cost_usd,
  })
  return next.sort((a, b) => a.bucket.localeCompare(b.bucket))
}

/** Apply one completed live-stream request onto the in-memory board snapshot. */
export function applyLiveRequestToBoard(
  board: BoardPayload,
  req: LiveRequest,
  range: BoardTimeRange,
): BoardPayload {
  if (!isTerminalBoardRequest(req) || !isWithinBoardRange(req.ts, range)) {
    return board
  }

  const prompt = num(req.prompt_tokens)
  const completion = num(req.completion_tokens)
  const totalTokens = num(req.total_tokens) || prompt + completion
  const cost = num(req.cost_usd)
  const latency = num(req.latency_ms)
  const success = req.status === 'success'

  const summary = { ...(board.summary ?? {}) }
  const prevTotal = summary.total_requests ?? 0
  const prevSuccessRate = summary.success_rate ?? 1
  const prevSuccessCount = Math.round(prevTotal * prevSuccessRate)
  const prevAvgLatency = summary.avg_latency_ms ?? 0

  const nextTotal = prevTotal + 1
  summary.total_requests = nextTotal
  summary.total_prompt_tokens = (summary.total_prompt_tokens ?? 0) + prompt
  summary.total_completion_tokens = (summary.total_completion_tokens ?? 0) + completion
  summary.total_tokens = (summary.total_tokens ?? 0) + totalTokens
  summary.total_cost_usd = (summary.total_cost_usd ?? 0) + cost
  summary.success_rate = (prevSuccessCount + (success ? 1 : 0)) / nextTotal
  summary.avg_latency_ms = (prevAvgLatency * prevTotal + latency) / nextTotal

  const pieDelta = { requests: 1, tokens: totalTokens, credits: 0, cost_usd: cost }
  const pies = { ...board.pies }
  pies.clients = bumpPieItem(pies.clients ?? [], UNKNOWN, pieDelta)
  pies.models = bumpPieItem(pies.models ?? [], pieKey(req.model), pieDelta)
  pies.providers = bumpPieItem(pies.providers ?? [], pieKey(req.provider_code), pieDelta)
  pies.tenants = bumpPieItem(pies.tenants ?? [], pieKey(req.tenant_id), pieDelta)
  if (!success) {
    pies.errors = bumpPieItem(pies.errors ?? [], pieKey(req.error_kind ?? undefined), pieDelta)
  }

  const trends = bumpTrend(board.trends ?? [], req.ts, range, {
    requests: 1,
    tokens: totalTokens,
    credits: 0,
    cost_usd: cost,
  })

  return {
    ...board,
    summary,
    pies,
    trends,
    days: range.days,
    source: 'live_sse_delta',
    cache_meta: {
      ...(board.cache_meta ?? {}),
      scope: board.cache_meta?.scope ?? 'global',
      source: 'live_sse_delta',
    },
  }
}
