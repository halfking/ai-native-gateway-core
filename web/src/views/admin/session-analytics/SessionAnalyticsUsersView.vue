<template>
  <div class="session-analytics-users-view">
    <PageHeader :title="t('sessionAnalytics.title')" :subtitle="t('nav.item.sessionAnalyticsUsers')">
      <template #actions>
        <button class="sa-btn" type="button" :disabled="loading" @click="refreshAll">
          {{ t('sessionAnalytics.action.refresh') }}
        </button>
      </template>
    </PageHeader>

    <!-- 会话统计面板 (来自 Dashboard) -->
    <SessionStatsPanel style="margin-bottom: 24px;" />

    <!-- Top10 用户排行 -->
    <div class="top-users-section">
      <div class="section-header">
        <h2 class="section-title">{{ t('sessionAnalytics.topUsersTitle') }}</h2>
        <div class="section-actions">
          <div class="sa-range">
            <span class="sa-range__label">{{ t('sessionAnalytics.range.label') }}</span>
            <KxDateRangePicker v-model="draftRange" :presets="presets" @apply="onApplyRange" />
            <SaRangeHint :start="range.start" :end="range.end" />
          </div>
        </div>
      </div>

      <SaStateBanner
        v-if="error"
        kind="error"
        :title="t('sessionAnalytics.error.userRankingFailed')"
        :reason="error"
        :action-label="t('sessionAnalytics.action.retry')"
        @action="loadTopUsers"
      />

      <SaStateBanner
        v-if="stale"
        kind="stale"
        :title="t('sessionAnalytics.state.staleTitle')"
        :reason="t('sessionAnalytics.state.staleReason')"
        :action-label="t('sessionAnalytics.action.refresh')"
        @action="loadTopUsers"
      />

      <p class="sa-note">{{ t('sessionAnalytics.costNote') }}</p>

      <DataTable
        :loading="loading"
        :empty="!loading && topUsers.length === 0"
        :empty-text="t('sessionAnalytics.empty.noResultUserRanking')"
        min-width="1040px"
      >
        <table class="sa-table">
          <thead>
            <tr>
              <th>{{ t('sessionAnalytics.col.rank') }}</th>
              <th>{{ t('sessionAnalytics.col.owner') }}</th>
              <th>{{ t('sessionAnalytics.col.email') }}</th>
              <th>{{ t('sessionAnalytics.col.sessions') }}</th>
              <th>{{ t('sessionAnalytics.col.projects') }}</th>
              <th>{{ t('sessionAnalytics.col.tasks') }}</th>
              <th>{{ t('sessionAnalytics.col.tokens') }}</th>
              <th>{{ t('sessionAnalytics.col.cost') }}</th>
              <th>{{ t('sessionAnalytics.col.lastActive') }}</th>
              <th>{{ t('sessionAnalytics.action.viewDetail') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in topUsers" :key="u.owner">
              <td>{{ u.rank }}</td>
              <td>
                <button class="sa-link" type="button" @click="openDetail(u.owner)">
                  {{ u.display_name || u.owner }}
                </button>
              </td>
              <td>{{ u.email || '—' }}</td>
              <td>{{ fmt(u.session_count) }}</td>
              <td>{{ fmt(u.project_count) }}</td>
              <td>{{ fmt(u.task_count) }}</td>
              <td>{{ fmt(u.total_tokens) }}</td>
              <td>{{ money(u.total_cost) }}</td>
              <td>{{ formatTime(u.last_active_at) }}</td>
              <td>
                <button class="sa-link" type="button" @click="openDetail(u.owner)">
                  {{ t('sessionAnalytics.action.viewDetail') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </DataTable>

      <div v-if="topUsers.length > 0" class="view-all-link">
        <router-link to="/admin/session-analytics/user-ranking" class="sa-link">
          {{ t('sessionAnalytics.action.viewAllUsers') }} →
        </router-link>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// SessionAnalyticsUsersView.vue — 会话分析用户页面
// 整合 Dashboard 的"会话与统计"内容 + Top10 用户排行

import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import PageHeader from '../../../components/ui/PageHeader.vue'
import SessionStatsPanel from '../../../components/SessionStatsPanel.vue'
import DataTable from '../../../components/ui/DataTable.vue'
import KxDateRangePicker from '../../../components/ui/KxDateRangePicker.vue'
import { rangeFromSpanDays } from '../../../components/ui/kxDatePresets'
import type { KxDateRange, KxDateRangePreset } from '../../../components/ui/kx-date-types'
import SaStateBanner from './components/SaStateBanner.vue'
import SaRangeHint from './components/SaRangeHint.vue'
import { useSessionAnalyticsLoader } from './useSessionAnalytics'
import {
  getUserRanking,
  type UserRankingRow,
} from '../../../api/sessionAnalytics'
import { formatDateTime } from '../../../utils/datetime'

const { t } = useI18n()
const router = useRouter()
const { loading, error, stale, run } = useSessionAnalyticsLoader()

const range = ref<KxDateRange>(rangeFromSpanDays(7))
const draftRange = ref<KxDateRange>(range.value)
const topUsers = ref<UserRankingRow[]>([])

const presets = computed<KxDateRangePreset[]>(() => [
  { id: 'sa-7d', labelKey: 'sessionAnalytics.range.preset7', resolve: () => rangeFromSpanDays(7) },
  { id: 'sa-30d', labelKey: 'sessionAnalytics.range.preset30', resolve: () => rangeFromSpanDays(30) },
])

function onApplyRange(newRange: KxDateRange) {
  range.value = newRange
  draftRange.value = newRange
  void loadTopUsers()
}

async function loadTopUsers() {
  const result = await run(
    async () =>
      await getUserRanking({
        start_date: range.value.start,
        end_date: range.value.end,
        sort_by: 'tokens',
        order: 'desc',
        limit: 10,
        offset: 0,
      }),
    'sessionAnalytics.error.userRankingFailed',
  )
  if (result) {
    topUsers.value = result.users || []
  }
}

function refreshAll() {
  void loadTopUsers()
}

function openDetail(owner: string) {
  void router.push(`/admin/session-analytics/user-ranking/${owner}`)
}

function fmt(value: number | null | undefined): string {
  if (value == null) return '0'
  return value.toLocaleString()
}

function money(value: number | null | undefined): string {
  if (value == null) return '$0.00'
  // 内部成本（美元）
  return `$${value.toFixed(2)}`
}

function formatTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return formatDateTime(iso)
}

onMounted(() => {
  void loadTopUsers()
})
</script>

<style scoped>
.session-analytics-users-view {
  padding: 20px;
  max-width: 100%;
}

.top-users-section {
  background: var(--kx-bg-card, #fff);
  border-radius: 8px;
  padding: 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
}

.section-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
  flex-wrap: wrap;
  gap: 16px;
}

.section-title {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
  color: var(--kx-text-primary, #303133);
}

.section-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}

.sa-range {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sa-range__label {
  font-size: 14px;
  color: var(--kx-text-secondary, #606266);
  white-space: nowrap;
}

.sa-btn {
  padding: 8px 16px;
  border: 1px solid var(--kx-border-base, #dcdfe6);
  border-radius: 4px;
  background: var(--kx-bg-base, #fff);
  color: var(--kx-text-primary, #303133);
  font-size: 14px;
  cursor: pointer;
  transition: all 0.2s;
}

.sa-btn:hover:not(:disabled) {
  border-color: var(--kx-primary, #409eff);
  color: var(--kx-primary, #409eff);
}

.sa-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.sa-note {
  margin: 0 0 16px 0;
  padding: 12px;
  background: var(--kx-bg-light, #f5f7fa);
  border-radius: 4px;
  font-size: 13px;
  color: var(--kx-text-secondary, #606266);
}

.sa-table {
  width: 100%;
  border-collapse: collapse;
}

.sa-table th,
.sa-table td {
  padding: 12px 16px;
  text-align: left;
  border-bottom: 1px solid var(--kx-border-lighter, #e4e7ed);
}

.sa-table th {
  background: var(--kx-bg-light, #f5f7fa);
  font-weight: 600;
  font-size: 14px;
  color: var(--kx-text-primary, #303133);
}

.sa-table td {
  font-size: 14px;
  color: var(--kx-text-regular, #606266);
}

.sa-table tbody tr:hover {
  background: var(--kx-bg-lighter, #fafafa);
}

.sa-link {
  color: var(--kx-primary, #409eff);
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
  text-decoration: none;
  font-size: inherit;
  transition: color 0.2s;
}

.sa-link:hover {
  color: var(--kx-primary-light, #66b1ff);
  text-decoration: underline;
}

.view-all-link {
  margin-top: 16px;
  text-align: center;
  padding-top: 16px;
  border-top: 1px solid var(--kx-border-lighter, #e4e7ed);
}

.view-all-link .sa-link {
  font-size: 14px;
  font-weight: 500;
}
</style>
