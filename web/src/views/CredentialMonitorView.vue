<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCredentialMonitorSummary, promoteCredential, demoteCredential, type CredentialMonitorSummary, type CredentialMonitorMeta } from '../api'
// 2026-09-13 P2：统计行收敛到 ui/StatCard+StatsRow（方案 §4.5.3）
import StatCard from '../components/ui/StatCard.vue'
import StatsRow from '../components/ui/StatsRow.vue'
// 2026-09-13 P3：确认弹窗壳收敛到 ui/AppModal（方案 §4.5.7）
import AppModal from '../components/ui/AppModal.vue'
// 2026-09-13 P3-5 第二批：大文件按方案 §4.7-4 拆分，本视图只留装配
import CredentialMonitorTable, { type CredentialRow } from '../components/credential-monitor/CredentialMonitorTable.vue'
import CredentialMonitorFilters from '../components/credential-monitor/CredentialMonitorFilters.vue'
import CredentialDetailDrawer from '../components/credential-monitor/CredentialDetailDrawer.vue'
import { formatCacheMeta, formatRefreshAt } from '../components/credential-monitor/helpers'
import { useCredentialLabels } from '../composables/useCredentialLabels'
import { isSuperAdmin } from '../store'

const { t } = useI18n()

const { loadCredentialLabels } = useCredentialLabels()
const canManageMonitor = computed(() => isSuperAdmin())

const loading = ref(false)
const lastListRefreshAt = ref<Date | null>(null)
const listMeta = ref<CredentialMonitorMeta | null>(null)
const credentials = ref<CredentialMonitorSummary[]>([])
const selectedCred = ref<CredentialMonitorSummary | null>(null)

const providerFilter = ref(0)
const availStateFilter = ref('')
const healthFilter = ref('')
const quickFilter = ref<'none' | 'broken' | 'low-rate'>('none')

// Auto refresh (main list)
const autoRefresh = ref(false)
const refreshInterval = ref(30) // seconds
let refreshTimer: number | null = null
let listRequestSeq = 0

async function load() {
  const requestSeq = ++listRequestSeq
  loading.value = true
  try {
    // 2026-08-31: pass mode: 'core' so the backend takes the fast path
    // (skip request_logs_with_current_month + recent_success_rate() LATERAL
    // JOIN). Without mode=core, the default detail path scans every
    // credential's full request history and hits the 15s context deadline
    // on env 154 (production data scale), returning HTTP 500 and leaving
    // the table empty. The list view doesn't render per-(credential,
    // model) rows; the detail drawer still uses the full mode for its
    // models[] breakdown (CredentialDetailDrawer.openDetail).
    const res = await getCredentialMonitorSummary({
      provider_id: providerFilter.value || undefined,
      mode: 'core',
    })
    if (requestSeq !== listRequestSeq) return
    credentials.value = res.credentials
    listMeta.value = res.meta || null
    lastListRefreshAt.value = new Date()
  } catch (e) {
    console.error('load failed', e)
  } finally {
    if (requestSeq === listRequestSeq) {
      loading.value = false
    }
  }
}

// ── Derived summary cards ──────────────────────────────────────────────
const summary = computed(() => {
  // 🔧 2026-07-19: 强化防御 — credentials.value 可能初始为 null
  const all = Array.isArray(credentials.value) ? credentials.value : []
  const total = all.length
  const ready = all.filter(c => c.availability_state === 'ready').length
  const abnormal = all.filter(c =>
    ['unreachable', 'cooling', 'rate_limited', 'auth_failed', 'suspended'].includes(c.availability_state)
  ).length
  const brokenModels = all.reduce((sum, c) => sum + (c.broken_model_count ?? 0), 0)
  return { total, ready, abnormal, brokenModels }
})

