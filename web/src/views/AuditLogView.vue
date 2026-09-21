<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime, formatTimeOnly } from '../utils/datetime'
import { localeRef } from '../i18n'
import { fmtDateCompact } from '../i18n/useFormat'
import { ref, onMounted, computed } from 'vue'
import { getAuditLogs, type AuditLogEntry } from '../api'
// 2026-09-13 P2：筛选/分页收敛到 ui 组件（方案 §4.5.4/§4.5.6）
import FilterBar from '../components/ui/FilterBar.vue'
import type { FilterDefinition } from '../components/ui/filter-types'
import PaginationBar from '../components/ui/PaginationBar.vue'

const { t, te } = useI18n()
const entries = ref<AuditLogEntry[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(50)
const loading = ref(false)
const error = ref('')

const filters = ref<Record<string, string>>({ actor: '', action: '', from: '', to: '' })

// FilterBar 声明式定义（label 走 computed 以随语言切换更新）
const filterDefs = computed<FilterDefinition[]>(() => [
  { key: 'actor', type: 'search', label: t('auditLog.filter.actorLabel'), placeholder: t('auditLog.filter.actorPlaceholder') },
  { key: 'action', type: 'search', label: t('auditLog.filter.actionLabel'), placeholder: t('auditLog.filter.actionPlaceholder') },
  { key: 'time', type: 'daterange', fromKey: 'from', toKey: 'to', fromLabel: t('auditLog.filter.fromLabel'), toLabel: t('auditLog.filter.toLabel') },
])

const detailVisible = ref(false)
const detailEntry = ref<AuditLogEntry | null>(null)

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const r = await getAuditLogs({
      page: page.value,
      size: size.value,
      actor: filters.value.actor.trim() || undefined,
      action: filters.value.action.trim() || undefined,
      from: filters.value.from ? new Date(filters.value.from).toISOString() : undefined,
      to: filters.value.to ? new Date(filters.value.to).toISOString() : undefined,
    })
    entries.value = r.entries || []
    total.value = r.total || 0
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('auditLog.loadFailed')
    entries.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function resetPageAndLoad() {
  page.value = 1
  load()
}

function changePage(delta: number) {
  const next = page.value + delta
  if (next < 1 || next > totalPages.value) return
  page.value = next
  load()
}

function clearFilters() {
  filters.value = { actor: '', action: '', from: '', to: '' }
  resetPageAndLoad()
}

function onPageSizeChange(next: number) {
  size.value = next
  resetPageAndLoad()
}

function actionBadgeClass(action: string): string {
  if (action.startsWith('user.create')) return 'badge-system'
  if (action.startsWith('user.delete')) return 'badge-red'
  if (action.startsWith('user.')) return 'badge-blue'
  if (action.startsWith('auth.login_failed') || action.startsWith('auth.rate_limited')) return 'badge-red'
  if (action.startsWith('auth.')) return 'badge-green'
  return 'badge-gray'
}

function actionLabel(action: string): string {
  const normalized = action.replace(/^authentication\./, 'auth.')
  for (const candidate of [action, normalized]) {
    const key = `auditLog.actions.${candidate}`
    if (te(key)) return t(key)
  }
  return action
}

function fmtTime(s: string) {
  if (!s) return t('auditLog.dash')
  return formatTimeOnly(s, { locale: localeRef.value, options: { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' } })
}

function fmtTs(s: string) {
  if (!s) return t('auditLog.dash')
  return formatDateTime(s, { locale: localeRef.value, options: { hour12: false } })
}

function fmtJson(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2)
    } catch {
      return v
    }
  }
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

function detailPreview(e: AuditLogEntry): string {
  const raw = e.after_json ?? e.before_json
  if (raw == null) return t('auditLog.dash')
  const text = typeof raw === 'string' ? raw : JSON.stringify(raw)
  if (text.length <= 80) return text
  return text.slice(0, 79) + '…'
}

function openDetail(e: AuditLogEntry) {
  detailEntry.value = e
  detailVisible.value = true
}

function closeDetail() {
  detailVisible.value = false
  detailEntry.value = null
}

onMounted(load)
</script>

<template>
  <div class="audit-page">
    <div class="page-header">
      <h2>{{ t('auditLog.page.title') }}</h2>
      <div class="header-actions">
        <span class="count-chip" aria-live="polite">{{ t('auditLog.page.totalChip', { n: total }) }}</span>
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('auditLog.page.refreshing') : t('auditLog.page.refresh') }}
        </button>
      </div>
    </div>

    <p class="page-desc">{{ t('auditLog.page.desc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>

    <FilterBar
      v-model="filters"
      :definitions="filterDefs"
      :loading="loading"
      @search="resetPageAndLoad"
      @clear="resetPageAndLoad"
    />

    <PaginationBar
      v-if="!loading && total > 0"
      :page="page"
      :page-size="size"
      :total="total"
      :page-sizes="[25, 50, 100, 200]"
      @prev="changePage(-1)"
      @next="changePage(1)"
      @change-size="onPageSizeChange"
    />

    <div class="card table-card">
      <div class="table-wrap">
        <table class="data-table audit-table">
          <thead>
            <tr>
              <th class="col-time">{{ t('auditLog.table.headers.time') }}</th>
              <th class="col-actor">{{ t('auditLog.table.headers.actor') }}</th>
              <th class="col-action">{{ t('auditLog.table.headers.action') }}</th>
              <th class="col-target">{{ t('auditLog.table.headers.target') }}</th>
              <th class="col-details">{{ t('auditLog.table.headers.details') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="5" class="state-cell">{{ t('auditLog.page.loading') }}</td>
            </tr>
            <tr v-else-if="!entries.length">
              <td colspan="5" class="state-cell">
                <p>{{ t('auditLog.page.emptyTitle') }}</p>
                <p class="text-muted">{{ t('auditLog.page.emptyHint') }}</p>
              </td>
            </tr>
            <tr
              v-for="e in entries"
              v-else
              :key="e.id"
              class="audit-row"
              tabindex="0"
              :aria-label="`${e.actor} ${e.action}`"
              @click="openDetail(e)"
              @keyup.enter="openDetail(e)"
            >
              <td class="col-time" :title="fmtTs(e.ts)">
                <div class="cell-line1">{{ fmtDateCompact(e.ts) }}</div>
                <div class="cell-line2">{{ fmtTime(e.ts) }}</div>
              </td>
              <td class="col-actor">
                <span class="actor-name">{{ e.actor || t('auditLog.dash') }}</span>
              </td>
              <td class="col-action">
                <span class="badge" :class="actionBadgeClass(e.action)" :title="e.action">
                  {{ actionLabel(e.action) }}
                </span>
              </td>
              <td class="col-target">
                <template v-if="e.target_type">
                  <span class="target-type">{{ e.target_type }}</span>
                  <span class="target-id">#{{ e.target_id ?? '?' }}</span>
                </template>
                <span v-else class="text-muted">—</span>
              </td>
              <td class="col-details">
                <code class="detail-preview" :title="detailPreview(e)">{{ detailPreview(e) }}</code>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <PaginationBar
      v-if="!loading && total > 0"
      :page="page"
      :page-size="size"
      :total="total"
      :page-sizes="[25, 50, 100, 200]"
      @prev="changePage(-1)"
      @next="changePage(1)"
      @change-size="onPageSizeChange"
    />

    <div v-if="detailVisible && detailEntry" class="drawer-backdrop" @click="closeDetail">
      <div class="drawer-panel card drawer-panel-wide" role="dialog" aria-labelledby="audit-detail-title" @click.stop>
        <div class="drawer-header">
          <h3 id="audit-detail-title">{{ t('auditLog.detail.titleWithId', { id: detailEntry.id }) }}</h3>
          <button class="btn btn-sm btn-ghost" @click="closeDetail">{{ t('auditLog.detail.close') }}</button>
        </div>

        <div class="drawer-section detail-meta">
          <span><strong>{{ t('auditLog.detail.metaTime') }}</strong> {{ fmtTs(detailEntry.ts) }}</span>
          <span><strong>{{ t('auditLog.detail.metaActor') }}</strong> {{ detailEntry.actor || t('auditLog.dash') }}</span>
          <span>
            <strong>{{ t('auditLog.detail.metaAction') }}</strong>
            <span class="badge" :class="actionBadgeClass(detailEntry.action)">{{ actionLabel(detailEntry.action) }}</span>
          </span>
          <span v-if="detailEntry.target_type">
            <strong>{{ t('auditLog.detail.metaTarget') }}</strong> {{ detailEntry.target_type }} #{{ detailEntry.target_id ?? '?' }}
          </span>
        </div>

        <div v-if="detailEntry.before_json" class="drawer-section">
          <div class="drawer-section-title">{{ t('auditLog.detail.beforeTitle') }}</div>
          <pre class="json-block">{{ fmtJson(detailEntry.before_json) }}</pre>
        </div>

        <div v-if="detailEntry.after_json" class="drawer-section">
          <div class="drawer-section-title">{{ t('auditLog.detail.afterTitle') }}</div>
          <pre class="json-block">{{ fmtJson(detailEntry.after_json) }}</pre>
        </div>

        <div v-if="!detailEntry.before_json && !detailEntry.after_json" class="drawer-section">
          <p class="text-muted">{{ t('auditLog.detail.noExtra') }}</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.page-desc {
  margin: -12px 0 16px;
  font-size: 12px;
  color: var(--muted);
}

.count-chip {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  color: var(--accent-h);
}

.table-card {
  padding: 0;
  overflow: hidden;
}

.table-wrap {
  overflow-x: auto;
}

.audit-table {
  width: 100%;
  font-size: 12px;
}

.audit-table th,
.audit-table td {
  padding: 8px 12px;
  vertical-align: top;
}

.col-time {
  width: 5rem;
  white-space: nowrap;
}

.col-actor {
  min-width: 6rem;
  max-width: 10rem;
}

.col-action {
  min-width: 7rem;
  max-width: 11rem;
}

.col-target {
  min-width: 6rem;
  max-width: 9rem;
}

.col-details {
  min-width: 12rem;
}

.cell-line1 {
  font-size: 12px;
  line-height: 1.35;
}

.cell-line2 {
  color: var(--muted);
  font-size: 10px;
  line-height: 1.35;
  margin-top: 2px;
  font-variant-numeric: tabular-nums;
}

.actor-name {
  font-weight: 600;
  word-break: break-all;
}

.target-type {
  font-size: 12px;
}

.target-id {
  margin-left: 4px;
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--muted);
}

.detail-preview {
  display: block;
  font-family: ui-monospace, monospace;
  font-size: 11px;
  color: var(--muted);
  word-break: break-all;
  white-space: pre-wrap;
  line-height: 1.4;
  background: transparent;
  padding: 0;
}

.audit-row {
  cursor: pointer;
}

.audit-row:hover td,
.audit-row:focus-visible td {
  background: color-mix(in srgb, var(--accent) 8%, transparent);
}

.audit-row:focus-visible {
  outline: none;
}

.state-cell {
  text-align: center;
  padding: 36px 16px !important;
  color: var(--muted);
}

.state-cell p {
  margin: 0 0 4px;
}

.text-muted {
  color: var(--muted);
  font-size: 11px;
}

.detail-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 12px 20px;
  font-size: 12px;
}

.json-block {
  margin: 0;
  padding: 12px;
  border-radius: var(--radius);
  border: 1px solid var(--border);
  background: var(--bg);
  font-family: ui-monospace, monospace;
  font-size: 11px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 320px;
  overflow: auto;
}

@media (prefers-reduced-motion: reduce) {
  .audit-row:hover td,
  .audit-row:focus-visible td {
    transition: none;
  }
}
</style>
