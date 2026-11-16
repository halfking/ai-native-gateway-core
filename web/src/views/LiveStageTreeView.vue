<script setup lang="ts">
// LiveStageTreeView.vue —— **live2**：请求阶段树（2026-11-16，owner「过滤条件还是还原
// 到上一步，放在顶层，统计点开可以展开全页形式，盖在动态显示的上层。
// 请结合真实数据，生成 live2 页面，然后加入到系统的实时拓扑页」）
//
// 与 `/live`（旧三栏拓扑）**并存**一个版本周期：本页是新增路由 `/live2`，
// 旧页除过滤栏抽成共享组件外一行未改（可回退）。
//
// 版面（owner 2026-11-16 定稿）：
//   顶栏    标题 + 连接态 + **统计胶囊**（点开 ⇒ 全页浮层盖住动态显示）
//   同一行  **过滤条件常驻在顶层**（owner：「过滤条件还是还原到上一步，放在顶层」）
//   主区    **左右向请求阶段树**：最左是每个请求，发送轨（左→右）到供应商/令牌池，
//           接收轨（从右往左）回到客户端；逐个请求往上翻动，内存上限 50 条。
//
// ★ 数据全部来自**真实 SSE**（liveStreamStore），不造假值：
//   · 请求帧：模型/供应商/凭据/耗时/成本/客户端/协议/缓存 token/上游协议；
//   · request_lifecycle 动作流（liveactions）：阶段到达与耗时、模型切换、失败定位；
//   · 四段此前没有实时信号的阶段（审批/注入检测/脱敏/还原）在 2026-11-16
//     Phase 1 补点后有了动作信号；**补点之前一律显示「未测量」，不假装跑过**。
//
// ★ 口径纪律：
//   · 「未测量」≠「未到达」—— 两个不同的灰态（图标与文案都不同）；
//   · 缓存命中率带口径（分母随上游协议变），口径不可判定显示「—」而不是 0%；
//   · 最终匹配模型取 `node_selected` 动作的 model（= qr.resolvedModel()），
//     退到帧的 canonical_name / upstream_request 的 raw_model；
//   · 指纹可信度走前端 join（一次拉 admin 清单），关联不上显示「未测量」。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CaretBottom } from '@element-plus/icons-vue'
import { useLiveStream } from '../composables/useLiveStream'
import { useModelCredibility } from '../composables/useModelCredibility'
import {
  getRequestActions,
  nodesRef,
  queueRef,
  requestsRef,
  type LiveRequest,
} from '../composables/liveStreamStore'
import { applyFilters, filterOptions } from '../lib/liveTopologyFilter'
import { invalidNodeKeys } from '../lib/liveTopologyGraph'
import { cacheRateOf, deriveStageBand, type StageBand } from '../lib/liveStageTree'
import { DEFAULT_TOPO_FILTERS, type TopoFilters } from '../types/liveTopology'
import LiveTopologyFilterBar from '../components/live-topology/LiveTopologyFilterBar.vue'
import StageTreeBand from '../components/live-stage-tree/StageTreeBand.vue'
import StageTreeStatsOverlay from '../components/live-stage-tree/StageTreeStatsOverlay.vue'

const { t } = useI18n()
const { connection } = useLiveStream()
const credibility = useModelCredibility()

/** 过滤态（与 /live 同一份语义：同维 OR、跨维 AND）。 */
const filters = ref<TopoFilters>({ ...DEFAULT_TOPO_FILTERS })
/** 统计浮层开合。 */
const statsOpen = ref(false)
/** 画布引用（自动跟随贴底）。 */
const canvasRef = ref<HTMLElement | null>(null)
/** 是否跟随底部（用户上滚 ⇒ 暂停）。 */
const followBottom = ref(true)

/** 静默带阈值：3 秒无流量出现；5 分钟后只改时长与行高。 */
const IDLE_ONSET_MS = 3_000
const IDLE_LONG_MS = 5 * 60_000

const now = () => Date.now()

/** 无效节点（熔断/不可达/停用）集合 —— 与 /live 同一把尺。 */
const invalidNodes = computed(() => invalidNodeKeys(nodesRef.value as never))
const hiddenNodes = computed<Set<string> | undefined>(() =>
  filters.value.showInvalidNodes ? undefined : invalidNodes.value,
)

/** 过滤后的请求（与过滤栏同一子集 ⇒ 不会出现「图里没有、计数有」的矛盾）。 */
const filteredRequests = computed<LiveRequest[]>(() =>
  applyFilters(requestsRef.value as never, filters.value, now(), hiddenNodes.value) as never[],
)

/** 过滤项候选（带命中数）。 */
const options = computed(() =>
  filterOptions(filteredRequests.value as never, now(), filters.value.windowMs),
)

