<script setup lang="ts">
// LiveTopologyView.vue — 实时拓扑新首页（2026-10-17 改版：上→下 + 网关核心整体）
//
// 与旧首页并存：`/` 与 `/dashboard` 一行未改，本页是新增的 `/live`。
// 分工：泳道页（/dashboard?tab=stream）看**逐条精确**，本页看**当下态势**。
//
// 版面（维护者 2026-10-17 拍板）：
//   顶部  客户端 / 供应商 / 模型 / 节点 四维**多选**过滤（同维 OR、跨维 AND）
//   左    客户端请求摘要（聚合摘要 + 逐条明细）
//   中    实时拓扑：上→下 客户端 → 网关核心大方块 → 供应端节点 → 模型
//   右    供应商端响应的请求（点 ⤢ 放大，内联覆盖面板）
//   左下  供应商当天请求/token 统计（UTC 日桶，水位显式展示）
//
// **关键约束**：图、左右两栏、左下统计必须收敛到同一个过滤子集，
// 否则用户会看到「图里没有、右栏却有」的矛盾画面。

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElOption, ElSelect } from 'element-plus'
import { useLiveStream } from '../composables/useLiveStream'
import { redisHealthyRef as redisHealthy, redisErrorRef as redisError } from '../composables/liveStreamStore'
import { useLiveTopology, type PanelEntry } from '../composables/useLiveTopology'
import { clientKeyOf, modelKeyOf, providerNodeKeyOf } from '../lib/liveTopologyGraph'
import TopologyCanvas from '../components/live-topology/TopologyCanvas.vue'
import ClientRequestPanel from '../components/live-topology/ClientRequestPanel.vue'
import ProviderResponsePanel from '../components/live-topology/ProviderResponsePanel.vue'
import RequestZoomPanel from '../components/live-topology/RequestZoomPanel.vue'
import ProviderTodayRail from '../components/live-topology/ProviderTodayRail.vue'
import RoutingDecisionTimeline from '../components/live-topology/RoutingDecisionTimeline.vue'
import type { ParticleSpawn } from '../lib/liveTopologyParticles'
import type { GatewayModuleKey, TopoNode } from '../types/liveTopology'
import { DEFAULT_TOPO_FILTERS, TOPO_WINDOW_PRESETS } from '../types/liveTopology'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const { connection, initialDataReceived } = useLiveStream()
const topo = useLiveTopology()

const selectedNodeId = ref<string | null>(null)
const selectedRequestId = ref<string | null>(null)
const highlightEdges = ref<string[]>([])
const spawns = ref<ParticleSpawn[]>([])
/** 放大的那条请求（null = 未放大）。内联覆盖，不是弹窗。 */
const zoomEntry = ref<PanelEntry | null>(null)
/** 点网关模块 → 高亮该格（纯视觉聚焦，不改变过滤子集）。 */
const selectedModule = ref<GatewayModuleKey | null>(null)

/** 多选值在 URL 里是逗号分隔串（可分享、可刷新恢复）。 */
function parseList(value: unknown): string[] {
  const raw = Array.isArray(value) ? value.join(',') : typeof value === 'string' ? value : ''
  return [...new Set(raw.split(',').map((s) => s.trim()).filter(Boolean))]
}

function joinList(list: readonly string[]): string {
  return list.join(',')
}

/** URL ↔ 过滤态双向同步（与既有 ?tab= 契约不冲突）。 */
function syncFromQuery() {
  const q = route.query
  const windowMs = Number(q.window ?? '')
  topo.filters.value = {
    clients: parseList(q.client),
    providers: parseList(q.provider),
    models: parseList(q.model),
    nodes: parseList(q.node),
    status: typeof q.status === 'string' ? (q.status as never) : '',
    windowMs: Number.isFinite(windowMs) && windowMs > 0 ? windowMs : DEFAULT_TOPO_FILTERS.windowMs,
  }
}

function syncToQuery() {
  const f = topo.filters.value
  const query: Record<string, string> = {}
  if (f.providers.length) query.provider = joinList(f.providers)
  if (f.clients.length) query.client = joinList(f.clients)
  if (f.models.length) query.model = joinList(f.models)
  if (f.nodes.length) query.node = joinList(f.nodes)
  if (f.status) query.status = f.status
  if (f.windowMs !== DEFAULT_TOPO_FILTERS.windowMs) query.window = String(f.windowMs)
  void router.replace({ query })
}

