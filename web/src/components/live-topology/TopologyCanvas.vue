<script setup lang="ts">
// TopologyCanvas.vue — 实时拓扑画布（2026-10-17 改版：上→下 + 网关核心大方块）
//
// 分层渲染（方案 §5.1）：
//   · Canvas 2D —— 链路曲线、流动基底、请求粒子、节点光环（每帧重绘）
//   · DOM 覆盖层 —— 节点卡片 + 网关核心大方块（文字清晰、可聚焦、i18n、无障碍）
//
// 三条硬纪律（沿用 2026-10-16 轮）：
//  1. 时间基准用 dt(ms)，不用帧数 —— 掉帧时速度不变；
//  2. prefers-reduced-motion 时关掉流动与粒子，改静态密度点，信息不丢；
//  3. 页面不可见暂停 rAF；ResizeObserver / getContext 缺失时安静降级。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  bezierPoint,
  computeLayout,
  edgeWidth,
  isEdgeHighlighted,
  MIN_LAYOUT_HEIGHT,
  MIN_LAYOUT_WIDTH,
  particleRadius,
} from '../../lib/liveTopologyLayout'
import { ParticlePool } from '../../lib/liveTopologyParticles'
import type { ParticleSpawn } from '../../lib/liveTopologyParticles'
import type { TopoGraph } from '../../lib/liveTopologyGraph'
import { clientDisplayName } from '../../lib/liveTopologyGraph'
import {
  GATEWAY_CORE_ID,
  type GatewayCoreBox,
  type GatewayModuleKey,
  type TopoLayoutEdge,
  type TopoLayoutNode,
  type TopoNode,
  type TopoPluginChip,
} from '../../types/liveTopology'
import GatewayCoreBoxView from './GatewayCoreBox.vue'

const props = defineProps<{
  graph: TopoGraph
  /** 由上层按最新一批请求算好的粒子投放清单。 */
  spawns?: ParticleSpawn[]
  /** 高亮链路（点面板条目时）：命中的边不降透明度。 */
  highlightEdges?: string[]
  selectedNodeId?: string
  gatewayStats?: Record<GatewayModuleKey, { count: number; errors: number }>
  /** 当前聚焦的网关内部模块（纯视觉高亮，不改变过滤子集）。 */
  selectedModule?: GatewayModuleKey | null
  /** 在用插件清单（网关大方块插件带）。 */
  plugins?: TopoPluginChip[]
  /** 转发队列深度（存储转发带徽标；null = 未上报）。 */
  queueDepth?: number | null
}>()

const emit = defineEmits<{
  (e: 'select-node', node: TopoNode | null): void
  (e: 'select-module', key: GatewayModuleKey): void
}>()

const { t } = useI18n()

const wrapRef = ref<HTMLElement | null>(null)
const canvasRef = ref<HTMLCanvasElement | null>(null)
const size = ref({ width: 960, height: 640 })
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

const gatewayBox = computed<GatewayCoreBox>(() => layout.value.gateway)
const gatewayStats = computed(() => props.gatewayStats ?? ({} as Record<GatewayModuleKey, { count: number; errors: number }>))

const edgeByKey = computed(() => new Map(layout.value.edges.map((e) => [e.id, e])))

const palette = ref({
  success: '#3ecf8e', primary: '#5b8cff', warning: '#e0a53a', danger: '#f07167',
  muted: '#8b9bb0', border: '#2a3544', surface: '#1a222d',
})

function readPalette() {
  const styles = getComputedStyle(document.documentElement)
  const get = (name: string, fallback: string) => styles.getPropertyValue(name).trim() || fallback
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
    case 'success': return palette.value.success
    case 'failure': return palette.value.danger
    case 'rate_limited': return palette.value.warning
    case 'in_progress': return palette.value.primary
    default: return palette.value.muted
  }
}

/**
 * 协议配色（2026-10-18 owner 要求：连线按协议标注/着色）。
 * 常见协议钉固定色（运维形成色彩记忆），未知协议按稳定哈希落进
 * 备选色板 —— 同一协议永远同色，刷新/重排不变。
 */
const KNOWN_PROTOCOL_COLORS: Record<string, string> = {
  openai: '#3aa981',
  anthropic: '#e08c3a',
  gemini: '#5b8cff',
  'openai-responses': '#2dd4bf',
}
const FALLBACK_PROTOCOL_COLORS = ['#5b8cff', '#e0a53a', '#b48cff', '#2dd4bf', '#f07167', '#7ba7c9']

function protocolColor(name: string): string {
  const known = KNOWN_PROTOCOL_COLORS[name.toLowerCase()]
  if (known) return known
  let hash = 5381
  for (let i = 0; i < name.length; i += 1) hash = ((hash * 33) ^ name.charCodeAt(i)) >>> 0
  return FALLBACK_PROTOCOL_COLORS[hash % FALLBACK_PROTOCOL_COLORS.length]
}

