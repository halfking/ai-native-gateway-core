<script setup lang="ts">
// GatewayCoreBox.vue —— 网关核心「大方块」（2026-11-20 C 方案「分组瓦片」版；
// 2026-10-28 选路层版 → 2026-11-20 布局优化轮重排）
//
// owner 口径（2026-11-20 设计轮拍板 C 方案，二轮「流程性强」反馈后终版）：
//   · 紧凑：核心区高度上限 320 → 204（liveTopologyLayout.GATEWAY_BOX_MAX_H），
//     让供应端/模型行获得显示空间；
//   · 流程化：顶部「流程缎带」补全全程节点（入口治理 ▸ 选路压缩 ▸ 存储贯穿
//     ▸ ⬇派发供应端 ▸ 供应端处理 ▸ ⬆供应端返回 ▸ 出口观测 ▸ ↩回客户端），
//     组间 ▸▸ 衔接、①②组内补串行小箭头（真实串行才画，扇出仍不画）；
//   · 外框即类型（用户点名「不同模块不同外框」）：实线框=主链处理 /
//     粗左轨=选路决策 / 斜纹虚线=存储转发 / 虚线框=旁路观测 / 括号=基础服务；
//   · 图标即模块（仓规禁 Unicode 字形冒充图标 ⇒ 全 SVG 线性描边）。
//
// 四组（模块归属与 2026-10-28 版一致，只是「带」变「组」）：
//   ① 入口治理（实线蓝框）    缓存 → 脱敏 →（输入插件）
//   ② 选路 · 压缩（粗左轨）    模型选择 + 扇出竖排（auto/直连/指定）→ 压缩
//   ③ 存储转发（斜纹 amber）   队列徽标 + 会话写入 ⇄ 会话文件存储
//   ④ 出口 · 观测（虚线绿框） （输出插件）+ 任务识别 · 错误记录 · 探测任务
//   右栏 基础服务（括号）      路由管理 · 节点状态管理（不在链上）
//
// 每格显示窗口内命中数，失败转红；**没有数据就是 0**，不编数字。

import { computed, onBeforeUnmount, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  GATEWAY_BRANCH_MODULES,
  GATEWAY_INFRA_MODULES,
  GATEWAY_IN_MODULES,
  GATEWAY_OUT_MODULES,
  GATEWAY_STORE_MODULES,
  type GatewayModuleKey,
  type ModelSelectionStats,
  type TopoPluginChip,
} from '../../types/liveTopology'
import type { GatewayCoreBox } from '../../types/liveTopology'

const props = defineProps<{
  box: GatewayCoreBox
  stats: Record<GatewayModuleKey, { count: number; errors: number }>
  /** 聚焦中的模块（父层点选后传入，纯视觉）。 */
  selected?: GatewayModuleKey | null
  /** 在用插件清单（/api/admin/topology/plugins 投影）。 */
  plugins?: TopoPluginChip[]
  /** 转发队列深度（queue 快照 pipeline.depth；null = 管线未上报，隐藏徽标）。 */
  queueDepth?: number | null
  /** 选路扇出统计（模型选择三出口）；null/缺省 = 尚无数据，三卡画 0。 */
  selection?: ModelSelectionStats | null
  /** 内部直连分桶是否完成测量（供应商清单加载失败 = false，须标注未测量）。 */
  directMeasured?: boolean
}>()

const emit = defineEmits<{
  (e: 'select-module', key: GatewayModuleKey): void
}>()

const { t } = useI18n()

interface CellView {
  key: GatewayModuleKey
  label: string
  count: number
  branch: boolean
  tone: 'idle' | 'active' | 'error'
}

function cellOf(key: GatewayModuleKey): CellView {
  const stat = props.stats?.[key]
  const count = stat?.count ?? 0
  const errors = stat?.errors ?? 0
  return {
    key,
    label: t(`liveTopology.gateway.${key}`),
    count,
    branch: GATEWAY_BRANCH_MODULES.has(key),
    tone: errors > 0 && count > 0 ? 'error' : count > 0 ? 'active' : 'idle',
  }
}

/** ① 入口治理组：缓存 → 脱敏（输入插件随后）。 */
const ingressCells = computed<CellView[]>(() =>
  GATEWAY_IN_MODULES.filter((m) => m.key === 'cache' || m.key === 'sanitize').map((m) => cellOf(m.key)),
)
/** ② 选路 · 压缩组：模型选择（+扇出竖排）→ 压缩（真实管线序，2026-10-28 审计修正）。 */
const selectCells = computed<CellView[]>(() =>
  GATEWAY_IN_MODULES.filter((m) => m.key === 'modelSelect' || m.key === 'compress').map((m) => cellOf(m.key)),
)
const infraCells = computed<CellView[]>(() => GATEWAY_INFRA_MODULES.map((m) => cellOf(m.key)))
const storeCells = computed<CellView[]>(() => GATEWAY_STORE_MODULES.map((m) => cellOf(m.key)))
const outCells = computed<CellView[]>(() => GATEWAY_OUT_MODULES.map((m) => cellOf(m.key)))

