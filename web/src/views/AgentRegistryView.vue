<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import {
  getAgents,
  getAgent,
  getAgentNeighbors,
  getAgentStats,
  linkAgent,
  type AgentAsset,
  type AgentStatsResponse,
  type AssetKind,
  type RelationType,
} from '../api/agents'
import { isSuperAdmin, isDefaultTenant } from '../store'

const { t } = useI18n()


const KIND_OPTIONS: Array<{ value: AssetKind | 'all'; label: string }> = [
  { value: 'all', label: t('agentRegistryView.all') },
  { value: 'llm_endpoint', label: t('agentRegistryView.llm_endpoint') },
  { value: 'mcp_server', label: t('agentRegistryView.mcp_server') },
  { value: 'agent', label: t('agentRegistryView.agent') },
]

const RELATION_OPTIONS: Array<{ value: RelationType; label: string }> = [
  { value: 'depends_on', label: t('agentRegistryView.depends_on') },
  { value: 'calls', label: t('agentRegistryView.calls') },
  { value: 'similar_to', label: t('agentRegistryView.similar_to') },
]

const agents = ref<AgentAsset[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(50)
const kindFilter = ref<AssetKind | 'all'>('llm_endpoint')
const tenantFilter = ref('')
const search = ref('')
const autoRefresh = ref(false)
const loading = ref(false)
const error = ref<string | null>(null)
let autoRefreshTimer: ReturnType<typeof setInterval> | null = null

const showLinkDialog = ref(false)
const linkSource = ref<AgentAsset | null>(null)
const linkTargetId = ref<number | ''>('')
const linkType = ref<RelationType>('depends_on')
const linkSubmitting = ref(false)
const linkError = ref<string | null>(null)

const showDetailDialog = ref(false)
const detailLoading = ref(false)
const detail = ref<AgentAsset | null>(null)
const detailError = ref<string | null>(null)

// Phase 6: stats overview (total / by_kind / by_health / by_owner)
const stats = ref<AgentStatsResponse | null>(null)
const statsLoading = ref(false)
const statsError = ref<string | null>(null)

// Phase 6: neighbors topology dialog
const showNeighborsDialog = ref(false)
const neighborsLoading = ref(false)
const neighbors = ref<{ upstream: Array<{ kind: AssetKind; ref_id: number; name: string }>; downstream: Array<{ kind: AssetKind; ref_id: number; name: string }>; depth: number; count: number } | null>(null)
const neighborsSeed = ref<AgentAsset | null>(null)
const neighborsError = ref<string | null>(null)

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))

function startAutoRefresh() {
  stopAutoRefresh()
  autoRefreshTimer = setInterval(() => {
    if (!loading.value && !showDetailDialog.value && !showLinkDialog.value) {
      load()
    }
  }, 30000)
}

function stopAutoRefresh() {
  if (autoRefreshTimer !== null) {
    clearInterval(autoRefreshTimer)
    autoRefreshTimer = null
  }
}

function load() {
  loading.value = true
  error.value = null
  // Apply client-side search filter: pull a wider window then narrow.
  // Backend doesn't support substring search yet, so we fetch up to 500
  // and filter locally for a snappy UX on small tenants.
  const limit = search.value.trim() ? 500 : pageSize.value
  const offset = search.value.trim() ? 0 : (page.value - 1) * pageSize.value
  return getAgents({
    kind: kindFilter.value,
    tenant: tenantFilter.value || undefined,
    limit,
    offset,
  })
    .then((resp) => {
      const filtered = search.value.trim()
        ? resp.agents.filter((a) => {
            const q = search.value.trim().toLowerCase()
            return (
              a.name.toLowerCase().includes(q) ||
              (a.owner || '').toLowerCase().includes(q) ||
              (a.team || '').toLowerCase().includes(q) ||
              a.tenant_id.toLowerCase().includes(q)
            )
          })
        : resp.agents
      agents.value = filtered
      total.value = search.value.trim() ? filtered.length : resp.total
    })
    .catch((e: unknown) => {
      error.value = e instanceof Error ? e.message : t('agentRegistryView.loadFailed')
      agents.value = []
      total.value = 0
    })
    .finally(() => {
      loading.value = false
    })
}

function resetPageAndLoad() {
  page.value = 1
  return load()
}

function changePage(delta: number) {
  const next = page.value + delta
  if (next < 1 || next > totalPages.value) return
  page.value = next
  load()
}

