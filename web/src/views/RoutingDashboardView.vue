<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import {
  getAutoRouteIndex, getAutoRouteDecisions, getAutoRouteAudit,
  getCustomerCost, getModelCost, refreshAutoRouteIndex, simulateAutoRoute,
  DEFAULT_PROFILE_WEIGHTS, TASK_TYPES, TASK_TAGS,
  type AutoRouteIndexEntry, type AutoRouteDecision, type AutoRouteAudit,
  type CustomerCostRow, type ModelCostRow, type ProfileWeights,
} from '../api-autoroute'
import {
  getPolicy, patchPolicy, getScoringWeights, updateScoringWeights,
  resolveRouting,
  type RoutingPolicy, type ScoringWeights, type RoutingResolveResponse,
} from '../api'
import SixDimScoreBar from '../components/SixDimScoreBar.vue'

const activeTab = ref<'overview' | 'policy' | 'live'>('overview')

// ── Overview ──────────────────────────────────────────
const indexData = ref<AutoRouteIndexEntry[]>([])
const selectedTask = ref<string>('')
const selectedProfile = ref<'smart' | 'speed_first' | 'cost_first'>('smart')
const expandedModel = ref<string>('')
const indexLoading = ref(false)
const layer2Cache = ref<Record<string, RoutingResolveResponse | null>>({})
const layer2Loading = ref<string>('')

const audit = ref<AutoRouteAudit>({
  total_auto_requests: 0, success_rate: 0,
  task_distribution: {}, profile_distribution: {}, top_chosen_models: [],
})

const profileScoreKey = computed(() => {
  if (selectedProfile.value === 'speed_first') return 'score_speed_first' as const
  if (selectedProfile.value === 'cost_first') return 'score_cost_first' as const
  return 'score_smart' as const
})

const profileLabel = computed(() => {
  if (selectedProfile.value === 'speed_first') return 'Speed'
  if (selectedProfile.value === 'cost_first') return 'Cost'
  return 'Smart'
})

const sortedIndex = computed(() => {
  const key = profileScoreKey.value
  return [...indexData.value].sort((a, b) => (b[key] ?? 0) - (a[key] ?? 0))
})

async function loadIndex() {
  indexLoading.value = true
  try { indexData.value = await getAutoRouteIndex(30) }
  catch (e) { console.error('loadIndex', e) }
  finally { indexLoading.value = false }
}

async function loadLayer2(modelName: string) {
  const key = modelName
  if (layer2Cache.value[key] !== undefined) return
  layer2Loading.value = key
  try {
    layer2Cache.value[key] = await resolveRouting(modelName)
  } catch {
    layer2Cache.value[key] = null
  } finally {
    layer2Loading.value = ''
  }
}

function toggleModel(m: AutoRouteIndexEntry) {
  const id = m.credential_id + ':' + m.raw_model
  if (expandedModel.value === id) {
    expandedModel.value = ''
    return
  }
  expandedModel.value = id
  loadLayer2(m.canonical_name || m.raw_model)
}

function scoreOf(m: AutoRouteIndexEntry): number {
  return m[profileScoreKey.value] ?? 0
}

// ── Policy ────────────────────────────────────────────
const policy = ref<RoutingPolicy | null>(null)
const policyDraft = ref<Partial<RoutingPolicy>>({})
const weights = ref<ScoringWeights>({ price: 10, session_load: 5, failure_penalty: 20, default_price_cny: 5, default_price_usd: 5 })
const weightsDraft = ref<ScoringWeights>({ ...weights.value })
const savingPolicy = ref(false)
const policyMsg = ref('')

const POLICY_FIELDS: { key: keyof RoutingPolicy; label: string; min?: number; max?: number; step?: number }[] = [
  { key: 'algorithm_version',         label: '算法版本', min: 1, max: 2, step: 1 },
  { key: 'retry_per_credential',      label: '同凭据重试', min: 0, max: 5, step: 1 },
  { key: 'tier_fallback_max',         label: '跨级回退', min: 1, max: 20, step: 1 },
  { key: 'circuit_open_seconds',      label: '熔断冷却(s)', min: 1, max: 3600, step: 1 },
  { key: 'circuit_failure_threshold', label: '熔断失败次数', min: 1, max: 50, step: 1 },
  { key: 'circuit_max_open_seconds',  label: '熔断上限(s)', min: 1, max: 86400, step: 1 },
]