/** 流程缎带（二轮「流程性强」反馈）：补全派发/供应端/返回/回客户端全程节点。 */
const ribbon = computed(() => [
  { kind: 'step' as const, tone: 'in', label: t('liveTopology.ribbon.ingress') },
  { kind: 'chev' as const, tone: 'in', label: '' },
  { kind: 'step' as const, tone: 'in', label: t('liveTopology.ribbon.routing') },
  { kind: 'chev' as const, tone: 'in', label: '' },
  { kind: 'mark' as const, tone: 'store', label: t('liveTopology.ribbon.storage') },
  { kind: 'chev' as const, tone: 'in', label: '' },
  { kind: 'mark' as const, tone: 'in', label: `⬇ ${t('liveTopology.lane.toProvider')}` },
  { kind: 'chev' as const, tone: 'neutral', label: '' },
  { kind: 'mark' as const, tone: 'neutral', label: t('liveTopology.ribbon.providerHop') },
  { kind: 'chev' as const, tone: 'out', label: '' },
  { kind: 'mark' as const, tone: 'out', label: `⬆ ${t('liveTopology.lane.fromProvider')}` },
  { kind: 'chev' as const, tone: 'out', label: '' },
  { kind: 'step' as const, tone: 'out', label: t('liveTopology.ribbon.egress') },
  { kind: 'chev' as const, tone: 'out', label: '' },
  { kind: 'mark' as const, tone: 'out', label: `↩ ${t('liveTopology.ribbon.backToClient')}` },
])

/** 底部外框图例（「外框即类型」的说明书；窄屏隐藏）。 */
const frames = computed(() => [
  t('liveTopology.frames.solid'),
  t('liveTopology.frames.rail'),
  t('liveTopology.frames.storeDash'),
  t('liveTopology.frames.outDash'),
  t('liveTopology.frames.bracket'),
  t('liveTopology.frames.count'),
])

/** 模块 SVG 图标（线性描边 1.5px，viewBox 16；仓规禁 Unicode 字形当图标）。
 *  每模块一枚，形状语义=模块职责（缓存=闪电/脱敏=盾/选模=靶心/压缩=向心…）。 */
const ICONS: Record<GatewayModuleKey, string[]> = {
  cache: ['M9 1.5 3.5 9H7l-1 5.5L11.5 7H8l1-5.5z'],
  sanitize: [
    'M8 1.5l5.5 1.8v4.4c0 3.5-2.6 5.6-5.5 6.8-2.9-1.2-5.5-3.3-5.5-6.8V3.3L8 1.5z',
    'M5.6 7.8l1.7 1.7 3.1-3.4',
  ],
  modelSelect: [
    'M8 2.4a5.6 5.6 0 1 0 0 11.2 5.6 5.6 0 1 0 0-11.2z',
    'M8 5.8a2.2 2.2 0 1 0 0 4.4 2.2 2.2 0 1 0 0-4.4z',
    'M8 7.4v1.2',
  ],
  compress: ['M2.5 8h11', 'M5.2 4.6 8 7.4l2.8-2.8', 'M5.2 11.4 8 8.6l2.8 2.8'],
  sessionWrite: ['M3 13.2l.9-3.6 6.9-6.9 2.7 2.7-6.9 6.9-3.6.9z', 'M9.5 4l2.6 2.6'],
  sessionFile: ['M2 3.5h4.2l1.6 1.8H14v7.2H2v-9z'],
  taskDetect: [
    'M5.2 2H3.4A1.4 1.4 0 0 0 2 3.4v1.8',
    'M10.8 2h1.8A1.4 1.4 0 0 1 14 3.4v1.8',
    'M5.2 14H3.4A1.4 1.4 0 0 1 2 12.6v-1.8',
    'M10.8 14h1.8a1.4 1.4 0 0 0 1.4-1.4v-1.8',
    'M8 6a2 2 0 1 0 0 4 2 2 0 1 0 0-4z',
  ],
  errorRecord: ['M8 2.2 14 13H2l6-10.8z', 'M8 6.6v2.6', 'M8 10.6v1.2'],
  probeTask: [
    'M8 2.2a5.8 5.8 0 1 0 0 11.6 5.8 5.8 0 1 0 0-11.6z',
    'M8 7a1 1 0 1 0 0 2 1 1 0 1 0 0-2z',
    'M8 8l3.8-3.4',
  ],
  route: ['M2.5 12.5c5.5 0 2.5-9 8-9h2.4', 'M11.2 1.6 13.4 3.5l-2.2 1.9', 'M2.9 11.3a1.2 1.2 0 1 0 0 2.4 1.2 1.2 0 1 0 0-2.4z'],
  nodeState: ['M1.8 8h3l1.8-4 3.2 8 1.8-4h2.6'],
}

/** 选路扇出三出口卡（并行出口，非串行步骤）。 */
const modeCards = computed(() => {
  const sel = props.selection
  return [
    {
      key: 'auto' as const,
      label: t('liveTopology.selection.auto'),
      count: sel?.auto ?? 0,
      /** auto 的任务分布（后端原始词：chat/reasoning/code/…，跨语种一致）。 */
      detail: (sel?.autoTasks ?? []).map((x) => `${x.type} ×${x.count}`).join(' · '),
    },
    {
      key: 'direct' as const,
      label: t('liveTopology.selection.direct'),
      count: sel?.direct ?? 0,
      detail: '',
    },
    {
      key: 'explicit' as const,
      label: t('liveTopology.selection.explicit'),
      count: sel?.explicit ?? 0,
      detail: '',
    },
  ]
})