function clearFilters() {
  kindFilter.value = 'all'
  tenantFilter.value = ''
  search.value = ''
  resetPageAndLoad()
}

function healthLabel(s: string): string {
  const map: Record<string, string> = {
    healthy: t('agentRegistryView.healthy'),
    degraded: t('agentRegistryView.degraded'),
    down: t('agentRegistryView.down'),
    unknown: t('agentRegistryView.unknown'),
  }
  return map[s] || s
}

function healthBadgeClass(s: string): string {
  if (s === 'healthy') return 'badge-green'
  if (s === 'degraded') return 'badge-yellow'
  if (s === 'down') return 'badge-red'
  return 'badge-gray'
}

function kindLabel(k: string): string {
  const map: Record<string, string> = {
    llm_endpoint: t('agentRegistryView.llm_endpoint'),
    mcp_server: t('agentRegistryView.mcp_server'),
    agent: t('agentRegistryView.agent'),
  }
  return map[k] || k
}

function fmtTs(s?: string): string {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { hour12: false })
}

function fmtTimeAgo(s?: string): string {
  if (!s) return '—'
  const ms = new Date(s).getTime()
  if (Number.isNaN(ms)) return '—'
  const diff = Math.floor((Date.now() - ms) / 1000)
  if (diff < 60) return t('agentRegistryView.time.secondsAgo', { n: diff })
  if (diff < 3600) return t('agentRegistryView.time.minutesAgo', { n: Math.floor(diff / 60) })
  if (diff < 86400) return t('agentRegistryView.time.hoursAgo', { n: Math.floor(diff / 3600) })
  return t('agentRegistryView.time.daysAgo', { n: Math.floor(diff / 86400) })
}

function ellipsize(s: string | null | undefined, max = 36): string {
  if (!s) return '—'
  if (s.length <= max) return s
  return s.slice(0, Math.max(1, max - 1)) + '…'
}

function showDetail(row: AgentAsset) {
  showDetailDialog.value = true
  detailLoading.value = true
  detail.value = null
  detailError.value = null
  getAgent(row.ref_id)
    .then((r) => {
      detail.value = r.agent
    })
    .catch((e: unknown) => {
      detailError.value = e instanceof Error ? e.message : t('agentRegistryView.detailFailed')
    })
    .finally(() => {
      detailLoading.value = false
    })
}

function closeDetail() {
  showDetailDialog.value = false
  detail.value = null
  detailError.value = null
}

function openLinkDialog(row: AgentAsset) {
  linkSource.value = row
  linkTargetId.value = ''
  linkType.value = 'depends_on'
  linkError.value = null
  showLinkDialog.value = true
}

function closeLinkDialog() {
  showLinkDialog.value = false
  linkSource.value = null
  linkTargetId.value = ''
  linkError.value = null
}

async function submitLink() {
  if (!linkSource.value) return
  const tid = typeof linkTargetId.value === 'number' ? linkTargetId.value : Number(linkTargetId.value)
  if (!Number.isFinite(tid) || tid <= 0) {
    linkError.value = t('agentRegistryView.invalidTargetId')
    return
  }
  linkSubmitting.value = true
  linkError.value = null
  try {
    await linkAgent(linkSource.value.ref_id, tid, linkType.value)
    closeLinkDialog()
  } catch (e: unknown) {
    linkError.value = e instanceof Error ? e.message : t('agentRegistryView.linkFailed')
  } finally {
    linkSubmitting.value = false
  }
}

const canFilterByTenant = computed(() => isSuperAdmin() && isDefaultTenant())

function onAutoRefreshToggle(enabled: boolean) {
  if (enabled) startAutoRefresh()
  else stopAutoRefresh()
}

async function loadStats() {
  statsLoading.value = true
  statsError.value = null
  try {
    const resp = await getAgentStats()
    stats.value = resp
  } catch (e: unknown) {
    statsError.value = e instanceof Error ? e.message : t('agentRegistryView.statsFailed')
  } finally {
    statsLoading.value = false
  }
}

async function openNeighborsDialog(row: AgentAsset) {
  neighborsSeed.value = row
  neighbors.value = null
  neighborsError.value = null
  showNeighborsDialog.value = true
  neighborsLoading.value = true
  try {
    const resp = await getAgentNeighbors(row.ref_id, 2)
    neighbors.value = {
      upstream: resp.upstream,
      downstream: resp.downstream,
      depth: resp.depth,
      count: resp.count,
    }
  } catch (e: unknown) {
    neighborsError.value = e instanceof Error ? e.message : t('agentRegistryView.topologyFailed')
  } finally {
    neighborsLoading.value = false
  }
}

