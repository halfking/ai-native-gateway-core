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
  type RoutingPolicy, type ScoringWeights,
} from '../api'
import SixDimScoreBar from '../components/SixDimScoreBar.vue'

// ── Tab state ─────────────────────────────────────────
const activeTab = ref<'overview' | 'policy' | 'live'>('overview')

// ── Tab A: Overview ───────────────────────────────────
const indexData = ref<AutoRouteIndexEntry[]>([])
const selectedTask = ref<string>('')
const expandedModel = ref<string>('')
const indexLoading = ref(false)

const taskFilteredIndex = computed(() => {
  if (!selectedTask.value) return indexData.value
  // 按 score_smart 降序
  return [...indexData.value].sort((a, b) => (b.score_smart ?? 0) - (a.score_smart ?? 0))
})

async function loadIndex() {
  indexLoading.value = true
  try {
    indexData.value = await getAutoRouteIndex(30)
  } catch (e) {
    console.error('loadIndex', e)
  } finally {
    indexLoading.value = false
  }
}

// ── Tab B: Policy ─────────────────────────────────────
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
      if (policyDraft.value[f.key] !== policy.value![f.key]) {
        dirty[f.key] = policyDraft.value[f.key]
      }
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

// ── Tab B: Cost tables ────────────────────────────────
const customerCost = ref<CustomerCostRow[]>([])
const modelCost = ref<ModelCostRow[]>([])

async function loadCosts() {
  try {
    customerCost.value = await getCustomerCost(10)
    modelCost.value = await getModelCost(10)
  } catch (e) { console.error('loadCosts', e) }
}

// ── Tab C: Live decisions ────────────────────────────
const audit = ref<AutoRouteAudit>({ total_auto_requests: 0, success_rate: 0, task_distribution: {}, profile_distribution: {}, top_chosen_models: [] })
const decisions = ref<AutoRouteDecision[]>([])
const expandedDecision = ref<string>('')
const autoRefresh = ref(true)
let pollTimer: ReturnType<typeof setInterval> | null = null

// Simulator
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
  try {
    simResult.value = await simulateAutoRoute(simPrompt.value, simProfile.value)
  } finally { simLoading.value = false }
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

// ── Helpers ──────────────────────────────────────────
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

// ── Lifecycle ────────────────────────────────────────
onMounted(async () => {
  await loadIndex()
  await loadAudit()
})
onUnmounted(() => stopPoll())
</script>

