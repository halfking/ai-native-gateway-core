<script setup lang="ts">
// TopologyCanvas.vue — 实时拓扑画布（2026-10-16）
//
// 分层渲染（方案 §5.1）：
//   · Canvas 2D —— 链路曲线、流动基底、请求粒子、节点光环（每帧重绘）
//   · DOM 覆盖层 —— 节点卡片（文字清晰、可聚焦、i18n、无障碍）
//
// 三条硬纪律：
//  1. 时间基准用 dt(ms)，不用帧数 —— 掉帧时速度不变，否则慢机器上
//     粒子变慢会被误读成「网关变慢」；
//  2. prefers-reduced-motion 时关掉流动与粒子，改为「静态密度点 + 数字」，
//     信息不丢（无障碍不等于降级成空白）；
//  3. 页面不可见（document.hidden）暂停 rAF，回来时按真实时间补偿而不是跳帧。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { computeLayout, edgeWidth, particleRadius } from '../../lib/liveTopologyLayout'
import { ParticlePool } from '../../lib/liveTopologyParticles'
import type { ParticleSpawn } from '../../lib/liveTopologyParticles'
import type { TopoGraph } from '../../lib/liveTopologyGraph'
import type { TopoLayoutNode, TopoNode } from '../../types/liveTopology'

const props = defineProps<{
  graph: TopoGraph
  /** 由上层按最新一批请求算好的粒子投放清单（edgeKey 形如 `a->b`）。 */
  spawns?: ParticleSpawn[]
  /** 高亮链路（右栏点选请求时）：命中的边不降透明度，其余降下去。 */
  highlightEdges?: string[]
  selectedNodeId?: string
}>()

const emit = defineEmits<{
  (e: 'select-node', node: TopoNode | null): void
}>()

const { t } = useI18n()

const wrapRef = ref<HTMLElement | null>(null)
const canvasRef = ref<HTMLCanvasElement | null>(null)
const size = ref({ width: 960, height: 520 })
const hoveredId = ref<string | null>(null)

const pool = new ParticlePool()
const lastFrameAt = ref(0)
let raf = 0
let resizeObserver: ResizeObserver | null = null

const prefersReducedMotion = ref(false)
const active = computed(() => !document.hidden)

const layout = computed(() =>
  computeLayout({ nodes: props.graph.nodes, edges: props.graph.edges, width: size.value.width, height: size.value.height }),
)

const nodeById = computed(() => new Map(layout.value.nodes.map((n) => [n.id, n])))
const edgeByKey = computed(() => new Map(layout.value.edges.map((e) => [e.id, e])))

/** 颜色令牌从 CSS 变量读取（暗色主题自动跟随，不硬编码颜色）。 */
const palette = ref({ success: '#3ecf8e', primary: '#5b8cff', warning: '#e0a53a', danger: '#f07167', muted: '#8b9bb0', border: '#2a3544', surface: '#1a222d' })

function readPalette() {
  const styles = getComputedStyle(document.documentElement)
  const get = (name: string, fallback: string) => {
    const value = styles.getPropertyValue(name).trim()
    return value || fallback
  }
  palette.value = {
    success: get('--kx-success', palette.value.success),
    primary: get('--kx-primary', palette.value.primary),
    warning: get('--kx-warning', palette.value.warning),
    danger: get('--kx-danger', palette.value.danger),
    muted: get('--kx-muted', palette.value.muted),
    border: get('--kx-border', palette.value.border),
    surface: get('--kx-surface', palette.value.surface),
  }
}

function statusColor(status?: string): string {
  switch (status) {
    case 'success':
      return palette.value.success
    case 'failure':
      return palette.value.danger
    case 'rate_limited':
      return palette.value.warning
    case 'in_progress':
      return palette.value.primary
    default:
      return palette.value.muted
  }
}

function healthColor(node: TopoNode): string {
  switch (node.health) {
    case 'healthy':
      return palette.value.success
    case 'warning':
      return palette.value.warning
    case 'down':
      return palette.value.danger
    default:
      return palette.value.muted
  }
}

function bezierPoint(path: Array<{ x: number; y: number }>, t: number, lateral: number): { x: number; y: number } {
  const p0 = path[0]
  const p1 = path[1]
  const p2 = path[2]
  const p3 = path[3]
  const u = 1 - t
  const x = u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x
  const y = u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y
  // 横向偏移沿曲线的法线方向，幅度随 t 收缩（两端贴线、中段散开）
  const nx = p3.y - p0.y
  const ny = -(p3.x - p0.x)
  const norm = Math.hypot(nx, ny) || 1
  const spread = Math.sin(Math.PI * t) * lateral * 7
  return { x: x + (nx / norm) * spread, y: y + (ny / norm) * spread }
}