onMounted(() => {
  load()
  loadStats()
})

onBeforeUnmount(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2 style="margin:0">{{ t('agentRegistryView.pageTitle') }}</h2>
      <div style="display:flex;gap:8px;align-items:center">
        <label style="display:flex;align-items:center;gap:4px;font-size:12px;cursor:pointer;user-select:none">
          <input type="checkbox" :checked="autoRefresh" @change="onAutoRefreshToggle(($event.target as HTMLInputElement).checked)" style="cursor:pointer" />
          <span>{{ t('agentRegistryView.autoRefresh') }}</span>
        </label>
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="resetPageAndLoad">{{ t('agentRegistryView.refresh') }}</button>
      </div>
    </div>

    <div class="compact-filter-bar">
      <select v-model="kindFilter" class="cf-select cf-kind" :title="t('agentRegistryView.filter.kindTitle')" @change="resetPageAndLoad">
        <option v-for="opt in KIND_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
      </select>
      <select
        v-if="canFilterByTenant"
        v-model="tenantFilter"
        class="cf-select cf-tenant"
        :title="t('agentRegistryView.filter.tenantTitle')"
        @change="resetPageAndLoad"
      >
        <option value="">{{ t('agentRegistryView.filter.defaultTenant') }}</option>
        <option value="default">default</option>
      </select>
      <input
        v-model="search"
        type="text"
        class="cf-input cf-grow"
        :placeholder="t('agentRegistryView.filter.placeholder')"
        @keyup.enter="resetPageAndLoad"
      />
      <button class="btn btn-ghost btn-sm" @click="clearFilters">{{ t('agentRegistryView.filter.clear') }}</button>
      <button class="btn btn-primary btn-sm" @click="resetPageAndLoad">{{ t('agentRegistryView.filter.query') }}</button>
      <span class="cf-meta">{{ t('agentRegistryView.filter.totalCount', { n: total }) }}</span>
    </div>

    <!-- Phase 6: stats overview card -->
    <div v-if="stats || statsError" class="stats-grid">
      <div v-if="stats" class="stats-row">
        <div class="stat-card">
          <div class="stat-label">{{ t('agentRegistryView.stats.total') }}</div>
          <div class="stat-value">{{ stats.total }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('agentRegistryView.stats.llm') }}</div>
          <div class="stat-value">{{ stats.by_kind['llm_endpoint'] ?? 0 }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('agentRegistryView.stats.mcp') }}</div>
          <div class="stat-value">{{ stats.by_kind['mcp_server'] ?? 0 }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('agentRegistryView.stats.healthy') }}</div>
          <div class="stat-value stat-healthy">{{ stats.by_health['healthy'] ?? 0 }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('agentRegistryView.stats.degradedOrDown') }}</div>
          <div class="stat-value stat-down">{{ (stats.by_health['degraded'] ?? 0) + (stats.by_health['down'] ?? 0) }}</div>
        </div>
      </div>
      <p v-else-if="statsError" style="color:var(--danger);font-size:12px">{{ t('agentRegistryView.stats.loadError', { err: statsError }) }}</p>
    </div>

    <p v-if="error" style="color:var(--danger);margin-bottom:12px">{{ error }}</p>

    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-info">
        <span>{{ t('agentRegistryView.filter.totalCount', { n: total }) }}</span>
        <span v-if="!search.trim()">{{ t('agentRegistryView.pager.pageInfo', { page, total: totalPages }) }}</span>
        <span class="pagination-divider">·</span>
        <span class="page-size-label">{{ t('agentRegistryView.pager.perPage') }}</span>
        <select v-model.number="pageSize" :disabled="!!search.trim()" @change="resetPageAndLoad" class="page-size-select">
          <option :value="20">20</option>
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
        </select>
      </div>
      <div class="pagination-controls">
        <button class="btn btn-ghost btn-sm" :disabled="page <= 1 || !!search.trim()" @click="changePage(-1)">{{ t('agentRegistryView.pager.prev') }}</button>
        <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages || !!search.trim()" @click="changePage(1)">{{ t('agentRegistryView.pager.next') }}</button>
      </div>
    </div>

    <div class="card" style="overflow-x:auto">
      <table class="data-table agent-table" style="width:100%;font-size:12px">
        <thead>
          <tr>
            <th class="col-id">{{ t('agentRegistryView.table.id') }}</th>
            <th class="col-kind">{{ t('agentRegistryView.table.kind') }}</th>
            <th class="col-name">{{ t('agentRegistryView.table.name') }}</th>
            <th class="col-health">{{ t('agentRegistryView.table.health') }}</th>
            <th class="col-version">{{ t('agentRegistryView.table.version') }}</th>
            <th class="col-tenant">{{ t('agentRegistryView.table.tenant') }}</th>
            <th class="col-owner">{{ t('agentRegistryView.table.owner') }}</th>
            <th class="col-seen">{{ t('agentRegistryView.table.lastSeen') }}</th>
            <th class="col-actions">{{ t('agentRegistryView.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td :colspan="9">{{ t('agentRegistryView.loading') }}</td></tr>
          <tr v-else-if="!agents.length"><td :colspan="9">{{ t('agentRegistryView.empty.noAgents') }}</td></tr>
          <tr
            v-for="a in agents"
            :key="a.kind + ':' + a.ref_id"
            class="agent-row"
            @click="showDetail(a)"
          >
            <td class="col-id">
              <span class="cell-line1" :title="`${a.kind}:${a.ref_id}`">{{ a.ref_id }}</span>
            </td>
            <td class="col-kind">
              <span class="kind-tag" :class="'kind-' + a.kind">{{ kindLabel(a.kind) }}</span>
            </td>
            <td class="col-name">
              <div class="cell-line1 cell-clip" :title="a.name">{{ ellipsize(a.name, 48) }}</div>
              <div v-if="a.tags && Object.keys(a.tags).length" class="cell-line2 muted">
                {{ Object.entries(a.tags).slice(0, 2).map(([k, v]) => `${k}=${v}`).join(' · ') }}
              </div>
            </td>
            <td class="col-health">
              <span class="badge" :class="healthBadgeClass(a.health_state)">
                {{ healthLabel(a.health_state) }}
              </span>
            </td>
            <td class="col-version">
              <span class="cell-line1">{{ a.version || '—' }}</span>
            </td>
            <td class="col-tenant">
              <span class="cell-line1" :title="a.tenant_id">{{ ellipsize(a.tenant_id, 16) }}</span>
            </td>
            <td class="col-owner">
              <span class="cell-line1" :title="a.owner || '—'">{{ ellipsize(a.owner, 20) }}</span>
              <div v-if="a.team" class="cell-line2 muted">{{ ellipsize(a.team, 20) }}</div>
            </td>
            <td class="col-seen" :title="fmtTs(a.last_seen_at)">
              <div class="cell-line1">{{ fmtTimeAgo(a.last_seen_at) }}</div>
              <div class="cell-line2 muted">{{ fmtTs(a.last_seen_at) }}</div>
            </td>
            <td class="col-actions" @click.stop>
              <button class="btn btn-ghost btn-sm" @click="showDetail(a)">{{ t('agentRegistryView.table.actions') }}</button>
              <button class="btn btn-ghost btn-sm" @click="openLinkDialog(a)">{{ t('agentRegistryView.detail.addRelation') }}</button>
              <button class="btn btn-ghost btn-sm" @click="openNeighborsDialog(a)">{{ t('agentRegistryView.detail.showTopology') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-info">
        <span>{{ t('agentRegistryView.filter.totalCount', { n: total }) }}</span>
        <span v-if="!search.trim()">{{ t('agentRegistryView.pager.pageInfo', { page, total: totalPages }) }}</span>
      </div>
      <div class="pagination-controls">
        <button class="btn btn-ghost btn-sm" :disabled="page <= 1 || !!search.trim()" @click="changePage(-1)">{{ t('agentRegistryView.pager.prev') }}</button>
        <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages || !!search.trim()" @click="changePage(1)">{{ t('agentRegistryView.pager.next') }}</button>
      </div>
    </div>

    <!-- Detail Drawer -->
    <div v-if="showDetailDialog" class="drawer-backdrop" @click="closeDetail">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <h3 style="margin:0">{{ t('agentRegistryView.detail.title') }}</h3>
          <button class="btn btn-sm" @click="closeDetail">{{ t('agentRegistryView.detail.close') }}</button>
        </div>
        <div v-if="detailLoading" style="text-align:center;padding:40px">{{ t('agentRegistryView.loading') }}</div>
        <template v-else-if="detail">
          <div class="drawer-section">
            <h4 style="margin:0 0 8px">{{ detail.name }}</h4>
            <div style="display:flex;gap:16px;flex-wrap:wrap;font-size:12px;margin-bottom:12px">
              <span><strong>{{ t('agentRegistryView.table.id') }}:</strong> {{ detail.kind }}:{{ detail.ref_id }}</span>
              <span><strong>{{ t('agentRegistryView.detail.tenant') }}:</strong> {{ detail.tenant_id }}</span>
              <span><strong>{{ t('agentRegistryView.detail.owner') }}:</strong> {{ detail.owner || '—' }}</span>
              <span><strong>{{ t('agentRegistryView.detail.team') }}:</strong> {{ detail.team || '—' }}</span>
              <span v-if="detail.cost_center"><strong>Cost Center:</strong> {{ detail.cost_center }}</span>
              <span>
                <strong>{{ t('agentRegistryView.detail.health') }}:</strong>
                <span class="badge" :class="healthBadgeClass(detail.health_state)">{{ healthLabel(detail.health_state) }}</span>
              </span>
              <span><strong>{{ t('agentRegistryView.detail.version') }}:</strong> {{ detail.version || '—' }}</span>
            </div>
            <div style="display:flex;gap:16px;flex-wrap:wrap;font-size:12px;margin-bottom:12px">
              <span><strong>{{ t('agentRegistryView.detail.firstSeen') }}:</strong> {{ fmtTs(detail.registered_at) }}</span>
              <span><strong>{{ t('agentRegistryView.detail.lastSeen') }}:</strong> {{ fmtTs(detail.last_seen_at) }}</span>
            </div>
            <div v-if="detail.tags && Object.keys(detail.tags).length" style="margin-bottom:12px">
              <strong style="font-size:12px">Tags:</strong>
              <div style="display:flex;flex-wrap:wrap;gap:4px;margin-top:6px">
                <span
                  v-for="(value, key) in detail.tags"
                  :key="key"
                  class="badge badge-blue"
                  style="font-size:11px"
                >{{ key }}={{ value }}</span>
              </div>
            </div>
            <div v-if="detail.metadata && Object.keys(detail.metadata).length">
              <strong style="font-size:12px">{{ t('agentRegistryView.detail.metadata') }}:</strong>
              <pre class="metadata-block">{{ JSON.stringify(detail.metadata, null, 2) }}</pre>
            </div>
          </div>
        </template>
        <div v-else-if="detailError" style="color:var(--danger);padding:20px">{{ detailError }}</div>
      </div>
    </div>

    <!-- Link Dialog -->
    <div v-if="showLinkDialog" class="drawer-backdrop" @click="closeLinkDialog">
      <div class="drawer-panel card drawer-panel-narrow" @click.stop>
        <div class="drawer-header">
          <h3 style="margin:0">{{ t('agentRegistryView.link.title') }}</h3>
          <button class="btn btn-sm" @click="closeLinkDialog">{{ t('agentRegistryView.link.cancel') }}</button>
        </div>
        <div class="drawer-section" v-if="linkSource">
          <p style="margin:0 0 12px;font-size:12px;color:var(--muted)">
            {{ t('agentRegistryView.link.sourceAgent') }}: <strong>{{ linkSource.name }}</strong> ({{ linkSource.kind }}:{{ linkSource.ref_id }})
          </p>
          <div style="margin-bottom:12px">
            <label style="display:block;font-size:12px;margin-bottom:4px">{{ t('agentRegistryView.link.relationType') }}</label>
            <select v-model="linkType" class="cf-select" style="width:100%">
              <option v-for="opt in RELATION_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>
          <div style="margin-bottom:12px">
            <label style="display:block;font-size:12px;margin-bottom:4px">{{ t('agentRegistryView.link.targetId') }}</label>
            <input
              v-model.number="linkTargetId"
              type="number"
              class="cf-input"
              :placeholder="t('agentRegistryView.link.targetIdPlaceholder')"
              min="1"
            />
          </div>
          <p v-if="linkError" style="color:var(--danger);font-size:12px;margin:0 0 8px">{{ linkError }}</p>
          <div style="display:flex;gap:8px;justify-content:flex-end">
            <button class="btn btn-ghost btn-sm" @click="closeLinkDialog">{{ t('agentRegistryView.link.cancel') }}</button>
            <button class="btn btn-primary btn-sm" :disabled="linkSubmitting" @click="submitLink">
              {{ linkSubmitting ? t('agentRegistryView.link.creating') : t('agentRegistryView.link.submit') }}
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Phase 6: neighbors topology dialog -->
    <div v-if="showNeighborsDialog" class="drawer-backdrop" @click="showNeighborsDialog = false">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <h3>{{ t('agentRegistryView.topology.title', { depth: neighbors?.depth }) }} — {{ neighborsSeed?.name }} (#{{ neighborsSeed?.ref_id }})</h3>
          <button class="btn btn-sm" @click="showNeighborsDialog = false">{{ t('agentRegistryView.detail.close') }}</button>
        </div>
        <div v-if="neighborsLoading" class="loading-state">{{ t('agentRegistryView.loading') }}</div>
        <p v-else-if="neighborsError" style="color:var(--danger)">{{ neighborsError }}</p>
        <div v-else-if="neighbors" class="neighbors-body">
          <p class="neighbors-meta">{{ t('agentRegistryView.topology.depth') }} {{ neighbors.depth }} · {{ t('agentRegistryView.topology.totalNodes', { n: neighbors.count }) }}</p>
          <div class="neighbors-section">
            <h4>{{ t('agentRegistryView.topology.downstream', { n: neighbors.downstream.length }) }}</h4>
            <ul v-if="neighbors.downstream.length" class="neighbor-list">
              <li v-for="n in neighbors.downstream" :key="`d-${n.kind}-${n.ref_id}`">
                <span class="kind-tag kind-{{ n.kind }}">{{ n.kind }}</span>
                <strong>{{ n.name }}</strong>
                <span class="ref-id">#{{ n.ref_id }}</span>
              </li>
            </ul>
            <p v-else class="empty-note">{{ t('agentRegistryView.detail.noRelations') }}</p>
          </div>
          <div class="neighbors-section">
            <h4>{{ t('agentRegistryView.topology.upstream', { n: neighbors.upstream.length }) }}</h4>
            <ul v-if="neighbors.upstream.length" class="neighbor-list">
              <li v-for="n in neighbors.upstream" :key="`u-${n.kind}-${n.ref_id}`">
                <span class="kind-tag kind-{{ n.kind }}">{{ n.kind }}</span>
                <strong>{{ n.name }}</strong>
                <span class="ref-id">#{{ n.ref_id }}</span>
              </li>
            </ul>
            <p v-else class="empty-note">{{ t('agentRegistryView.detail.noRelations') }}</p>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.stats-grid { margin-bottom: 12px; }
.stats-row {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}
.stat-card {
  flex: 1 1 120px;
  padding: 10px 14px;
  background: var(--surface-1, rgba(255,255,255,0.04));
  border: 1px solid var(--border, rgba(255,255,255,0.08));
  border-radius: 8px;
}
.stat-label {
  font-size: 11px;
  opacity: 0.7;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.stat-value {
  font-size: 22px;
  font-weight: 600;
  margin-top: 4px;
}
.stat-healthy { color: #4ade80; }
.stat-down { color: #f87171; }

.neighbors-body { padding: 8px 0; }
.neighbors-meta { font-size: 12px; opacity: 0.7; margin: 0 0 12px; }
.neighbors-section { margin-bottom: 14px; }
.neighbors-section h4 {
  font-size: 13px;
  margin: 0 0 6px;
  opacity: 0.85;
}
.neighbor-list { list-style: none; padding: 0; margin: 0; }
.neighbor-list li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-bottom: 1px solid var(--border, rgba(255,255,255,0.06));
  font-size: 13px;
}
.ref-id { opacity: 0.5; font-size: 11px; }
.empty-note { font-size: 12px; opacity: 0.6; padding: 4px 8px; }

.kind-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 600;
}
.kind-llm_endpoint { background: rgba(99, 102, 241, 0.18); color: #818cf8; }
.kind-mcp_server   { background: rgba(34, 197, 94, 0.18); color: #4ade80; }
.kind-agent        { background: rgba(245, 158, 11, 0.18); color: #fbbf24; }

.metadata-block {
  margin: 6px 0 0;
  padding: 10px 12px;
  background: var(--surface-secondary, rgba(255, 255, 255, 0.04));
  border: 1px solid var(--border, #333);
  border-radius: 6px;
  font-size: 11px;
  font-family: 'SF Mono', 'Fira Code', monospace;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 320px;
  overflow: auto;
}

.agent-row {
  cursor: pointer;
  transition: background 0.1s ease;
}
.agent-row:hover {
  background: rgba(99, 102, 241, 0.06);
}
</style>