<template>
  <div class="routing-dashboard">
    <div class="page-header">
      <h2>🗺️ 路由全景</h2>
      <div class="header-actions">
        <button class="btn btn-sm btn-ghost" @click="loadIndex(); loadAudit()" title="刷新">🔄</button>
      </div>
    </div>

    <!-- Tabs -->
    <div class="tabs">
      <button class="tab-btn" :class="{ active: activeTab === 'overview' }" @click="activeTab = 'overview'">🎯 两层路由</button>
      <button class="tab-btn" :class="{ active: activeTab === 'policy' }" @click="activeTab = 'policy'">⚙️ 策略与数据</button>
      <button class="tab-btn" :class="{ active: activeTab === 'live' }" @click="activeTab = 'live'">📡 实时决策</button>
    </div>

    <!-- ════════════════ Tab A: Overview ════════════════ -->
    <div v-if="activeTab === 'overview'" class="tab-content">
      <!-- Stat grid -->
      <div class="stat-grid">
        <div class="stat-card">
          <div class="label">候选模型</div>
          <div class="value">{{ indexData.length }}</div>
        </div>
        <div class="stat-card">
          <div class="label">Auto 请求 (24h)</div>
          <div class="value">{{ audit.total_auto_requests }}</div>
        </div>
        <div class="stat-card">
          <div class="label">成功率</div>
          <div class="value">{{ fmt(audit.success_rate * 100, 1) }}%</div>
        </div>
        <div class="stat-card">
          <div class="label">Top 模型</div>
          <div class="value" style="font-size:14px">{{ audit.top_chosen_models[0]?.model || '-' }}</div>
        </div>
      </div>

      <!-- Task type selector -->
      <div class="card">
        <h3>🧠 第一层：任务意图分类 → 模型选择</h3>
        <p class="hint">点击任务类型，查看该类型推荐的最优模型（基于实时 6 维评分指数）</p>
        <div class="task-grid">
          <button
            v-for="t in TASK_TYPES"
            :key="t.key"
            class="task-card"
            :class="{ active: selectedTask === t.key }"
            @click="selectedTask = selectedTask === t.key ? '' : t.key"
          >
            <span class="task-icon">{{ t.icon }}</span>
            <span class="task-label">{{ t.label }}</span>
            <span v-if="TASK_TAGS[t.key]?.length" class="task-tags">{{ TASK_TAGS[t.key].join(', ') }}</span>
          </button>
        </div>
      </div>

      <!-- Model recommendations -->
      <div v-if="indexData.length > 0" class="card">
        <h3>📊 第一层结果：模型推荐 Top-{{ Math.min(taskFilteredIndex.length, 10) }}</h3>
        <table>
          <thead>
            <tr>
              <th>#</th><th>模型</th><th>Smart</th><th>Speed</th><th>Cost</th>
              <th>价格(in/out)/1M</th><th>P95</th><th>成功率</th><th>上下文</th><th></th>
            </tr>
          </thead>
          <tbody>
            <template v-for="(m, i) in taskFilteredIndex.slice(0, 10)" :key="m.credential_id + ':' + m.raw_model">
              <tr class="model-row" :class="{ expanded: expandedModel === m.credential_id + ':' + m.raw_model }" @click="expandedModel = expandedModel === m.credential_id + ':' + m.raw_model ? '' : m.credential_id + ':' + m.raw_model">
                <td>{{ i + 1 }}</td>
                <td><strong>{{ m.canonical_name || m.raw_model }}</strong><br><span class="muted">{{ m.raw_model }}</span></td>
                <td><span class="badge" :class="m.score_smart && m.score_smart >= 70 ? 'badge-green' : 'badge-gray'">{{ fmt(m.score_smart, 1) }}</span></td>
                <td>{{ fmt(m.score_speed_first, 0) }}</td>
                <td>{{ fmt(m.score_cost_first, 0) }}</td>
                <td>{{ fmt(m.unit_price_in_per_1m, 2) }} / {{ fmt(m.unit_price_out_per_1m, 2) }}</td>
                <td>{{ fmtMs(m.p95_latency_ms) }}</td>
                <td>{{ fmt((m.success_rate ?? 0) * 100, 1) }}%</td>
                <td>{{ m.context_window ? (m.context_window / 1000) + 'K' : '-' }}</td>
                <td>{{ expandedModel === m.credential_id + ':' + m.raw_model ? '▼' : '▶' }}</td>
              </tr>
              <tr v-if="expandedModel === m.credential_id + ':' + m.raw_model" class="detail-row">
                <td colspan="10">
                  <div class="credential-detail">
                    <div class="cd-info">
                      <span class="badge badge-blue">Cred #{{ m.credential_id }}</span>
                      <span class="badge badge-gray">{{ m.billing_mode || 'token' }}</span>
                      <span v-if="m.pressure_ratio" class="badge" :class="m.pressure_ratio > 0.7 ? 'badge-red' : 'badge-green'">
                        压力 {{ fmt(m.pressure_ratio * 100, 0) }}%
                      </span>
                      <span v-if="m.active_sessions" class="muted">活跃 {{ m.active_sessions }} / {{ m.concurrency_limit || '-' }}</span>
                    </div>
                    <div class="cd-scores">
                      <SixDimScoreBar :scores="{
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
      <div v-else-if="!indexLoading" class="card empty">
        credential_model_index 尚无数据。点击右上角刷新，或等待 5 分钟自动刷新。
      </div>
    </div>

    <!-- ════════════════ Tab B: Policy ════════════════ -->
    <div v-if="activeTab === 'policy'" class="tab-content">
      <!-- v2.0 Profile weights matrix -->
      <div class="card">
        <h3>🎛️ v2.0 三模式权重矩阵</h3>
        <p class="hint">不同 profile 下 6 个维度的权重分布（决定 auto-route 选模型时的偏好）</p>
        <div class="profile-matrix">
          <div v-for="(w, name) in DEFAULT_PROFILE_WEIGHTS" :key="name" class="profile-row">
            <span class="profile-name">{{ name === 'smart' ? '🚗 智能选择' : name === 'speed_first' ? '⚡ 速度优先' : '💰 成本优先' }}</span>
            <div class="profile-bars">
              <SixDimScoreBar :scores="{
                price_score: (w as ProfileWeights).Price / maxDimValue(w as ProfileWeights) * 100,
                speed_score: (w as ProfileWeights).Speed / maxDimValue(w as ProfileWeights) * 100,
                stability_score: (w as ProfileWeights).Stability / maxDimValue(w as ProfileWeights) * 100,
                match_score: (w as ProfileWeights).Match / maxDimValue(w as ProfileWeights) * 100,
                pressure_score: (w as ProfileWeights).Pressure / maxDimValue(w as ProfileWeights) * 100,
                context_fit: (w as ProfileWeights).ContextFit / maxDimValue(w as ProfileWeights) * 100,
              }" compact />
            </div>
          </div>
        </div>
      </div>

      <!-- Algorithm params + Scoring weights -->
      <div class="policy-grid">
        <div class="card">
          <h3>⚙️ 路由算法参数</h3>
          <div class="policy-fields">
            <div v-for="f in POLICY_FIELDS" :key="f.key" class="form-group">
              <label>{{ f.label }}</label>
              <input type="number" :min="f.min" :max="f.max" :step="f.step" v-model.number="policyDraft[f.key]" />
            </div>
          </div>
          <button class="btn btn-primary btn-sm" :disabled="savingPolicy" @click="savePolicy">保存</button>
        </div>
        <div class="card">
          <h3>📐 Scoring Weights</h3>
          <div class="policy-fields">
            <div class="form-group">
              <label>价格权重</label>
              <input type="number" min="0" max="100" v-model.number="weightsDraft.price" />
            </div>
            <div class="form-group">
              <label>会话负载权重</label>
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
      <div v-if="policyMsg" class="alert alert-info">{{ policyMsg }}</div>

      <!-- Cost tables -->
      <div class="card">
        <h3>💰 客户成本面板</h3>
        <table v-if="customerCost.length > 0">
          <thead><tr><th>API Key</th><th>24h</th><th>7d</th><th>Auto请求数</th><th>活跃并发</th><th>最佳Smart</th></tr></thead>
          <tbody>
            <tr v-for="c in customerCost" :key="c.api_key_id">
              <td>#{{ c.api_key_id }} {{ c.key_alias || '' }}</td>
              <td>{{ fmtCost(c.cost_usd_24h) }}</td>
              <td>{{ fmtCost(c.cost_usd_7d) }}</td>
              <td>{{ c.total_auto_requests || 0 }}</td>
              <td>{{ c.active_concurrent || 0 }}</td>
              <td>{{ fmt(c.best_score_smart, 1) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="muted">暂无数据</div>
      </div>

      <div class="card">
        <h3>📊 模型成本排行</h3>
        <table v-if="modelCost.length > 0">
          <thead><tr><th>模型</th><th>总费用</th><th>均费/1M</th><th>成功率</th><th>均延迟</th><th>请求数</th></tr></thead>
          <tbody>
            <tr v-for="m in modelCost" :key="m.raw_model">
              <td>{{ m.raw_model }}</td>
              <td>{{ fmtCost(m.total_cost_usd) }}</td>
              <td>{{ fmtCost(m.avg_cost_per_1m_usd) }}</td>
              <td>{{ fmt((m.success_rate ?? 0) * 100, 1) }}%</td>
              <td>{{ fmtMs(m.avg_latency_ms) }}</td>
              <td>{{ m.total_requests || 0 }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="muted">暂无数据</div>
      </div>
    </div>

    <!-- ════════════════ Tab C: Live ════════════════ -->
    <div v-if="activeTab === 'live'" class="tab-content">
      <!-- Audit stats -->
      <div class="stat-grid">
        <div class="stat-card">
          <div class="label">Auto 总请求</div>
          <div class="value">{{ audit.total_auto_requests }}</div>
        </div>
        <div class="stat-card">
          <div class="label">成功率</div>
          <div class="value">{{ fmt(audit.success_rate * 100, 1) }}%</div>
        </div>
        <div class="stat-card">
          <div class="label">Top Task</div>
          <div class="value" style="font-size:14px">{{ distEntries(audit.task_distribution)[0]?.[0] || '-' }}</div>
        </div>
        <div class="stat-card">
          <div class="label">Top Model</div>
          <div class="value" style="font-size:14px">{{ audit.top_chosen_models[0]?.model || '-' }}</div>
        </div>
      </div>

      <!-- Distributions -->
      <div class="card">
        <h3>📋 任务 & Profile 分布</h3>
        <div class="dist-grid">
          <div>
            <h4>任务类型</h4>
            <div v-for="[task, count] in distEntries(audit.task_distribution)" :key="task" class="dist-row">
              <span class="dist-label">{{ task }}</span>
              <div class="dist-bar-bg">
                <div class="dist-bar-fill" :style="{ width: (count / distMax(audit.task_distribution) * 100) + '%' }" />
              </div>
              <span class="dist-count">{{ count }}</span>
            </div>
          </div>
          <div>
            <h4>Profile</h4>
            <div v-for="[p, count] in distEntries(audit.profile_distribution)" :key="p" class="dist-row">
              <span class="dist-label">{{ p }}</span>
              <div class="dist-bar-bg">
                <div class="dist-bar-fill" :style="{ width: (count / distMax(audit.profile_distribution) * 100) + '%', background: 'var(--accent)' }" />
              </div>
              <span class="dist-count">{{ count }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Recent decisions -->
      <div class="card">
        <div class="card-header">
          <h3>📜 最近 Auto 决策</h3>
          <label class="auto-refresh-toggle">
            <input type="checkbox" v-model="autoRefresh" /> 5 秒刷新
          </label>
        </div>
        <table v-if="decisions.length > 0">
          <thead><tr><th>时间</th><th>任务</th><th>Profile</th><th>选中模型</th><th>置信度</th><th>状态</th><th></th></tr></thead>
          <tbody>
            <template v-for="d in decisions" :key="d.request_id">
              <tr class="model-row" @click="expandedDecision = expandedDecision === d.request_id ? '' : d.request_id">
                <td>{{ new Date(d.ts).toLocaleTimeString() }}</td>
                <td><span class="badge badge-blue">{{ d.task_type || d.auto_decision?.task_type || '-' }}</span></td>
                <td>{{ d.auto_profile || d.auto_decision?.profile || '-' }}</td>
                <td><strong>{{ d.auto_decision?.chosen_model || d.outbound_model || '-' }}</strong></td>
                <td>{{ fmt((d.auto_confidence ?? d.auto_decision?.confidence ?? 0) * 100, 0) }}%</td>
                <td><span :class="d.success ? 'badge badge-green' : 'badge badge-red'">{{ d.success ? '✓' : '✗' }}</span></td>
                <td>{{ expandedDecision === d.request_id ? '▼' : '▶' }}</td>
              </tr>
              <tr v-if="expandedDecision === d.request_id && d.auto_decision" class="detail-row">
                <td colspan="7">
                  <div class="decision-detail">
                    <p>分类器: {{ d.auto_decision.classifier }} · 置信度: {{ fmt((d.auto_decision.confidence ?? 0) * 100, 1) }}%</p>
                    <div v-if="d.auto_decision.candidates_top3?.length" class="candidates-list">
                      <div v-for="(c, i) in d.auto_decision.candidates_top3" :key="i" class="candidate-item">
                        <span class="rank">#{{ i + 1 }}</span>
                        <strong>{{ c.model }}</strong>
                        <span class="badge" :class="i === 0 ? 'badge-green' : 'badge-gray'">{{ fmt(c.composite_score, 1) }}</span>
                        <SixDimScoreBar v-if="c.price_score !== undefined" :scores="c" compact />
                      </div>
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
        <div v-else class="muted">暂无 auto 决策。使用路由模拟器发送第一个请求。</div>
      </div>

      <!-- Simulator -->
      <div class="card">
        <h3>🔬 路由模拟器</h3>
        <p class="hint">输入 prompt → 选择 profile → 实时展示 v2.0 分类→评分→选择决策过程</p>
        <div class="sim-input">
          <input v-model="simPrompt" placeholder="输入测试 prompt..." class="sim-prompt-input" />
          <select v-model="simProfile">
            <option value="smart">🚗 智能选择</option>
            <option value="speed_first">⚡ 速度优先</option>
            <option value="cost_first">💰 成本优先</option>
          </select>
          <button class="btn btn-primary btn-sm" :disabled="simLoading" @click="runSim">
            {{ simLoading ? '发送中...' : '发送' }}
          </button>
        </div>
        <div v-if="simResult" class="sim-result">
          <div v-if="simResult.error" class="alert alert-danger">{{ simResult.error }}</div>
          <div v-else-if="simResult.decision" class="sim-steps">
            <div class="sim-step">
              <span class="step-num">1</span>
              <strong>分类</strong>: task=<span class="badge badge-blue">{{ (simResult.decision as Record<string, unknown>).task_type }}</span>
              · confidence={{ fmt(Number((simResult.decision as Record<string, unknown>).confidence) * 100, 1) }}%
              · classifier={{ (simResult.decision as Record<string, unknown>).classifier }}
            </div>
            <div class="sim-step">
              <span class="step-num">2</span>
              <strong>评分 Top-3</strong>:
              <div v-if="Array.isArray((simResult.decision as Record<string, unknown>).candidates_top3)" class="sim-candidates">
                <div v-for="(c, i) in (simResult.decision as Record<string, unknown>).candidates_top3 as Array<Record<string, number>>" :key="i" class="sim-candidate">
                  #{{ i + 1 }} {{ c.model }} = {{ fmt(c.composite_score, 1) }}
                </div>
              </div>
            </div>
            <div class="sim-step">
              <span class="step-num">3</span>
              <strong>选择</strong>: <span class="badge badge-green">{{ (simResult.decision as Record<string, unknown>).chosen_model }}</span>
              · credential #{{ (simResult.decision as Record<string, unknown>).chosen_credential_id }}
              · HTTP {{ simResult.status }}
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.routing-dashboard {
  max-width: 1200px;
}
.tab-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.card-header h3 {
  margin: 0;
}
.auto-refresh-toggle {
  font-size: 12px;
  color: var(--muted);
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
}
.hint {
  color: var(--muted);
  font-size: 12px;
  margin: -4px 0 12px;
}
.muted {
  color: var(--muted);
}

/* Task grid */
.task-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 8px;
}
.task-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 12px 8px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
  transition: all 0.15s;
  gap: 4px;
}
.task-card:hover {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 8%, var(--bg-subtle));
}
.task-card.active {
  border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 15%, var(--bg-subtle));
  box-shadow: 0 0 0 1px var(--accent);
}
.task-icon {
  font-size: 24px;
}
.task-label {
  font-size: 12px;
  font-weight: 500;
}
.task-tags {
  font-size: 9px;
  color: var(--muted);
}