function drawEdge(ctx: CanvasRenderingContext2D, edgeKey: string, dimmed: boolean) {
  const edge = edgeByKey.value.get(edgeKey)
  if (!edge) return
  const [p0, p1, p2, p3] = edge.path
  ctx.save()
  ctx.globalAlpha = dimmed ? 0.12 : 0.9
  ctx.lineWidth = edgeWidth(edge.requests)
  if (edge.requests > 0) {
    ctx.strokeStyle = edge.failure > 0 ? palette.value.danger : statusColor(edge.lastStatus)
  } else {
    // 没流量 ≠ 故障：中性细虚线（承 network-switch `probe=false` 画中性线的教训）
    ctx.strokeStyle = palette.value.border
    ctx.setLineDash([4, 5])
  }
  ctx.beginPath()
  ctx.moveTo(p0.x, p0.y)
  ctx.bezierCurveTo(p1.x, p1.y, p2.x, p2.y, p3.x, p3.y)
  ctx.stroke()
  ctx.restore()
}

function drawFlow(ctx: CanvasRenderingContext2D, edgeKey: string, phase: number, dimmed: boolean) {
  const edge = edgeByKey.value.get(edgeKey)
  if (!edge || edge.requests <= 0) return
  const [p0, p1, p2, p3] = edge.path
  ctx.save()
  ctx.globalAlpha = dimmed ? 0.06 : 0.35
  ctx.strokeStyle = statusColor(edge.lastStatus)
  ctx.lineWidth = 1.2
  ctx.setLineDash([6, 10])
  ctx.lineDashOffset = -phase
  ctx.beginPath()
  ctx.moveTo(p0.x, p0.y)
  ctx.bezierCurveTo(p1.x, p1.y, p2.x, p2.y, p3.x, p3.y)
  ctx.stroke()
  ctx.restore()
}

function drawNodeGlow(ctx: CanvasRenderingContext2D, node: TopoLayoutNode, time: number, dimmed: boolean) {
  const color = healthColor(node)
  const cx = node.x + node.w / 2
  const cy = node.y + node.h / 2
  ctx.save()
  ctx.globalAlpha = dimmed ? 0.15 : 1

  // 供应端节点的在途压力环：在途越多环越大（一眼看出谁在承压）
  if (node.column === 'provider' && node.metric.inFlight > 0) {
    const base = 18 + Math.min(26, node.metric.inFlight * 4)
    const pulse = prefersReducedMotion.value ? 0 : Math.sin(time / 420) * 3
    ctx.beginPath()
    ctx.arc(cx, cy - node.h / 2 - 6, base + pulse, 0, Math.PI * 2)
    ctx.strokeStyle = node.health === 'down' ? palette.value.danger : palette.value.primary
    ctx.globalAlpha = dimmed ? 0.1 : 0.45
    ctx.lineWidth = 1.4
    ctx.stroke()
  }

  // 健康点：实心圆（healthy）/ 空心（unknown）—— 形状与颜色双重编码，色觉友好
  ctx.beginPath()
  ctx.arc(node.x + 10, node.y + node.h / 2, 4, 0, Math.PI * 2)
  if (node.health === 'unknown') {
    ctx.strokeStyle = color
    ctx.lineWidth = 1.6
    ctx.stroke()
  } else {
    ctx.fillStyle = color
    ctx.fill()
  }
  ctx.restore()
}

/** 取 2D 上下文。某些环境（隐私模式 / 禁用 canvas 的浏览器、jsdom）
 *  getContext 会**抛异常**而不是返回 null —— 那种情况下必须安静降级为
 *  「只有 DOM 层、没有动画层」，不能整页崩掉。 */
function context2d(canvas: HTMLCanvasElement): CanvasRenderingContext2D | null {
  try {
    return canvas.getContext('2d')
  } catch {
    return null
  }
}