/** 已生效过滤条件数。 */
const activeFilterCount = computed(
  () =>
    filters.value.clients.length +
    filters.value.providers.length +
    filters.value.models.length +
    filters.value.nodes.length +
    (filters.value.status ? 1 : 0),
)

/** 阶段带（**最新在下**：新请求追加在末尾 ⇒ 老请求被顶上去 = 逐个请求往上翻动）。 */
const bands = computed<StageBand[]>(() =>
  filteredRequests.value
    .slice()
    .sort((a, b) => (Date.parse(a.ts ?? '') || 0) - (Date.parse(b.ts ?? '')))
    .map(r => deriveStageBand(r, getRequestActions(r.request_id ?? ''))),
)

/** 在途数（实时口径）。 */
const inFlight = computed(() => bands.value.filter(b => b.inFlight).length)

/** 最后一次有流量的时刻（静默带判定）。 */
const lastTrafficAt = ref(Date.now())
watch(
  () => filteredRequests.value.length,
  n => {
    if (n > 0) lastTrafficAt.value = Date.now()
  },
)

/** 静默带：≤5 分钟一行矮条；>5 分钟只改时长与行高（不新增行）。 */
const idleWaitMs = computed(() => Date.now() - lastTrafficAt.value)
const idleVisible = computed(
  () => bands.value.length === 0 && idleWaitMs.value > IDLE_ONSET_MS,
)
const idleIsLong = computed(() => idleWaitMs.value > IDLE_LONG_MS)

/** 统计胶囊读数（口径：与过滤同一子集）。 */
const statsReadout = computed(() => {
  const total = bands.value.length
  const done = bands.value.filter(b => b.failedStage === null && !b.inFlight).length
  const rate = total ? Math.round((done / total) * 100) : null
  return { total, rate, inFlight: inFlight.value }
})

/** 缓存命中率的展示形态（口径由纯函数层给出）。 */
function cacheChip(band: StageBand): { label: string; tip: string; tone: 'hit' | 'na' | 'bad' } {
  const cache = band.cache
  if (!cache) {
    return {
      label: t('liveStageTree.band.cache.na'),
      tip: t('liveStageTree.band.cache.tip.na'),
      tone: 'na',
    }
  }
  if (cache.anomaly) {
    return {
      label: t('liveStageTree.band.cache.anomaly'),
      tip: t('liveStageTree.band.cache.tip.anomaly'),
      tone: 'bad',
    }
  }
  if (cache.pct === null) {
    return {
      label: t('liveStageTree.band.cache.na'),
      tip: t('liveStageTree.band.cache.tip.unknown'),
      tone: 'na',
    }
  }
  // 口径说明按口径类型二选一（逐字面量 t()：动态键是引用扫描器的盲区）。
  const tip =
    cache.convention === 'parallel_anthropic'
      ? t('liveStageTree.band.cache.tip.parallel')
      : t('liveStageTree.band.cache.tip.subset')
  return {
    label: t('liveStageTree.band.cache.hit', { pct: cache.pct }),
    tip,
    tone: 'hit',
  }
}

/** 模型入口行（auto ⇒ 「auto → 最终模型」；其余 ⇒ 「请求 <名>」）。 */
function modelEntryOf(band: StageBand): string {
  if (band.model.isAuto) {
    return t('liveStageTree.band.modelEntry.auto', { model: band.model.final || '—' })
  }
  return t('liveStageTree.band.modelEntry.direct', { model: band.model.requested || '—' })
}

/** 摘要行的解析链 chip（auto 任务类型 / 别名解析 / 模型切换）。 */
function modelChainOf(band: StageBand): string {
  const m = band.model
  if (m.switched) {
    return t('liveStageTree.band.chain.switch', { from: m.switched.from, to: m.switched.to })
  }
  if (m.isAuto) {
    return t('liveStageTree.band.chain.auto', {
      task: m.taskType || '—',
      model: m.final || '—',
    })
  }
  if (m.requested && m.final && m.requested !== m.final) {
    return t('liveStageTree.band.chain.alias', { from: m.requested, model: m.final })
  }
  return ''
}

/** 最终匹配模型的来源说明（tooltip，如实标注出处）。 */
function finalModelTipOf(band: StageBand): string {
  // ★ 逐字面量 t()：动态键无法被引用扫描器解析 ⇒
  //   ① 会被「共有死键」判据记成「8 语种共有但全仓 0 引用」；
  //   ② 键路径写错时**没有任何门会报**（键存在性检查只看被引用到的键）。
  const src = band.model.source
  if (src === 'action') return t('liveStageTree.band.finalModelTip.action')
  if (src === 'canonical') return t('liveStageTree.band.finalModelTip.canonical')
  if (src === 'upstream_raw') return t('liveStageTree.band.finalModelTip.upstreamRaw')
  if (src === 'frame') return t('liveStageTree.band.finalModelTip.frame')
  return t('liveStageTree.band.finalModelTip.none')
}