/** 协议 chip 投放清单：挂在 client→gateway 边中点（小字标注 + 协议色）。 */
/** 协议 chip 投放清单：挂在 client→gateway 边上（小字标注 + 协议色）。
 *  同一条边最多两个协议时**沿路径错开**（t=0.42/0.58）—— 审计轮抓到
 *  两个 chip 同锚 t=0.45 会完全重叠，夹具里每客户端单协议所以肉眼没暴露。 */
const CHIP_T_POSITIONS = [0.42, 0.58] as const
const protocolChips = computed(() => {
  const out: Array<{ id: string; x: number; y: number; name: string; count: number; color: string }> = []
  for (const edge of layout.value.edges) {
    if (edge.to !== GATEWAY_CORE_ID || !edge.from.startsWith('client:')) continue
    if (!edge.protocolCounts) continue
    const ranked = Object.entries(edge.protocolCounts)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .slice(0, 2)
    ranked.forEach(([name, count], i) => {
      const p = bezierOn(edge.path, CHIP_T_POSITIONS[i] ?? 0.42, 0)
      out.push({ id: `${edge.id}#${name}`, x: p.x, y: p.y, name, count, color: protocolColor(name) })
    })
  }
  return out
})

function healthColor(node: TopoNode): string {
  switch (node.health) {
    case 'healthy': return palette.value.success
    case 'warning': return palette.value.warning
    case 'down': return palette.value.danger
    default: return palette.value.muted
  }
}

function bezierOn(path: Array<{ x: number; y: number }>, t: number, lateral: number): { x: number; y: number } {
  return bezierPoint(path, t, lateral)
}

function context2d(canvas: HTMLCanvasElement): CanvasRenderingContext2D | null {
  try {
    return canvas.getContext('2d')
  } catch {
    return null
  }
}

function drawEdge(ctx: CanvasRenderingContext2D, edgeKey: string, dimmed: boolean, flowPhase: number) {
  const edge = edgeByKey.value.get(edgeKey)
  if (!edge) return
  const [p0, p1, p2, p3] = edge.path
  ctx.save()
  ctx.globalAlpha = dimmed ? 0.12 : 0.9
  ctx.lineWidth = edgeWidth(edge.requests)
  if (edge.requests > 0) {
    // 客户端→网关边按**主导协议**着色（owner 2026-10-18：连线要能看出
    // 协议）；失败红优先于协议色 —— 故障信息比协议归属更重要。
    const protoDominant =
      edge.to === GATEWAY_CORE_ID && edge.from.startsWith('client:') && edge.protocolCounts
        ? Object.entries(edge.protocolCounts).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))[0]?.[0]
        : undefined
    if (edge.failure > 0) ctx.strokeStyle = palette.value.danger
    else if (protoDominant) ctx.strokeStyle = protocolColor(protoDominant)
    else ctx.strokeStyle = statusColor(edge.lastStatus)
  } else {
    // 没流量 ≠ 故障：中性细虚线
    ctx.strokeStyle = palette.value.border
    ctx.setLineDash([4, 5])
  }
  ctx.beginPath()
  ctx.moveTo(p0.x, p0.y)
  ctx.bezierCurveTo(p1.x, p1.y, p2.x, p2.y, p3.x, p3.y)
  ctx.stroke()
  ctx.restore()

  if (edge.requests > 0 && !prefersReducedMotion.value) {
    ctx.save()
    ctx.globalAlpha = dimmed ? 0.06 : 0.35
    ctx.strokeStyle = statusColor(edge.lastStatus)
    ctx.lineWidth = 1.2
    ctx.setLineDash([6, 10])
    ctx.lineDashOffset = flowPhase
    ctx.beginPath()
    ctx.moveTo(p0.x, p0.y)
    ctx.bezierCurveTo(p1.x, p1.y, p2.x, p2.y, p3.x, p3.y)
    ctx.stroke()
    ctx.restore()
  }
}

