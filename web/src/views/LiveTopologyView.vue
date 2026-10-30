<script setup lang="ts">
// LiveTopologyView.vue — 实时拓扑新首页（2026-10-17 改版：上→下 + 网关核心整体）
//
// 与旧首页并存：`/` 与 `/dashboard` 一行未改，本页是新增的 `/live`。
// 分工：泳道页（/dashboard?tab=stream）看**逐条精确**，本页看**当下态势**。
//
// 版面（维护者 2026-10-17 拍板；2026-10-28 头部统计轮更新）：
//   顶部  客户端 / 供应商 / 模型 / 节点 四维**多选**过滤（同维 OR、跨维 AND）
//   头部  区间统计面板（客户端+供应商+模型三组 + 请求数/token/成功率总量行，
//         5分钟/1小时/当天，可折叠；2026-10-29 owner 口径：全页只有这一份统计，
//         SSE 三块汇总退役；在途/排队等流程状态并入总量行）
//   左    客户端请求逐条明细（聚合已上移头部）
//   中    实时拓扑：上→下 客户端 → 网关核心大方块 → 供应端节点 → 模型
//   右    供应商端响应的请求（点行弹摘要浮层，「查看全景」开新窗）
//
// 点选联动（owner 2026-10-29）：点中间图的客户端/供应端/模型节点 ⇒ 左右
// 两栏自动收敛到该节点（写入同一份 filters，与 URL 双向同步）；再点同一个
// 节点 = 取消该维过滤；点画布空白区 = 三个维度全部失效，恢复完整数据。
// 网关大方块的模块点选仍是纯视觉聚焦，不参与过滤。
//
// **关键约束**：图、头部统计面板、左右两栏必须收敛到同一个
// 过滤子集，否则用户会看到「图里没有、右栏却有」的矛盾画面。

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElOption, ElSelect } from 'element-plus'
import { useLiveStream } from '../composables/useLiveStream'
import { redisHealthyRef as redisHealthy, redisErrorRef as redisError, liveDataRevision } from '../composables/liveStreamStore'
import { useLiveTopology, type PanelEntry } from '../composables/useLiveTopology'
import { clientKeyOf, modelKeyOf, providerNodeKeyOf } from '../lib/liveTopologyGraph'
import { openRequestDetailPage } from '../utils/openRequestDetailPage'
import TopologyCanvas from '../components/live-topology/TopologyCanvas.vue'
import ClientRequestPanel from '../components/live-topology/ClientRequestPanel.vue'
import ProviderResponsePanel from '../components/live-topology/ProviderResponsePanel.vue'
import StatsPanel from '../components/live-topology/StatsPanel.vue'
import RoutingDecisionTimeline from '../components/live-topology/RoutingDecisionTimeline.vue'
import RequestZoomPanel from '../components/live-topology/RequestZoomPanel.vue'
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
/** 点行弹出的内联摘要浮层（两步交互第一步：先摘要，后全景）。
 *  side 记录浮层归属哪一列（2026-10-28 owner 口径）：点左栏请求 → 浮层
 *  弹在左栏、点右栏请求 → 弹在右栏 —— 摘要跟着点击侧走，不跨屏搬运。 */
const zoomEntry = ref<PanelEntry | null>(null)
const zoomSide = ref<'left' | 'right'>('right')
const highlightEdges = ref<string[]>([])
const spawns = ref<ParticleSpawn[]>([])
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

/** URL ↔ 过滤态双向同步（与既有 ?tab= 契约不冲突）。
 *  两个数据开关只在「打开」（非默认）时进 URL：probe=1 / invalid=1 ——
 *  默认链接不带参数 = 默认隐藏，分享出去的链接语义与页面一致。 */
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
    showProbe: q.probe === '1',
    showInvalidNodes: q.invalid === '1',
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
  if (f.showProbe) query.probe = '1'
  if (f.showInvalidNodes) query.invalid = '1'
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

/** 转发队列深度：queue 快照没上报（未接线）时传 null，存储带隐藏徽标。 */
const queueDepth = computed(() => {
  const q = topo.queue.value
  if (!q?.pipeline) return null
  return q.pipeline.depth
})