/** 指纹可信度 chip（与模型名同步显示；关联不上显示「未测量」）。 */
function credibilityChip(band: StageBand): {
  label: string
  tip: string
  tone: 'verified' | 'suspected' | 'diluted' | 'unknown' | 'na'
} {
  const row = credibility.get(band.credentialId, band.rawModel)
  if (!row) {
    return {
      label: t('liveStageTree.credibility.unmeasured'),
      tip: t('liveStageTree.credibility.tip.unmeasured'),
      tone: 'na',
    }
  }
  const verdict = row.verdict
  const tone =
    verdict === 'verified'
      ? 'verified'
      : verdict === 'suspected'
        ? 'suspected'
        : verdict === 'diluted'
          ? 'diluted'
          : 'unknown'
  // ★ 判定词用**字面量** t() 取（动态键无法被引用扫描器解析 ⇒ 会被 i18n 门
  //   记成「未决调用点」⇒ 必须人工登记才放行）。
  const verdictText =
    verdict === 'verified'
      ? t('liveStageTree.credibility.verified')
      : verdict === 'suspected'
        ? t('liveStageTree.credibility.suspected')
        : verdict === 'diluted'
          ? t('liveStageTree.credibility.diluted')
          : t('liveStageTree.credibility.unknown')
  const score = row.credibility_score
  // 判定词与分数分开渲染：分数是数字，不是文案 ⇒ 不需要「{verdict} {score}」
  // 这种跨语种逐字相同的模板键（那会被「未翻译」判据当成没翻）。
  const label = score == null ? verdictText : `${verdictText} ${score}`
  const fingerprint = (row.system_fingerprint ?? '').trim()
  // ★ 兜底文案先算出来再传参：t() 的第二参里**不许再嵌 t()**
  //   （参数扫描器遇到嵌套调用会判「参数集无法静态确定」⇒ i18n 门记未决）。
  // 上游系统指纹生产不可得（211 非空率 0）⇒ 显式「未提供」，不编。
  const fingerprintText = fingerprint || t('liveStageTree.credibility.fingerprintMissing')
  const measuredAtText = row.measured_at || t('liveStageTree.credibility.measuredAtMissing')
  const tip = t('liveStageTree.credibility.tip.detail', {
    verdict: verdictText,
    identity: row.identity_score ?? '—',
    capability: row.capability_score ?? '—',
    consistency: row.consistency_score ?? '—',
    reference: row.reference_score ?? '—',
    fingerprint: fingerprintText,
    measuredAt: measuredAtText,
  })
  return { label, tip, tone }
}

function statusOf(band: StageBand): string {
  if (band.failedStage) return 'failure'
  if (band.inFlight) return 'in_progress'
  return 'success'
}

function onFilterPatch(patch: Partial<TopoFilters>) {
  filters.value = { ...filters.value, ...patch }
}

function clearFilters() {
  filters.value = { ...DEFAULT_TOPO_FILTERS, windowMs: filters.value.windowMs }
}

/** 跟随底部：新请求到达后把视图拉到底部（老请求被顶上去）。 */
function scrollToBottom() {
  const el = canvasRef.value
  if (!el || !followBottom.value) return
  el.scrollTop = el.scrollHeight
}

watch(
  () => bands.value.length,
  () => {
    requestAnimationFrame(scrollToBottom)
  },
)