function drawNodeGlow(ctx: CanvasRenderingContext2D, node: TopoLayoutNode, time: number, dimmed: boolean) {
  const color = healthColor(node)
  const cx = node.x + node.w / 2
  const cy = node.y + node.h / 2
  ctx.save()
  ctx.globalAlpha = dimmed ? 0.15 : 1

  if (node.column === 'provider' && node.metric.inFlight > 0) {
    const base = 14 + Math.min(22, node.metric.inFlight * 3)
    const pulse = prefersReducedMotion.value ? 0 : Math.sin(time / 420) * 3
    ctx.beginPath()
    ctx.arc(cx, node.y - 5, base + pulse, 0, Math.PI * 2)
    ctx.strokeStyle = node.health === 'down' ? palette.value.danger : palette.value.primary
    ctx.globalAlpha = dimmed ? 0.1 : 0.45
    ctx.lineWidth = 1.4
    ctx.stroke()
  }

  ctx.beginPath()
  ctx.arc(node.x + 10, cy, 4, 0, Math.PI * 2)
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

  // 高亮按**原始 edge id** 匹配（见 isEdgeHighlighted 的注释）：窄屏折叠后
  // 布局边 id 变了，只比 id 的话折叠一发生高亮就静默失效。
  const isHighlighted = (edgeId: string, sourceIds?: readonly string[]) =>
    isEdgeHighlighted(highlight, edgeId, sourceIds)

  for (const edge of layout.value.edges) {
    drawEdge(ctx, edge.id, highlight.size > 0 && !isHighlighted(edge.id, edge.sourceIds), phase)
  }

  // reduced-motion 替代表达：每条有流量的链路一个静态密度点
  if (prefersReducedMotion.value) {
    for (const edge of layout.value.edges) {
      if (edge.requests <= 0) continue
      const p = bezierOn(edge.path, 0.5, 0)
      ctx.save()
      ctx.globalAlpha = highlight.size > 0 && !isHighlighted(edge.id, edge.sourceIds) ? 0.15 : 0.8
      ctx.fillStyle = statusColor(edge.lastStatus)
      ctx.beginPath()
      ctx.arc(p.x, p.y, Math.min(6, 2 + Math.log10(edge.requests + 1) * 1.6), 0, Math.PI * 2)
      ctx.fill()
      ctx.restore()
    }
  } else {
    for (const particle of pool.list()) {
      const edge = edgeByKey.value.get(particle.edgeKey)
      if (!edge) continue
      const p = bezierOn(edge.path, particle.t, particle.lateral)
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
        ctx.shadowBlur = 0
        ctx.strokeStyle = '#ffffff'
        ctx.lineWidth = 1
        ctx.stroke()
      }
      ctx.restore()
    }
  }

  for (const node of layout.value.nodes) {
    drawNodeGlow(ctx, node, nowMs, Boolean(hoveredId.value && hoveredId.value !== node.id && !isRelated(node.id)))
  }
}

function isRelated(id: string): boolean {
  if (!hoveredId.value) return false
  // 关联判断同时看布局边 id 与其**原始** id：折叠后布局边 id 形如
  // `x->model:folded:8`，而 hover 到的可能是未折叠的另一侧，只比 id 会漏。
  return layout.value.edges.some((e) => {
    const ids = [e.from, e.to, ...(e.sourceIds ?? [])]
    return ids.some(
      (v) => v.includes(`${hoveredId.value}->`) || v.includes(`->${hoveredId.value}`),
    )
  }) || id === hoveredId.value
}

function frame(nowMs: number) {
  raf = requestAnimationFrame(frame)
  if (!active.value) {
    lastFrameAt.value = nowMs
    return
  }
  const last = lastFrameAt.value || nowMs
  const dt = Math.min(120, nowMs - last)
  lastFrameAt.value = nowMs
  pool.step(dt)
  render(nowMs)
}

function measure() {
  const el = wrapRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  // 用容器真实尺寸。早先这里强制 `Math.max(640, …)`，而 `.topo-canvas` 是
  // `overflow:hidden` —— 窄屏时画布画到 640 宽，右侧那截（480 视口下 113px）
  // 直接被裁掉，大方块右边和每行末尾的卡永远看不见。**下限大于容器就是丢信息**。
  size.value = {
    width: Math.max(MIN_LAYOUT_WIDTH, Math.floor(rect.width)),
    height: Math.max(MIN_LAYOUT_HEIGHT, Math.floor(rect.height)),
  }
}

function onVisibility() {
  if (!document.hidden) lastFrameAt.value = 0
}

function watchTheme() {
  const observer = new MutationObserver(() => readPalette())
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'class', 'style'] })
  onBeforeUnmount(() => observer.disconnect())
}

watch(
  () => props.spawns,
  (list) => {
    if (!list?.length || prefersReducedMotion.value) return
    for (const spawn of list) pool.spawn(spawn)
  },
  { deep: false },
)

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

// 纵向流程下的层标签复用既有的 column.* 键（不另造 layer.* ——
// 同一语义两份 key 必然漂移）。网关层由大方块自带标题，不进这组标签。
const layerLabels = computed(() => [
  { id: 'client', label: t('liveTopology.column.client'), y: layout.value.rowY.client },
  { id: 'provider', label: t('liveTopology.column.provider'), y: layout.value.rowY.provider },
  { id: 'model', label: t('liveTopology.column.model'), y: layout.value.rowY.model },
])