/** 插件 chip 按方向落组：input/both 在①入口治理，output 在④出口观测。 */
const inPlugins = computed(() => (props.plugins ?? []).filter((p) => p.direction !== 'output'))
const outPlugins = computed(() => (props.plugins ?? []).filter((p) => p.direction === 'output'))

const showQueue = computed(() => typeof props.queueDepth === 'number' && props.queueDepth >= 0)

/** 窄画布（box.w < 900）：四组折成 2×2、缎带图例让位（布局侧同步放宽高度帽）。 */
const isNarrow = computed(() => props.box.w < 900)

/** 模块格「刚处理过请求」脉冲（2026-10-28 live 页动效轮）：窗口内命中数
 *  **增加**的那一格闪一次（470ms 渐隐）—— 「这个模块正在干活」在图上
 *  有了直接表达，而不只是一个静默更新的数字。
 *  ★ 首见不闪：stats 从空到有值是「数据到达」不是「新活动」；
 *  ★ count 下降不闪：窗口滑动导致计数回落，不是新请求命中。 */
const flashing = reactive(new Set<GatewayModuleKey>())
const flashTimers = new Map<GatewayModuleKey, number>()
const lastCounts = new Map<GatewayModuleKey, number>()

watch(
  () => props.stats,
  (stats) => {
    for (const [key, s] of Object.entries(stats ?? {})) {
      const k = key as GatewayModuleKey
      const prev = lastCounts.get(k)
      if (prev !== undefined && s.count > prev) {
        flashing.add(k)
        const old = flashTimers.get(k)
        if (old) window.clearTimeout(old)
        flashTimers.set(
          k,
          window.setTimeout(() => flashing.delete(k), 470),
        )
      }
      lastCounts.set(k, s.count)
    }
  },
  { deep: true, immediate: true },
)
onBeforeUnmount(() => {
  for (const t of flashTimers.values()) window.clearTimeout(t)
})

const title = computed(() => t('liveTopology.gateway.title'))
const infraTitle = computed(() => t('liveTopology.gateway.infra'))
</script>