const filteredCreds = computed(() => {
  // 🔧 2026-07-19: 强化防御 — credentials.value 可能初始为 null
  let result = Array.isArray(credentials.value) ? credentials.value : []
  if (availStateFilter.value) {
    result = result.filter(c => c.availability_state === availStateFilter.value)
  }
  if (healthFilter.value) {
    result = result.filter(c => c.health_status === healthFilter.value)
  }
  if (quickFilter.value === 'broken') {
    result = result.filter(c => (c.broken_model_count ?? 0) > 0)
  }
  if (quickFilter.value === 'low-rate') {
    result = result.filter(c => c.aggregated_success_rate != null && c.aggregated_success_rate < 0.5)
  }
  return result
})

const displayCreds = computed<CredentialRow[]>(() =>
  filteredCreds.value.map((c) => {
    return {
      ...c,
      modelTotal: c.model_total ?? 0,
      modelAvailable: c.model_available ?? 0,
    }
  }),
)

// Batch operations
const selectedIds = ref<Set<number>>(new Set())
const batchDialogOpen = ref(false)
const batchAction = ref<'promote' | 'demote'>('promote')
const batchReason = ref('')
const batchHours = ref(2)

function toggleSelectAll() {
  if (displayCreds.value.length > 0 && displayCreds.value.every(c => selectedIds.value.has(c.id))) {
    selectedIds.value.clear()
  } else {
    filteredCreds.value.forEach(c => selectedIds.value.add(c.id))
  }
}

function toggleSelect(id: number) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id)
  } else {
    selectedIds.value.add(id)
  }
}

// 打开详情抽屉：选中行即开，详情加载由 CredentialDetailDrawer 内聚处理
function openDetail(cred: CredentialMonitorSummary) {
  selectedCred.value = cred
}

function startAutoRefresh() {
  if (refreshTimer) return
  autoRefresh.value = true
  refreshTimer = window.setInterval(() => load(), refreshInterval.value * 1000)
}

function stopAutoRefresh() {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  autoRefresh.value = false
}

function toggleAutoRefresh() {
  autoRefresh.value ? stopAutoRefresh() : startAutoRefresh()
}

function openBatchDialog(action: 'promote' | 'demote') {
  if (!canManageMonitor.value) return
  if (selectedIds.value.size === 0) {
    alert(t('credentialMonitor.error.selectFirst'))
    return
  }
  batchAction.value = action
  batchReason.value = ''
  batchHours.value = 2
  batchDialogOpen.value = true
}

async function submitBatch() {
  if (!canManageMonitor.value) return
  const ids = Array.from(selectedIds.value)
  const promises = ids.map(id =>
    batchAction.value === 'promote'
      ? promoteCredential(id, batchReason.value)
      : demoteCredential(id, batchReason.value, batchHours.value)
  )
  try {
    await Promise.all(promises)
    batchDialogOpen.value = false
    selectedIds.value.clear()
    load()
  } catch (e) {
    alert(t('credentialMonitor.error.batchFailed') + (e instanceof Error ? e.message : String(e)))
  }
}

onMounted(() => {
  void loadCredentialLabels()
  load()
})

onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div class="page-container">
    <!-- Top bar: split into two rows to prevent overflow.
         Row 1: title + auto-refresh controls
         Row 2: filters + batch actions（CredentialMonitorFilters 子组件） -->
    <div class="top-bar-wrapper">
      <div class="top-bar top-bar-primary">
        <router-link to="/routing-v2" class="back-link">← 路由全景</router-link>
        <h1>{{ t('credentialMonitor.page.title') }}</h1>
        <div class="refresh-group">
          <label>
            <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
            自动刷新
          </label>
          <select v-model.number="refreshInterval" class="field-input">
            <option :value="10">10秒</option>
            <option :value="30">30秒</option>
            <option :value="60">60秒</option>
          </select>
          <button class="btn btn-primary btn-sm" @click="load">手动刷新</button>
          <span class="refresh-meta">列表 {{ loading ? '刷新中...' : formatRefreshAt(lastListRefreshAt) }}</span>
          <span class="refresh-meta">{{ formatCacheMeta(listMeta) }}</span>
        </div>
      </div>

      <CredentialMonitorFilters
        v-model:avail-state-filter="availStateFilter"
        v-model:health-filter="healthFilter"
        v-model:quick-filter="quickFilter"
        :selected-count="selectedIds.size"
        :can-manage="canManageMonitor"
        @batch="openBatchDialog"
      />
    </div>

    <!-- Summary cards → ui/StatCard + StatsRow（方案 §4.5.3，2026-09-13） -->
    <StatsRow>
      <StatCard label="总凭据" :value="summary.total" />
      <StatCard label="可用 (ready)" :value="summary.ready" tone="success" />
      <StatCard
        label="异常"
        :value="summary.abnormal"
        :tone="summary.abnormal > 0 ? 'warning' : 'neutral'"
        sub="unreachable/cooling/rate_limited"
      />
      <StatCard
        label="broken 模型"
        :value="summary.brokenModels"
        :tone="summary.brokenModels > 0 ? 'danger' : 'neutral'"
        sub="probe 确认坏掉"
      />
    </StatsRow>

    <div v-if="loading" style="text-align:center;padding:32px">加载中...</div>
    <div v-else-if="!filteredCreds.length" style="text-align:center;padding:32px">暂无凭据</div>

    <CredentialMonitorTable
      v-else
      :rows="displayCreds"
      :selected-ids="selectedIds"
      @toggle-all="toggleSelectAll"
      @toggle="toggleSelect"
      @open="openDetail"
    />

    <!-- Detail Drawer（2026-09-13 P3-5b 壳层交由 ui/AppDrawer 承载） -->
    <CredentialDetailDrawer v-model="selectedCred" @refresh-list="load" />

    <!-- Batch Dialog（2026-09-13 手写弹层迁 ui/AppModal sm） -->
    <AppModal
      v-model="batchDialogOpen"
      :title="`批量${batchAction === 'promote' ? '恢复' : '降级'} (${selectedIds.size} 个凭据)`"
      size="sm"
    >
      <div style="margin-bottom:16px">
        <label class="field-label">原因</label>
        <input v-model="batchReason" class="field-input" placeholder="请输入原因" />
      </div>
      <div v-if="batchAction === 'demote'" style="margin-bottom:16px">
        <label class="field-label">自动恢复时间 (小时)</label>
        <input v-model.number="batchHours" type="number" min="0.5" step="0.5" class="field-input" />
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="batchDialogOpen = false">取消</button>
        <button :class="batchAction === 'promote' ? 'btn btn-success' : 'btn btn-danger'" @click="submitBatch">
          确认{{ batchAction === 'promote' ? '恢复' : '降级' }}
        </button>
      </template>
    </AppModal>
  </div>
</template>

<style scoped>
/* Outer layout — top-left aligned, stretches across the full available width. */
.page-container {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
}

/* Top bar wrapper — split into two rows to prevent horizontal overflow.
   Row 1 (primary): title + auto-refresh controls
   Row 2 (secondary): filters + batch actions（CredentialMonitorFilters） */
.top-bar-wrapper {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.top-bar {
  padding: 6px 10px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 11px;
  color: var(--muted);
}
.top-bar > * { flex-shrink: 0; }

/* Primary row: prevent wrapping for core controls */
.top-bar-primary {
  flex-wrap: nowrap;
}
.back-link {
  font-size: 11px;
  color: var(--muted);
  text-decoration: none;
}
.back-link:hover { color: var(--accent-h); }
.top-bar h1 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  flex-shrink: 0;
  color: var(--text);
}
.top-bar .refresh-group {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: nowrap;
}
.top-bar .refresh-group label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--muted);
}
.top-bar .refresh-group .field-input {
  width: auto;
  font-size: 11px;
  padding: 2px 6px;
}
.top-bar .refresh-meta {
  font-size: 10px;
  color: var(--muted);
  white-space: nowrap;
}
.top-bar .field-input {
  width: auto;
  max-width: 120px;
  font-size: 11px;
  padding: 2px 6px;
}
.top-bar .btn-sm { font-size: 11px; padding: 2px 8px; }

.field-label {
  display: block;
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 2px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
</style>
