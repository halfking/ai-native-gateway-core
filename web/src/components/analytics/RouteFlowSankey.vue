<script setup lang="ts">
import { computed } from 'vue'
import type { AnalyticsFlow } from '../../api-autoroute'

const props = defineProps<{
  data: AnalyticsFlow | null
  loading?: boolean
  minHeight?: number
}>()

const W = 720
const colX = [80, 360, 640]
const nodeH = 18
const gap = 4
const MIN_H = 200

// ── Task-type color palette ──────────────────────────
const TASK_COLORS: Record<string, string> = {
  chat:          '#6366f1', // indigo
  reasoning:     '#a855f7', // purple
  code:          '#22c55e', // green
  agent:         '#f97316', // orange
  creative:      '#ec4899', // pink
  long_context:  '#a16207', // amber-brown
  vision:        '#06b6d4', // cyan
  function_call: '#eab308', // yellow
}
const FALLBACK_COLOR = '#94a3b8' // slate-400

function colorForTask(taskKey: string): string {
  return TASK_COLORS[taskKey] || FALLBACK_COLOR
}

function linkColor(taskType?: string): string {
  return taskType ? colorForTask(taskType) : FALLBACK_COLOR
}

const layers = computed(() => {
  if (!props.data) return [[], [], []]
  const out: Array<Array<{ id: string; label: string; layer: number; total: number }>> = [[], [], []]
  const totals: Record<string, number> = {}
  for (const l of props.data.links) {
    totals[l.source] = (totals[l.source] || 0) + l.value
    totals[l.target] = (totals[l.target] || 0) + l.value
  }
  for (const n of props.data.nodes) {
    const layer = Math.min(Math.max(n.layer, 0), 2)
    out[layer].push({ ...n, total: totals[n.id] || 0 })
  }
  for (const layer of out) {
    layer.sort((a, b) => b.total - a.total)
  }
  return out
})

const H = computed(() => {
  const maxNodes = Math.max(...layers.value.map(l => l.length), 1)
  const h = 24 + maxNodes * (nodeH + gap) + 24
  return Math.max(h, props.minHeight || 0, MIN_H)
})

interface LayoutNode {
  id: string
  label: string
  layer: number
  x: number
  y: number
  h: number
}

const layout = computed(() => {
  const nodes: LayoutNode[] = []
  const pos: Record<string, { x: number; y: number; h: number }> = {}
  const maxTotal = Math.max(
    ...layers.value.flat().map(n => n.total),
    1,
  )

  layers.value.forEach((layerNodes, li) => {
    let y = 24
    const colHeight = H.value - 48
    const totalLayer = layerNodes.reduce((s, n) => s + n.total, 0) || 1
    for (const n of layerNodes) {
      const h = Math.max(nodeH, (n.total / totalLayer) * colHeight * 0.85)
      const x = colX[li] - 60
      pos[n.id] = { x, y, h }
      nodes.push({ id: n.id, label: n.label, layer: n.layer, x, y, h })
      y += h + gap
    }
    void maxTotal
  })
  return { nodes, pos }
})

function linkPath(sourceId: string, targetId: string): string {
  const s = layout.value.pos[sourceId]
  const t = layout.value.pos[targetId]
  if (!s || !t) return ''
  const x0 = s.x + 120
  const x1 = t.x
  const y0 = s.y + s.h / 2
  const y1 = t.y + t.h / 2
  const mx = (x0 + x1) / 2
  return `M ${x0} ${y0} C ${mx} ${y0}, ${mx} ${y1}, ${x1} ${y1}`
}

function linkWidth(value: number): number {
  const max = Math.max(...(props.data?.links.map(l => l.value) ?? [1]), 1)
  return 1 + (value / max) * 10
}

const isEmpty = computed(() =>
  !props.loading && (!props.data || !props.data.links.length)
)

function truncLabel(s: string, max = 18): string {
  return s.length > max ? s.slice(0, max - 1) + '…' : s
}

const layerLabels = ['任务类型', '出站模型', '供应商']
const layerColors = [
  'color-mix(in srgb, var(--accent) 25%, var(--bg-subtle))',
  'color-mix(in srgb, var(--success) 20%, var(--bg-subtle))',
  'color-mix(in srgb, var(--warning, #d29922) 18%, var(--bg-subtle))',
]