function render(nowMs: number) {
  const canvas = canvasRef.value
  if (!canvas) return
  const ctx = context2d(canvas)
  if (!ctx) return
  const dpr = window.devicePixelRatio || 1
  const { width, height } = size.value
  if (canvas.width !== width * dpr || canvas.height !== height * dpr) {
    canvas.width = Math.floor(width * dpr)
    canvas.height = Math.floor(height * dpr)
    canvas.style.width = `${width}px`
    canvas.style.height = `${height}px`
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, width, height)

  const highlight = new Set(props.highlightEdges ?? [])
  const phase = prefersReducedMotion.value ? 0 : nowMs / 26

  // 1) 链路底纹
  for (const edge of layout.value.edges) drawEdge(ctx, edge.id, highlight.size > 0 && !highlight.has(edge.id))

  // 2) 流动基底（reduced-motion 下不画）
  if (!prefersReducedMotion.value) {
    for (const edge of layout.value.edges) {
      if (edge.requests > 0) drawFlow(ctx, edge.id, phase, highlight.size > 0 && !highlight.has(edge.id))
    }
  } else {
    // 无动画时的替代表达：每条有流量的链路画一个静态密度点，信息量不减
    for (const edge of layout.value.edges) {
      if (edge.requests <= 0) continue
      const p = bezierPoint(edge.path, 0.5, 0)
      ctx.save()
      ctx.globalAlpha = highlight.size > 0 && !highlight.has(edge.id) ? 0.15 : 0.8
      ctx.fillStyle = statusColor(edge.lastStatus)
      ctx.beginPath()
      ctx.arc(p.x, p.y, Math.min(6, 2 + Math.log10(edge.requests + 1) * 1.6), 0, Math.PI * 2)
      ctx.fill()
      ctx.restore()
    }
  }

  // 3) 请求粒子
  if (!prefersReducedMotion.value) {
    for (const particle of pool.list()) {
      const edge = edgeByKey.value.get(particle.edgeKey)
      if (!edge) continue
      const p = bezierPoint(edge.path, particle.t, particle.lateral)
      const color = statusColor(particle.status)
      ctx.save()
      ctx.globalAlpha = Math.max(0.25, 1 - particle.t * 0.35)
      ctx.shadowBlur = 10
      ctx.shadowColor = color
      ctx.fillStyle = color
      ctx.beginPath()
      ctx.arc(p.x, p.y, particleRadius(particle.tokens), 0, Math.PI * 2)
      ctx.fill()
      if (particle.isProbe) {
        // 探测流量用白心描边区分，不与真实业务流量混为一谈
        ctx.shadowBlur = 0
        ctx.strokeStyle = '#ffffff'
        ctx.lineWidth = 1
        ctx.stroke()
      }
      ctx.restore()
    }
  }

  // 4) 节点光环
  for (const node of layout.value.nodes) {
    drawNodeGlow(ctx, node, nowMs, Boolean(hoveredId.value && hoveredId.value !== node.id && !isRelated(node.id)))
  }

  ctx.restore?.()
}

function isRelated(id: string): boolean {
  if (!hoveredId.value) return false
  return layout.value.edges.some(
    (e) => (e.id.includes(`${hoveredId.value}->`) || e.id.includes(`->${hoveredId.value}`)),
  ) || id === hoveredId.value
}

function frame(nowMs: number) {
  raf = requestAnimationFrame(frame)
  if (!active.value) {
    lastFrameAt.value = nowMs
    return
  }
  const last = lastFrameAt.value || nowMs
  const dt = Math.min(120, nowMs - last) // 卡顿上限 120ms：避免切回标签页时粒子瞬移
  lastFrameAt.value = nowMs
  pool.step(dt)
  render(nowMs)
}

function measure() {
  const el = wrapRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  size.value = {
    width: Math.max(640, Math.floor(rect.width)),
    height: Math.max(320, Math.floor(rect.height)),
  }
}

function onVisibility() {
  if (!document.hidden) lastFrameAt.value = 0 // 回来后按真实时间重新起步
}

watch(
  () => props.spawns,
  (list) => {
    if (!list?.length || prefersReducedMotion.value) return
    for (const spawn of list) pool.spawn(spawn)
  },
  { deep: false },
)

watch(
  () => props.selectedNodeId,
  () => {
    // 高亮链路由父层通过 highlightEdges 传入，这里不重复维护第二份状态。
  },
)

function watchTheme() {
  const root = document.documentElement
  const observer = new MutationObserver(() => readPalette())
  observer.observe(root, { attributes: true, attributeFilter: ['data-theme', 'class', 'style'] })
  onBeforeUnmount(() => observer.disconnect())
}

onMounted(() => {
  readPalette()
  watchTheme()
  measure()
  const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
  prefersReducedMotion.value = mq.matches
  const onMq = (e: MediaQueryListEvent) => {
    prefersReducedMotion.value = e.matches
  }
  mq.addEventListener('change', onMq)
  // ResizeObserver 在 jsdom / 老浏览器上不存在：缺它时退化为「首次测量 + 窗口 resize」，
  // 不能因为拿不到观察器就整个页面崩掉。
  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(() => measure())
    if (wrapRef.value) resizeObserver.observe(wrapRef.value)
  } else {
    window.addEventListener('resize', measure)
  }
  document.addEventListener('visibilitychange', onVisibility)
  raf = requestAnimationFrame(frame)
  onBeforeUnmount(() => {
    mq.removeEventListener('change', onMq)
    resizeObserver?.disconnect()
    window.removeEventListener('resize', measure)
    document.removeEventListener('visibilitychange', onVisibility)
    cancelAnimationFrame(raf)
    pool.clear()
  })
})