<template>
  <section
    class="gw"
    :class="{ 'is-narrow': isNarrow }"
    :style="{ left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px` }"
    :aria-label="title"
    data-testid="gateway-core-box"
  >
    <header class="gw__head">
      <span class="gw__title">{{ title }}</span>
      <span class="gw__flow gw__flow--in" data-testid="gw-flow-in">▼ {{ t('liveTopology.lane.in') }}</span>
      <span class="gw__flow gw__flow--out" data-testid="gw-flow-out">▲ {{ t('liveTopology.lane.out') }}</span>
    </header>

    <!-- 流程缎带：从左到右写明一次请求的完整旅程（二轮「流程性强」）。 -->
    <nav class="gw__ribbon" data-testid="gw-ribbon" :aria-label="t('liveTopology.ribbon.ingress')">
      <template v-for="(node, i) in ribbon" :key="i">
        <span v-if="node.kind === 'step'" class="gw__rstep" :class="`is-${node.tone}`">
          <i class="gw__rnum">{{ ribbon.filter((n, j) => n.kind === 'step' && j <= i).length }}</i>{{ node.label }}
        </span>
        <span v-else-if="node.kind === 'mark'" class="gw__rmark" :class="`is-${node.tone}`">{{ node.label }}</span>
        <span v-else class="gw__rchev" :class="`is-${node.tone}`" aria-hidden="true">▸</span>
      </template>
      <span class="gw__rmark is-infra">└ {{ infraTitle }}</span>
    </nav>

    <!-- 窄版插件整行（真插件名长且数量不定，塞进组行会数据依赖地折行；
         独立成行 ⇒ 组行恒单行，高度确定）。宽版仍在组内。 -->
    <div v-if="isNarrow && (inPlugins.length || outPlugins.length)" class="gw__plugrow">
      <span v-if="inPlugins.length" class="gw__plugins" data-testid="gw-plugins-in">
        <span
          v-for="p in inPlugins"
          :key="p.id"
          class="gw__plugin"
          :class="`is-${p.status}`"
          :title="`${p.name} · ${p.status}`"
        >{{ p.name }}</span>
      </span>
      <span v-if="outPlugins.length" class="gw__plugins" data-testid="gw-plugins-out">
        <span
          v-for="p in outPlugins"
          :key="p.id"
          class="gw__plugin"
          :class="`is-${p.status}`"
          :title="`${p.name} · ${p.status}`"
        >{{ p.name }}</span>
      </span>
    </div>

    <!-- 主体：左四组（流程顺序），右基础服务栏（owner 口径：放一侧）。 -->
    <div class="gw__body">
      <div class="gw__groups">
        <!-- ① 入口治理：缓存 → 脱敏 →（输入插件）——真实串行才画箭头 -->
        <div class="gw__grp gw__grp--in" data-testid="gw-lane-in">
          <span class="gw__grp-t is-in">{{ t('liveTopology.ribbon.ingress') }}</span>
          <div class="gw__grp-cells">
            <template v-for="(cell, i) in ingressCells" :key="cell.key">
              <span v-if="i > 0" class="gw__tarr" aria-hidden="true">→</span>
              <button
                type="button"
                class="gw__cell gw__cell--tile"
                :class="[`is-${cell.tone}`, { 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
                :data-testid="`gw-cell-${cell.key}`"
                :aria-label="`${cell.label} ${cell.count}`"
                @click="emit('select-module', cell.key)"
              >
                <svg class="gw__ic" viewBox="0 0 16 16" aria-hidden="true"><path v-for="d in ICONS[cell.key]" :key="d" :d="d" /></svg>
                <span class="gw__cell-label">{{ cell.label }}</span>
                <span class="gw__cell-count">{{ cell.count }}</span>
              </button>
            </template>
            <!-- 输入插件挂在脱敏之后（输入侧治理件在请求进上游前检查；窄版上移独立行） -->
            <template v-if="inPlugins.length && !isNarrow">
              <span class="gw__tarr" aria-hidden="true">→</span>
              <span class="gw__plugins" data-testid="gw-plugins-in">
                <span
                  v-for="p in inPlugins"
                  :key="p.id"
                  class="gw__plugin"
                  :class="`is-${p.status}`"
                  :title="`${p.name} · ${p.status}`"
                >{{ p.name }}</span>
              </span>
            </template>
          </div>
        </div>

        <span class="gw__chev is-in" aria-hidden="true">▸▸</span>

        <!-- ② 选路 · 压缩：模型选择（扇出竖排）→ 压缩（候选解析先于压缩） -->
        <div class="gw__grp gw__grp--sel" data-testid="gw-lane-select">
          <span class="gw__grp-t is-in">{{ t('liveTopology.ribbon.routing') }}</span>
          <div class="gw__grp-cells">
            <button
              v-for="cell in selectCells.filter((c) => c.key === 'modelSelect')"
              :key="cell.key"
              type="button"
              class="gw__cell gw__cell--tile"
              :class="[`is-${cell.tone}`, { 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
              :data-testid="`gw-cell-${cell.key}`"
              :aria-label="`${cell.label} ${cell.count}`"
              @click="emit('select-module', cell.key)"
            >
              <svg class="gw__ic" viewBox="0 0 16 16" aria-hidden="true"><path v-for="d in ICONS[cell.key]" :key="d" :d="d" /></svg>
              <span class="gw__cell-label">{{ cell.label }}</span>
              <span class="gw__cell-count">{{ cell.count }}</span>
            </button>
            <!-- 扇出竖排：模型选择这一步的三个并行出口（非串行 ⇒ 卡间不画箭头） -->
            <div class="gw__fan">
              <span
                v-for="mode in modeCards"
                :key="mode.key"
                class="gw__mode"
                :class="{ 'is-active': mode.count > 0, 'is-multi': !!mode.detail }"
                :data-testid="`gw-mode-${mode.key}`"
                :aria-label="`${mode.label} ${mode.count}`"
                :title="mode.detail || mode.label"
              >
                <span class="gw__mode-label">{{ mode.label }}</span>
                <span class="gw__mode-count">{{ mode.count }}</span>
                <!-- auto 的任务分布是可见数据（在卡内第二行，不是藏 tooltip） -->
                <span v-if="mode.detail" class="gw__mode-detail">{{ mode.detail }}</span>
              </span>
              <!-- 直连分桶未测量必须说出口：0 是「没有直连」，「没测过」不是 0。 -->
              <span v-if="directMeasured === false" class="gw__mode-note" data-testid="gw-mode-unmeasured">
                {{ t('liveTopology.selection.unmeasured') }}
              </span>
            </div>
            <span class="gw__tarr" aria-hidden="true">→</span>
            <button
              v-for="cell in selectCells.filter((c) => c.key === 'compress')"
              :key="cell.key"
              type="button"
              class="gw__cell gw__cell--tile"
              :class="[`is-${cell.tone}`, { 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
              :data-testid="`gw-cell-${cell.key}`"
              :aria-label="`${cell.label} ${cell.count}`"
              @click="emit('select-module', cell.key)"
            >
              <svg class="gw__ic" viewBox="0 0 16 16" aria-hidden="true"><path v-for="d in ICONS[cell.key]" :key="d" :d="d" /></svg>
              <span class="gw__cell-label">{{ cell.label }}</span>
              <span class="gw__cell-count">{{ cell.count }}</span>
            </button>
          </div>
        </div>

        <span class="gw__chev is-store" aria-hidden="true">▸▸</span>

        <!-- ③ 存储转发：队列徽标 + 写入 ⇄ 文件（贯穿①→④，非串行一步） -->
        <div class="gw__grp gw__grp--store" data-testid="gw-lane-store">
          <span class="gw__grp-t is-store">{{ t('liveTopology.lane.store') }}</span>
          <div class="gw__grp-cells">
            <span
              v-if="showQueue"
              class="gw__queue"
              :class="{ 'is-busy': (queueDepth ?? 0) > 0 }"
              data-testid="gw-queue-depth"
            >{{ t('liveTopology.lane.queue', { n: queueDepth ?? 0 }) }}</span>
            <template v-for="(cell, i) in storeCells" :key="cell.key">
              <span v-if="i > 0" class="gw__store-arrow" aria-hidden="true">⇄</span>
              <button
                type="button"
                class="gw__cell gw__cell--tile gw__cell--store"
                :class="[`is-${cell.tone}`, { 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
                :data-testid="`gw-cell-${cell.key}`"
                :aria-label="`${cell.label} ${cell.count}`"
                @click="emit('select-module', cell.key)"
              >
                <svg class="gw__ic" viewBox="0 0 16 16" aria-hidden="true"><path v-for="d in ICONS[cell.key]" :key="d" :d="d" /></svg>
                <span class="gw__cell-label">{{ cell.label }}</span>
                <span class="gw__cell-count">{{ cell.count }}</span>
              </button>
            </template>
          </div>
        </div>

        <span class="gw__chev is-out" aria-hidden="true">▸▸</span>

        <!-- ④ 出口 · 观测：输出插件 + 旁路三格（并行观测 ⇒ 格间不画箭头） -->
        <div class="gw__grp gw__grp--out" data-testid="gw-lane-out">
          <span class="gw__grp-t is-out">{{ t('liveTopology.ribbon.egress') }}</span>
          <div class="gw__grp-cells">
            <span v-if="outPlugins.length && !isNarrow" class="gw__plugins" data-testid="gw-plugins-out">
              <span
                v-for="p in outPlugins"
                :key="p.id"
                class="gw__plugin"
                :class="`is-${p.status}`"
                :title="`${p.name} · ${p.status}`"
              >{{ p.name }}</span>
            </span>
            <button
              v-for="cell in outCells"
              :key="cell.key"
              type="button"
              class="gw__cell gw__cell--tile gw__cell--out"
              :class="[`is-${cell.tone}`, { 'is-branch': cell.branch, 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
              :data-testid="`gw-cell-${cell.key}`"
              :aria-label="`${cell.label} ${cell.count}`"
              @click="emit('select-module', cell.key)"
            >
              <svg class="gw__ic" viewBox="0 0 16 16" aria-hidden="true"><path v-for="d in ICONS[cell.key]" :key="d" :d="d" /></svg>
              <span class="gw__cell-label">{{ cell.label }}</span>
              <span class="gw__cell-count">{{ cell.count }}</span>
            </button>
          </div>
        </div>
      </div>

      <!-- 基础服务右栏：路由管理 · 节点状态管理（服务于主线，不在主链上） -->
      <aside class="gw__infra" data-testid="gw-infra" :aria-label="infraTitle">
        <span class="gw__infra-tag">{{ infraTitle }}</span>
        <div class="gw__infra-cells">
          <button
            v-for="cell in infraCells"
            :key="cell.key"
            type="button"
            class="gw__cell gw__cell--infra"
            :class="[`is-${cell.tone}`, { 'is-selected': cell.key === selected, 'is-flash': flashing.has(cell.key) }]"
            :data-testid="`gw-cell-${cell.key}`"
            :aria-label="`${cell.label} ${cell.count}`"
            @click="emit('select-module', cell.key)"
          >
            <svg class="gw__ic" viewBox="0 0 16 16" aria-hidden="true"><path v-for="d in ICONS[cell.key]" :key="d" :d="d" /></svg>
            <span class="gw__cell-label">{{ cell.label }}</span>
            <span class="gw__cell-count">{{ cell.count }}</span>
          </button>
        </div>
      </aside>
    </div>

    <!-- 外框图例：五族外框各表一义（「外框即类型」的说明书） -->
    <div v-if="!isNarrow" class="gw__legend" data-testid="gw-legend">
      <span v-for="note in frames" :key="note" class="gw__lg">{{ note }}</span>
    </div>
  </section>