// Collect which task types actually appear in the data (for legend)
const activeTaskTypes = computed(() => {
  const keys = new Set<string>()
  if (!props.data) return []
  for (const l of props.data.links) {
    if (l.task_type) keys.add(l.task_type)
  }
  return [...keys]
})

const TASK_LABELS: Record<string, string> = {
  chat: '通用对话', reasoning: '逻辑推理', code: '代码生成', agent: 'Agent',
  creative: '创意写作', long_context: '长文档', vision: '图像理解', function_call: '函数调用',
}
</script>

<template>
  <div class="sankey-wrap">
    <div v-if="loading" class="sankey-hint">加载流向图…</div>
    <div v-else-if="isEmpty" class="sankey-hint">暂无流向数据 — 任务→模型→供应商链路需 Auto 请求样本</div>
    <div v-else class="sankey-svg-wrap">
      <div class="sankey-legend">
        <span v-for="(lbl, i) in layerLabels" :key="lbl" class="legend-chip">
          <span class="legend-swatch" :style="{ background: layerColors[i] }" />
          {{ lbl }}
        </span>
        <span class="legend-divider" />
        <span v-for="tk in activeTaskTypes" :key="tk" class="legend-chip">
          <span class="legend-swatch" :style="{ background: colorForTask(tk) }" />
          {{ TASK_LABELS[tk] || tk }}
        </span>
      </div>
      <svg :viewBox="`0 0 ${W} ${H}`" class="sankey-svg" preserveAspectRatio="xMidYMid meet">
        <text :x="colX[0]" y="14" class="layer-title">任务类型</text>
        <text :x="colX[1]" y="14" class="layer-title">出站模型</text>
        <text :x="colX[2]" y="14" class="layer-title">供应商</text>

        <g class="links">
          <path
            v-for="(l, i) in data!.links"
            :key="'l-' + i"
            :d="linkPath(l.source, l.target)"
            class="flow-link"
            :stroke="linkColor(l.task_type)"
            :stroke-width="linkWidth(l.value)"
            :opacity="0.35 + (l.value / Math.max(...data!.links.map(x => x.value), 1)) * 0.45"
          />
        </g>

        <g class="nodes">
          <g v-for="n in layout.nodes" :key="n.id">
            <rect
              :x="n.x"
              :y="n.y"
              width="120"
              :height="n.h"
              rx="3"
              class="flow-node"
              :class="'layer-' + n.layer"
            />
            <title>{{ n.label }}</title>
            <text :x="n.x + 4" :y="n.y + n.h / 2 + 3" class="node-label">
              {{ truncLabel(n.label) }}
            </text>
          </g>
        </g>
      </svg>
    </div>
  </div>
</template>

<style scoped>
.sankey-wrap { width: 100%; }
.sankey-hint {
  padding: 16px;
  text-align: center;
  color: var(--muted);
  font-size: 11px;
}
.sankey-svg-wrap { overflow-x: auto; }
.sankey-legend {
  display: flex;
  gap: 12px;
  justify-content: center;
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.legend-chip {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 9px;
  color: var(--muted);
}
.legend-swatch {
  width: 10px;
  height: 10px;
  border-radius: 2px;
  border: 1px solid var(--border);
}
.legend-divider {
  width: 1px;
  height: 12px;
  background: var(--border);
  margin: 0 2px;
}
.sankey-svg {
  width: 100%;
  min-width: 480px;
  height: auto;
  display: block;
}
.layer-title {
  font-size: 9px;
  fill: var(--muted);
  text-anchor: middle;
  font-weight: 600;
}
.flow-link {
  fill: none;
  stroke-linecap: round;
}
.flow-node {
  stroke: var(--border);
  stroke-width: 1;
}
.flow-node.layer-0 { fill: color-mix(in srgb, var(--accent) 25%, var(--bg-subtle)); }
.flow-node.layer-1 { fill: color-mix(in srgb, var(--success) 20%, var(--bg-subtle)); }
.flow-node.layer-2 { fill: color-mix(in srgb, var(--warning, #d29922) 18%, var(--bg-subtle)); }
.node-label {
  font-size: 9px;
  fill: var(--text);
  pointer-events: none;
}
</style>
