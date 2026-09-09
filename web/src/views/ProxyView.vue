<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted } from 'vue'
import {
  getProxySubscriptions,
  createProxySubscription,
  refreshProxySubscription,
  deleteProxySubscription,
  updateProxySubscription,
  getProxyNodes,
  createProxyNode,
  healthCheckProxyNode,
  deleteProxyNode,
  getProxyStatus,
  healthCheckAllProxyNodes,
  healthCheckProxySubscription,
  forceSwapProxy,
  getProxyPolicy,
  setProxyPolicy,
  getProxyRegions,
  setProxyNodeRegionBan,
  type ProxySubscription,
  type ProxyNode,
  type ProxyStatus,
  type ProxySelectionPolicy,
  type ProxyRegionStats,
  type ProxySelectionState,
} from '../api'
import { confirmDialog } from '../composables/useConfirmDialog'

const { t } = useI18n()

type Tab = 'status' | 'subscriptions' | 'nodes'
const activeTab = ref<Tab>('status')
const loadingCountByTab = ref<Record<Tab, number>>({
  status: 0,
  subscriptions: 0,
  nodes: 0,
})
const errorByTab = ref<Record<Tab, string>>({
  status: '',
  subscriptions: '',
  nodes: '',
})
const loading = computed(() => loadingCountByTab.value[activeTab.value] > 0)
const error = computed(() => errorByTab.value[activeTab.value])

function setError(tab: Tab, message: string) {
  errorByTab.value[tab] = message
}
function beginLoading(tab: Tab) {
  loadingCountByTab.value[tab] += 1
}
function endLoading(tab: Tab) {
  loadingCountByTab.value[tab] = Math.max(0, loadingCountByTab.value[tab] - 1)
}
async function loadForTab(tab: Tab, load: () => Promise<void>, fallbackError: string) {
  beginLoading(tab)
  setError(tab, '')
  try {
    await load()
  } catch (e: unknown) {
    setError(tab, e instanceof Error ? e.message : fallbackError)
  } finally {
    endLoading(tab)
  }
}

// ── 状态 ──
const status = ref<ProxyStatus | null>(null)
const regions = ref<ProxyRegionStats[]>([])
const policy = ref<ProxySelectionPolicy | null>(null)
const swapState = ref<ProxySelectionState | null>(null)

async function loadStatus() {
  await loadForTab('status', async () => {
    status.value = await getProxyStatus()
    if (status.value?.regions) regions.value = status.value.regions
    if (status.value?.policy) policy.value = status.value.policy
    if (status.value?.swap_state) swapState.value = status.value.swap_state
  }, t('proxy.error.loadStatusFailed'))
}

async function loadRegions() {
  try {
    const data = await getProxyRegions()
    regions.value = data.items ?? []
  } catch { /* ignore */ }
}

async function loadPolicy() {
  try {
    const data = await getProxyPolicy()
    policy.value = data.policy
    swapState.value = data.swap_state
  } catch { /* ignore */ }
}

// ── 批量操作 ──
const batchBusy = ref<string | null>(null)
async function withBatchBusy(name: string, fn: () => Promise<void>) {
  batchBusy.value = name
  try { await fn() } finally { batchBusy.value = null }
}

async function handleBatchHealthCheckAll() {
  if (!(await confirmDialog(t('proxy.confirmBatchAll')))) return
  await withBatchBusy('health-all', async () => {
    try {
      const res = await healthCheckAllProxyNodes()
      alert(t('proxy.batchResult.healthAll', {
        ok: res.ok_count ?? 0, failed: res.failed_count ?? 0, skipped: res.skipped ?? 0,
      }))
      await loadStatus()
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : t('proxy.error.batchHealthFailed'))
    }
  })
}

async function handleBatchHealthCheckSub(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmBatchSub', { name })))) return
  await withBatchBusy(`sub-${id}`, async () => {
    try {
      const res = await healthCheckProxySubscription(id)
      alert(t('proxy.batchResult.healthSub', {
        name, ok: res.ok_count ?? 0, failed: res.failed_count ?? 0,
      }))
      await loadSubscriptions()
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : t('proxy.error.healthCheckFailed'))
    }
  })
}

async function handleForceSwap() {
  await withBatchBusy('swap', async () => {
    try {
      const res = await forceSwapProxy()
      alert(t('proxy.batchResult.swap', { name: res.selected.name }))
      await loadStatus()
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : t('proxy.error.swapFailed'))
    }
  })
}

