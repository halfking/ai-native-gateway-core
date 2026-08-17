<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { authBearer, isSuperAdmin } from '../store'
import { emergencyRepair, patchCandidateBinding, resolveRouting, type RoutingCandidate } from '../api/routing'
import { updateCredentialLifecycle, type CredentialLifecycleStatus } from '../api/providers'
import {
  getCredentialDecisions,
  getCredentialMonitorSummary,
  getModelHistory,
  getSlidingWindow,
  setManualDisabled,
  toggleModelAvailability,
  type CredentialModelStatus,
  type CredentialMonitorSummary,
  type CredentialRoutingDecision,
  type ModelHistoryEvent,
  type WindowStats,
} from '../api/credential-monitor'
import type { LiveNodeStatus } from '../composables/liveStreamStore'

const props = defineProps<{
  modelValue: boolean
  node: LiveNodeStatus | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  applied: []
}>()

type Tab = 'detail' | 'settings'
const activeTab = ref<Tab>('detail')
const loading = ref(false)
const loadError = ref('')
const candidate = ref<RoutingCandidate | null>(null)
const monitor = ref<CredentialMonitorSummary | null>(null)
const selectedModel = ref('')
const windowEntries = ref<Array<{ rid: string; ts: number; ok: boolean; lat: number; err?: string }>>([])
const windowStats = ref<WindowStats | null>(null)
const windowSource = ref('')
const history = ref<ModelHistoryEvent[]>([])
const decisions = ref<CredentialRoutingDecision[]>([])
const saving = ref(false)
const actionMessage = ref('')
const actionError = ref('')
const lifecycle = ref<CredentialLifecycleStatus>('active')
const manualPriority = ref(99)
const routingTier = ref(2)
const weight = ref(100)
const modelActionReason = ref('')
let sequence = 0

const visible = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit('update:modelValue', value),
})
const canEdit = computed(() => isSuperAdmin())
const models = computed<CredentialModelStatus[]>(() => monitor.value?.models ?? [])
const selectedModelStatus = computed(() => models.value.find(model => model.raw_model_name === selectedModel.value) ?? null)
const headlineState = computed(() => {
  const node = props.node
  if (!node) return '未知'
  if (node.manual_disabled || node.disable_kind === 'manual') return '手工禁用'
  if (node.circuit_state === 'open') return '熔断'
  if (node.health_status === 'unreachable' || node.availability_state === 'unreachable') return '不可达'
  if (node.availability_state === 'cooling' || node.circuit_state === 'half_open') return '恢复中'
  return '可用'
})
const errorKinds = computed(() => Object.entries(windowStats.value?.error_kinds ?? {}))

function fmtTime(value: string | number | null | undefined): string {
  if (value == null || value === '') return '—'
  const date = new Date(typeof value === 'number' ? value : value)
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString()
}
function pct(value: number | null | undefined): string {
  return value == null ? '—' : `${(value * 100).toFixed(1)}%`
}
function statusClass(value: string | null | undefined): string {
  if (['ready', 'healthy', 'active', 'closed', 'ok', 'available'].includes(value || '')) return 'is-ok'
  if (['cooling', 'half_open', 'low', 'warning', 'degraded'].includes(value || '')) return 'is-warn'
  return 'is-bad'
}

async function loadModelDetails(model: string, requestSequence: number) {
  const node = props.node
  if (!node || !model) {
    windowEntries.value = []
    windowStats.value = null
    history.value = []
    return
  }
  const [windowResult, historyResult] = await Promise.allSettled([
    getSlidingWindow(node.credential_id, model, 60),
    getModelHistory(node.credential_id, model, 30),
  ])
  if (requestSequence !== sequence || props.node?.credential_id !== node.credential_id || selectedModel.value !== model) return
  if (windowResult.status === 'fulfilled') {
    windowEntries.value = windowResult.value.entries
    windowStats.value = windowResult.value.stats
    windowSource.value = windowResult.value.source
  } else {
    windowEntries.value = []
    windowStats.value = null
    windowSource.value = ''
  }
  history.value = historyResult.status === 'fulfilled' ? historyResult.value.events : []
}