function onNodeEnter(id: string) {
  hoveredId.value = id
}

function onNodeLeave() {
  hoveredId.value = null
}

function onNodeClick(node: TopoNode) {
  emit('select-node', node)
}

const columnLabels = computed(() => [
  { id: 'client', label: t('liveTopology.column.client') },
  { id: 'gateway', label: t('liveTopology.column.gateway') },
  { id: 'provider', label: t('liveTopology.column.provider') },
  { id: 'model', label: t('liveTopology.column.model') },
])
</script>

<template>
  <div ref="wrapRef" class="topo-canvas" :class="{ 'is-degraded': !active }">
    <canvas ref="canvasRef" class="topo-canvas__layer" aria-hidden="true" />
    <div class="topo-canvas__columns" aria-hidden="true">
      <span
        v-for="col in columnLabels"
        :key="col.id"
        class="topo-canvas__column"
        :style="{ left: `${(layout.columnX[col.id as 'client'] ?? 0)}px` }"
      >{{ col.label }}</span>
    </div>
    <ul class="topo-canvas__nodes">
      <li
        v-for="node in layout.nodes"
        :key="node.id"
        class="topo-node"
        :class="[
          `is-${node.health}`,
          { 'is-selected': node.id === selectedNodeId, 'is-dimmed': hoveredId && hoveredId !== node.id && !isRelated(node.id) },
        ]"
        :style="{ left: `${node.x}px`, top: `${node.y}px`, width: `${node.w}px`, height: `${node.h}px` }"
        tabindex="0"
        role="button"
        :aria-label="`${node.label} ${node.metric.requests}`"
        @mouseenter="onNodeEnter(node.id)"
        @mouseleave="onNodeLeave"
        @focus="onNodeEnter(node.id)"
        @blur="onNodeLeave"
        @click="onNodeClick(node)"
        @keydown.enter.prevent="onNodeClick(node)"
      >
        <span class="topo-node__label">{{ node.label }}</span>
        <span v-if="node.sub" class="topo-node__sub">{{ node.sub }}</span>
        <span class="topo-node__metric">{{ node.metric.requests }}<i v-if="node.metric.inFlight">·{{ node.metric.inFlight }}↻</i></span>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.topo-canvas {
  position: relative;
  width: 100%;
  height: 100%;
  min-height: 320px;
  overflow: hidden;
  background:
    radial-gradient(circle at 1px 1px, color-mix(in srgb, var(--kx-border) 60%, transparent) 1px, transparent 0) 0 0 / 22px 22px,
    var(--kx-surface);
}

.topo-canvas.is-degraded {
  filter: saturate(0.55);
}

.topo-canvas__layer {
  position: absolute;
  inset: 0;
}

.topo-canvas__columns {
  position: absolute;
  inset: 0;
  pointer-events: none;
}

.topo-canvas__column {
  position: absolute;
  top: 6px;
  transform: translateX(-50%);
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.topo-canvas__nodes {
  position: absolute;
  inset: 0;
  margin: 0;
  padding: 0;
  list-style: none;
}

.topo-node {
  position: absolute;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 1px;
  padding: 4px 8px 4px 20px;
  border: 1px solid var(--kx-border);
  border-radius: 10px;
  background: color-mix(in srgb, var(--kx-surface) 88%, transparent);
  box-shadow: var(--kx-shadow-sm);
  cursor: pointer;
  transition: opacity 160ms ease, transform 160ms ease;
  overflow: hidden;
}

.topo-node.is-dimmed {
  opacity: 0.25;
}

.topo-node.is-selected {
  outline: 2px solid var(--kx-primary);
  outline-offset: 1px;
}

.topo-node.is-down {
  border-color: var(--kx-danger);
}

.topo-node.is-warning {
  border-color: var(--kx-warning);
}

.topo-node__label {
  font-size: var(--kx-font-sm, 12px);
  font-weight: 600;
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.topo-node__sub {
  font-size: 11px;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.topo-node__metric {
  position: absolute;
  right: 6px;
  bottom: 4px;
  font-size: 11px;
  color: var(--kx-primary);
}

@media (prefers-reduced-motion: reduce) {
  .topo-node {
    transition: none;
  }
}
</style>