// ── 策略 ──
const policyDraft = ref<Partial<ProxySelectionPolicy>>({})
const showPolicyModal = ref(false)
async function openPolicyEditor() {
  if (!policy.value) await loadPolicy()
  policyDraft.value = { ...(policy.value || {}) }
  showPolicyModal.value = true
}
async function savePolicy() {
  await withBatchBusy('policy', async () => {
    try {
      const res = await setProxyPolicy(policyDraft.value)
      policy.value = res.policy
      showPolicyModal.value = false
      alert(t('proxy.batchResult.policySaved'))
    } catch (e: unknown) {
      alert(e instanceof Error ? e.message : t('proxy.error.savePolicyFailed'))
    }
  })
}

// ── 订阅 ──
const subscriptions = ref<ProxySubscription[]>([])
const safeSubscriptions = computed(() => subscriptions.value.filter(
  (sub): sub is ProxySubscription => !!sub && typeof sub === 'object'
    && typeof sub.id === 'number' && typeof sub.name === 'string'
    && typeof sub.subscribe_url === 'string' && typeof sub.status === 'string'
    && typeof sub.node_count === 'number'
    && (sub.last_fetch_at === null || typeof sub.last_fetch_at === 'string'),
))
const showCreateSub = ref(false)
const subForm = ref({
  name: '', subscribe_url: '', notes: '', banned_regions_input: '',
})

async function loadSubscriptions() {
  await loadForTab('subscriptions', async () => {
    subscriptions.value = await getProxySubscriptions()
  }, t('proxy.error.loadSubsFailed'))
}

function parseRegionInput(raw: string): string[] {
  return Array.from(new Set(
    raw.split(/[\s,]+/)
      .map(s => s.trim().toUpperCase())
      .filter(s => s.length > 0),
  ))
}

async function handleCreateSub() {
  if (!subForm.value.name || !subForm.value.subscribe_url) {
    setError('subscriptions', t('proxy.error.nameUrlRequired'))
    return
  }
  const urlLower = subForm.value.subscribe_url.toLowerCase()
  if (!urlLower.startsWith('http://') && !urlLower.startsWith('https://')) {
    setError('subscriptions', t('proxy.error.subscribeUrlMustHttp'))
    return
  }
  beginLoading('subscriptions')
  setError('subscriptions', '')
  try {
    const payload = {
      name: subForm.value.name,
      subscribe_url: subForm.value.subscribe_url,
      notes: subForm.value.notes,
      banned_regions: parseRegionInput(subForm.value.banned_regions_input),
    }
    await createProxySubscription(payload)
    showCreateSub.value = false
    subForm.value = { name: '', subscribe_url: '', notes: '', banned_regions_input: '' }
    await loadSubscriptions()
  } catch (e: unknown) {
    setError('subscriptions', e instanceof Error ? e.message : t('proxy.error.createSubFailed'))
  } finally {
    endLoading('subscriptions')
  }
}

async function handleRefreshSub(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmRefresh', { name })))) return
  beginLoading('subscriptions')
  setError('subscriptions', '')
  try {
    await refreshProxySubscription(id)
    await loadSubscriptions()
  } catch (e: unknown) {
    setError('subscriptions', e instanceof Error ? e.message : t('proxy.error.refreshSubFailed'))
  } finally {
    endLoading('subscriptions')
  }
}

async function handleDeleteSub(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmDeleteSub', { name })))) return
  beginLoading('subscriptions')
  setError('subscriptions', '')
  try {
    await deleteProxySubscription(id)
    await loadSubscriptions()
  } catch (e: unknown) {
    setError('subscriptions', e instanceof Error ? e.message : t('proxy.error.deleteSubFailed'))
  } finally {
    endLoading('subscriptions')
  }
}

const editingSubBan = ref<number | null>(null)
const editSubBanInput = ref('')
function startEditSubBan(sub: ProxySubscription) {
  editingSubBan.value = sub.id
  editSubBanInput.value = (sub.banned_regions || []).join(', ')
}
async function saveSubBan(sub: ProxySubscription) {
  beginLoading('subscriptions')
  setError('subscriptions', '')
  try {
    await updateProxySubscription(sub.id, {
      banned_regions: parseRegionInput(editSubBanInput.value),
    })
    editingSubBan.value = null
    await loadSubscriptions()
  } catch (e: unknown) {
    setError('subscriptions', e instanceof Error ? e.message : t('proxy.error.saveSubBanFailed'))
  } finally {
    endLoading('subscriptions')
  }
}