async function loadNode() {
  const node = props.node
  if (!node || !visible.value) return
  const requestSequence = ++sequence
  loading.value = true
  loadError.value = ''
  actionMessage.value = ''
  actionError.value = ''
  candidate.value = null
  monitor.value = null
  windowEntries.value = []
  windowStats.value = null
  history.value = []
  decisions.value = []
  const preferredModel = node.raw_models?.[0] || ''
  selectedModel.value = preferredModel
  try {
    const [monitorResult, decisionResult, resolveResult] = await Promise.allSettled([
      getCredentialMonitorSummary({ credential_id: node.credential_id }),
      getCredentialDecisions(node.credential_id, 30),
      preferredModel ? resolveRouting(preferredModel) : Promise.reject(new Error('节点未上报模型绑定')),
    ])
    if (requestSequence !== sequence || props.node?.credential_id !== node.credential_id) return
    if (monitorResult.status === 'fulfilled') {
      const monitorPayload = monitorResult.value as { credentials?: CredentialMonitorSummary[] }
      monitor.value = monitorPayload.credentials?.[0] ?? null
      const preferred = monitor.value?.models?.find(model => model.raw_model_name === preferredModel)
        ?? monitor.value?.models?.find(model => model.probe_state === 'broken_confirmed')
        ?? monitor.value?.models?.[0]
      selectedModel.value = preferred?.raw_model_name ?? preferredModel
    }
    if (decisionResult.status === 'fulfilled') {
      decisions.value = (decisionResult.value as { decisions?: CredentialRoutingDecision[] }).decisions ?? []
    }
    if (resolveResult.status === 'fulfilled') {
      candidate.value = resolveResult.value.candidates.find(item => item.credential_id === node.credential_id) ?? null
    }
    if (monitorResult.status === 'rejected' && resolveResult.status === 'rejected') {
      loadError.value = '未能加载节点的持久化明细；仍展示实时状态。'
    }
    lifecycle.value = (candidate.value?.lifecycle_status ?? 'active') as CredentialLifecycleStatus
    manualPriority.value = candidate.value?.manual_priority ?? 99
    routingTier.value = candidate.value?.tier ?? 2
    weight.value = candidate.value?.weight ?? 100
    await loadModelDetails(selectedModel.value, requestSequence)
  } finally {
    if (requestSequence === sequence) loading.value = false
  }
}

function chooseModel(model: string) {
  if (model === selectedModel.value) return
  selectedModel.value = model
  void loadModelDetails(model, ++sequence)
}