/** 用户上滚 ⇒ 暂停跟随（内容增长不误判成用户意图）。 */
function onCanvasScroll() {
  const el = canvasRef.value
  if (!el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  followBottom.value = nearBottom
}

onMounted(() => {
  requestAnimationFrame(scrollToBottom)
  // 可信度清单：拉一次即可（可信度按 (凭据, 上游模型) 维度，不随请求变）。
  void credibility.load()
})
onBeforeUnmount(() => {
  /* 组件卸载时 SSE 由 useLiveStream 的 release 钩子收尾。 */
})
</script>

<template>
  <div class="stage-tree">
    <header class="stage-tree__bar">
      <h2 class="stage-tree__title">{{ t('liveStageTree.title') }}</h2>
      <span class="stage-tree__conn" :class="`is-${connection}`">
        {{ t(`liveTopology.conn.${connection}`) }}
      </span>

      <!-- 统计胶囊：点开 ⇒ 全页浮层（盖住动态显示）。 -->
      <button
        type="button"
        class="stage-tree__stats"
        data-testid="stage-stats-open"
        :aria-expanded="statsOpen"
        :title="t('liveStageTree.stats.open')"
        @click="statsOpen = true"
      >
        {{ t('liveStageTree.stats.readout', {
          total: statsReadout.total,
          rate: statsReadout.rate === null ? '—' : statsReadout.rate,
          inFlight: statsReadout.inFlight,
        }) }}
        <span class="stage-tree__stats-caret"><CaretBottom /></span>
      </button>

      <!-- 过滤条件：常驻顶层（owner 2026-11-16）。 -->
      <LiveTopologyFilterBar
        class="stage-tree__filters"
        :filters="filters"
        :options="options"
        :active-filter-count="activeFilterCount"
        @patch="onFilterPatch"
        @clear="clearFilters"
      />
    </header>

    <!-- 主区：阶段树（动态显示） -->
    <div ref="canvasRef" class="stage-tree__canvas" data-testid="stage-tree-canvas" @scroll.passive="onCanvasScroll">
      <StageTreeBand
        v-for="band in bands"
        :key="band.requestId"
        :request-id="band.requestId"
        :stages="band.stages"
        :client-name="band.clientName"
        :client-type="band.clientType"
        :client-protocol="band.clientProtocol"
        :is-auto="band.model.isAuto"
        :model-entry="modelEntryOf(band)"
        :final-model="band.model.final"
        :final-model-tip="finalModelTipOf(band)"
        :upstream-model="band.model.upstream"
        :model-chain="modelChainOf(band)"
        :credibility-label="credibilityChip(band).label"
        :credibility-tip="credibilityChip(band).tip"
        :credibility-tone="credibilityChip(band).tone"
        :provider-label="band.providerLabel || band.providerCode"
        :status="statusOf(band)"
        :error-kind="band.errorKind"
        :latency-ms="band.latencyMs"
        :total-tokens="band.totalTokens"
        :cost-usd="band.costUsd"
        :cache-label="cacheChip(band).label"
        :cache-tip="cacheChip(band).tip"
        :cache-tone="cacheChip(band).tone"
      />

      <!-- 静默带：前端自造（与后端 idle_marker 心跳严格分离）。 -->
      <div
        v-if="idleVisible"
        class="stage-tree__idle"
        :class="{ 'is-long': idleIsLong }"
        data-testid="stage-idle-band"
      >
        <span class="stage-tree__idle-dot"></span>
        <span>{{ t('liveStageTree.idle.wait', { ms: idleWaitMs }) }}</span>
      </div>

      <p v-if="!bands.length && !idleVisible" class="stage-tree__empty" data-testid="stage-tree-empty">
        {{ t('liveStageTree.empty') }}
      </p>
    </div>

    <StageTreeStatsOverlay
      :open="statsOpen"
      :in-flight="inFlight"
      :queue-depth="queueRef?.pipeline?.depth ?? null"
      @close="statsOpen = false"
    />
  </div>
</template>

<style scoped>
.stage-tree {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--kx-bg);
}

.stage-tree__bar {
  flex: none;
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--kx-space-2);
  padding: var(--kx-space-2) var(--kx-space-3);
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
}

.stage-tree__title {
  margin: 0;
  font-size: var(--kx-text-lg);
  font-weight: 650;
  color: var(--kx-text);
}

.stage-tree__conn {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.stage-tree__conn.is-open {
  color: var(--kx-success);
}
.stage-tree__conn.is-connecting {
  color: var(--kx-warning);
}
.stage-tree__conn.is-down {
  color: var(--kx-danger);
}

/* 统计胶囊：读数 + 展开指示（点开是全页浮层，不是小浮窗）。 */
.stage-tree__stats {
  display: inline-flex;
  align-items: center;
  gap: var(--kx-space-1);
  height: 24px;
  padding: 0 var(--kx-space-2);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-container);
  color: var(--kx-text-secondary);
  font-size: var(--kx-text-2xs);
  cursor: pointer;
}

.stage-tree__stats:hover {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}

.stage-tree__stats-caret {
  color: var(--kx-muted);
  display: inline-flex;
  align-items: center;
}

/* element-plus 图标是裸 <svg viewBox>，必须显式定尺寸。 */
.stage-tree__stats-caret > svg {
  width: 12px;
  height: 12px;
}

/* 过滤栏常驻顶层：自己占满一行（owner 2026-11-16）。 */
.stage-tree__filters {
  flex: 1 1 100%;
  min-width: 0;
}

.stage-tree__canvas {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  overflow-anchor: none;
}

.stage-tree__idle {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  height: 34px;
  padding: 0 var(--kx-space-3);
  border-bottom: 1px dashed var(--kx-border);
  color: var(--kx-muted);
  font-size: var(--kx-text-xs);
}

/* 长静默（>5 分钟）：只加高行高，不新增行。 */
.stage-tree__idle.is-long {
  height: 96px;
  align-items: flex-start;
  padding-top: var(--kx-space-2);
}

.stage-tree__idle-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--kx-muted);
}

.stage-tree__empty {
  margin: 0;
  padding: var(--kx-space-6);
  color: var(--kx-muted);
  font-size: var(--kx-text-xs);
  text-align: center;
}
</style>
