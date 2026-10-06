<script setup lang="ts">
// Sparkline — 依赖零的 SVG 迷你趋势线（移动端快查不需要完整图表库）。
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    points: number[]
    width?: number
    height?: number
    tone?: string
    fill?: boolean
  }>(),
  { width: 120, height: 36, tone: 'var(--app-primary)', fill: true },
)

const path = computed<string>(() => {
  const pts = props.points
  const w = props.width
  const h = props.height
  if (pts.length < 2) return ''
  const max = Math.max(...pts, 1)
  const min = Math.min(...pts, 0)
  const span = max - min || 1
  const step = w / (pts.length - 1)
  return pts
    .map((v, i) => {
      const x = i * step
      const y = h - 3 - ((v - min) / span) * (h - 6)
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)} ${y.toFixed(1)}`
    })
    .join(' ')
})

const areaPath = computed<string>(() => {
  if (!path.value || !props.fill) return ''
  return `${path.value} L${props.width} ${props.height} L0 ${props.height} Z`
})
</script>

<template>
  <!--
    ★ 宽度**不再**烘进 `width` 属性：`width` 属性 + `display:block` 会让这个 SVG
      钉在固有宽度上，在 flex 行里永远填不满容器。实跑读数（776 CSS px 视口，
      8782 上 `/m` 总览页）：描边跨度约 340 设备 px ÷ DPR 1.523 ≈ **223 CSS px**，
      恰好等于 HomeView 传的 `:width="220"`，而卡片可用宽度约 688 CSS px
      ⇒ 左侧 2/3 全空。视口越宽，占比越小。

    改法三件套，缺一不可：
      ① 不绑 `width` 属性，靠 CSS `width:100%` + `flex:1 1 auto; min-width:0` 吃满剩余空间；
      ② `preserveAspectRatio="none"` 让 viewBox 横向拉伸而**不**留信箱边
         （默认 `xMidYMid meet` 会上下居中留白）；
      ③ `vector-effect="non-scaling-stroke"` 让描边宽度不被非等比缩放拉粗。
  -->
  <svg
    :height="height"
    :viewBox="`0 0 ${width} ${height}`"
    preserveAspectRatio="none"
    class="sparkline"
    aria-hidden="true"
  >
    <path v-if="areaPath" :d="areaPath" :fill="tone" opacity="0.12" />
    <path
      :d="path"
      fill="none"
      :stroke="tone"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
      vector-effect="non-scaling-stroke"
    />
  </svg>
</template>

<style scoped>
.sparkline {
  display: block;
  /* ★ 横向吃满 flex 行的剩余空间；`min-width:0` 防 flex item 不肯收缩导致溢出。 */
  flex: 1 1 auto;
  min-width: 0;
  width: 100%;
  /* ⚠️ 这里**不能**写 `height: auto`：CSS 优先于 SVG 的 `height` 表现属性，
     一旦让高度由 viewBox 比例推导，600px 宽时高度会变成 600/220*40 ≈ 109px。
     保持 height 由属性决定，高度就锁在 `height` prop 上。 */
}
</style>
