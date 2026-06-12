<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  resolveRouting, probeModel,
  getScoreDetails, updateManualPriority,
  getFeaturedModelsDynamic,
  type RoutingCandidate, type ProbeResult, type RoutingResolveResponse,
  type ScoreDetail, type FeaturedModel,
} from '../api'
import ModelPicker from '../components/ModelPicker.vue'

const modelInput  = ref('')
const clientProfile = ref('')
const resolution  = ref<RoutingResolveResponse | null>(null)
const candidates  = ref<RoutingCandidate[]>([])
const resolving   = ref(false)
const resolveErr  = ref('')
const resolved    = ref(false)
const showUnavailable = ref(false)

const probing     = ref(false)
const probeResult = ref<ProbeResult | null>(null)
const probeErr    = ref('')

const scoreDetails = ref<ScoreDetail[]>([])
const loadingScores = ref(false)
const featuredModels = ref<FeaturedModel[]>([])
const editingPriority = ref<number | null>(null)
const editingValue = ref(99)

const filteredCandidates = computed(() =>
  showUnavailable.value ? candidates.value : candidates.value.filter(c => c.routable)
)

const unavailableCount = computed(() =>
  candidates.value.filter(c => !c.routable).length
)

const compositeScores = computed(() => {
  const map = new Map<number, number>()
  for (const d of scoreDetails.value) {
    map.set(d.credential_id, d.composite_score)
  }
  return map
})

onMounted(async () => {
  await loadFeaturedModels()
})

async function loadFeaturedModels() {
  try {
    const res = await getFeaturedModelsDynamic()
    featuredModels.value = res.models
  } catch (e) {
    console.warn('Failed to load featured models:', e)
  }
}

function displayName(m: FeaturedModel): string {
  return m.standardized_name || m.name
}

function clickModel(m: FeaturedModel) {
  selectModel(displayName(m))
}

async function selectModel(name: string) {
  modelInput.value = name
  await doResolve()
}

async function doResolve() {
  if (!modelInput.value.trim()) return
  resolving.value = true
  resolveErr.value = ''
  probeResult.value = null
  probeErr.value = ''
  try {
    const res = await resolveRouting(
      modelInput.value.trim(),
      clientProfile.value.trim() || undefined,
    )
    resolution.value = res
    candidates.value = res.candidates
    resolved.value = true
    await loadScoreDetails()
  } catch (e: unknown) {
    resolveErr.value = e instanceof Error ? e.message : '查询失败'
  } finally {
    resolving.value = false
  }
}

async function loadScoreDetails() {
  if (!modelInput.value.trim()) return
  loadingScores.value = true
  try {
    const res = await getScoreDetails(modelInput.value.trim())
    scoreDetails.value = res.candidates
  } catch (e) {
    console.warn('Failed to load score details:', e)
    scoreDetails.value = []
  } finally {
    loadingScores.value = false
  }
}

async function doProbe() {
  if (!modelInput.value.trim()) return
  probing.value = true
  probeErr.value = ''
  probeResult.value = null
  try {
    probeResult.value = await probeModel(
      modelInput.value.trim(),
      undefined,
      20,
      clientProfile.value.trim() || 'roocode',
    )
  } catch (e: unknown) {
    probeErr.value = e instanceof Error ? e.message : '测试失败'
  } finally {
    probing.value = false
  }
}

function startEditPriority(credId: number, current: number) {
  editingPriority.value = credId
  editingValue.value = current
}

async function savePriority(credId: number, modelName: string) {
  if (editingValue.value < 1 || editingValue.value > 99) {
    alert('手工序号必须在 1-99 之间')
    return
  }
  try {
    await updateManualPriority(credId, modelName, editingValue.value)
    editingPriority.value = null
    await loadScoreDetails()
    await doResolve()
  } catch (e: unknown) {
    alert('保存失败: ' + (e instanceof Error ? e.message : '未知错误'))
  }
}

function cancelEdit() {
  editingPriority.value = null
}