async function loadPolicy() {
  try {
    policy.value = await getPolicy()
    policyDraft.value = { ...policy.value }
    weights.value = await getScoringWeights()
    weightsDraft.value = { ...weights.value }
  } catch (e) { console.error('loadPolicy', e) }
}

async function savePolicy() {
  savingPolicy.value = true
  policyMsg.value = ''
  try {
    const dirty: Partial<RoutingPolicy> = {}
    for (const f of POLICY_FIELDS) {
      if (policyDraft.value[f.key] !== policy.value![f.key]) dirty[f.key] = policyDraft.value[f.key]
    }
    if (Object.keys(dirty).length > 0) {
      policy.value = await patchPolicy(dirty)
      policyDraft.value = { ...policy.value }
    }
    policyMsg.value = '策略已保存'
  } catch (e) { policyMsg.value = '保存失败: ' + String(e) }
  finally { savingPolicy.value = false }
}

async function saveWeights() {
  savingPolicy.value = true
  policyMsg.value = ''
  try {
    await updateScoringWeights(weightsDraft.value)
    weights.value = { ...weightsDraft.value }
    policyMsg.value = '权重已保存'
  } catch (e) { policyMsg.value = '保存失败: ' + String(e) }
  finally { savingPolicy.value = false }
}

const customerCost = ref<CustomerCostRow[]>([])
const modelCost = ref<ModelCostRow[]>([])

async function loadCosts() {
  try {
    customerCost.value = await getCustomerCost(10)
    modelCost.value = await getModelCost(10)
  } catch (e) { console.error('loadCosts', e) }
}

// ── Live ──────────────────────────────────────────────
const decisions = ref<AutoRouteDecision[]>([])
const expandedDecision = ref<string>('')
const autoRefresh = ref(true)
let pollTimer: ReturnType<typeof setInterval> | null = null

const simPrompt = ref('用 Python 写一个快速排序')
const simProfile = ref('smart')
const simResult = ref<{ status: number; decision?: Record<string, unknown>; error?: string } | null>(null)
const simLoading = ref(false)

async function loadAudit() {
  try { audit.value = await getAutoRouteAudit() } catch (e) { console.error('loadAudit', e) }
}
async function loadDecisions() {
  try { decisions.value = await getAutoRouteDecisions(15) } catch (e) { console.error('loadDecisions', e) }
}

async function runSim() {
  simLoading.value = true
  simResult.value = null
  try { simResult.value = await simulateAutoRoute(simPrompt.value, simProfile.value) }
  finally { simLoading.value = false }
}

function startPoll() {
  stopPoll()
  pollTimer = setInterval(() => { loadAudit(); loadDecisions() }, 5000)
}
function stopPoll() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}

watch(autoRefresh, (v) => { v ? startPoll() : stopPoll() })
watch(activeTab, (tab) => {
  if (tab === 'live') { loadAudit(); loadDecisions(); if (autoRefresh.value) startPoll() }
  else stopPoll()
  if (tab === 'policy') { loadPolicy(); loadCosts() }
})

