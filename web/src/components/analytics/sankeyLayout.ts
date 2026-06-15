import type { AnalyticsFlow } from '../../api-autoroute'

export const SANKEY_NODE_H = 32
export const SANKEY_GAP = 8
export const SANKEY_V_PAD = 60 // 30 top + 30 bottom inside viewBox
/** External legend row above the SVG (may wrap to two lines). */
export const SANKEY_DOM_LEGEND_H = 56
export const SANKEY_SECTION_HEAD_H = 36
export const SANKEY_MIN_H = 400
export const SANKEY_VIEW_W = 720

export interface SankeyLayerNode {
  id: string
  label: string
  layer: number
  total: number
}

/** Minimum inner column height so every node keeps min height under proportional layout. */
export function requiredColHeight(layerNodes: Array<{ total: number }>): number {
  const n = layerNodes.length
  if (!n) return 0

  const totalGap = (n - 1) * SANKEY_GAP
  const totalLayer = layerNodes.reduce((s, nd) => s + nd.total, 0) || 1

  const sumAt = (available: number) => {
    let sum = 0
    for (const nd of layerNodes) {
      sum += Math.max(SANKEY_NODE_H, (nd.total / totalLayer) * available)
    }
    return sum
  }

  let lo = n * SANKEY_NODE_H
  let hi = lo
  while (sumAt(hi) > hi) {
    hi = Math.ceil(hi * 1.5)
    if (hi > 12000) break
  }

  while (lo < hi) {
    const mid = Math.floor((lo + hi) / 2)
    if (sumAt(mid) <= mid) hi = mid
    else lo = mid + 1
  }

  return lo + totalGap
}

export function buildSankeyLayers(data: AnalyticsFlow | null): SankeyLayerNode[][] {
  if (!data) return [[], [], []]

  const out: SankeyLayerNode[][] = [[], [], []]
  const totals: Record<string, number> = {}
  for (const l of data.links) {
    totals[l.source] = (totals[l.source] || 0) + l.value
    totals[l.target] = (totals[l.target] || 0) + l.value
  }
  for (const n of data.nodes) {
    const layer = Math.min(Math.max(n.layer, 0), 2)
    out[layer].push({ ...n, total: totals[n.id] || 0 })
  }
  for (const layer of out) {
    layer.sort((a, b) => b.total - a.total)
  }
  return out
}

/** SVG viewBox height for the Sankey diagram (excludes external legend DOM). */
export function computeSankeyHeight(
  data: AnalyticsFlow | null,
  minHeight = 0,
): number {
  if (!data?.nodes?.length) {
    return Math.max(minHeight, SANKEY_MIN_H)
  }

  const layers = buildSankeyLayers(data)
  const maxCol = Math.max(...layers.map(requiredColHeight), 1)
  const base = SANKEY_V_PAD + maxCol
  return Math.max(base, minHeight, SANKEY_MIN_H)
}

/** Full card height: section head + external legend + SVG. */
export function computeSankeyCardHeight(
  data: AnalyticsFlow | null,
  minHeight = 0,
): number {
  return SANKEY_SECTION_HEAD_H
    + SANKEY_DOM_LEGEND_H
    + computeSankeyHeight(data, minHeight)
}