function getScoreFor(c: RoutingCandidate): number | null {
  if (c.composite_score != null) return c.composite_score
  return compositeScores.value.get(c.credential_id) ?? null
}

function getScoreBreakdown(credId: number): ScoreDetail | null {
  return scoreDetails.value.find(s => s.credential_id === credId) ?? null
}

function latencyLabel(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function rateLabel(r: number): string {
  return r <= 0 ? '—' : `${(r * 100).toFixed(1)}%`
}

function money(value: number | string | null | undefined, currency = 'USD'): string {
  if (value === null || value === undefined) return '—'
  const n = typeof value === 'string' ? Number(value) : value
  return Number.isNaN(n) ? '—' : `${n.toFixed(4)} ${currency}`
}

function priceLabel(c: RoutingCandidate): string {
  return `${money(c.unit_price_in_per_1m, c.currency || 'USD')} / ${money(c.unit_price_out_per_1m, c.currency || 'USD')}`
}

function quotaLabel(c: RoutingCandidate): string {
  const cap = Number(c.quota_cap_usd || 0)
  const used = Number(c.quota_used_usd || 0)
  if (cap > 0) return `${money(Math.max(0, cap - used), 'USD')} left`
  return money(c.balance_usd, 'USD')
}

function dateWindow(c: RoutingCandidate): string {
  const start = c.effective_at ? c.effective_at.slice(0, 10) : 'now'
  const end = c.expires_at ? c.expires_at.slice(0, 10) : '∞'
  return `${start} → ${end}`
}
</script>

<template>
  <div>
    <div class="page-header">
      <h2>路由测试</h2>
    </div>
    <p style="color:var(--muted);margin-bottom:20px">
      输入客户端模型名，查看解析路径与路由候选；可模拟 Cursor/RooCode 等终端 profile。
    </p>

    <div class="card" style="margin-bottom:20px">
      <div style="display:flex;gap:10px;align-items:center;flex-wrap:wrap">
        <div style="flex:1;min-width:240px">
          <ModelPicker
            v-model="modelInput"
            :allow-free-text="true"
            placeholder="选择或输入模型，例如 gpt-4o-mini"
          />
        </div>
        <input
          v-model="clientProfile"
          class="input"
          style="width:160px"
          placeholder="client profile"
          title="cursor / roocode / cline"
        />
        <button class="btn btn-primary" @click="doResolve" :disabled="resolving || !modelInput.trim()">
          {{ resolving ? '查询中…' : '查询路由' }}
        </button>
        <button
          class="btn btn-ghost"
          @click="doProbe"
          :disabled="probing || !modelInput.trim()"
          title="向路由到的供应商发送一个小型测试请求"
        >
          {{ probing ? '测试中…' : '发送测试请求' }}
        </button>
      </div>
      <div v-if="featuredModels.length > 0" style="margin-top:12px;display:flex;gap:6px;flex-wrap:wrap;align-items:center">
        <span style="font-size:12px;color:var(--muted)">常用模型：</span>
        <button
          v-for="m in featuredModels"
          :key="m.name"
          class="btn btn-ghost btn-sm"
          :class="{ active: modelInput === displayName(m) }"
          :title="m.name !== displayName(m) ? `原始: ${m.name}` : undefined"
          @click="clickModel(m)"
          style="font-size:12px;padding:4px 10px"
        >
          {{ displayName(m) }} <span style="color:var(--muted);font-size:10px">({{ m.count }})</span>
        </button>
      </div>
    </div>

    <div v-if="resolveErr" class="alert alert-danger">{{ resolveErr }}</div>

    <div class="card" v-if="resolution" style="margin-bottom:20px">
      <h4 style="margin:0 0 12px">模型解析</h4>
      <div style="display:flex;gap:20px;flex-wrap:wrap;font-size:13px;align-items:baseline">
        <div><span class="cell-muted">客户端模型：</span><code>{{ resolution.client_model }}</code></div>
        <div><span class="cell-muted">解析路径：</span>{{ resolution.resolution_path }}</div>
        <div><span class="cell-muted">Canonical：</span>{{ resolution.canonical_name || '—' }}</div>
        <div><span class="cell-muted">Raw 扩展：</span><code style="font-size:11px">{{ resolution.raw_models.join(', ') }}</code></div>
      </div>
      <div v-if="resolution.plan_order.length" style="margin-top:12px;font-size:12px;color:var(--muted)">
        执行顺序（P2C+粘性）：
        <span v-for="(p, i) in resolution.plan_order" :key="p.credential_id">
          {{ i > 0 ? ' → ' : '' }}#{{ p.credential_id }} ({{ p.raw_model }})
        </span>
      </div>
    </div>

    <div class="card" v-if="resolved" style="margin-bottom:20px">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:12px;flex-wrap:wrap;gap:8px">
        <h4 style="margin:0">路由候选 — {{ modelInput }}</h4>
        <label v-if="unavailableCount > 0" style="display:flex;align-items:center;gap:6px;font-size:13px;cursor:pointer;color:var(--muted)">
          <input type="checkbox" v-model="showUnavailable" style="width:auto" />
          显示不可用（{{ unavailableCount }} 个）
        </label>
      </div>
      <div v-if="candidates.length === 0" class="alert alert-danger" style="margin:0">
        该模型暂无凭据配置，无法路由
      </div>
      <div v-else-if="filteredCandidates.length === 0 && !showUnavailable" class="alert alert-danger" style="margin:0">
        该模型暂无可用凭据 — {{ unavailableCount }} 个候选不可用（凭据限流/冷却/余额不足）
      </div>
      <table v-else-if="filteredCandidates.length > 0">
        <thead>
          <tr>
            <th>综合得分</th>
            <th>供应商</th>
            <th>目录代码</th>
            <th>凭据</th>
            <th>上游 raw</th>
            <th>计费</th>
            <th>策略</th>
            <th>手工序号</th>
            <th>价格 in/out</th>
            <th>会话/错误</th>
            <th>状态</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in filteredCandidates" :key="c.credential_id" :style="c.routable ? '' : 'opacity:0.55'">
            <td style="text-align:center">
              <span
                class="score-badge"
                :class="getScoreFor(c) === 0 ? 'score-free' : (getScoreFor(c) !== null && getScoreFor(c)! < 20 ? 'score-good' : 'score-normal')"
                :title="getScoreFor(c) === 0 ? '免费模型，最优先' : ''"
              >
                {{ getScoreFor(c) !== null ? getScoreFor(c)!.toFixed(1) : '—' }}
              </span>
              <div v-if="getScoreBreakdown(c.credential_id)" class="cell-muted" style="font-size:10px">
                <span title="手工序号">P{{ getScoreBreakdown(c.credential_id)!.manual_priority }}</span> ·
                <span title="归一化价格">C{{ getScoreBreakdown(c.credential_id)!.normalized_cost.toFixed(2) }}</span> ·
                <span title="会话负载">L{{ getScoreBreakdown(c.credential_id)!.session_load.toFixed(2) }}</span> ·
                <span title="错误次数">F{{ getScoreBreakdown(c.credential_id)!.consecutive_failures }}</span>
              </div>
            </td>
            <td>{{ c.provider_name }}</td>
            <td><code style="font-size:11px">{{ c.catalog_code }}</code></td>
            <td>
              <div>{{ c.credential_label }}</div>
              <div class="cell-muted">#{{ c.credential_id }} · 并发 {{ c.effective_concurrency ?? c.concurrency_limit ?? '—' }}</div>
            </td>
            <td><code style="font-size:11px">{{ c.model_name }}</code></td>
            <td>
              <span class="badge" :class="c.billing_round === 1 ? 'badge-green' : ''" style="font-size:11px">
                {{ c.billing_mode || 'token' }}
              </span>
              <div v-if="c.billing_round === 2" class="cell-muted">第2轮 · 并发溢出</div>
            </td>
            <td>T{{ c.tier }} · w{{ c.weight }}</td>
            <td>
              <div v-if="editingPriority === c.credential_id" style="display:flex;gap:4px;align-items:center">
                <input
                  v-model.number="editingValue"
                  type="number"
                  min="1"
                  max="99"
                  style="width:50px;padding:2px 6px;font-size:12px"
                  @keyup.enter="savePriority(c.credential_id, c.model_name)"
                  @keyup.esc="cancelEdit"
                />
                <button class="btn btn-primary btn-sm" @click="savePriority(c.credential_id, c.model_name)" style="font-size:11px;padding:2px 6px">保存</button>
                <button class="btn btn-ghost btn-sm" @click="cancelEdit" style="font-size:11px;padding:2px 6px">取消</button>
              </div>
              <div v-else style="cursor:pointer" @click="startEditPriority(c.credential_id, getScoreBreakdown(c.credential_id)?.manual_priority ?? c.manual_priority ?? 99)">
                <span style="font-weight:600">{{ getScoreBreakdown(c.credential_id)?.manual_priority ?? c.manual_priority ?? 99 }}</span>
                <span class="cell-muted" style="font-size:10px;margin-left:4px">✎</span>
              </div>
            </td>
            <td>{{ priceLabel(c) }}</td>
            <td>
              <div style="font-size:12px">
                会话: <strong>{{ getScoreBreakdown(c.credential_id)?.active_sessions ?? 0 }}</strong>
                / {{ c.concurrency_limit ?? '—' }}
              </div>
              <div class="cell-muted" style="font-size:11px">
                错误: {{ getScoreBreakdown(c.credential_id)?.consecutive_failures ?? 0 }}
              </div>
            </td>
            <td>
              <span class="badge" :class="c.routable ? 'badge-green' : 'badge-red'">
                {{ c.routable ? '可路由' : '不可用' }}
              </span>
              <div class="cell-muted">{{ c.credential_status }} · {{ c.circuit_state || 'closed' }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="probeErr" class="alert alert-danger">{{ probeErr }}</div>
    <div class="card" v-if="probeResult" style="margin-bottom:20px">
      <h4 style="margin:0 0 12px">
        测试结果
        <span class="badge" :class="probeResult.success ? 'badge-green' : 'badge-red'" style="margin-left:8px">
          {{ probeResult.success ? '成功' : '失败' }}
        </span>
      </h4>
      <div class="stat-grid">
        <div class="stat-card">
          <div class="stat-label">供应商</div>
          <div class="stat-value" style="font-size:16px">{{ probeResult.provider_name }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">目录代码</div>
          <div class="stat-value" style="font-size:16px">{{ probeResult.catalog_code }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">延迟</div>
          <div class="stat-value" style="font-size:16px">{{ latencyLabel(probeResult.latency_ms) }}</div>
        </div>
      </div>
      <div v-if="probeResult.reply" style="margin-top:16px">
        <div style="font-size:12px;color:var(--muted);margin-bottom:6px">模型回复：</div>
        <pre style="background:var(--bg-subtle,#161b22);border:1px solid var(--border,#30363d);border-radius:6px;padding:12px;font-size:13px;margin:0;white-space:pre-wrap;color:var(--text,#e6edf3)">{{ probeResult.reply }}</pre>
      </div>
      <div v-if="probeResult.error" class="alert alert-danger" style="margin-top:12px">
        {{ probeResult.error }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.cell-muted { color: var(--muted); font-size: 11px; margin-top: 3px; }

.score-badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 10px;
  font-weight: 600;
  font-size: 12px;
  min-width: 40px;
  text-align: center;
}
.score-free {
  background: #dcfce7;
  color: #166534;
}
.score-good {
  background: #dbeafe;
  color: #1e40af;
}
.score-normal {
  background: #f3f4f6;
  color: #374151;
}
</style>