// ── Helpers ───────────────────────────────────────────
function fmt(n: number | undefined, digits = 2): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  return n.toFixed(digits)
}
function fmtMs(ms: number | undefined): string {
  if (!ms || ms <= 0) return '-'
  return ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(1)}s`
}
function fmtCost(n: number | undefined): string {
  if (!n || n <= 0) return '$0'
  return '$' + n.toFixed(4)
}
function maxDimValue(w: ProfileWeights): number {
  return Math.max(w.Price, w.Speed, w.Stability, w.Match, w.Pressure, w.ContextFit, 1)
}
function distEntries(d: Record<string, number>): Array<[string, number]> {
  return Object.entries(d).sort((a, b) => b[1] - a[1])
}
function distMax(d: Record<string, number>): number {
  return Math.max(...Object.values(d), 1)
}
function taskLabel(key: string): string {
  return TASK_TYPES.find(t => t.key === key)?.label ?? key
}

onMounted(async () => {
  await loadIndex()
  await loadAudit()
})
onUnmounted(() => stopPoll())
</script>

<template>
  <div class="routing-dashboard">
    <!-- Header -->
    <div class="page-header compact">
      <div>
        <h2>路由全景</h2>
        <p class="subtitle">model=auto 两层路由：L1 任务分类选模型 → L2 凭据调度选上游</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-sm btn-ghost" @click="loadIndex(); loadAudit()" title="刷新">↻</button>
      </div>
    </div>

    <!-- Routing flow -->
    <div class="flow-bar">
      <div class="flow-step l1">
        <span class="flow-badge">L1</span>
        <span class="flow-text">Prompt → 分类器(8类) → 6维评分 → 选模型</span>
      </div>
      <span class="flow-arrow">→</span>
      <div class="flow-step l2">
        <span class="flow-badge">L2</span>
        <span class="flow-text">模型解析 → Tier回退 → P2C → 凭据执行</span>
      </div>
    </div>

    <!-- Tabs -->
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'overview' }" @click="activeTab = 'overview'">两层路由</button>
      <button class="tab-btn" :class="{ active: activeTab === 'policy' }" @click="activeTab = 'policy'">策略与数据</button>
      <button class="tab-btn" :class="{ active: activeTab === 'live' }" @click="activeTab = 'live'">实时决策</button>
    </div>

    <!-- ═══ Tab A: Overview ═══ -->
    <div v-if="activeTab === 'overview'" class="tab-content">
      <!-- Inline stats -->
      <div class="stat-inline">
        <span class="chip">候选 <strong>{{ indexData.length }}</strong></span>
        <span class="chip">24h Auto <strong>{{ audit.total_auto_requests }}</strong></span>
        <span class="chip">成功率 <strong>{{ fmt(audit.success_rate * 100, 1) }}%</strong></span>
        <span class="chip">Top <strong>{{ audit.top_chosen_models[0]?.model || '-' }}</strong></span>
      </div>

      <!-- L1: Task + Profile -->
      <div class="card compact-card">
        <div class="section-head">
          <span class="layer-tag l1">L1</span>
          <h3>任务分类 → 模型选择</h3>
        </div>
        <div class="task-pills">
          <button
            v-for="t in TASK_TYPES"
            :key="t.key"
            class="task-pill"
            :class="{ active: selectedTask === t.key }"
            :title="TASK_TAGS[t.key]?.join(', ') || ''"
            @click="selectedTask = selectedTask === t.key ? '' : t.key"
          >
            {{ t.icon }} {{ t.label }}
          </button>
        </div>
        <div class="profile-pills">
          <span class="pill-label">Profile:</span>
          <button class="profile-pill" :class="{ active: selectedProfile === 'smart' }" @click="selectedProfile = 'smart'">智能</button>
          <button class="profile-pill" :class="{ active: selectedProfile === 'speed_first' }" @click="selectedProfile = 'speed_first'">速度</button>
          <button class="profile-pill" :class="{ active: selectedProfile === 'cost_first' }" @click="selectedProfile = 'cost_first'">成本</button>
        </div>
      </div>

      <!-- Model ranking table -->
      <div v-if="indexData.length > 0" class="card compact-card">
        <div class="section-head">
          <span class="layer-tag l1">L1</span>
          <h3>模型推荐 Top-{{ Math.min(sortedIndex.length, 10) }}
            <span v-if="selectedTask" class="task-hint">· {{ taskLabel(selectedTask) }}</span>
          </h3>
        </div>
        <div class="table-wrap">
          <table class="dense-table">
            <thead>
              <tr>
                <th>#</th><th>模型</th><th>{{ profileLabel }}</th><th>P95</th><th>成功率</th><th>价格/1M</th><th>压力</th><th></th>
              </tr>
            </thead>
            <tbody>
              <template v-for="(m, i) in sortedIndex.slice(0, 10)" :key="m.credential_id + ':' + m.raw_model">
                <tr
                  class="model-row"
                  :class="{ expanded: expandedModel === m.credential_id + ':' + m.raw_model }"
                  @click="toggleModel(m)"
                >
                  <td class="num">{{ i + 1 }}</td>
                  <td>
                    <div class="model-name">{{ m.canonical_name || m.raw_model }}</div>
                    <div class="model-sub">{{ m.raw_model }}</div>
                  </td>
                  <td>
                    <span class="score-pill" :class="scoreOf(m) >= 70 ? 'good' : ''">{{ fmt(scoreOf(m), 1) }}</span>
                  </td>
                  <td>{{ fmtMs(m.p95_latency_ms) }}</td>
                  <td>{{ fmt((m.success_rate ?? 0) * 100, 0) }}%</td>
                  <td class="price-cell">{{ fmt(m.unit_price_in_per_1m, 2) }}/{{ fmt(m.unit_price_out_per_1m, 2) }}</td>
                  <td>
                    <span v-if="m.pressure_ratio" :class="m.pressure_ratio > 0.7 ? 'text-danger' : 'text-muted'">
                      {{ fmt(m.pressure_ratio * 100, 0) }}%
                    </span>
                    <span v-else class="text-muted">-</span>
                  </td>
                  <td class="expand-icon">{{ expandedModel === m.credential_id + ':' + m.raw_model ? '▼' : '▶' }}</td>
                </tr>
                <!-- L2 detail -->
                <tr v-if="expandedModel === m.credential_id + ':' + m.raw_model" class="detail-row">
                  <td colspan="8">
                    <div class="layer2-panel">
                      <div class="section-head sm">
                        <span class="layer-tag l2">L2</span>
                        <span>凭据调度 · {{ m.canonical_name || m.raw_model }}</span>
                      </div>
                      <div v-if="layer2Loading === (m.canonical_name || m.raw_model)" class="text-muted">加载凭据计划…</div>
                      <template v-else-if="layer2Cache[m.canonical_name || m.raw_model]">
                        <div class="l2-meta">
                          <span>解析: {{ layer2Cache[m.canonical_name || m.raw_model]!.resolution_path }}</span>
                          <span v-if="layer2Cache[m.canonical_name || m.raw_model]!.plan_order.length">
                            执行链:
                            <template v-for="(p, pi) in layer2Cache[m.canonical_name || m.raw_model]!.plan_order.slice(0, 5)" :key="p.credential_id">
                              {{ pi > 0 ? ' → ' : '' }}#{{ p.credential_id }}(T{{ p.tier }})
                            </template>
                          </span>
                        </div>
                        <div class="l2-creds">
                          <div
                            v-for="(c, ci) in layer2Cache[m.canonical_name || m.raw_model]!.candidates.filter(x => x.routable).slice(0, 4)"
                            :key="c.credential_id"
                            class="l2-cred"
                          >
                            <span class="l2-rank">#{{ ci + 1 }}</span>
                            <span class="l2-prov">{{ c.provider_name }}</span>
                            <span class="badge badge-gray">T{{ c.tier }} w{{ c.weight }}</span>
                            <span class="text-muted">#{{ c.credential_id }}</span>
                            <span v-if="c.composite_score != null" class="score-pill sm">{{ c.composite_score.toFixed(1) }}</span>
                          </div>
                        </div>
                      </template>
                      <div v-else class="text-muted">无 L2 凭据数据</div>
                      <div class="l1-scores">
                        <SixDimScoreBar compact :scores="{
                          price_score: m.score_smart,
                          speed_score: m.score_speed_first,
                          stability_score: (m.success_rate ?? 0.9) * 100,
                          match_score: 50,
                          pressure_score: (1 - (m.pressure_ratio ?? 0)) * 100,
                          context_fit: m.context_window ? Math.min(100, m.context_window / 4096) : 50,
                        }" />
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
        </div>
      </div>
      <div v-else-if="!indexLoading" class="card compact-card empty-hint">
        索引暂无数据 — 等待 5 分钟自动刷新或点击 ↻
      </div>
    </div>

    <!-- ═══ Tab B: Policy ═══ -->
    <div v-if="activeTab === 'policy'" class="tab-content">
      <!-- Profile weights -->
      <div class="card compact-card">
        <div class="section-head"><h3>Profile 权重矩阵</h3></div>
        <div class="profile-matrix compact">
          <div v-for="(w, name) in DEFAULT_PROFILE_WEIGHTS" :key="name" class="profile-row">
            <span class="profile-name">{{ name === 'smart' ? '智能' : name === 'speed_first' ? '速度' : '成本' }}</span>
            <SixDimScoreBar compact :scores="{
              price_score: (w as ProfileWeights).Price / maxDimValue(w as ProfileWeights) * 100,
              speed_score: (w as ProfileWeights).Speed / maxDimValue(w as ProfileWeights) * 100,
              stability_score: (w as ProfileWeights).Stability / maxDimValue(w as ProfileWeights) * 100,
              match_score: (w as ProfileWeights).Match / maxDimValue(w as ProfileWeights) * 100,
              pressure_score: (w as ProfileWeights).Pressure / maxDimValue(w as ProfileWeights) * 100,
              context_fit: (w as ProfileWeights).ContextFit / maxDimValue(w as ProfileWeights) * 100,
            }" />
          </div>
        </div>
      </div>

      <!-- Policy + Weights side by side -->
      <div class="policy-grid">
        <div class="card compact-card">
          <div class="section-head"><h3>L2 路由算法</h3></div>
          <div class="policy-fields">
            <div v-for="f in POLICY_FIELDS" :key="f.key" class="form-group">
              <label>{{ f.label }}</label>
              <input type="number" :min="f.min" :max="f.max" :step="f.step" v-model.number="policyDraft[f.key]" />
            </div>
          </div>
          <button class="btn btn-primary btn-sm" :disabled="savingPolicy" @click="savePolicy">保存</button>
        </div>
        <div class="card compact-card">
          <div class="section-head"><h3>综合得分系数</h3></div>
          <div class="policy-fields">
            <div class="form-group">
              <label>价格权重</label>
              <input type="number" min="0" max="100" v-model.number="weightsDraft.price" />
            </div>
            <div class="form-group">
              <label>会话负载</label>
              <input type="number" min="0" max="100" v-model.number="weightsDraft.session_load" />
            </div>
            <div class="form-group">
              <label>错误惩罚</label>
              <input type="number" min="0" max="100" v-model.number="weightsDraft.failure_penalty" />
            </div>
          </div>
          <button class="btn btn-primary btn-sm" :disabled="savingPolicy" @click="saveWeights">保存权重</button>
        </div>
      </div>
      <div v-if="policyMsg" class="alert alert-info compact-alert">{{ policyMsg }}</div>

      <!-- Cost tables side by side -->
      <div class="cost-grid">
        <div class="card compact-card">
          <div class="section-head"><h3>客户成本</h3></div>
          <table v-if="customerCost.length" class="dense-table">
            <thead><tr><th>Key</th><th>24h</th><th>7d</th><th>Auto</th></tr></thead>
            <tbody>
              <tr v-for="c in customerCost" :key="c.api_key_id">
                <td>#{{ c.api_key_id }}</td>
                <td>{{ fmtCost(c.cost_usd_24h) }}</td>
                <td>{{ fmtCost(c.cost_usd_7d) }}</td>
                <td>{{ c.total_auto_requests || 0 }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无</div>
        </div>
        <div class="card compact-card">
          <div class="section-head"><h3>模型成本</h3></div>
          <table v-if="modelCost.length" class="dense-table">
            <thead><tr><th>模型</th><th>费用</th><th>成功率</th><th>请求</th></tr></thead>
            <tbody>
              <tr v-for="m in modelCost" :key="m.raw_model">
                <td class="model-name">{{ m.raw_model }}</td>
                <td>{{ fmtCost(m.total_cost_usd) }}</td>
                <td>{{ fmt((m.success_rate ?? 0) * 100, 0) }}%</td>
                <td>{{ m.total_requests || 0 }}</td>
              </tr>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无</div>
        </div>
      </div>
    </div>

    <!-- ═══ Tab C: Live ═══ -->
    <div v-if="activeTab === 'live'" class="tab-content">
      <div class="stat-inline">
        <span class="chip">Auto <strong>{{ audit.total_auto_requests }}</strong></span>
        <span class="chip">成功率 <strong>{{ fmt(audit.success_rate * 100, 1) }}%</strong></span>
        <span class="chip">Top Task <strong>{{ distEntries(audit.task_distribution)[0]?.[0] || '-' }}</strong></span>
        <span class="chip">Top Model <strong>{{ audit.top_chosen_models[0]?.model || '-' }}</strong></span>
      </div>

      <!-- Simulator (top for quick test) -->
      <div class="card compact-card">
        <div class="section-head"><h3>路由模拟器</h3></div>
        <div class="sim-row">
          <input v-model="simPrompt" placeholder="输入 prompt…" class="sim-input" />
          <select v-model="simProfile" class="sim-select">
            <option value="smart">智能</option>
            <option value="speed_first">速度</option>
            <option value="cost_first">成本</option>
          </select>
          <button class="btn btn-primary btn-sm" :disabled="simLoading" @click="runSim">{{ simLoading ? '…' : '发送' }}</button>
        </div>
        <div v-if="simResult" class="sim-out">
          <div v-if="simResult.error" class="alert alert-danger compact-alert">{{ simResult.error }}</div>
          <div v-else-if="simResult.decision" class="sim-steps">
            <span class="sim-chip">L1 {{ (simResult.decision as Record<string, unknown>).task_type }}</span>
            <span class="sim-chip">{{ fmt(Number((simResult.decision as Record<string, unknown>).confidence) * 100, 0) }}%</span>
            <span class="sim-chip win">→ {{ (simResult.decision as Record<string, unknown>).chosen_model }}</span>
            <span class="text-muted">cred #{{ (simResult.decision as Record<string, unknown>).chosen_credential_id }}</span>
          </div>
        </div>
      </div>

      <!-- Distributions -->
      <div class="card compact-card">
        <div class="dist-grid compact">
          <div>
            <h4>任务分布</h4>
            <div v-for="[task, count] in distEntries(audit.task_distribution)" :key="task" class="dist-row">
              <span class="dist-label">{{ task }}</span>
              <div class="dist-bar-bg"><div class="dist-bar-fill" :style="{ width: (count / distMax(audit.task_distribution) * 100) + '%' }" /></div>
              <span class="dist-count">{{ count }}</span>
            </div>
          </div>
          <div>
            <h4>Profile</h4>
            <div v-for="[p, count] in distEntries(audit.profile_distribution)" :key="p" class="dist-row">
              <span class="dist-label">{{ p }}</span>
              <div class="dist-bar-bg"><div class="dist-bar-fill accent" :style="{ width: (count / distMax(audit.profile_distribution) * 100) + '%' }" /></div>
              <span class="dist-count">{{ count }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Decisions -->
      <div class="card compact-card">
        <div class="section-head">
          <h3>最近决策</h3>
          <label class="auto-toggle"><input type="checkbox" v-model="autoRefresh" /> 5s</label>
        </div>
        <div class="table-wrap">
          <table v-if="decisions.length" class="dense-table">
            <thead><tr><th>时间</th><th>任务</th><th>Profile</th><th>模型</th><th>置信</th><th>状态</th><th></th></tr></thead>
            <tbody>
              <template v-for="d in decisions" :key="d.request_id">
                <tr class="model-row" @click="expandedDecision = expandedDecision === d.request_id ? '' : d.request_id">
                  <td>{{ new Date(d.ts).toLocaleTimeString() }}</td>
                  <td><span class="badge badge-blue">{{ d.task_type || d.auto_decision?.task_type || '-' }}</span></td>
                  <td>{{ d.auto_profile || d.auto_decision?.profile || '-' }}</td>
                  <td class="model-name">{{ d.auto_decision?.chosen_model || d.outbound_model || '-' }}</td>
                  <td>{{ fmt((d.auto_confidence ?? d.auto_decision?.confidence ?? 0) * 100, 0) }}%</td>
                  <td><span :class="d.success ? 'badge badge-green' : 'badge badge-red'">{{ d.success ? '✓' : '✗' }}</span></td>
                  <td class="expand-icon">{{ expandedDecision === d.request_id ? '▼' : '▶' }}</td>
                </tr>
                <tr v-if="expandedDecision === d.request_id && d.auto_decision" class="detail-row">
                  <td colspan="7">
                    <div class="decision-detail">
                      <span class="text-muted">{{ d.auto_decision.classifier }} · {{ fmt((d.auto_decision.confidence ?? 0) * 100, 1) }}%</span>
                      <div v-if="d.auto_decision.candidates_top3?.length" class="candidates-compact">
                        <div v-for="(c, i) in d.auto_decision.candidates_top3" :key="i" class="cand-chip">
                          #{{ i + 1 }} {{ c.model }} <span class="score-pill sm">{{ fmt(c.composite_score, 1) }}</span>
                        </div>
                      </div>
                    </div>
                  </td>
                </tr>
              </template>
            </tbody>
          </table>
          <div v-else class="text-muted">暂无 auto 决策</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.routing-dashboard { max-width: 1100px; }

.page-header.compact { margin-bottom: 10px; }
.page-header.compact h2 { font-size: 16px; margin: 0; }
.subtitle { font-size: 11px; color: var(--muted); margin: 2px 0 0; }

/* Flow bar */
.flow-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 12px;
  font-size: 11px;
}
.flow-step { display: flex; align-items: center; gap: 6px; flex: 1; }
.flow-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px; height: 18px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 700;
  flex-shrink: 0;
}
.flow-step.l1 .flow-badge { background: rgba(99,102,241,.2); color: var(--accent-h); }
.flow-step.l2 .flow-badge { background: rgba(63,185,80,.2); color: var(--success); }
.flow-text { color: var(--muted); }
.flow-arrow { color: var(--muted); font-size: 14px; flex-shrink: 0; }

.tab-content { display: flex; flex-direction: column; gap: 10px; }

/* Inline stats */
.stat-inline { display: flex; flex-wrap: wrap; gap: 6px; }
.chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 3px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 11px;
  color: var(--muted);
}
.chip strong { color: var(--text); font-weight: 600; }

/* Cards */
.compact-card { padding: 10px 12px; }
.section-head {
  display: flex; align-items: center; gap: 8px;
  margin-bottom: 8px;
}
.section-head h3 { margin: 0; font-size: 13px; font-weight: 600; }
.section-head.sm { margin-bottom: 6px; font-size: 12px; }
.layer-tag {
  display: inline-flex; align-items: center; justify-content: center;
  width: 24px; height: 16px;
  border-radius: 3px;
  font-size: 9px; font-weight: 700;
}
.layer-tag.l1 { background: rgba(99,102,241,.2); color: var(--accent-h); }
.layer-tag.l2 { background: rgba(63,185,80,.2); color: var(--success); }
.task-hint { font-weight: 400; color: var(--muted); font-size: 11px; }

/* Task pills */
.task-pills { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 8px; }
.task-pill {
  padding: 4px 10px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 11px;
  cursor: pointer;
  transition: all .12s;
  color: var(--text);
}
.task-pill:hover { border-color: var(--accent); }
.task-pill.active {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, var(--bg-subtle));
}

.profile-pills { display: flex; align-items: center; gap: 4px; }
.pill-label { font-size: 11px; color: var(--muted); margin-right: 4px; }
.profile-pill {
  padding: 2px 8px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 99px;
  font-size: 10px;
  cursor: pointer;
  color: var(--muted);
}
.profile-pill.active {
  border-color: var(--accent);
  color: var(--accent-h);
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}

/* Dense table */
.table-wrap { overflow-x: auto; }
.dense-table { font-size: 12px; }
.dense-table thead th { padding: 4px 8px; font-size: 10px; }
.dense-table tbody td { padding: 5px 8px; }
.dense-table .num { color: var(--muted); width: 24px; }
.model-name { font-weight: 500; font-size: 12px; }
.model-sub { font-size: 10px; color: var(--muted); }
.price-cell { font-variant-numeric: tabular-nums; font-size: 11px; }
.expand-icon { color: var(--muted); width: 20px; text-align: center; font-size: 10px; }

.score-pill {
  display: inline-block;
  padding: 1px 6px;
  border-radius: 99px;
  font-size: 11px;
  font-weight: 600;
  background: rgba(139,148,158,.15);
  color: var(--muted);
}
.score-pill.good { background: rgba(63,185,80,.15); color: var(--success); }
.score-pill.sm { font-size: 10px; padding: 0 5px; }

.model-row { cursor: pointer; }
.model-row:hover { background: rgba(255,255,255,.03); }
.model-row.expanded { background: color-mix(in srgb, var(--accent) 6%, transparent); }
.detail-row td { padding: 8px; background: var(--bg-subtle); border-top: none; }

/* Layer 2 panel */
.layer2-panel { display: flex; flex-direction: column; gap: 8px; }
.l2-meta { display: flex; flex-wrap: wrap; gap: 12px; font-size: 11px; color: var(--muted); }
.l2-creds { display: flex; flex-wrap: wrap; gap: 6px; }
.l2-cred {
  display: flex; align-items: center; gap: 6px;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 11px;
}
.l2-rank { font-weight: 700; color: var(--muted); }
.l2-prov { font-weight: 500; }
.l1-scores { max-width: 360px; }

/* Policy */
.policy-grid, .cost-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 10px;
}
.policy-fields {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
  gap: 6px;
  margin-bottom: 8px;
}
.policy-fields .form-group { margin-bottom: 0; }
.policy-fields label { font-size: 10px; }
.policy-fields input { padding: 3px 6px; font-size: 12px; }

.profile-matrix.compact { gap: 6px; }
.profile-row {
  display: grid;
  grid-template-columns: 48px 1fr;
  align-items: center;
  gap: 8px;
}
.profile-name { font-size: 11px; font-weight: 500; }

.compact-alert { padding: 6px 10px; margin-bottom: 0; font-size: 12px; }
.empty-hint { text-align: center; color: var(--muted); font-size: 12px; padding: 20px; }

/* Live tab */
.sim-row { display: flex; gap: 6px; }
.sim-input { flex: 1; font-size: 12px; padding: 4px 8px; }
.sim-select { width: 80px; font-size: 12px; padding: 4px 6px; }
.sim-out { margin-top: 8px; }
.sim-steps { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 12px; }
.sim-chip {
  padding: 2px 8px;
  background: var(--bg-subtle);
  border-radius: 99px;
  font-size: 11px;
}
.sim-chip.win { background: rgba(63,185,80,.15); color: var(--success); font-weight: 600; }

.dist-grid.compact { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
.dist-grid h4 { font-size: 10px; text-transform: uppercase; color: var(--muted); margin: 0 0 6px; }
.dist-row {
  display: grid;
  grid-template-columns: 72px 1fr 32px;
  align-items: center;
  gap: 6px;
  margin-bottom: 3px;
  font-size: 11px;
}
.dist-label { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.dist-bar-bg { height: 10px; background: color-mix(in srgb, var(--border) 30%, transparent); border-radius: 2px; overflow: hidden; }
.dist-bar-fill { height: 100%; background: var(--success); border-radius: 2px; }
.dist-bar-fill.accent { background: var(--accent); }
.dist-count { text-align: right; font-variant-numeric: tabular-nums; font-size: 10px; }

.auto-toggle { font-size: 11px; color: var(--muted); display: flex; align-items: center; gap: 4px; cursor: pointer; }
.decision-detail { display: flex; flex-direction: column; gap: 6px; }
.candidates-compact { display: flex; flex-wrap: wrap; gap: 4px; }
.cand-chip {
  padding: 2px 8px;
  background: var(--bg);
  border-radius: 4px;
  font-size: 11px;
}

.text-muted { color: var(--muted); font-size: 11px; }
.text-danger { color: var(--danger); font-size: 11px; }

@media (max-width: 640px) {
  .flow-bar { flex-direction: column; align-items: stretch; }
  .flow-arrow { display: none; }
  .dist-grid.compact { grid-template-columns: 1fr; }
}
</style>