watch(() => route.query, syncFromQuery, { deep: true })
watch(topo.filters, syncToQuery, { deep: true })
onMounted(syncFromQuery)

const connectionTone = computed(() => {
  if (connection.value === 'open') return 'is-open'
  if (connection.value === 'connecting' || connection.value === 'reconnecting') return 'is-connecting'
  return 'is-down'
})

const isEmptyByFilter = computed(() => topo.filteredOut.value)

/** 新请求 → 粒子投放清单（edgeKey 形如 `client:x->gateway:core`）。 */
function buildSpawns(): ParticleSpawn[] {
  const out: ParticleSpawn[] = []
  const latest = topo.filteredRequests.value.slice(-24)
  for (const req of latest) {
    if (!req.request_id) continue
    const clientId = `client:${clientKeyOf(req as never)}`
    const providerId = providerNodeKeyOf(req as never)
    const modelId = `model:${modelKeyOf(req as never)}`
    const chain: Array<[string, string]> = [[clientId, 'gateway:core']]
    if (providerId) {
      chain.push(['gateway:core', providerId], [providerId, modelId])
    } else {
      chain.push(['gateway:core', modelId])
    }
    for (const [from, to] of chain) {
      out.push({
        id: `${req.request_id}|${from}->${to}`,
        edgeKey: `${from}->${to}`,
        status: req.status,
        tokens: req.total_tokens ?? null,
        measuredMs: req.latency_ms ?? undefined,
        isProbe: !!req.is_probe,
      })
    }
  }
  return out
}

let lastSeenId = ''
// 新请求到达 → 立即投放粒子（1s 轮询足够：SSE 事件本身是即时帧，
// 这里只做「图上有没有」的低成本兜底节奏）。卸载时必须清掉，
// 否则离开页面后仍在跑定时器（后台烧 CPU）。
const spawnTimer = window.setInterval(() => {
  if (document.hidden) return
  const latest = topo.filteredRequests.value[topo.filteredRequests.value.length - 1]
  if (!latest?.request_id || latest.request_id === lastSeenId) return
  lastSeenId = latest.request_id
  spawns.value = buildSpawns()
}, 1000)
onUnmounted(() => window.clearInterval(spawnTimer))

function onSelectNode(node: TopoNode | null) {
  selectedNodeId.value = node?.id ?? null
  if (!node) {
    selectedRequestId.value = null
    highlightEdges.value = []
    return
  }
  // 点节点 → 高亮它的相关链路
  highlightEdges.value = topo.graph.value.edges
    .filter((e) => e.id.includes(`${node.id}->`) || e.id.includes(`->${node.id}`))
    .map((e) => e.id)
}

function onSelectRequest(requestId: string) {
  selectedRequestId.value = requestId
  highlightEdges.value = topo.edgePathFor(requestId)
  selectedNodeId.value = null
}

function onZoomRequest(entry: PanelEntry) {
  zoomEntry.value = entry
  selectedRequestId.value = entry.requestId
  highlightEdges.value = topo.edgePathFor(entry.requestId)
}

/** 点网关模块 = 纯视觉聚焦（再点一次取消）。**不改过滤子集**：那会让人
 *  以为这个模块在筛数据，而它只是主链路/旁路的一个格子。 */
function onSelectModule(key: GatewayModuleKey) {
  selectedModule.value = selectedModule.value === key ? null : key
}

function clearFilters() {
  topo.filters.value = { ...DEFAULT_TOPO_FILTERS, windowMs: topo.filters.value.windowMs }
}

const decisions = computed(() => (selectedRequestId.value ? topo.decisionsFor(selectedRequestId.value) : []))

const zoomDecisions = computed(() => (zoomEntry.value ? topo.decisionsFor(zoomEntry.value.requestId) : []))

const coreSummary = computed(() => {
  const m = topo.coreMetric.value
  const q = topo.queue.value
  if (!m) return ''
  const parts = [
    t('liveTopology.summary.inFlight', { n: m.inFlight }),
    t('liveTopology.summary.success', { n: m.success }),
    t('liveTopology.summary.failure', { n: m.failure }),
  ]
  if (q?.pipeline?.depth) parts.push(t('liveTopology.summary.queue', { n: q.pipeline.depth }))
  return parts.join(' · ')
})