</template>

<style scoped>
.gw {
  position: absolute;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--kx-primary);
  border-radius: 14px;
  background:
    linear-gradient(180deg, color-mix(in srgb, var(--kx-primary) 10%, transparent), transparent 40%),
    color-mix(in srgb, var(--kx-surface) 92%, transparent);
  pointer-events: auto;
  /* 2026-10-29 空白清空轮：面板表面必须自己接住点击。此前 none 让点击
     穿透到画布根 —— 画布根的空白点击=清除点选，而大方块占画面正中，
     用户不会认为点它是「点空白」⇒ 过滤被误清。TopologyCanvas 在本组件
     根上挂了 @click.stop：表面/头部/格子的点击到此为止，不再冒泡。 */
  overflow: hidden;
}

.gw__head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex: none;
  height: 24px;
  padding: 4px 12px 0;
}

.gw__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 700;
  color: var(--kx-text);
  letter-spacing: 0.04em;
}

.gw__flow {
  font-size: 11px;
  letter-spacing: 0.04em;
}

.gw__flow--in {
  color: var(--kx-primary);
}

.gw__flow--out {
  color: var(--kx-success);
}

/* ── 流程缎带（二轮「流程性强」）：全程节点一条线读完 ───── */
.gw__ribbon {
  flex: none;
  display: flex;
  align-items: center;
  gap: 4px;
  height: 22px;
  margin: 2px 10px 4px;
  overflow: hidden;
  white-space: nowrap;
}

.gw__rstep {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  font-weight: 600;
  color: var(--kx-text);
}

.gw__rnum {
  flex: none;
  width: 15px;
  height: 15px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-family: var(--kx-font-mono, monospace);
  font-size: 10px;
  font-style: normal;
  color: #fff;
}

.gw__rstep.is-in .gw__rnum {
  background: var(--kx-primary);
}

.gw__rstep.is-out .gw__rnum {
  background: var(--kx-success);
}

