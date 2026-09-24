<script setup lang="ts">
// TaskProfileView.vue — 任务档案页（v2 规划 P0③，2026-09-24）。
//
// taskprofile 插件模块的运营门面：档案注册表 + 修正统计 + 分层建议 + apply
// （写 task_type_tier_config）+ overlay reload。替代标注统计页内嵌的
// taskprofile 区块成为一等入口；后端契约全部复用 api/taskProfile.ts
// （/api/admin/task-profile 前缀，taskprofile/handler.go）。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getTaskProfile,
  getTaskTypeCorrectionStats,
  applyTierConfig,
  reloadTaskProfile,
  exportCorrectionsBlob,
  type TaskProfileView as TaskProfileViewModel,
  type CorrectionStatsResponse,
} from '../api/taskProfile'

const { t } = useI18n()

const profile = ref<TaskProfileViewModel | null>(null)
const stats = ref<CorrectionStatsResponse | null>(null)
const loading = ref(false)
const error = ref('')
const notice = ref('')
const sinceDays = ref(30)
const applying = ref(false)
const reloading = ref(false)
const exporting = ref(false)

type ProfileRow = TaskProfileViewModel['profiles'][number] & {
  rate: number
  total: number
  suggestTier: string
  suggestSource: string
}

const rows = computed<ProfileRow[]>(() => {
  if (!profile.value) return []
  return profile.value.profiles.map((p) => {
    const cs = stats.value?.stats?.[p.task_type]
    const sug = stats.value?.suggestions?.[p.task_type]
    return {
      ...p,
      total: cs?.total ?? 0,
      rate: cs?.correction_rate ?? 0,
      suggestTier: sug?.tier ?? p.suggestion?.tier ?? p.preferred_tier,
      suggestSource: sug?.tier_source ?? p.suggestion?.tier_source ?? '',
    }
  }).sort((a, b) => b.rate - a.rate || a.task_type.localeCompare(b.task_type))
})

const totalCorrected = computed(() =>
  rows.value.reduce((acc, r) => acc + (r.rate > 0 ? 1 : 0), 0)
)

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const [p, s] = await Promise.all([
      getTaskProfile(),
      getTaskTypeCorrectionStats(sinceDays.value),
    ])
    profile.value = p
    stats.value = s
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('taskProfile.loadFailed')
  } finally {
    loading.value = false
  }
}

async function apply() {
  if (!window.confirm(t('taskProfile.action.applyConfirm'))) return
  applying.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await applyTierConfig([])
    notice.value = r.applied.length === 0
      ? t('taskProfile.action.applyNone')
      : t('taskProfile.action.applyDone', {
          types: r.applied.map((a) => `${a.task_type}→${a.preferred_tier}`).join(', '),
        })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    applying.value = false
  }
}

async function reload() {
  if (!window.confirm(t('taskProfile.action.reloadConfirm'))) return
  reloading.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await reloadTaskProfile()
    notice.value = t('taskProfile.action.reloadDone', { version: r.registry_version })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    reloading.value = false
  }
}

