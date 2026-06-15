import type { AnalyticsFlow } from '../../api-autoroute'

/** Minimum node height (label readability). */
export const SANKEY_NODE_H = 18
/** Max height ratio within a column (hot node vs smallest allocated). */
export const SANKEY_MAX_HEIGHT_RATIO = 3.5
/** Per-layer pow exponents (layer 0 uses log1p instead). */
export const SANKEY_LAYER_EXPONENTS = [0.32, 0.42, 0.5] as const

/**
 * Map raw flow total to layout weight.
 * Layer 0 (task types): log1p — strongest dampening, traffic↑ coefficient↓.
 * Other layers: pow(total, k) with moderate k.
 */
export function scaleNodeTotal(total: number, layer = 0): number {
  if (total <= 0) return 0
  const li = Math.min(Math.max(layer, 0), 2)
  if (li === 0) return Math.log1p(total)
  return Math.pow(total, SANKEY_LAYER_EXPONENTS[li])
}

/** Allocate node heights for one column with sublinear weights + max/min cap. */
export function nodeHeightsForColumn(
  layerNodes: Array<{ total: number }>,
  layerIndex: number,
  available: number,
): number[] {
  const n = layerNodes.length
  if (!n) return []
  if (available <= 0) return layerNodes.map(() => SANKEY_NODE_H)

  const weights = layerNodes.map(nd => scaleNodeTotal(nd.total, layerIndex))
  const sum = weights.reduce((a, b) => a + b, 0) || 1
  let heights = weights.map(w => Math.max(SANKEY_NODE_H, (w / sum) * available))

  const minH = Math.min(...heights)
  const cap = minH * SANKEY_MAX_HEIGHT_RATIO
  if (Math.max(...heights) > cap) {
    heights = heights.map(h => Math.min(h, cap))
  }
  return heights
}
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

/** Minimum inner column height so every node keeps min height under sublinear layout. */
export function requiredColHeight(
  layerNodes: Array<{ total: number }>,
  layerIndex = 0,
): number {
  const n = layerNodes.length
  if (!n) return 0

  const totalGap = (n - 1) * SANKEY_GAP

  const sumAt = (available: number) =>
    nodeHeightsForColumn(layerNodes, layerIndex, available)
      .reduce((s, h) => s + h, 0)

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
  const maxCol = Math.max(...layers.map((ln, i) => requiredColHeight(ln, i)), 1)
  const base = SANKEY_V_PAD + maxCol
  // SANKEY_MIN_H applies to empty state only; do not inflate viewBox and stretch nodes.
  return Math.max(base, minHeight)
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