// 在用插件清单（网关大方块插件带）。挂载时拉一次 —— 插件集合只在
// 启动/安装时变化，不值得轮询；失败 = degraded 提示 chip（不冒充空）。
// 内部直连供应商清单（kind='local'）同批拉：选路扇出带的「直连」分桶
// 与供应端节点的直连徽标都吃它；失败 = 扇出带标注「未测量」。
onMounted(() => {
  void topo.loadPlugins()
  void topo.loadLocalProviders()
})

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
    // 空白区 = 点选失效：四个内容维度全部恢复不过滤（owner 2026-10-29 口径：
    // 「点空白重新显示完整的数据」——nodes 维与点选写入的三维同族，URL/顶栏
    // 带进来的 node 过滤不清的话「完整数据」不成立；审计轮判据钉住）。
    // status / 窗口 / 数据开关不属于「点选」这件事，不动 —— 那是顶部
    // 「清除」按钮的职责。
    topo.filters.value.clients = []
    topo.filters.value.providers = []
    topo.filters.value.models = []
    topo.filters.value.nodes = []
    return
  }
  // 点节点 → 高亮它的相关链路
  highlightEdges.value = topo.graph.value.edges
    .filter((e) => e.id.includes(`${node.id}->`) || e.id.includes(`->${node.id}`))
    .map((e) => e.id)
  // 点节点 → 左右两栏与图收敛到该节点（同一个过滤子集，owner 2026-10-29 口径）
  applyNodeFilter(node)
}

/** 画布点节点 ⇒ 把对应维度收敛为该节点（再点同一个 = 取消该维过滤）。
 *  供应端节点 id（`cred:<id>` / `pcode:<code>`）本身就是 providers 维度的
 *  合法值 —— matchesFilters 对该维度同时比对 provider_code 与节点键。
 *  聚合节点（「+N」）不是任何真实键：高亮可以，过滤不行（设了必是空集）。 */
function applyNodeFilter(node: TopoNode) {
  if (node.isAggregate) return
  if (node.id.startsWith('client:')) {
    topo.filters.value.clients = toggleSingle(topo.filters.value.clients, node.id.slice('client:'.length))
  } else if (node.column === 'provider') {
    topo.filters.value.providers = toggleSingle(topo.filters.value.providers, node.id)
  } else if (node.id.startsWith('model:')) {
    topo.filters.value.models = toggleSingle(topo.filters.value.models, node.id.slice('model:'.length))
  }
}

/** 单值切换：已收敛到该值 → 清空（取消）；否则收敛为该值（替换，不叠加 ——
 *  图上点的是「这一个节点」，不是「往多选里再加一项」）。 */
function toggleSingle(list: readonly string[], value: string): string[] {
  return list.length === 1 && list[0] === value ? [] : [value]
}

function onSelectRequest(requestId: string) {
  selectedRequestId.value = requestId
  highlightEdges.value = topo.edgePathFor(requestId)
  selectedNodeId.value = null
}

/**
 * 点面板行 = 「弹出这条请求的摘要」（两步交互的第一步，owner 2026-10-27 口径）。
 *
 * 2026-10-23 曾把点行改成直接开全页详情（跳过摘要）；属主实测后要求改回
 * 旧版两步流：点行 → 内联摘要浮层（本页不跳转、不开新窗，拓扑图仍在视野）；
 * 浮层里的「查看全景」（onOpenPage）才开新窗展示整个请求详情。
 *
 * 2026-10-28：浮层归属列 = 点击的那一列（side 由模板按面板传死，不进面板
 * 组件 —— 面板不知道自己在屏幕哪边，知道的是布局）。
 */
function onZoomRequest(entry: PanelEntry, side: 'left' | 'right') {
  zoomEntry.value = entry
  zoomSide.value = side
  onSelectRequest(entry.requestId)
}

/** 「查看全景」（浮层按钮 / 面板行 ↗）：全页详情开新窗。
 *  与站内其余入口（RequestLogsView / DashboardViewV2 / DispatchWaterfallView …）
 *  共用同一个 openRequestDetailPage。 */
