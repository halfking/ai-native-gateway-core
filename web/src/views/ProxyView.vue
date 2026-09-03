<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted } from 'vue'
import {
  getProxySubscriptions,
  createProxySubscription,
  refreshProxySubscription,
  deleteProxySubscription,
  getProxyNodes,
  createProxyNode,
  healthCheckProxyNode,
  deleteProxyNode,
  getProxyStatus,
  type ProxySubscription,
  type ProxyNode,
  type ProxyStatus,
} from '../api'
import { confirmDialog } from '../composables/useConfirmDialog'

const { t } = useI18n()

const activeTab = ref<'status' | 'subscriptions' | 'nodes'>('status')
const loading = ref(false)
const error = ref('')

// ── 状态 ──
const status = ref<ProxyStatus | null>(null)

async function loadStatus() {
  loading.value = true
  error.value = ''
  try {
    status.value = await getProxyStatus()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.loadStatusFailed')
  } finally {
    loading.value = false
  }
}

// ── 订阅 ──
const subscriptions = ref<ProxySubscription[]>([])
const showCreateSub = ref(false)
const subForm = ref({ name: '', subscribe_url: '', notes: '' })

async function loadSubscriptions() {
  loading.value = true
  error.value = ''
  try {
    const list = await getProxySubscriptions()
    subscriptions.value = Array.isArray(list) ? list : []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.loadSubsFailed')
  } finally {
    loading.value = false
  }
}

async function handleCreateSub() {
  if (!subForm.value.name || !subForm.value.subscribe_url) {
    error.value = t('proxy.error.nameUrlRequired')
    return
  }
  // 校验订阅 URL 必须为 http/https
  const urlLower = subForm.value.subscribe_url.toLowerCase()
  if (!urlLower.startsWith('http://') && !urlLower.startsWith('https://')) {
    error.value = t('proxy.error.subscribeUrlMustHttp')
    return
  }
  loading.value = true
  error.value = ''
  try {
    await createProxySubscription(subForm.value)
    showCreateSub.value = false
    subForm.value = { name: '', subscribe_url: '', notes: '' }
    await loadSubscriptions()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.createSubFailed')
  } finally {
    loading.value = false
  }
}

async function handleRefreshSub(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmRefresh', { name })))) return
  loading.value = true
  error.value = ''
  try {
    await refreshProxySubscription(id)
    await loadSubscriptions()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.refreshSubFailed')
  } finally {
    loading.value = false
  }
}

async function handleDeleteSub(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmDeleteSub', { name })))) return
  loading.value = true
  error.value = ''
  try {
    await deleteProxySubscription(id)
    await loadSubscriptions()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.deleteSubFailed')
  } finally {
    loading.value = false
  }
}

// ── 节点 ──
const nodes = ref<ProxyNode[]>([])
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
  health_check_url: 'https://www.google.com/generate_204',
})

async function loadNodes() {
  loading.value = true
  error.value = ''
  try {
    const list = await getProxyNodes()
    nodes.value = Array.isArray(list) ? list : []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.loadNodesFailed')
  } finally {
    loading.value = false
  }
}

async function handleCreateNode() {
  if (!nodeForm.value.name || !nodeForm.value.server || nodeForm.value.subscription_id <= 0) {
    error.value = t('proxy.error.nodeFieldsRequired')
    return
  }
  // 协议白名单校验：只能创建可拨号节点（http/https/socks5）
  const allowed = ['http', 'https', 'socks5']
  if (!allowed.includes(nodeForm.value.protocol)) {
    error.value = t('proxy.error.protocolNotDialable')
    return
  }
  loading.value = true
  error.value = ''
  try {
    await createProxyNode(nodeForm.value)
    showCreateNode.value = false
    nodeForm.value = {
      subscription_id: 0,
      name: '',
      protocol: 'http',
      server: '',
      port: 8080,
      username: '',
      password: '',
      location: '',
      health_check_url: 'https://www.google.com/generate_204',
    }
    await loadNodes()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.createNodeFailed')
  } finally {
    loading.value = false
  }
}

async function handleHealthCheckNode(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmHealthCheck', { name })))) return
  loading.value = true
  error.value = ''
  try {
    await healthCheckProxyNode(id)
    await loadNodes()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.healthCheckFailed')
  } finally {
    loading.value = false
  }
}

async function handleDeleteNode(id: number, name: string) {
  if (!(await confirmDialog(t('proxy.confirmDeleteNode', { name })))) return
  loading.value = true
  error.value = ''
  try {
    await deleteProxyNode(id)
    await loadNodes()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('proxy.error.deleteNodeFailed')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadStatus()
  loadSubscriptions()
  loadNodes()
})

function switchTab(tab: 'status' | 'subscriptions' | 'nodes') {
  activeTab.value = tab
  error.value = ''
}

const subOptions = computed(() => {
  return (subscriptions.value ?? [])
    .filter((s: ProxySubscription | null): s is ProxySubscription => !!s && s.status === 'active')
    .map((s: ProxySubscription) => ({ value: s.id, label: `${s.name} (${s.node_count} nodes)` }))
})
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
        <div v-if="status.warning" class="warning-banner">
          {{ status.warning }}
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
            <th>{{ t('proxy.subscriptions.status') }}</th>
            <th>{{ t('proxy.subscriptions.lastFetch') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="sub in subscriptions" :key="sub.id">
            <td>{{ sub.id }}</td>
            <td>{{ sub.name }}</td>
            <td class="url-cell">{{ sub.subscribe_url }}</td>
            <td>{{ sub.node_count }}</td>
            <td :class="{ 'status-active': sub.status === 'active', 'status-error': sub.status === 'error' }">
              {{ sub.status }}
            </td>
            <td>{{ sub.last_fetch_at || t('common.never') }}</td>
            <td>
              <button @click="handleRefreshSub(sub.id, sub.name)" class="btn-small">{{ t('proxy.subscriptions.refresh') }}</button>
              <button @click="handleDeleteSub(sub.id, sub.name)" class="btn-small btn-danger">{{ t('common.delete') }}</button>
            </td>
          </tr>
          <tr v-if="subscriptions.length === 0">
            <td colspan="7" class="empty-state">{{ t('proxy.subscriptions.empty') }}</td>
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
            <th>{{ t('proxy.node.dialable') }}</th>
            <th>{{ t('proxy.node.status') }}</th>
            <th>{{ t('proxy.node.latency') }}</th>
            <th>{{ t('proxy.node.failures') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in nodes" :key="node.id">
            <td>{{ node.id }}</td>
            <td>{{ node.name }}</td>
            <td>{{ node.protocol }}</td>
            <td>{{ node.server }}:{{ node.port }}</td>
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
          <tr v-if="nodes.length === 0">
            <td colspan="9" class="empty-state">{{ t('proxy.nodes.empty') }}</td>
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