// ── 节点 ──
const nodes = ref<ProxyNode[]>([])
const safeNodes = computed(() => nodes.value.filter(
  (node): node is ProxyNode => !!node && typeof node === 'object'
    && typeof node.id === 'number' && typeof node.name === 'string'
    && typeof node.protocol === 'string' && typeof node.server === 'string'
    && typeof node.port === 'number' && typeof node.dialable === 'boolean'
    && typeof node.status === 'string' && typeof node.response_time_ms === 'number'
    && typeof node.consecutive_failures === 'number',
))
const showCreateNode = ref(false)
const nodeForm = ref({
  subscription_id: 0,
  name: '',
  protocol: 'http',
  server: '',
  port: 8080,
  username: '',
  password: '',
  location: '',
  banned_regions_input: '',
  health_check_url: 'https://www.google.com/generate_204',
})

async function loadNodes() {
  await loadForTab('nodes', async () => {
    nodes.value = await getProxyNodes()
  }, t('proxy.error.loadNodesFailed'))
}

async function handleCreateNode() {
  if (!nodeForm.value.name || !nodeForm.value.server || nodeForm.value.subscription_id <= 0) {
    setError('nodes', t('proxy.error.nodeFieldsRequired'))
    return
  }
  const allowed = ['http', 'https', 'socks5']
  if (!allowed.includes(nodeForm.value.protocol)) {
    setError('nodes', t('proxy.error.protocolNotDialable'))
    return
  }
  beginLoading('nodes')
  setError('nodes', '')
  try {
    const payload = {
      subscription_id: nodeForm.value.subscription_id,
      name: nodeForm.value.name,
      protocol: nodeForm.value.protocol,
      server: nodeForm.value.server,
      port: nodeForm.value.port,
      username: nodeForm.value.username,
      password: nodeForm.value.password,
      location: nodeForm.value.location,
      health_check_url: nodeForm.value.health_check_url,
      banned_regions: parseRegionInput(nodeForm.value.banned_regions_input),
    } as Parameters<typeof createProxyNode>[0] & { banned_regions: string[] }
    await createProxyNode(payload)
    showCreateNode.value = false
    nodeForm.value = {
      subscription_id: 0, name: '', protocol: 'http', server: '', port: 8080,
      username: '', password: '', location: '', banned_regions_input: '',
      health_check_url: 'https://www.google.com/generate_204',
    }
    await loadNodes()
  } catch (e: unknown) {
    setError('nodes', e instanceof Error ? e.message : t('proxy.error.createNodeFailed'))
  } finally {
    endLoading('nodes')
  }
}

async function handleHealthCheckNode(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmHealthCheck', { name })))) return
  beginLoading('nodes')
  setError('nodes', '')
  try {
    await healthCheckProxyNode(id)
    await loadNodes()
  } catch (e: unknown) {
    setError('nodes', e instanceof Error ? e.message : t('proxy.error.healthCheckFailed'))
  } finally {
    endLoading('nodes')
  }
}

async function handleDeleteNode(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmDeleteNode', { name })))) return
  beginLoading('nodes')
  setError('nodes', '')
  try {
    await deleteProxyNode(id)
    await loadNodes()
  } catch (e: unknown) {
    setError('nodes', e instanceof Error ? e.message : t('proxy.error.deleteNodeFailed'))
  } finally {
    endLoading('nodes')
  }
}

const editingNodeBan = ref<number | null>(null)
const editNodeBanInput = ref('')
function startEditNodeBan(node: ProxyNode) {
  editingNodeBan.value = node.id
  editNodeBanInput.value = (node.banned_regions || []).join(', ')
}
async function saveNodeBan(node: ProxyNode) {
  beginLoading('nodes')
  setError('nodes', '')
  try {
    await setProxyNodeRegionBan(node.id, parseRegionInput(editNodeBanInput.value))
    editingNodeBan.value = null
    await loadNodes()
  } catch (e: unknown) {
    setError('nodes', e instanceof Error ? e.message : t('proxy.error.saveNodeBanFailed'))
  } finally {
    endLoading('nodes')
  }
}

onMounted(() => {
  loadStatus()
  loadSubscriptions()
  loadNodes()
  loadRegions()
  loadPolicy()
})

function switchTab(tab: Tab) {
  activeTab.value = tab
}

const subOptions = computed(() => {
  return (subscriptions.value ?? [])
    .filter((s: ProxySubscription | null): s is ProxySubscription => !!s && s.status === 'active')
    .map((s: ProxySubscription) => ({ value: s.id, label: `${s.name} (${s.node_count} nodes)` }))
})