.gw__rmark {
  display: inline-flex;
  align-items: center;
  padding: 1px 8px;
  border-radius: 999px;
  border: 1px dashed var(--kx-border);
  font-size: 10.5px;
  color: var(--kx-muted);
}

.gw__rmark.is-in {
  color: var(--kx-primary);
  border-color: color-mix(in srgb, var(--kx-primary) 50%, var(--kx-border));
}

.gw__rmark.is-out {
  color: var(--kx-success);
  border-color: color-mix(in srgb, var(--kx-success) 50%, var(--kx-border));
}

.gw__rmark.is-store {
  color: var(--kx-warning);
  border-color: color-mix(in srgb, var(--kx-warning) 50%, var(--kx-border));
}

.gw__rmark.is-infra {
  margin-left: auto;
}

.gw__rchev {
  font-size: 10px;
  color: var(--kx-muted);
}

.gw__rchev.is-in {
  color: var(--kx-primary);
}

.gw__rchev.is-out {
  color: var(--kx-success);
}

/* ── 主体：左四组 + 右基础服务栏 ─────────────────────────── */
.gw__body {
  display: flex;
  flex: 1;
  min-height: 0;
  gap: 8px;
  padding: 0 10px;
}

.gw__groups {
  display: flex;
  flex: 1;
  min-width: 0;
  min-height: 0;
  gap: 7px;
  align-items: stretch;
}

/* 组间粗箭头：四组按流程顺序衔接（窄屏折行时让位） */
.gw__chev {
  flex: none;
  align-self: center;
  font-size: 15px;
  font-weight: 700;
  line-height: 1;
  color: var(--kx-muted);
}

.gw__chev.is-in {
  color: var(--kx-primary);
}

.gw__chev.is-store {
  color: var(--kx-warning);
}

.gw__chev.is-out {
  color: var(--kx-success);
}

/* ── 分组外框族（「外框即类型」，用户 2026-11-20 点名） ──── */
.gw__grp {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 3px;
  border-radius: 10px;
  padding: 3px 7px 5px;
  min-width: 0;
  min-height: 0;
}

.gw__grp-t {
  flex: none;
  font-size: 10px;
  letter-spacing: 0.1em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ① 实线蓝框 = 主链处理 */
.gw__grp--in {
  border: 1px solid color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
  background: color-mix(in srgb, var(--kx-primary) 5%, transparent);
}

.gw__grp--in .gw__grp-t,
.gw__grp--sel .gw__grp-t {
  color: var(--kx-primary);
}

/* ② 粗左轨 = 选路决策（含扇出） */
.gw__grp--sel {
  border: 1px solid color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
  border-left: 3px solid var(--kx-primary);
  background: color-mix(in srgb, var(--kx-primary) 7%, transparent);
}

/* ③ 斜纹 amber 虚线 = 存储转发 */
.gw__grp--store {
  border: 1px dashed color-mix(in srgb, var(--kx-warning) 55%, var(--kx-border));
  background: repeating-linear-gradient(135deg, transparent 0 6px, color-mix(in srgb, var(--kx-warning) 7%, transparent) 6px 12px);
}

.gw__grp--store .gw__grp-t {
  color: var(--kx-warning);
}

/* ④ 绿虚线 = 出口观测（旁路族） */
.gw__grp--out {
  border: 1px dashed color-mix(in srgb, var(--kx-success) 50%, var(--kx-border));
  background: color-mix(in srgb, var(--kx-success) 5%, transparent);
}

.gw__grp--out .gw__grp-t {
  color: var(--kx-success);
}

.gw__grp-cells {
  display: flex;
  flex: 1;
  min-width: 0;
  min-height: 0;
  align-items: stretch;
  gap: 5px;
}

/* 组内串行小箭头（①②组真实串行才画；扇出/旁路不画） */
.gw__tarr {
  flex: none;
  align-self: center;
  font-size: 10px;
  color: var(--kx-muted);
}

/* ── 瓦片格：图标 + 名 + 计数 三行竖排 ──────────────────── */
.gw__cell {
  display: flex;
  min-width: 0;
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: color-mix(in srgb, var(--kx-surface) 92%, transparent);
  pointer-events: auto;
  cursor: pointer;
  transition: transform 140ms var(--kx-motion-ease-out), border-color 140ms var(--kx-motion-ease-out);
}

.gw__cell:hover,
.gw__cell:focus-visible {
  transform: translateY(-1px);
  border-color: var(--kx-primary);
  outline: none;
}

.gw__cell--tile {
  flex: 1 1 0;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  padding: 3px 5px;
}

.gw__ic {
  width: 14px;
  height: 14px;
  color: var(--kx-primary);
}

.gw__cell--store .gw__ic {
  color: var(--kx-warning);
}

.gw__cell--out .gw__ic {
  color: var(--kx-success);
}

.gw__cell--infra .gw__ic {
  color: var(--kx-violet, var(--kx-muted));
}

.gw__ic path {
  fill: none;
  stroke: currentColor;
  stroke-width: 1.5;
  stroke-linecap: round;
  stroke-linejoin: round;
}

/* 旁路模块：虚线 + 淡色，避免被读成串行流水线的一步 */
.gw__cell.is-branch {
  border-style: dashed;
  opacity: 0.82;
}

.gw__cell.is-active {
  border-color: color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
}

.gw__cell.is-error {
  border-color: var(--kx-danger);
}

.gw__cell.is-error .gw__ic {
  color: var(--kx-danger);
}

.gw__cell.is-idle {
  opacity: 0.7;
}

/* 聚焦中的模块：环形描边（不是整格变色 —— 变红会被读成故障） */
.gw__cell.is-selected {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--kx-primary) 60%, transparent);
}