/** 左下统计栏跟随供应商过滤（多选时取第一项，其余靠过滤后收敛）。 */
const activeProvider = computed(() => topo.filters.value.providers[0] ?? '')

function onSelectProvider(code: string) {
  const list = topo.filters.value.providers
  const next = list.includes(code) ? list.filter((c) => c !== code) : [...list, code]
  topo.filters.value.providers = next
}
</script>

<template>
  <div class="live-topology">
    <header class="live-topology__bar">
      <h2 class="live-topology__title">{{ t('liveTopology.title') }}</h2>
      <span class="live-topology__conn" :class="connectionTone">
        {{ t(`liveTopology.conn.${connection}`) }}
      </span>
      <span v-if="!redisHealthy" class="live-topology__redis">{{ redisError || t('liveTopology.redisDown') }}</span>

      <div class="live-topology__filters">
        <el-select
          :model-value="topo.filters.value.clients"
          multiple
          collapse-tags
          clearable
          class="live-topology__select"
          :placeholder="t('liveTopology.filter.allClients')"
          :aria-label="t('liveTopology.filter.client')"
          data-testid="filter-clients"
          @update:model-value="(v: string[]) => (topo.filters.value.clients = v ?? [])"
        >
          <el-option v-for="opt in topo.options.value.clients" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
        </el-select>

        <el-select
          :model-value="topo.filters.value.providers"
          multiple
          collapse-tags
          clearable
          class="live-topology__select"
          :placeholder="t('liveTopology.filter.allProviders')"
          :aria-label="t('liveTopology.filter.provider')"
          data-testid="filter-providers"
          @update:model-value="(v: string[]) => (topo.filters.value.providers = v ?? [])"
        >
          <el-option v-for="opt in topo.options.value.providers" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
        </el-select>

        <el-select
          :model-value="topo.filters.value.models"
          multiple
          collapse-tags
          clearable
          class="live-topology__select"
          :placeholder="t('liveTopology.filter.allModels')"
          :aria-label="t('liveTopology.filter.model')"
          data-testid="filter-models"
          @update:model-value="(v: string[]) => (topo.filters.value.models = v ?? [])"
        >
          <el-option v-for="opt in topo.options.value.models" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
        </el-select>

        <el-select
          :model-value="topo.filters.value.nodes"
          multiple
          collapse-tags
          clearable
          class="live-topology__select"
          :placeholder="t('liveTopology.filter.allNodes')"
          :aria-label="t('liveTopology.filter.node')"
          data-testid="filter-nodes"
          @update:model-value="(v: string[]) => (topo.filters.value.nodes = v ?? [])"
        >
          <el-option v-for="opt in topo.options.value.nodes" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
        </el-select>

        <select v-model="topo.filters.value.status" :aria-label="t('liveTopology.filter.status')">
          <option value="">{{ t('liveTopology.filter.allStatuses') }}</option>
          <option value="success">{{ t('liveTopology.filter.success') }}</option>
          <option value="in_progress">{{ t('liveTopology.filter.inProgress') }}</option>
          <option value="failure">{{ t('liveTopology.filter.failure') }}</option>
          <option value="rate_limited">{{ t('liveTopology.filter.rateLimited') }}</option>
        </select>

        <select v-model.number="topo.filters.value.windowMs" :aria-label="t('liveTopology.filter.window')">
          <option v-for="preset in TOPO_WINDOW_PRESETS" :key="preset.value" :value="preset.value">
            {{ t(preset.labelKey) }}
          </option>
        </select>

        <button v-if="topo.activeFilterCount.value" type="button" class="live-topology__clear" @click="clearFilters">
          {{ t('liveTopology.filter.clear') }}
        </button>
      </div>
    </header>

    <p v-if="coreSummary" class="live-topology__summary">{{ coreSummary }}</p>

    <p v-if="connection !== 'open'" class="live-topology__banner">
      {{ t('liveTopology.banner.connecting') }}
    </p>
    <p v-else-if="!initialDataReceived" class="live-topology__banner">
      {{ t('liveTopology.banner.waitingFirstFrame') }}
    </p>
    <p v-else-if="isEmptyByFilter" class="live-topology__banner">
      {{ t('liveTopology.banner.filteredEmpty') }}
    </p>

    <div class="live-topology__body">
      <div class="live-topology__left">
        <ClientRequestPanel
          class="live-topology__clients"
          :summaries="topo.clientSummaries.value"
          :requests="topo.clientRequests.value"
          :active-request-id="selectedRequestId ?? undefined"
          @select-request="onSelectRequest"
        />
        <ProviderTodayRail
          class="live-topology__rail"
          :active-provider="activeProvider"
          @select-provider="onSelectProvider"
        />
      </div>

      <div class="live-topology__stage">
        <TopologyCanvas
          :graph="topo.graph.value"
          :spawns="spawns"
          :highlight-edges="highlightEdges"
          :selected-node-id="selectedNodeId ?? undefined"
          :selected-module="selectedModule"
          :gateway-stats="topo.gatewayStats.value"
          @select-node="onSelectNode"
          @select-module="onSelectModule"
        />
        <RoutingDecisionTimeline :decisions="decisions" />
      </div>

      <div class="live-topology__right">
        <ProviderResponsePanel
          :responses="topo.providerResponses.value"
          :active-request-id="selectedRequestId ?? undefined"
          @select-request="onSelectRequest"
          @zoom="onZoomRequest"
        />
        <RequestZoomPanel
          v-if="zoomEntry"
          :entry="zoomEntry"
          :decisions="zoomDecisions"
          @close="zoomEntry = null"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.live-topology {
  display: flex;
  flex-direction: column;
  gap: 8px;
  height: 100%;
  min-height: 0;
  padding: 10px 12px 12px;
}