async function testNow() {
  if (!props.node?.provider_id || saving.value) return
  saving.value = true
  actionMessage.value = ''
  actionError.value = ''
  try {
    const response = await fetch(`/api/admin/providers/${props.node.provider_id}/test-now`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${authBearer()}` },
    })
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    const result = await response.json()
    actionMessage.value = `测试完成${typeof result.latency_ms === 'number' ? `：${result.latency_ms}ms` : ''}`
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : '测试失败'
  } finally {
    saving.value = false
  }
}

async function saveSettings() {
  const node = props.node
  const row = candidate.value
  if (!node || !row || !canEdit.value || saving.value) return
  saving.value = true
  actionMessage.value = ''
  actionError.value = ''
  try {
    const patch: { manual_priority?: number; routing_tier?: number; weight?: number } = {}
    if (manualPriority.value !== (row.manual_priority ?? 99)) patch.manual_priority = manualPriority.value
    if (routingTier.value !== row.tier) patch.routing_tier = routingTier.value
    if (weight.value !== row.weight) patch.weight = weight.value
    if (Object.keys(patch).length) await patchCandidateBinding(row.credential_id, row.model_name, patch)
    if (lifecycle.value !== (row.lifecycle_status ?? 'active')) {
      await updateCredentialLifecycle(node.provider_id!, node.credential_id, lifecycle.value)
    }
    actionMessage.value = '设置已保存，正在等待实时状态对账。'
    emit('applied')
    await loadNode()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : '保存设置失败'
  } finally {
    saving.value = false
  }
}

async function repair(action: 'force_enable' | 'force_disable' | 'clear_circuit' | 'reset_errors') {
  const node = props.node
  if (!node || saving.value || !canEdit.value) return
  const labels = { force_enable: '强制启用', force_disable: '强制禁用', clear_circuit: '清除熔断', reset_errors: '重置错误' }
  if (action === 'force_disable' && !confirm(`确认${labels[action]}节点 ${node.credential_id}？`)) return
  saving.value = true
  actionMessage.value = ''
  actionError.value = ''
  try {
    await emergencyRepair({ credential_id: node.credential_id, raw_model: selectedModel.value, action, reason: `dashboard node detail: ${labels[action]}` })
    actionMessage.value = `${labels[action]}已提交，等待 node_update 实时对账。`
    emit('applied')
    await loadNode()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : '操作失败'
  } finally {
    saving.value = false
  }
}

async function setCredentialDisabled(disabled: boolean) {
  const node = props.node
  if (!node || saving.value || !canEdit.value) return
  const reason = modelActionReason.value.trim()
  if (!reason) {
    actionError.value = '请输入维护原因。'
    return
  }
  saving.value = true
  try {
    await setManualDisabled(node.credential_id, disabled, reason)
    actionMessage.value = disabled ? '凭据已手工禁用。' : '凭据已恢复。'
    modelActionReason.value = ''
    emit('applied')
    await loadNode()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : '维护失败'
  } finally {
    saving.value = false
  }
}

async function toggleSelectedModel() {
  const node = props.node
  const model = selectedModelStatus.value
  if (!node || !model || saving.value || !canEdit.value) return
  const reason = modelActionReason.value.trim()
  if (!reason) {
    actionError.value = '请输入维护原因。'
    return
  }
  const action = model.binding_unavailable_reason === 'manual_offline' ? 'online' : 'offline'
  saving.value = true
  try {
    await toggleModelAvailability(node.credential_id, model.raw_model_name, action, reason)
    actionMessage.value = action === 'online' ? '模型已恢复上线。' : '模型已手工下线。'
    modelActionReason.value = ''
    emit('applied')
    await loadNode()
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : '模型维护失败'
  } finally {
    saving.value = false
  }
}

watch(() => [props.modelValue, props.node?.credential_id] as const, ([open]) => {
  if (open) {
    activeTab.value = 'detail'
    void loadNode()
  } else {
    sequence++
  }
}, { immediate: true })
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="nd-mask" @click.self="visible = false" />
    <aside v-if="visible && node" class="nd-drawer" role="dialog" aria-modal="true" :aria-label="`节点 ${node.credential_id} 详情`">
      <header class="nd-header">
        <div>
          <div class="nd-eyebrow">节点 #{{ node.credential_id }} · {{ node.provider_code || `Provider ${node.provider_id ?? '—'}` }}</div>
          <h2>{{ selectedModel || '未上报模型绑定' }}</h2>
          <div class="nd-state-row">
            <span class="nd-state" :class="statusClass(headlineState === '可用' ? 'ready' : headlineState)">{{ headlineState }}</span>
            <span v-if="node.in_flight" class="nd-muted">在途 {{ node.in_flight }}</span>
            <span v-if="node.last_latency_ms != null" class="nd-muted">最近 {{ node.last_latency_ms }}ms</span>
          </div>
        </div>
        <div class="nd-header-actions">
          <button class="btn btn-sm btn-ghost" :disabled="loading || saving" @click="loadNode">↻ 刷新</button>
          <button class="btn btn-sm btn-ghost" @click="visible = false">关闭</button>
        </div>
      </header>

      <div class="nd-tabs" role="tablist">
        <button :class="{ active: activeTab === 'detail' }" role="tab" @click="activeTab = 'detail'">明细与近期情况</button>
        <button :class="{ active: activeTab === 'settings' }" role="tab" @click="activeTab = 'settings'">设置与维护</button>
      </div>

      <div class="nd-body">
        <p v-if="loadError" class="nd-notice nd-notice--warn">{{ loadError }}</p>
        <p v-if="actionMessage" class="nd-notice nd-notice--ok">{{ actionMessage }}</p>
        <p v-if="actionError" class="nd-notice nd-notice--error">{{ actionError }}</p>
        <div v-if="loading" class="nd-loading">正在加载节点路由、健康和近期请求数据…</div>

        <template v-else-if="activeTab === 'detail'">
          <section class="nd-section">
            <h3>实时状态</h3>
            <dl class="nd-grid">
              <div><dt>熔断</dt><dd :class="statusClass(node.circuit_state)">{{ node.circuit_state || '未知' }}</dd></div>
              <div><dt>可用性</dt><dd :class="statusClass(node.availability_state)">{{ node.availability_state || '未知' }}</dd></div>
              <div><dt>配额</dt><dd :class="statusClass(node.quota_state)">{{ node.quota_state || '未知' }}</dd></div>
              <div><dt>健康</dt><dd :class="statusClass(node.health_status)">{{ node.health_status || '未知' }}</dd></div>
              <div><dt>手工禁用</dt><dd>{{ node.manual_disabled ? '是' : '否' }}</dd></div>
              <div><dt>最近错误</dt><dd class="nd-wrap">{{ node.last_error || '—' }}</dd></div>
            </dl>
          </section>

          <section v-if="candidate" class="nd-section">
            <h3>路由候选明细</h3>
            <dl class="nd-grid">
              <div><dt>凭据</dt><dd>#{{ candidate.credential_id }} · {{ candidate.credential_label }}</dd></div>
              <div><dt>Provider</dt><dd>{{ candidate.provider_name }}</dd></div>
              <div><dt>是否可路由</dt><dd :class="candidate.routable ? 'is-ok' : 'is-bad'">{{ candidate.routable ? '是' : '否' }}</dd></div>
              <div><dt>阻断原因</dt><dd class="nd-wrap">{{ candidate.runtime_block_reason || candidate.block_reason || '—' }}</dd></div>
              <div><dt>生命周期</dt><dd>{{ candidate.lifecycle_status || '—' }}</dd></div>
              <div><dt>配额 / 余额</dt><dd>{{ candidate.quota_state || '—' }} / {{ candidate.balance_usd ?? '—' }} USD</dd></div>
              <div><dt>熔断 / 冷却</dt><dd>{{ candidate.circuit_state || 'closed' }} / {{ fmtTime(candidate.cooling_until) }}</dd></div>
              <div><dt>并发 / 会话</dt><dd>{{ candidate.effective_concurrency ?? '—' }} / {{ candidate.active_sessions ?? 0 }}</dd></div>
              <div><dt>Tier / 权重 / 优先级</dt><dd>T{{ candidate.tier }} / {{ candidate.weight }} / {{ candidate.manual_priority ?? 99 }}</dd></div>
              <div><dt>成功率 / P95</dt><dd>{{ pct(candidate.success_rate) }} / {{ candidate.p95_latency_ms ? `${candidate.p95_latency_ms}ms` : '—' }}</dd></div>
              <div><dt>连续失败</dt><dd :class="(candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0) ? 'is-warn' : 'is-ok'">{{ candidate.credential_consecutive_failures ?? candidate.consecutive_failures ?? 0 }}</dd></div>
              <div><dt>生效窗口</dt><dd>{{ fmtTime(candidate.effective_at) }} ～ {{ fmtTime(candidate.expires_at) }}</dd></div>
            </dl>
          </section>

          <section v-if="models.length" class="nd-section">
            <h3>模型状态</h3>
            <div class="nd-models">
              <button v-for="model in models" :key="model.raw_model_name" class="nd-model" :class="{ active: selectedModel === model.raw_model_name }" @click="chooseModel(model.raw_model_name)">
                <strong>{{ model.raw_model_name }}</strong>
                <span :class="statusClass(model.effective_state === 'available' ? 'ready' : model.effective_state)">{{ model.effective_state }}</span>
                <span>{{ pct(model.recent_success_rate) }} · {{ model.p95_latency_ms ?? '—' }}ms</span>
              </button>
            </div>
          </section>

          <section v-if="selectedModel" class="nd-section">
            <h3>滑动窗口 <small>最近 1 小时{{ windowSource ? ` · ${windowSource}` : '' }}</small></h3>
            <div v-if="windowEntries.length" class="nd-window" :aria-label="`${selectedModel} 最近调用结果`">
              <span v-for="entry in windowEntries.slice(0, 100)" :key="`${entry.rid}-${entry.ts}`" class="nd-window-cell" :class="entry.ok ? 'ok' : 'bad'" :title="`${entry.ok ? '成功' : '失败'} · ${entry.lat}ms ${entry.err || ''}`" />
            </div>
            <p v-else class="nd-muted">该模型在窗口内没有调用样本。</p>
            <div v-if="windowStats" class="nd-stat-line">总计 {{ windowStats.total }} · 成功 {{ windowStats.success }} · 失败 {{ windowStats.failed }} · 失败率 {{ (windowStats.failure_rate * 100).toFixed(1) }}%</div>
            <div v-if="errorKinds.length" class="nd-error-kinds"><span v-for="[kind, count] in errorKinds" :key="kind">{{ kind }} × {{ count }}</span></div>
          </section>

          <section class="nd-section nd-two-col">
            <div>
              <h3>模型状态变化</h3>
              <p v-if="!history.length" class="nd-muted">暂无状态变化记录。</p>
              <ol v-else class="nd-history"><li v-for="(event, index) in history" :key="index"><time>{{ fmtTime(event.ts) }}</time><strong>{{ event.source === 'manual' ? '手动' : '自动' }} · {{ event.event }}</strong><span>{{ event.reason || event.error_code || event.error_message || '—' }}</span></li></ol>
            </div>
            <div>
              <h3>最近路由请求</h3>
              <p v-if="!decisions.length" class="nd-muted">暂无路由请求记录。</p>
              <div v-else class="nd-table-wrap"><table class="nd-table"><thead><tr><th>时间</th><th>请求</th><th>模型</th><th>结果</th><th>延迟</th><th>错误</th></tr></thead><tbody><tr v-for="decision in decisions" :key="decision.request_id"><td>{{ fmtTime(decision.ts) }}</td><td>{{ decision.request_id.slice(0, 8) }}</td><td>{{ decision.client_model || decision.model }}</td><td :class="decision.success ? 'is-ok' : 'is-bad'">{{ decision.success ? '成功' : '失败' }}</td><td>{{ decision.latency_ms == null ? '—' : `${decision.latency_ms}ms` }}</td><td>{{ decision.error_class || '—' }}</td></tr></tbody></table></div>
            </div>
          </section>
        </template>

        <template v-else>
          <p v-if="!canEdit" class="nd-notice nd-notice--warn">仅系统管理员可以维护节点；当前以只读方式展示。</p>
          <section class="nd-section">
            <h3>连通性与紧急维护</h3>
            <div class="nd-actions"><button class="btn btn-primary btn-sm" :disabled="saving || !node.provider_id" @click="testNow">{{ saving ? '处理中…' : '立即测试' }}</button><button class="btn btn-success btn-sm" :disabled="saving || !canEdit" @click="repair('force_enable')">强制启用</button><button class="btn btn-danger btn-sm" :disabled="saving || !canEdit" @click="repair('force_disable')">强制禁用</button><button class="btn btn-warning btn-sm" :disabled="saving || !canEdit" @click="repair('clear_circuit')">清除熔断</button><button class="btn btn-sm" :disabled="saving || !canEdit" @click="repair('reset_errors')">重置错误</button></div>
          </section>
          <section v-if="candidate" class="nd-section">
            <h3>路由排序与生命周期</h3>
            <div class="nd-form-grid"><label>人工优先级<input v-model.number="manualPriority" type="number" min="0" max="99" :disabled="!canEdit" /></label><label>Routing Tier<input v-model.number="routingTier" type="number" min="0" max="9" :disabled="!canEdit" /></label><label>权重<input v-model.number="weight" type="number" min="0" max="10000" :disabled="!canEdit" /></label><label>生命周期<select v-model="lifecycle" :disabled="!canEdit"><option value="active">active（在用）</option><option value="disabled">disabled（停用）</option><option value="suspended">suspended（暂停）</option><option value="retired">retired（退役）</option></select></label></div><button class="btn btn-primary btn-sm" :disabled="saving || !canEdit" @click="saveSettings">保存设置</button>
          </section>
          <section class="nd-section">
            <h3>凭据与模型维护</h3>
            <label class="nd-reason">维护原因（必填）<input v-model="modelActionReason" :disabled="!canEdit" placeholder="说明本次状态修改原因" /></label>
            <div class="nd-actions"><button class="btn btn-danger btn-sm" :disabled="saving || !canEdit" @click="setCredentialDisabled(true)">禁用凭据</button><button class="btn btn-success btn-sm" :disabled="saving || !canEdit" @click="setCredentialDisabled(false)">恢复凭据</button><button v-if="selectedModelStatus" class="btn btn-sm" :disabled="saving || !canEdit" @click="toggleSelectedModel">{{ selectedModelStatus.binding_unavailable_reason === 'manual_offline' ? '恢复当前模型' : '下线当前模型' }}</button></div>
          </section>
        </template>
      </div>
    </aside>
  </Teleport>
</template>

<style scoped>
.nd-mask { position: fixed; inset: 0; z-index: 3000; background: color-mix(in srgb, #000 38%, transparent); }
.nd-drawer { position: fixed; z-index: 3001; top: 0; right: 0; width: min(940px, 94vw); height: 100vh; display: flex; flex-direction: column; background: var(--kx-surface, var(--card)); box-shadow: -12px 0 32px rgba(0,0,0,.24); color: var(--kx-text, var(--text)); }
.nd-header { padding: 18px 22px 14px; border-bottom: 1px solid var(--kx-border, var(--border)); display:flex; justify-content:space-between; gap:16px; }
.nd-eyebrow,.nd-muted,small { color: var(--kx-text-secondary, var(--muted)); font-size:12px; }.nd-header h2 { margin:4px 0 7px; font-size:18px; overflow-wrap:anywhere; }.nd-header-actions,.nd-state-row,.nd-actions,.nd-error-kinds { display:flex; gap:8px; align-items:center; flex-wrap:wrap; }.nd-tabs { display:flex; gap:4px; padding:10px 22px 0; border-bottom:1px solid var(--kx-border, var(--border)); }.nd-tabs button { border:0; background:transparent; color:var(--kx-text-secondary, var(--muted)); padding:8px 12px; cursor:pointer; border-bottom:2px solid transparent; }.nd-tabs button.active { color:var(--kx-accent, var(--accent)); border-bottom-color:var(--kx-accent, var(--accent)); font-weight:600; }.nd-body { overflow:auto; padding:16px 22px 34px; }.nd-section { border:1px solid var(--kx-border, var(--border)); border-radius:8px; padding:14px; margin-bottom:12px; }.nd-section h3 { margin:0 0 12px; font-size:14px; }.nd-grid { display:grid; grid-template-columns:repeat(3, minmax(0,1fr)); gap:12px; margin:0; }.nd-grid div { min-width:0; }.nd-grid dt { color:var(--kx-text-secondary, var(--muted)); font-size:11px; margin-bottom:3px; }.nd-grid dd { margin:0; font-size:12px; overflow-wrap:anywhere; }.is-ok { color:var(--kx-success, #10b981); }.is-warn { color:var(--kx-warning, #f59e0b); }.is-bad { color:var(--kx-danger, #ef4444); }.nd-state { border-radius:999px; padding:2px 8px; border:1px solid currentColor; font-size:12px; }.nd-models { display:flex; gap:6px; flex-wrap:wrap; }.nd-model { display:grid; gap:3px; text-align:left; padding:8px; border:1px solid var(--kx-border, var(--border)); border-radius:6px; background:transparent; color:inherit; cursor:pointer; max-width:250px; }.nd-model.active { border-color:var(--kx-accent, var(--accent)); background:color-mix(in srgb, var(--kx-accent, var(--accent)) 8%, transparent); }.nd-model span { font-size:11px; }.nd-window { display:flex; align-items:stretch; height:26px; gap:2px; overflow:hidden; }.nd-window-cell { width:5px; min-width:3px; border-radius:2px; background:var(--kx-danger, #ef4444); }.nd-window-cell.ok { background:var(--kx-success, #10b981); }.nd-stat-line { font-size:12px; margin-top:8px; }.nd-error-kinds span { font-size:11px; border:1px solid var(--kx-border, var(--border)); border-radius:999px; padding:2px 7px; }.nd-two-col { display:grid; grid-template-columns:1fr 1.25fr; gap:16px; }.nd-history { padding:0; list-style:none; margin:0; }.nd-history li { display:grid; grid-template-columns:140px 100px 1fr; gap:8px; border-bottom:1px solid var(--kx-border, var(--border)); padding:7px 0; font-size:11px; }.nd-history time { color:var(--kx-text-secondary, var(--muted)); }.nd-table-wrap { overflow:auto; }.nd-table { border-collapse:collapse; width:100%; font-size:11px; }.nd-table th,.nd-table td { text-align:left; padding:6px; border-bottom:1px solid var(--kx-border, var(--border)); white-space:nowrap; }.nd-notice { padding:8px 10px; border-radius:6px; margin:0 0 12px; font-size:12px; }.nd-notice--warn { background:color-mix(in srgb, var(--kx-warning, #f59e0b) 12%, transparent); color:var(--kx-warning, #f59e0b); }.nd-notice--ok { background:color-mix(in srgb, var(--kx-success, #10b981) 12%, transparent); color:var(--kx-success, #10b981); }.nd-notice--error { background:color-mix(in srgb, var(--kx-danger, #ef4444) 12%, transparent); color:var(--kx-danger, #ef4444); }.nd-loading { padding:36px; text-align:center; color:var(--kx-text-secondary, var(--muted)); }.nd-form-grid { display:grid; grid-template-columns:repeat(2, minmax(0,1fr)); gap:12px; margin-bottom:12px; }.nd-form-grid label,.nd-reason { display:grid; gap:5px; font-size:12px; }.nd-form-grid input,.nd-form-grid select,.nd-reason input { box-sizing:border-box; width:100%; padding:7px; border:1px solid var(--kx-border, var(--border)); border-radius:5px; background:var(--kx-bg, var(--bg)); color:inherit; }.nd-reason { margin-bottom:10px; max-width:560px; }
@media (max-width:700px) { .nd-drawer { width:100vw; }.nd-header { padding:14px; }.nd-body { padding:12px; }.nd-grid,.nd-two-col,.nd-form-grid { grid-template-columns:1fr; }.nd-history li { grid-template-columns:1fr; gap:2px; }.nd-header { flex-direction:column; }.nd-header-actions { justify-content:flex-end; } }
</style>