.gw__cell-label {
  font-size: var(--kx-text-2xs, 11px);
  color: var(--kx-text);
  overflow: hidden;
  max-width: 100%;
}

/* 瓦片标签两行居中：长名（会话数据文件存储）换行展示，不做省略号截断 */
.gw__cell--tile .gw__cell-label {
  white-space: normal;
  text-align: center;
  line-height: 1.15;
  max-height: 2.4em;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}

.gw__cell-count {
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-primary);
}

.gw__cell.is-error .gw__cell-count {
  color: var(--kx-danger);
}

.gw__cell.is-idle .gw__cell-count {
  color: var(--kx-muted);
}

/* 存储格沿用斜纹语义（组框已有斜纹，格内淡化为边色） */
.gw__cell--store {
  border-color: color-mix(in srgb, var(--kx-warning) 40%, var(--kx-border));
}

.gw__cell--store .gw__cell-count {
  color: var(--kx-warning);
}

.gw__store-arrow {
  flex: none;
  align-self: center;
  font-size: 12px;
  color: var(--kx-warning);
}

.gw__queue {
  flex: none;
  align-self: center;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
  border: 1px dashed var(--kx-border);
  white-space: nowrap;
}

.gw__queue.is-busy {
  color: var(--kx-warning);
  border-color: var(--kx-warning);
}

/* ── 扇出竖排：三个并行出口卡 ───────────────────────────── */
.gw__fan {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 2px;
  min-width: 0;
}

.gw__mode {
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 1px 8px;
  border-radius: 999px;
  border: 1px solid color-mix(in srgb, var(--kx-primary) 25%, var(--kx-border));
  background: color-mix(in srgb, var(--kx-primary) 7%, transparent);
  white-space: nowrap;
  overflow: hidden;
}

/* auto 卡带任务分布 ⇒ 折两行（标签+计数一行，分布微字第二行） */
.gw__mode.is-multi {
  flex-wrap: wrap;
}

.gw__mode.is-active {
  border-color: color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
}

.gw__mode-label {
  font-size: 10.5px;
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__mode-count {
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-primary);
}

.gw__mode-detail {
  flex-basis: 100%;
  font-size: 10px;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 150px;
}

.gw__mode-note {
  padding: 0 8px;
  border-radius: 999px;
  font-size: 10px;
  color: var(--kx-muted);
  white-space: nowrap;
}

/* ── 基础服务右栏（括号族：左粗竖线，无整框） ────────────── */
.gw__infra {
  flex: none;
  width: 118px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 2px 2px 2px 8px;
  border-left: 1px dashed color-mix(in srgb, var(--kx-violet, var(--kx-muted)) 45%, var(--kx-border));
}