.live-topology__bar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.live-topology__title {
  margin: 0;
  font-size: 18px;
  color: var(--kx-text);
}

.live-topology__conn {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
}

.live-topology__conn.is-open {
  color: var(--kx-success);
  background: var(--kx-success-soft);
}

.live-topology__conn.is-connecting {
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

.live-topology__conn.is-down {
  color: var(--kx-danger);
  background: var(--kx-danger-soft);
}

.live-topology__redis {
  font-size: 11px;
  color: var(--kx-warning);
}

.live-topology__filters {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-left: auto;
}

.live-topology__select {
  width: 176px;
}

.live-topology__filters select {
  max-width: 140px;
  padding: 3px 6px;
  border: 1px solid var(--kx-border);
  border-radius: 7px;
  background: var(--kx-surface);
  color: var(--kx-text);
  font-size: var(--kx-font-sm, 12px);
}

.live-topology__clear {
  padding: 3px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 7px;
  background: transparent;
  color: var(--kx-muted);
  cursor: pointer;
}

.live-topology__summary {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-muted);
}

.live-topology__banner {
  margin: 0;
  padding: 6px 10px;
  border-radius: 8px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

/* 默认（窄）：单列堆叠 —— 上方拓扑，下方两个请求面板。
   ≥1024：左栏 + 拓扑两列，右侧面板落到下一行；
   ≥1440：左面板 / 拓扑 / 右面板三列并排。
   断点只允许用 scripts/responsive-audit.mjs 的白名单值（480/640/768/1024/1440）。 */
.live-topology__body {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 10px;
  flex: 1;
  min-height: 0;
}

.live-topology__left,
.live-topology__right,
.live-topology__stage {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 0;
}

.live-topology__clients,
.live-topology__rail {
  flex: 1;
  min-height: 0;
}

.live-topology__rail {
  max-height: 240px;
  overflow-y: auto;
}

.live-topology__stage {
  flex: 1;
}

@media (min-width: 1024px) {
  .live-topology__body {
    grid-template-columns: 260px minmax(0, 1fr);
  }

  .live-topology__right {
    grid-column: 1 / -1;
    max-height: 240px;
  }

  .live-topology__clients {
    min-height: 320px;
  }
}

@media (min-width: 1440px) {
  .live-topology__body {
    grid-template-columns: 280px minmax(0, 1fr) 300px;
  }

  .live-topology__right {
    grid-column: auto;
    max-height: none;
  }

  .live-topology__rail {
    max-height: 220px;
  }
}
</style>