// 策略下拉常量（保持前后端枚举一致）
const LB_STRATEGIES = ['best_only', 'round_robin', 'weighted_rr', 'least_conn', 'consistent_hash']
const AFFINITY_POLICIES = ['any', 'prefer_same', 'require_same']
</script>

<template>
  <div class="proxy-view">
    <h1>{{ t('proxy.title') }}</h1>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div class="tabs">
      <button
        :class="{ active: activeTab === 'status' }"
        @click="switchTab('status')"
      >
        {{ t('proxy.tabs.status') }}
      </button>
      <button
        :class="{ active: activeTab === 'subscriptions' }"
        @click="switchTab('subscriptions')"
      >
        {{ t('proxy.tabs.subscriptions') }}
      </button>
      <button
        :class="{ active: activeTab === 'nodes' }"
        @click="switchTab('nodes')"
      >
        {{ t('proxy.tabs.nodes') }}
      </button>
    </div>

    <!-- 状态 Tab -->
    <div v-if="activeTab === 'status'" class="tab-content">
      <div class="actions">
        <button class="btn-primary" :disabled="batchBusy === 'health-all'" @click="handleBatchHealthCheckAll">
          {{ batchBusy === 'health-all' ? t('common.loading') + '…' : t('proxy.actions.batchHealthCheckAll') }}
        </button>
        <button class="btn-primary" :disabled="batchBusy === 'swap'" @click="handleForceSwap">
          {{ batchBusy === 'swap' ? t('common.loading') + '…' : t('proxy.actions.forceSwap') }}
        </button>
        <button class="btn-secondary" @click="loadStatus">{{ t('common.refresh') }}</button>
      </div>
      <div v-if="loading" class="loading">{{ t('common.loading') }}...</div>
      <div v-else-if="status" class="status-overview">
        <div class="status-card">
          <h3>{{ t('proxy.status.overview') }}</h3>
          <p><strong>{{ t('proxy.status.subscriptions') }}:</strong> {{ status.active_subscriptions }} / {{ status.subscription_count }}</p>
          <p><strong>{{ t('proxy.status.nodes') }}:</strong> {{ status.node_count }}</p>
          <p><strong>{{ t('proxy.status.dialable') }}:</strong> {{ status.dialable_count }}</p>
          <p><strong>{{ t('proxy.status.healthy') }}:</strong> {{ status.healthy_count }}</p>
          <p><strong>{{ t('proxy.status.unhealthy') }}:</strong> {{ status.unhealthy_count }}</p>
        </div>
        <div class="status-card">
          <h3>{{ t('proxy.status.selectedNode') }}</h3>
          <div v-if="status.selected_node">
            <p><strong>{{ t('proxy.node.name') }}:</strong> {{ status.selected_node.name }}</p>
            <p><strong>{{ t('proxy.node.protocol') }}:</strong> {{ status.selected_node.protocol }}</p>
            <p><strong>{{ t('proxy.node.server') }}:</strong> {{ status.selected_node.server }}:{{ status.selected_node.port }}</p>
            <p><strong>{{ t('proxy.node.latency') }}:</strong> {{ status.selected_node.response_time_ms }}ms</p>
          </div>
          <div v-else-if="status.selection_error" class="error-text">
            {{ status.selection_error }}
          </div>
          <div v-else>{{ t('proxy.status.noNodeSelected') }}</div>
        </div>
        <div class="status-card">
          <h3>{{ t('proxy.status.swapState') }}</h3>
          <div v-if="status.swap_state">
            <p><strong>{{ t('proxy.status.currentNode') }}:</strong> #{{ status.swap_state.current_node_id }}</p>
            <p><strong>{{ t('proxy.status.lastProbeAt') }}:</strong> {{ status.swap_state.last_probe_at || t('common.never') }}</p>
            <p><strong>{{ t('proxy.status.consecutiveFails') }}:</strong> {{ status.swap_state.consecutive_fails }}</p>
            <p><strong>{{ t('proxy.status.probeInterval') }}:</strong> {{ status.swap_state.probe_interval_ms }} ms</p>
          </div>
          <div v-else>{{ t('proxy.status.swapIdle') }}</div>
        </div>
        <div class="status-card status-card-wide">
          <h3>{{ t('proxy.status.regions') }}</h3>
          <div v-if="regions.length === 0" class="empty-state">{{ t('proxy.regions.empty') }}</div>
          <table v-else class="data-table regions-table">
            <thead>
              <tr>
                <th>{{ t('proxy.regions.region') }}</th>
                <th>{{ t('proxy.regions.total') }}</th>
                <th>{{ t('proxy.regions.dialable') }}</th>
                <th>{{ t('proxy.regions.active') }}</th>
                <th>{{ t('proxy.regions.unhealthy') }}</th>
                <th>{{ t('proxy.regions.banned') }}</th>
                <th>{{ t('proxy.regions.avgLatency') }}</th>
                <th>{{ t('proxy.regions.bestNode') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in regions" :key="r.region">
                <td>{{ r.region }}</td>
                <td>{{ r.total }}</td>
                <td>{{ r.dialable }}</td>
                <td>{{ r.active }}</td>
                <td>{{ r.unhealthy }}</td>
                <td :class="{ 'status-error': r.banned > 0 }">{{ r.banned }}</td>
                <td>{{ r.avg_latency_ms }} ms</td>
                <td>
                  <span v-if="r.best_node_name">{{ r.best_node_name }} ({{ r.best_latency_ms }}ms)</span>
                  <span v-else>-</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="status-card status-card-wide">
          <h3>{{ t('proxy.status.policyTitle') }}</h3>
          <div v-if="policy">
            <p><strong>{{ t('proxy.policy.lbStrategy') }}:</strong> {{ policy.load_balance_strategy }}</p>
            <p><strong>{{ t('proxy.policy.affinity') }}:</strong> {{ policy.location_affinity }}</p>
            <p><strong>{{ t('proxy.policy.autoDisableThreshold') }}:</strong> {{ policy.auto_disable_threshold }}</p>
            <p><strong>{{ t('proxy.policy.autoDisableEnabled') }}:</strong> {{ policy.auto_disable_enabled ? t('common.yes') : t('common.no') }}</p>
            <p><strong>{{ t('proxy.policy.autoRecoverEnabled') }}:</strong> {{ policy.auto_recover_enabled ? t('common.yes') : t('common.no') }}</p>
            <p><strong>{{ t('proxy.policy.swapIntervalMs') }}:</strong> {{ policy.swap_check_interval_ms }}</p>
            <p><strong>{{ t('proxy.policy.swapFailThreshold') }}:</strong> {{ policy.swap_failure_threshold }}</p>
            <div class="actions">
              <button class="btn-secondary" @click="openPolicyEditor">{{ t('proxy.policy.edit') }}</button>
            </div>
          </div>
          <div v-else>{{ t('proxy.policy.loading') }}</div>
        </div>
        <div v-if="status.warning" class="warning-banner">
          {{ status.warning }}
        </div>
      </div>

      <!-- 策略编辑 Modal -->
      <div v-if="showPolicyModal" class="modal-overlay" @click.self="showPolicyModal = false">
        <div class="modal-content">
          <h2>{{ t('proxy.policy.editTitle') }}</h2>
          <form @submit.prevent="savePolicy">
            <div class="form-group">
              <label>{{ t('proxy.policy.lbStrategy') }}</label>
              <select v-model="policyDraft.load_balance_strategy">
                <option v-for="s in LB_STRATEGIES" :key="s" :value="s">{{ s }}</option>
              </select>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.policy.affinity') }}</label>
              <select v-model="policyDraft.location_affinity">
                <option v-for="s in AFFINITY_POLICIES" :key="s" :value="s">{{ s }}</option>
              </select>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.policy.autoDisableThreshold') }}</label>
              <input v-model.number="policyDraft.auto_disable_threshold" type="number" min="1" />
            </div>
            <div class="form-group form-group-inline">
              <label>
                <input type="checkbox" v-model="policyDraft.auto_disable_enabled" />
                {{ t('proxy.policy.autoDisableEnabled') }}
              </label>
              <label>
                <input type="checkbox" v-model="policyDraft.auto_recover_enabled" />
                {{ t('proxy.policy.autoRecoverEnabled') }}
              </label>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.policy.swapIntervalMs') }}</label>
              <input v-model.number="policyDraft.swap_check_interval_ms" type="number" min="1000" step="500" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.policy.swapFailThreshold') }}</label>
              <input v-model.number="policyDraft.swap_failure_threshold" type="number" min="1" />
            </div>
            <div class="modal-actions">
              <button type="submit" class="btn-primary" :disabled="batchBusy === 'policy'">
                {{ batchBusy === 'policy' ? t('common.loading') + '…' : t('common.save') }}
              </button>
              <button type="button" class="btn-secondary" @click="showPolicyModal = false">{{ t('common.cancel') }}</button>
            </div>
          </form>
        </div>
      </div>
    </div>

    <!-- 订阅 Tab -->
    <div v-if="activeTab === 'subscriptions'" class="tab-content">
      <div class="actions">
        <button @click="showCreateSub = true" class="btn-primary">{{ t('proxy.subscriptions.create') }}</button>
        <button @click="loadSubscriptions" class="btn-secondary">{{ t('common.refresh') }}</button>
      </div>
      <div v-if="loading" class="loading">{{ t('common.loading') }}...</div>
      <table v-else class="data-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>{{ t('proxy.subscriptions.name') }}</th>
            <th>{{ t('proxy.subscriptions.url') }}</th>
            <th>{{ t('proxy.subscriptions.nodeCount') }}</th>
            <th>{{ t('proxy.subscriptions.bannedRegions') }}</th>
            <th>{{ t('proxy.subscriptions.status') }}</th>
            <th>{{ t('proxy.subscriptions.lastFetch') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="sub in safeSubscriptions" :key="sub.id">
            <td>{{ sub.id }}</td>
            <td>{{ sub.name }}</td>
            <td class="url-cell">{{ sub.subscribe_url }}</td>
            <td>{{ sub.node_count }}</td>
            <td>
              <div v-if="editingSubBan === sub.id" class="inline-edit">
                <input v-model="editSubBanInput" type="text" placeholder="US, JP" />
                <button class="btn-small" @click="saveSubBan(sub)">{{ t('common.save') }}</button>
                <button class="btn-small btn-secondary" @click="editingSubBan = null">{{ t('common.cancel') }}</button>
              </div>
              <div v-else>
                <span v-if="(sub.banned_regions || []).length === 0">-</span>
                <span v-else class="ban-chip">{{ (sub.banned_regions || []).join(', ') }}</span>
                <button class="btn-small" @click="startEditSubBan(sub)">{{ t('common.edit') }}</button>
              </div>
            </td>
            <td :class="{ 'status-active': sub.status === 'active', 'status-error': sub.status === 'error' }">
              {{ sub.status }}
            </td>
            <td>{{ sub.last_fetch_at || t('common.never') }}</td>
            <td>
              <button @click="handleBatchHealthCheckSub(sub.id, sub.name)" class="btn-small">
                {{ t('proxy.subscriptions.batchHealthCheck') }}
              </button>
              <button @click="handleRefreshSub(sub.id, sub.name)" class="btn-small">{{ t('proxy.subscriptions.refresh') }}</button>
              <button @click="handleDeleteSub(sub.id, sub.name)" class="btn-small btn-danger">{{ t('common.delete') }}</button>
            </td>
          </tr>
          <tr v-if="safeSubscriptions.length === 0">
            <td colspan="8" class="empty-state">{{ t('proxy.subscriptions.empty') }}</td>
          </tr>
        </tbody>
      </table>

      <!-- 创建订阅 Modal -->
      <div v-if="showCreateSub" class="modal-overlay" @click.self="showCreateSub = false">
        <div class="modal-content">
          <h2>{{ t('proxy.subscriptions.createTitle') }}</h2>
          <form @submit.prevent="handleCreateSub">
            <div class="form-group">
              <label>{{ t('proxy.subscriptions.name') }} *</label>
              <input v-model="subForm.name" type="text" required />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.subscriptions.url') }} *</label>
              <input v-model="subForm.subscribe_url" type="url" required placeholder="https://..." />
              <small>{{ t('proxy.subscriptions.urlHint') }}</small>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.subscriptions.notes') }}</label>
              <textarea v-model="subForm.notes" rows="3"></textarea>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.subscriptions.bannedRegions') }}</label>
              <input v-model="subForm.banned_regions_input" type="text" placeholder="US, JP, HK" />
              <small>{{ t('proxy.subscriptions.bannedRegionsHint') }}</small>
            </div>
            <div class="modal-actions">
              <button type="submit" class="btn-primary">{{ t('common.create') }}</button>
              <button type="button" @click="showCreateSub = false" class="btn-secondary">{{ t('common.cancel') }}</button>
            </div>
          </form>
        </div>
      </div>
    </div>

    <!-- 节点 Tab -->
    <div v-if="activeTab === 'nodes'" class="tab-content">
      <div class="actions">
        <button @click="showCreateNode = true" class="btn-primary">{{ t('proxy.nodes.create') }}</button>
        <button @click="loadNodes" class="btn-secondary">{{ t('common.refresh') }}</button>
      </div>
      <div v-if="loading" class="loading">{{ t('common.loading') }}...</div>
      <table v-else class="data-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>{{ t('proxy.node.name') }}</th>
            <th>{{ t('proxy.node.protocol') }}</th>
            <th>{{ t('proxy.node.server') }}</th>
            <th>{{ t('proxy.node.location') }}</th>
            <th>{{ t('proxy.node.bannedRegions') }}</th>
            <th>{{ t('proxy.node.dialable') }}</th>
            <th>{{ t('proxy.node.status') }}</th>
            <th>{{ t('proxy.node.latency') }}</th>
            <th>{{ t('proxy.node.failures') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in safeNodes" :key="node.id">
            <td>{{ node.id }}</td>
            <td>{{ node.name }}</td>
            <td>{{ node.protocol }}</td>
            <td>{{ node.server }}:{{ node.port }}</td>
            <td>{{ node.location || '-' }}</td>
            <td>
              <div v-if="editingNodeBan === node.id" class="inline-edit">
                <input v-model="editNodeBanInput" type="text" placeholder="US, JP" />
                <button class="btn-small" @click="saveNodeBan(node)">{{ t('common.save') }}</button>
                <button class="btn-small btn-secondary" @click="editingNodeBan = null">{{ t('common.cancel') }}</button>
              </div>
              <div v-else>
                <span v-if="(node.banned_regions || []).length === 0">-</span>
                <span v-else class="ban-chip">{{ (node.banned_regions || []).join(', ') }}</span>
                <button class="btn-small" @click="startEditNodeBan(node)">{{ t('common.edit') }}</button>
              </div>
            </td>
            <td :class="{ 'status-ok': node.dialable, 'status-error': !node.dialable }">
              {{ node.dialable ? t('common.yes') : t('common.no') }}
            </td>
            <td :class="{ 'status-active': node.status === 'active', 'status-error': node.status === 'unhealthy' }">
              {{ node.status }}
            </td>
            <td>{{ node.response_time_ms }}ms</td>
            <td :class="{ 'status-error': node.consecutive_failures >= 3 }">{{ node.consecutive_failures }}</td>
            <td>
              <button @click="handleHealthCheckNode(node.id, node.name)" class="btn-small">{{ t('proxy.nodes.healthCheck') }}</button>
              <button @click="handleDeleteNode(node.id, node.name)" class="btn-small btn-danger">{{ t('common.delete') }}</button>
            </td>
          </tr>
          <tr v-if="safeNodes.length === 0">
            <td colspan="11" class="empty-state">{{ t('proxy.nodes.empty') }}</td>
          </tr>
        </tbody>
      </table>

      <!-- 创建节点 Modal -->
      <div v-if="showCreateNode" class="modal-overlay" @click.self="showCreateNode = false">
        <div class="modal-content">
          <h2>{{ t('proxy.nodes.createTitle') }}</h2>
          <form @submit.prevent="handleCreateNode">
            <div class="form-group">
              <label>{{ t('proxy.nodes.subscription') }} *</label>
              <select v-model.number="nodeForm.subscription_id" required>
                <option :value="0" disabled>{{ t('proxy.nodes.selectSubscription') }}</option>
                <option v-for="opt in subOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </select>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.name') }} *</label>
              <input v-model="nodeForm.name" type="text" required />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.protocol') }} *</label>
              <select v-model="nodeForm.protocol" required>
                <option value="http">HTTP</option>
                <option value="https">HTTPS</option>
                <option value="socks5">SOCKS5</option>
              </select>
              <small>{{ t('proxy.nodes.protocolHint') }}</small>
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.server') }} *</label>
              <input v-model="nodeForm.server" type="text" required placeholder="127.0.0.1" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.port') }} *</label>
              <input v-model.number="nodeForm.port" type="number" required min="1" max="65535" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.username') }}</label>
              <input v-model="nodeForm.username" type="text" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.password') }}</label>
              <input v-model="nodeForm.password" type="password" autocomplete="new-password" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.location') }}</label>
              <input v-model="nodeForm.location" type="text" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.bannedRegions') }}</label>
              <input v-model="nodeForm.banned_regions_input" type="text" placeholder="US, JP" />
            </div>
            <div class="form-group">
              <label>{{ t('proxy.node.healthCheckUrl') }}</label>
              <input v-model="nodeForm.health_check_url" type="url" />
            </div>
            <div class="modal-actions">
              <button type="submit" class="btn-primary">{{ t('common.create') }}</button>
              <button type="button" @click="showCreateNode = false" class="btn-secondary">{{ t('common.cancel') }}</button>
            </div>
          </form>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.proxy-view {
  padding: 2rem;
  max-width: 1400px;
  margin: 0 auto;
}

h1 {
  margin-bottom: 1.5rem;
  color: var(--text-primary);
}

.error-banner {
  background: var(--error-bg);
  color: var(--error-text);
  padding: 0.75rem 1rem;
  border-radius: 4px;
  margin-bottom: 1rem;
}

.warning-banner {
  background: var(--warning-bg);
  color: var(--warning-text);
  padding: 0.75rem 1rem;
  border-radius: 4px;
  margin-top: 1rem;
}

.tabs {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 1.5rem;
  border-bottom: 2px solid var(--border-color);
}

.tabs button {
  padding: 0.75rem 1.5rem;
  border: none;
  background: transparent;
  cursor: pointer;
  font-size: 1rem;
  color: var(--text-secondary);
  border-bottom: 3px solid transparent;
  transition: all 0.2s;
}

.tabs button:hover {
  color: var(--text-primary);
}

.tabs button.active {
  color: var(--primary-color);
  border-bottom-color: var(--primary-color);
}

.tab-content {
  min-height: 400px;
}

.actions {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 1rem;
  flex-wrap: wrap;
}

.loading {
  text-align: center;
  padding: 2rem;
  color: var(--text-secondary);
}

.status-overview {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 1rem;
}

.status-card {
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 8px;
  padding: 1.5rem;
}

.status-card-wide {
  grid-column: 1 / -1;
}

.status-card h3 {
  margin-top: 0;
  margin-bottom: 1rem;
  color: var(--text-primary);
}

.status-card p {
  margin: 0.5rem 0;
}

.error-text {
  color: var(--error-text);
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: 8px;
}

.data-table th,
.data-table td {
  padding: 0.75rem;
  text-align: left;
  border-bottom: 1px solid var(--border-color);
}

.data-table th {
  background: var(--header-bg);
  font-weight: 600;
  color: var(--text-primary);
}

.data-table tr:last-child td {
  border-bottom: none;
}

.url-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty-state {
  text-align: center;
  color: var(--text-secondary);
  padding: 2rem !important;
}

.status-active {
  color: var(--success-color);
  font-weight: 500;
}

.status-error {
  color: var(--error-text);
  font-weight: 500;
}

.status-ok {
  color: var(--success-color);
}

.ban-chip {
  display: inline-block;
  background: var(--warning-bg);
  color: var(--warning-text);
  padding: 0.15rem 0.5rem;
  border-radius: 4px;
  margin-right: 0.5rem;
  font-size: 0.85rem;
}

.inline-edit {
  display: flex;
  align-items: center;
  gap: 0.25rem;
}

.inline-edit input {
  width: 100px;
  padding: 0.25rem 0.5rem;
}

.regions-table th,
.regions-table td {
  padding: 0.5rem 0.75rem;
  font-size: 0.9rem;
}

.btn-primary,
.btn-secondary,
.btn-small,
.btn-danger {
  padding: 0.5rem 1rem;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.9rem;
  transition: all 0.2s;
}

.btn-primary {
  background: var(--primary-color);
  color: #fff;
}

.btn-primary:hover {
  background: var(--primary-hover);
}

.btn-primary:disabled {
  background: var(--secondary-bg);
  cursor: not-allowed;
}

.btn-secondary {
  background: var(--secondary-bg);
  color: #fff;
}

.btn-secondary:hover {
  background: var(--secondary-hover);
}

.btn-small {
  padding: 0.25rem 0.75rem;
  font-size: 0.85rem;
  margin-right: 0.5rem;
  background: var(--secondary-bg);
  color: #fff;
}

.btn-small:hover {
  background: var(--secondary-hover);
}

.btn-danger {
  background: var(--error-bg);
  color: #fff;
}

.btn-danger:hover {
  background: var(--error-hover);
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-content {
  background: var(--card-bg);
  border-radius: 8px;
  padding: 2rem;
  max-width: 600px;
  width: 90%;
  max-height: 80vh;
  overflow-y: auto;
}

.modal-content h2 {
  margin-top: 0;
  margin-bottom: 1.5rem;
  color: var(--text-primary);
}

.form-group {
  margin-bottom: 1rem;
}

.form-group-inline label {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  margin-right: 1.5rem;
  font-weight: 400;
}

.form-group label {
  display: block;
  margin-bottom: 0.25rem;
  font-weight: 500;
  color: var(--text-primary);
}

.form-group input,
.form-group select,
.form-group textarea {
  width: 100%;
  padding: 0.5rem;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  font-size: 1rem;
}

.form-group small {
  display: block;
  margin-top: 0.25rem;
  color: var(--text-secondary);
  font-size: 0.85rem;
}

.modal-actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
  margin-top: 1.5rem;
}
</style>