.gw__infra-tag {
  flex: none;
  font-size: 10px;
  letter-spacing: 0.1em;
  color: var(--kx-violet, var(--kx-muted));
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__infra-cells {
  display: flex;
  flex: 1;
  min-height: 0;
  flex-direction: column;
  gap: 4px;
}

.gw__cell--infra {
  flex: 1 1 0;
  flex-direction: row;
  align-items: center;
  gap: 5px;
  padding: 2px 6px;
  border: none;
  border-left: 2px solid var(--kx-violet, var(--kx-muted));
  border-radius: 0 6px 6px 0;
  background: color-mix(in srgb, var(--kx-violet, var(--kx-muted)) 6%, transparent);
}

.gw__cell--infra .gw__cell-label {
  flex: 1;
  font-size: 10.5px;
}

.gw__cell--infra .gw__cell-count {
  color: var(--kx-muted);
}

/* ── 外框图例行 ─────────────────────────────────────────── */
.gw__legend {
  flex: none;
  display: flex;
  gap: 10px;
  align-items: center;
  height: 16px;
  margin: 3px 10px 0;
  overflow: hidden;
  white-space: nowrap;
}

.gw__lg {
  font-size: 10px;
  color: var(--kx-muted);
}

.gw__lg:last-child {
  margin-left: auto;
}

/* ── 插件 chip ──────────────────────────────────────────── */
.gw__plugins {
  display: flex;
  flex: none;
  max-width: 34%;
  flex-wrap: wrap;
  gap: 3px;
  overflow: hidden;
  align-content: center;
}

.gw__plugin {
  display: inline-flex;
  align-items: center;
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  white-space: nowrap;
  border: 1px solid var(--kx-border);
  color: var(--kx-text);
  background: color-mix(in srgb, var(--kx-surface) 80%, transparent);
}

.gw__plugin.is-builtin {
  border-color: color-mix(in srgb, var(--kx-primary) 45%, var(--kx-border));
}

.gw__plugin.is-ready {
  border-color: color-mix(in srgb, var(--kx-success) 55%, var(--kx-border));
  color: var(--kx-success);
}

.gw__plugin.is-degraded {
  border-color: color-mix(in srgb, var(--kx-warning) 65%, var(--kx-border));
  color: var(--kx-warning);
}

.gw__plugin.is-failed,
.gw__plugin.is-stopped {
  border-color: color-mix(in srgb, var(--kx-danger) 65%, var(--kx-border));
  color: var(--kx-danger);
}

.gw__plugin.is-starting {
  color: var(--kx-muted);
}

/* 模块格「刚处理过请求」脉冲：从命中色指数减速回落到默认
   （470ms ≤ 800ms 一次性上限；起始帧不透明度不为 0 ⇒ 非入场类）。 */
.gw__cell.is-flash {
  animation: gw-cell-flash 0.47s var(--kx-motion-ease-out);
}

@keyframes gw-cell-flash {
  0% {
    border-color: color-mix(in srgb, var(--kx-primary) 80%, var(--kx-border));
    background-color: color-mix(in srgb, var(--kx-primary) 16%, var(--kx-surface));
  }
}

.gw__cell.is-error.is-flash {
  animation-name: gw-cell-flash-error;
}

@keyframes gw-cell-flash-error {
  0% {
    border-color: var(--kx-danger);
    background-color: color-mix(in srgb, var(--kx-danger) 14%, var(--kx-surface));
  }
}

/* ── 窄版插件整行：真插件名长且数量不定，独立成行（可折≤2行），组行保恒单行 ── */
.gw__plugrow {
  flex: none;
  display: flex;
  gap: 8px;
  margin: 0 8px 2px;
  max-height: 46px;
  overflow: hidden;
}

.gw__plugrow .gw__plugins {
  max-width: 100%;
}

/* ── 窄画布（box.w < 900）：四组折 2×2、瓦片转横排小芯片、图例/粗箭头让位 ──
   实测 1440 视口下三栏网格的画布仅 ~820px ⇒ 窄版是常态而非兜底：
   横排芯片把组高压到 ~46px，两行组 + 头 + 缎带 ≈ 190px（帽 240）。 */
.gw.is-narrow .gw__chev {
  display: none;
}

.gw.is-narrow .gw__groups {
  flex-wrap: wrap;
  gap: 5px;
}

/* 窄版每组占整行、组题内联在左（44% 半行会让芯片折 3~4 行，实测组高 126px；
   整行单线组 ~34px，四组两倍更矮） */
.gw.is-narrow .gw__grp {
  flex: 1 1 100%;
  flex-direction: row;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.gw.is-narrow .gw__grp-t {
  margin-right: 2px;
}

.gw.is-narrow .gw__grp-cells {
  /* ★ 组行恒单行（nowrap + 收缩 + 标签省略）：高度不随生产数据折行漂移 */
  flex-wrap: nowrap;
  align-items: center;
  gap: 4px;
  overflow: hidden;
}

.gw.is-narrow .gw__fan {
  flex-direction: row;
  flex-wrap: nowrap;
  overflow: hidden;
}

.gw.is-narrow .gw__mode {
  flex: 0 1 auto;
  min-width: 0;
}

.gw.is-narrow .gw__mode.is-multi {
  flex-wrap: nowrap;
}

.gw.is-narrow .gw__mode-detail {
  flex-basis: auto;
  max-width: 120px;
}

/* 瓦片转横排小芯片：图标 + 名 + 计数 一行（纵向瓦片在 700px 里放不下四组）；
   标签窄版做省略号（211 实测真插件名/长模块名会把组折到 3 行） */
.gw.is-narrow .gw__cell--tile {
  flex: 0 1 auto;
  flex-direction: row;
  align-items: center;
  gap: 5px;
  padding: 2px 7px;
}

.gw.is-narrow .gw__cell--tile .gw__cell-label {
  white-space: nowrap;
  max-width: 108px;
  text-overflow: ellipsis;
  display: block;
  -webkit-line-clamp: unset;
}

.gw.is-narrow .gw__cell--tile .gw__ic {
  width: 12px;
  height: 12px;
}

.gw.is-narrow .gw__body {
  flex-direction: column;
}

.gw.is-narrow .gw__infra {
  width: auto;
  flex-direction: row;
  align-items: center;
  border-left: none;
  border-top: 1px dashed color-mix(in srgb, var(--kx-violet, var(--kx-muted)) 45%, var(--kx-border));
  padding: 4px 0 0;
}

.gw.is-narrow .gw__infra-cells {
  flex-direction: row;
  gap: 8px;
}

.gw.is-narrow .gw__cell--infra {
  flex: 0 1 auto;
}

.gw.is-narrow .gw__rmark.is-infra,
/* 中性「供应端处理」节点让位（供应端行本身就在下方表达这一跳；
   实测 1024 视口缎带超宽 75px ⇒ 藏中性节点+其后中性箭头刚好收进） */
.gw.is-narrow .gw__rmark.is-neutral,
.gw.is-narrow .gw__rchev.is-neutral {
  display: none;
}

.gw.is-narrow .gw__ribbon {
  gap: 3px;
  margin: 2px 8px 3px;
}

.gw.is-narrow .gw__rmark {
  padding: 1px 5px;
  font-size: 10px;
}

.gw.is-narrow .gw__rstep {
  font-size: 10.5px;
}

@media (prefers-reduced-motion: reduce) {
  .gw__cell {
    transition: none;
  }
  .gw__cell.is-flash {
    animation: none;
  }
}
</style>