/**
 * 节点的无障碍标签。
 * 早先只有 `${label} ${requests}` —— 一个 `down` 节点读出来完全没有故障信息，
 * 而聚合节点（label 就是 `+7`）读出来是「+7 123」，纯属噪音。
 * 现在把健康态说清楚，聚合节点只报数量。
 */
function nodeLabel(node: TopoNode): string {
  if (node.isAggregate) {
    return t('liveTopology.node.aggregate', { n: node.metric.requests })
  }
  const label = node.column === 'client' ? displayLabel(node) : node.label
  const health = t(`liveTopology.health.${node.health}`)
  return `${label} · ${health} · ${t('liveTopology.node.requests', { n: node.metric.requests })}`
}

/** 客户端卡片显示名（owner 2026-10-18 更易懂）：identity 键 → 品牌写法；
 *  兜底键 unknown → 「未知客户端」。非 client 列原样。 */
function displayLabel(node: TopoNode): string {
  if (node.label === 'unknown') return t('liveTopology.client.unknown')
  return clientDisplayName(node.label)
}

/** 客户端卡片的副行（owner 2026-10-18 更易懂）：`类型 · 主导协议`
 *  —— 协议来自该客户端→网关边上的分布（与连线 chip 同源）。 */
function clientSubLine(node: TopoNode): string {
  const parts: string[] = []
  if (node.sub) parts.push(node.sub)
  const edge = edgeByKey.value.get(`${node.id}->${GATEWAY_CORE_ID}`)
  const dominant = edge?.protocolCounts
    ? Object.entries(edge.protocolCounts).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))[0]?.[0]
    : undefined
  if (dominant) parts.push(dominant)
  return parts.join(' · ')
}

function onNodeEnter(id: string) {
  hoveredId.value = id
}function onNodeLeave() {
  hoveredId.value = null
}

function onNodeClick(node: TopoNode) {
  emit('select-node', node)
}
</script>

<template>
  <div ref="wrapRef" class="topo-canvas" :class="{ 'is-degraded': !active }">
    <canvas ref="canvasRef" class="topo-canvas__layer" aria-hidden="true" />
    <div class="topo-canvas__layers" aria-hidden="true">
      <span
        v-for="layer in layerLabels"
        :key="layer.id"
        class="topo-canvas__layer-label"
        :style="{ top: `${layer.y}px` }"
      >{{ layer.label }}</span>
    </div>
    <!-- 协议小字 chip：client→gateway 边上「协议 ×条数」（owner 2026-10-18）。 -->
    <div class="topo-canvas__chips" aria-hidden="true">
      <span
        v-for="chip in protocolChips"
        :key="chip.id"
        class="topo-canvas__chip"
        :data-testid="`proto-chip-${chip.name}`"
        :style="{ left: `${chip.x}px`, top: `${chip.y}px`, borderColor: chip.color, color: chip.color }"
      >{{ chip.name }} ×{{ chip.count }}</span>
    </div>
    <GatewayCoreBoxView
      :box="gatewayBox"
      :stats="gatewayStats"
      :selected="selectedModule"
      :plugins="plugins"
      :queue-depth="queueDepth"
      @select-module="emit('select-module', $event)"
    />
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
        :aria-label="nodeLabel(node)"
        @mouseenter="onNodeEnter(node.id)"
        @mouseleave="onNodeLeave"
        @focus="onNodeEnter(node.id)"
        @blur="onNodeLeave"
        @click="onNodeClick(node)"
        @keydown.enter.prevent="onNodeClick(node)"
      >
        <span class="topo-node__label">{{ node.column === 'client' ? displayLabel(node) : node.label }}</span>
        <span v-if="node.column === 'client' ? clientSubLine(node) : node.sub" class="topo-node__sub">
          {{ node.column === 'client' ? clientSubLine(node) : node.sub }}
        </span>
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
  min-height: 480px;
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

.topo-canvas__layers {
  position: absolute;
  inset: 0;
  pointer-events: none;
}

.topo-canvas__layer-label {
  position: absolute;
  left: 10px;
  transform: translateY(-50%);
  font-size: 11px;
  color: var(--kx-muted);
  letter-spacing: 0.08em;
}

.topo-canvas__chips {
  position: absolute;
  inset: 0;
  pointer-events: none;
}

.topo-canvas__chip {
  position: absolute;
  transform: translate(-50%, -50%);
  padding: 0 6px;
  border: 1px solid;
  border-radius: 999px;
  font-size: 10px;
  line-height: 16px;
  white-space: nowrap;
  background: color-mix(in srgb, var(--kx-surface) 82%, transparent);
  font-variant-numeric: tabular-nums;
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