function onOpenPage(entry: PanelEntry) {
  if (!entry.requestId) return
  openRequestDetailPage(entry.requestId, undefined, router)
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

/** 摘要浮层里的路由决策时间线（与左侧 timeline 同一份数据源）。 */
const zoomDecisions = computed(() => (zoomEntry.value ? topo.decisionsFor(zoomEntry.value.requestId) : []))

/** 头部统计面板跟随客户端/供应商/模型过滤（多选时取第一项，其余靠过滤后收敛）。 */
const activeClient = computed(() => topo.filters.value.clients[0] ?? '')
const activeProvider = computed(() => topo.filters.value.providers[0] ?? '')
const activeModel = computed(() => topo.filters.value.models[0] ?? '')

function onSelectClient(key: string) {
  const list = topo.filters.value.clients
  const next = list.includes(key) ? list.filter((c) => c !== key) : [...list, key]
  topo.filters.value.clients = next
}

function onSelectProvider(code: string) {
  const list = topo.filters.value.providers
  const next = list.includes(code) ? list.filter((c) => c !== code) : [...list, code]
  topo.filters.value.providers = next
}

function onSelectModel(model: string) {
  const list = topo.filters.value.models
  const next = list.includes(model) ? list.filter((m) => m !== model) : [...list, model]
  topo.filters.value.models = next
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

        <!-- 两个数据开关（owner 2026-10-18：默认不勾 = 默认去除）。 -->
        <label class="live-topology__toggle" data-testid="toggle-probe">
          <input v-model="topo.filters.value.showProbe" type="checkbox" />
          <span>{{ t('liveTopology.filter.showProbe') }}</span>
        </label>
        <label class="live-topology__toggle" data-testid="toggle-invalid">
          <input v-model="topo.filters.value.showInvalidNodes" type="checkbox" />
          <span>{{ t('liveTopology.filter.showInvalid') }}</span>
        </label>

        <button v-if="topo.activeFilterCount.value" type="button" class="live-topology__clear" @click="clearFilters">
          {{ t('liveTopology.filter.clear') }}
        </button>
      </div>
    </header>

    <!-- 头部区间统计面板（2026-10-28 owner 口径：所有统计放页面上部）：
         区间 5分钟/1小时/当天 + 总量行常驻 + 供应商/模型明细可折叠，
         点行联动图过滤（与图、两栏同一过滤子集）。 -->
    <StatsPanel
      class="live-topology__statspanel"
      :active-client="activeClient"
      :active-provider="activeProvider"
      :active-model="activeModel"
      :in-flight="topo.coreMetric.value?.inFlight ?? null"
      :queue-depth="queueDepth"
      :data-revision="liveDataRevision"
      @select-client="onSelectClient"
      @select-provider="onSelectProvider"
      @select-model="onSelectModel"
    />

    <p v-if="connection !== 'open'" class="live-topology__banner">
      {{ t('liveTopology.banner.connecting') }}
    </p>
    <p v-else-if="!initialDataReceived" class="live-topology__banner">
      {{ t('liveTopology.banner.waitingFirstFrame') }}
    </p>
    <p v-else-if="isEmptyByFilter" class="live-topology__banner">
      {{ t('liveTopology.banner.filteredEmpty') }}
    </p>
    <!-- 窗口丢弃必须说出口：否则图空了用户分不清是「真没流量」还是
         「有流量但落在窗口外」——这两种情况该采取的动作完全不同。 -->
    <p v-if="topo.droppedByWindow.value" class="live-topology__banner live-topology__banner--muted">
      {{ t('liveTopology.banner.droppedByWindow', { n: topo.droppedByWindow.value }) }}
    </p>
    <!-- 数据开关隐藏了多少条也必须说出口（同上一条的理由：图上比预期少
         需要能被解释，否则「被开关藏了」会被读成「丢数据」）。 -->
    <p
      v-if="topo.hiddenProbeCount.value || topo.hiddenInvalidCount.value"
      class="live-topology__banner live-topology__banner--muted"
      data-testid="hidden-counts-banner"
    >
      <template v-if="topo.hiddenProbeCount.value">{{ t('liveTopology.banner.hiddenProbe', { n: topo.hiddenProbeCount.value }) }}</template>
      <template v-if="topo.hiddenProbeCount.value && topo.hiddenInvalidCount.value"> · </template>
      <template v-if="topo.hiddenInvalidCount.value">{{ t('liveTopology.banner.hiddenInvalid', { n: topo.hiddenInvalidCount.value }) }}</template>
    </p>

    <div class="live-topology__body">
      <div class="live-topology__left">
        <ClientRequestPanel
          class="live-topology__clients"
          :requests="topo.clientRequests.value"
          :active-request-id="selectedRequestId ?? undefined"
          @select-request="onSelectRequest"
          @zoom="(entry) => onZoomRequest(entry, 'left')"
          @open-page="onOpenPage"
        />
        <!-- 内联摘要浮层（两步交互第一步）：点左栏请求时弹在本列，
             靠 .live-topology__left 的 position:relative 当定位基准。 -->
        <RequestZoomPanel
          v-if="zoomEntry && zoomSide === 'left'"
          side="left"
          :entry="zoomEntry"
          :decisions="zoomDecisions"
          @close="zoomEntry = null"
          @open-page="onOpenPage"
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
          :plugins="topo.plugins.value"
          :queue-depth="queueDepth"
          :selection="topo.selectionStats.value"
          :direct-measured="topo.localProviderCodes.value !== undefined"
          :local-provider-codes="topo.localProviderCodes.value"
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
          @zoom="(entry) => onZoomRequest(entry, 'right')"
          @open-page="onOpenPage"
        />
        <!-- 内联摘要浮层（两步交互第一步）：点右栏请求时弹在本列，absolute
             定位靠本列的 position:relative 当定位基准（丢了基准会退到视口盖满整屏）。 -->
        <RequestZoomPanel
          v-if="zoomEntry && zoomSide === 'right'"
          :entry="zoomEntry"
          :decisions="zoomDecisions"
          @close="zoomEntry = null"
          @open-page="onOpenPage"
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
  /* 窄屏（<1024）：高度自然增长、由 .main-body 整页滚动 —— 钉死 100% 会把
     内容压进视口（栅格行塌成等分三段、列表 0 高，2026-10-28 实测）。
     桌面（≥1024）：100% 铺满主区，配合栅格行 minmax(min(320px,50vh),1fr)
     收进视口（下限的由来见 .live-topology__body 的「行下限」注释）。 */
  height: auto;
  min-height: 0;
  padding: 8px 12px 12px;
}

.live-topology__bar {
  display: flex;
  align-items: center;
  gap: 8px;
  /* 标题/连接态一行，过滤栏独占第二行且自身永不换行 —— owner 要求
     「筛选条件放在一行内」。浏览器实测 1600 宽下过滤控件需 ~1110px，
     挤在标题同一行只剩 ~1010px，右端必被裁。 */
  flex-wrap: wrap;
}

.live-topology__title {
  margin: 0;
  font-size: 16px;
  color: var(--kx-text);
}

.live-topology__conn {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
}

/* 心跳圆点（2026-10-28 live 页动效轮）：「已连接」不是一句静态文案，
   是一条活着的 SSE 连接 —— 呼吸的圆点让「在线」有生命体征。
   connecting 呼吸更快（正在重连）；down 不呼吸（常亮红 = 故障要醒目，
   动起来反而像「正在恢复」）。 */
.live-topology__conn::before {
  content: '';
  flex: none;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.live-topology__conn.is-open::before {
  animation: conn-breathe 2.2s ease-in-out infinite;
}

.live-topology__conn.is-connecting::before {
  animation: conn-breathe 1.1s ease-in-out infinite;
}

@keyframes conn-breathe {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
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

@media (prefers-reduced-motion: reduce) {
  .live-topology__conn.is-open::before,
  .live-topology__conn.is-connecting::before {
    animation: none;
  }
}

.live-topology__redis {
  font-size: 11px;
  color: var(--kx-warning);
}

.live-topology__filters {
  display: flex;
  align-items: center;
  gap: 4px;
  /* 独占一行（width:100%）、永不换行；极窄兜底横向滚动（丢滚动好过丢控件）。
     owner 2026-10-18：筛选条件一行放下，字号更小且各控件大小一致。 */
  width: 100%;
  flex-wrap: nowrap;
  overflow-x: auto;
  scrollbar-width: thin;
  font-size: 11px;
}

.live-topology__select {
  width: 132px;
  /* el-select 内部字号统一为 11px，与旁边的原生 select/按钮一致。 */
  --el-font-size-base: 11px;
  flex: none;
}

.live-topology__filters select {
  max-width: 120px;
  padding: 0 4px;
  height: 24px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  font-size: 11px;
  flex: none;
}

/* 数据开关：与其它控件同高同字号（checkbox + label，一行内不凸起）。 */
.live-topology__toggle {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  height: 24px;
  padding: 0 4px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  font-size: 11px;
  white-space: nowrap;
  cursor: pointer;
  user-select: none;
  flex: none;
}

.live-topology__toggle input {
  margin: 0;
  width: 12px;
  height: 12px;
  accent-color: var(--kx-primary);
}

.live-topology__clear {
  height: 24px;
  padding: 0 8px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: transparent;
  color: var(--kx-muted);
  font-size: 11px;
  cursor: pointer;
  flex: none;
}

/* 头部区间统计面板：不参与伸缩（高度=内容），把剩余高度全部留给 body。 */
.live-topology__statspanel {
  flex: none;
}

.live-topology__banner {
  margin: 0;
  padding: 4px 8px;
  border-radius: 8px;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-warning);
  background: var(--kx-warning-soft);
}

/* 窗口丢弃提示是「解释」不是「告警」：用中性色，别和真正的故障横幅抢眼。 */
.live-topology__banner--muted {
  color: var(--kx-muted);
  background: var(--kx-surface);
  border: 1px dashed var(--kx-border);
}

/* 默认（窄）：单列堆叠 —— 客户端请求 / 拓扑 / 供应商响应。
   ≥1024：左栏 + 拓扑两列，右侧面板落到下一行；
   ≥1440：左面板 / 拓扑 / 右面板三列并排。
   断点只允许用 scripts/responsive-audit.mjs 的白名单值（480/640/768/1024/1440）。
   ★ 行高（2026-10-28 浮层归属轮）：桌面 grid 行必须收进视口（minmax(0,1fr)），
   否则行高被 50 行/页的明细内容撑到两三千像素 —— 浮层锚在「整列底部」
   ⇒ 点行后摘要弹在视口外一千多像素处，用户根本看不见（本轮实测抓到）。
   窄屏两个请求列表各自限高 + 列表内滚动：不限高时整页 4623px（390 宽实测），
   拓扑与右栏被推到「永远划不到」的地方，手机上没法观察请求。
   ★★ 行下限（2026-10-31 浮层塌缩轮）：纯 minmax(0,1fr) 有另一个失败模式 ——
   当头部统计面板足够高（1280×713 实测吃掉 ~450px）而 .live-topology 高度
   钉死 100% 时，body 只剩 90px，第 1 行被压到 **0px**：拓扑画布整块不可见、
   摘要浮层的 max-height: calc(100% - 24px) 以 0 高列为基准 ⇒ 塌成 16px 一条缝
   ⇒ 点行「看不见摘要」（线上实测读数，详见本轮审计档）。
   ⇒ 第 1 行加下限 min(320px, 50vh)（与 .live-topology__clients 的既有
   min-height: 320px 对齐）：空间充足时行为与旧版完全一致（1fr 吃满剩余），
   空间不足时行保住 320px、栅格内容溢出由 .main-body 滚动兜底 ——
   宁可滚，不可塌。 */
.live-topology__body {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 8px;
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
  /* RequestZoomPanel 是 position:absolute 的内联摘要浮层。没有这行，
     它的 containing block 会退到视口，右下角那块 460px 面板会盖住拓扑区
     和左栏 —— 窄屏下几乎占满整屏。必须由本列当定位基准（stage 列里的
     TopologyCanvas 覆盖层同样依赖它）。 */
  position: relative;
}

/* 窄屏（手机/平板）：两个请求列表限高，列表体内滚动（.cp__list /
   .pp__list 本来就是 overflow-y:auto，缺的只是容器高度约束）。 */
.live-topology__clients {
  max-height: 70vh;
}

.live-topology__right {
  max-height: 70vh;
}

.live-topology__clients {
  flex: 1;
  min-height: 0;
}

.live-topology__stage {
  flex: 1;
}

@media (min-width: 1024px) {
  .live-topology {
    height: 100%;
  }

  .live-topology__body {
    grid-template-columns: 260px minmax(0, 1fr);
    /* 行收进视口：第一行（左栏+拓扑）吃满剩余高度，第二行（右栏横排）
      由内容自定（max-height 240px 封顶）。
      ★ 下限 min(320px, 50vh) 见上方「行下限」注释：统计面板挤占空间时
      第 1 行不许塌 0（塌 0 ⇒ 拓扑不可见 + 摘要浮层缩成一条缝）。
      ★★ 第 2 行同样要下限（2026-10-31 部署后原生复测补的）：面板本体是
      flex 列 + flex:1/min-height:0 的子面板 ⇒ 其 max-content 贡献 ≈ 0，
      auto 行在空间不足时整行塌 0 ⇒ 右栏摘要浮层照样缩成 16px 一条缝
      （1280×713 部署后实测：左 296px / 右 16px）。下限取 min(240px,40vh)
      与该行 max-height: 240px 封顶对齐。 */
    grid-template-rows: minmax(min(320px, 50vh), 1fr) minmax(min(240px, 40vh), auto);
  }

  .live-topology__right {
    grid-column: 1 / -1;
    max-height: 240px;
  }

  .live-topology__clients {
    /* ★★★ **2026-10-31 12h 审计轮修（★ 原值 `min-height: 320px` 是硬钉）**：★★★
     * ★ 原值 320px **大于**所在栅格行的下限 `min(320px, 50vh)`（★ 50vh < 320px
     * ★ 的矮窗下，★ 视口高 < 640px 时行下限 = 50vh < 320px）⇒ ⇒ ⇒ ★★ **项比行高**
     * ★★ ⇒ ⇒ 溢出 ⇒ ⇒ ⇒ **压到第 2 行（右栏）**⇒ ⇒ ⇒ ★★ ⇒ 而那正是本轮要修的
     * ★★ 「右栏摘要浮层被压成 16px 一条缝」**同一种形态** ⇒ ⇒ ⇒ ★★ ⇒
     * ★★ **也就是说：行下限被它自己项上的硬钉抵消了** ⇒ ⇒ ⇒ ★★ ⇒
     * ★★ 修法：★ 项的 min-height 与**行下限同口径** ⇒ ⇒ ★ `min(320px, 50vh)`
     * ★★ ⇒ ⇒ 项永不超过行 ⇒ ⇒ ⇒ 跨行压盖消失 ⇒ ⇒ ⇒ ★★ 且行仍不会塌 0
     * ★★ （★ 50vh > 0 ⇒ ⇒ 矮窗也留得住）⇒ ⇒ ⇒ ★★ ⇒ 修复的意图一条没丢。
     * ★★ （窄屏 < 1024px 不受本规则影响 ⇒ ⇒ 那里由 65vh 帽子管。） */
    min-height: min(320px, 50vh);
    /* 桌面行高已收进视口，限高交给栅格行；65vh 帽子只在窄屏起作用。 */
    max-height: none;
  }
}

@media (min-width: 1440px) {
  .live-topology__body {
    grid-template-columns: 280px minmax(0, 1fr) 300px;
    /* 三列单行：行高 = 本体高度 ⇒ 三列都在视口内，浮层锚列底必然可见。
       ★ 同样带 320px 下限：矮窗（如 1440×768）下统计面板挤占后剩余空间
       若小于下限，行保住 320px、交给 .main-body 滚动兜底。 */
    grid-template-rows: minmax(min(320px, 50vh), 1fr);
  }

  .live-topology__right {
    grid-column: auto;
    max-height: none;
  }
}
</style>