async function exportCsv() {
  exporting.value = true
  error.value = ''
  try {
    const blob = await exportCorrectionsBlob({ sinceDays: sinceDays.value })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `task-type-corrections-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    exporting.value = false
  }
}

function rateClass(rate: number): string {
  if (rate >= 0.3) return 'rate-high'
  if (rate >= 0.1) return 'rate-mid'
  return 'rate-low'
}

onMounted(load)
</script>

<template>
  <div class="stats-page">
    <div class="page-header">
      <h2>{{ t('taskProfile.title') }}</h2>
      <div class="header-actions">
        <label class="inline-label">
          {{ t('taskProfile.action.days') }}
          <select v-model.number="sinceDays" class="select-sm" @change="load">
            <option :value="7">7</option>
            <option :value="14">14</option>
            <option :value="30">30</option>
            <option :value="90">90</option>
          </select>
        </label>
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('taskProfile.refreshing') : t('taskProfile.refresh') }}
        </button>
      </div>
    </div>

    <p class="page-desc">{{ t('taskProfile.desc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
    <div v-if="notice" class="alert alert-success" role="status">{{ notice }}</div>

    <div v-if="loading" class="loading-container">
      <p>{{ t('taskProfile.loading') }}</p>
    </div>

    <div v-else-if="profile" class="stats-container">
      <div class="stats-cards">
        <div class="stat-card">
          <div class="stat-icon stat-icon-primary">🗂️</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('taskProfile.registry.version') }}</div>
            <div class="stat-value stat-value-sm">{{ profile.registry_version }}</div>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-info">🧬</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('taskProfile.registry.schema') }}</div>
            <div class="stat-value">{{ profile.schema_version }}</div>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-success">🧩</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('taskProfile.registry.types') }}</div>
            <div class="stat-value">{{ profile.profiles.length }}</div>
          </div>
        </div>
        <div class="stat-card">
          <div class="stat-icon stat-icon-warning">🛠️</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('taskProfile.registry.corrections') }}</div>
            <div class="stat-value">{{ totalCorrected }}</div>
          </div>
        </div>
      </div>

      <div class="toolbar">
        <button class="btn btn-primary btn-sm" :disabled="applying" @click="apply">
          {{ applying ? t('taskProfile.status.applying') : t('taskProfile.action.apply') }}
        </button>
        <button class="btn btn-sm" :disabled="reloading" @click="reload">
          {{ reloading ? t('taskProfile.status.reloading') : t('taskProfile.action.reload') }}
        </button>
        <button class="btn btn-sm" :disabled="exporting" @click="exportCsv">
          {{ exporting ? t('taskProfile.status.exporting') : t('taskProfile.action.exportCsv') }}
        </button>
      </div>

      <div class="table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('taskProfile.table.taskType') }}</th>
              <th>{{ t('taskProfile.table.description') }}</th>
              <th>{{ t('taskProfile.table.tier') }}</th>
              <th>{{ t('taskProfile.table.fallbacks') }}</th>
              <th>{{ t('taskProfile.table.minConf') }}</th>
              <th>{{ t('taskProfile.table.total') }}</th>
              <th>{{ t('taskProfile.table.rate') }}</th>
              <th>{{ t('taskProfile.table.suggestion') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.task_type">
              <td class="mono">{{ r.task_type }}</td>
              <td class="desc-cell">{{ r.description }}</td>
              <td><span class="tier-badge" :class="`tier-${r.preferred_tier}`">{{ r.preferred_tier }}</span></td>
              <td class="mono dim">{{ r.fallback_tiers.join(' → ') || '—' }}</td>
              <td class="mono">{{ r.min_confidence.toFixed(2) }}</td>
              <td class="mono">{{ r.total }}</td>
              <td>
                <span :class="rateClass(r.rate)">{{ (r.rate * 100).toFixed(1) }}%</span>
              </td>
              <td>
                <span
                  v-if="r.suggestTier !== r.preferred_tier"
                  class="tier-badge tier-changed"
                  :title="r.suggestSource"
                >{{ r.suggestTier }} ↑</span>
                <span v-else class="dim">{{ r.suggestTier }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.inline-label {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  color: var(--text-secondary, #666);
}
.select-sm {
  padding: 0.2rem 0.4rem;
  border: 1px solid var(--border-color, #ccc);
  border-radius: 6px;
  background: var(--bg-card, #fff);
}
.toolbar {
  display: flex;
  gap: 0.6rem;
  margin: 1rem 0;
  flex-wrap: wrap;
}
.table-wrap {
  overflow-x: auto;
}
.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}
.data-table th,
.data-table td {
  padding: 0.5rem 0.6rem;
  border-bottom: 1px solid var(--border-color, #e5e7eb);
  text-align: left;
  vertical-align: top;
}
.data-table th {
  font-weight: 600;
  color: var(--text-secondary, #555);
  white-space: nowrap;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: nowrap;
}
.dim {
  color: var(--text-secondary, #999);
}
.desc-cell {
  min-width: 14rem;
  max-width: 26rem;
}
.stat-value-sm {
  font-size: 1rem;
}
.tier-badge {
  display: inline-block;
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  border: 1px solid var(--border-color, #d1d5db);
}
.tier-tier-a {
  background: rgba(239, 68, 68, 0.12);
  border-color: rgba(239, 68, 68, 0.4);
}
.tier-tier-b {
  background: rgba(59, 130, 246, 0.12);
  border-color: rgba(59, 130, 246, 0.4);
}
.tier-tier-c {
  background: rgba(16, 185, 129, 0.12);
  border-color: rgba(16, 185, 129, 0.4);
}
.tier-changed {
  background: rgba(245, 158, 11, 0.15);
  border-color: rgba(245, 158, 11, 0.5);
  cursor: help;
}
.rate-high {
  color: var(--danger, #dc2626);
  font-weight: 600;
}
.rate-mid {
  color: var(--warning, #d97706);
}
.rate-low {
  color: var(--text-secondary, #6b7280);
}
</style>