/* Model table */
.model-row {
  cursor: pointer;
}
.model-row:hover {
  background: color-mix(in srgb, var(--accent) 5%, transparent);
}
.model-row.expanded {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}
.detail-row td {
  padding: 12px 16px;
  background: var(--bg-subtle);
  border-top: 1px solid var(--border);
}
.credential-detail {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 16px;
  align-items: center;
}
.cd-info {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.cd-scores {
  min-width: 200px;
}

/* Profile matrix */
.profile-matrix {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.profile-row {
  display: grid;
  grid-template-columns: 120px 1fr;
  align-items: center;
  gap: 12px;
}
.profile-name {
  font-size: 13px;
  font-weight: 500;
}
.profile-bars {
  min-width: 250px;
}

/* Policy grid */
.policy-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 16px;
}
.policy-fields {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
  gap: 8px;
  margin-bottom: 12px;
}
.policy-fields .form-group {
  margin-bottom: 0;
}
.policy-fields label {
  font-size: 11px;
}
.policy-fields input {
  width: 100%;
  padding: 4px 8px;
  font-size: 13px;
}

/* Distributions */
.dist-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
}
.dist-grid h4 {
  font-size: 12px;
  text-transform: uppercase;
  color: var(--muted);
  margin: 0 0 8px;
}
.dist-row {
  display: grid;
  grid-template-columns: 100px 1fr 40px;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  font-size: 12px;
}
.dist-label {
  color: var(--muted);
}
.dist-bar-bg {
  height: 14px;
  background: color-mix(in srgb, var(--border) 30%, transparent);
  border-radius: 3px;
  overflow: hidden;
}
.dist-bar-fill {
  height: 100%;
  background: var(--success);
  border-radius: 3px;
  transition: width 0.3s;
}
.dist-count {
  text-align: right;
  font-variant-numeric: tabular-nums;
}

/* Decisions table */
.decision-detail {
  padding: 8px 0;
}
.candidates-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 8px;
}
.candidate-item {
  display: grid;
  grid-template-columns: 30px 120px 60px 1fr;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  background: var(--bg);
  border-radius: 4px;
}
.candidate-item .rank {
  font-weight: bold;
  color: var(--muted);
}

/* Simulator */
.sim-input {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}
.sim-prompt-input {
  flex: 1;
}
.sim-result {
  margin-top: 12px;
}
.sim-steps {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.sim-step {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  background: var(--bg-subtle);
  border-radius: 6px;
  font-size: 13px;
}
.step-num {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  background: var(--accent);
  color: white;
  border-radius: 50%;
  font-size: 11px;
  font-weight: bold;
}
.sim-candidates {
  display: flex;
  gap: 12px;
  margin-left: 28px;
}
.sim-candidate {
  font-size: 12px;
  padding: 4px 8px;
  background: var(--bg);
  border-radius: 4px;
}
</style>