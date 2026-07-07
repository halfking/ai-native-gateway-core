<template>
  <div class="page-layout">
    <div class="page-header">
      <div>
        <h2>{{ t('sessions.list.title') }}</h2>
        <p class="text-muted">{{ t('sessions.list.description', { hours }) }}</p>
      </div>
      <div class="header-actions">
        <select v-model="hours" class="cf-select" @change="loadData">
          <option :value="24">24h</option>
          <option :value="72">72h</option>
          <option :value="168">7d</option>
          <option :value="720">30d</option>
        </select>
        <input v-model="searchQ" class="cf-input" :placeholder="t('sessions.list.searchPlaceholder')" :aria-label="t('sessions.list.searchPlaceholder')" @keyup.enter="loadData" />
        <button class="btn btn-primary btn-sm" @click="loadData">{{ t('sessions.list.refresh') }}</button>
      </div>
    </div>

    <!-- 加载态：spinner + 文案，避免裸文本 -->
    <div v-if="loading" class="loading-state" role="status" aria-live="polite">
      <span class="spinner" aria-hidden="true"></span>
      <span>{{ t('sessions.list.loading') }}</span>
    </div>
    <!-- 错误态：带重试按钮 -->
    <div v-if="error && !loading" class="alert alert-danger" role="alert">
      <span class="alert-icon" aria-hidden="true">⚠️</span>
      <span class="alert-text">{{ error }}</span>
      <button class="btn btn-sm alert-retry" @click="loadData" aria-label="重新加载">{{ t('sessions.list.refresh') }}</button>
    </div>

    <!-- Stats -->
    <div v-if="data" class="card" style="margin-bottom: 12px; display: flex; gap: 24px; flex-wrap: wrap;">
      <div><span class="text-muted" style="font-size:12px;">{{ t('sessions.list.totalSessions') }}</span><div style="font-size:20px;font-weight:600;">{{ data.total }}</div></div>
      <div><span class="text-muted" style="font-size:12px;">{{ t('sessions.list.compressed') }}</span><div style="font-size:20px;font-weight:600;color:var(--accent)">{{ compressedCount }}</div></div>
      <div><span class="text-muted" style="font-size:12px;">{{ t('sessions.list.currentPage') }}</span><div style="font-size:20px;font-weight:600;">{{ data.sessions.length }}</div></div>
    </div>

    <!-- Session Table -->
    <div class="table-wrap" v-if="data?.sessions.length">
      <table class="data-table">
        <thead>
          <tr>
            <th v-for="(h, i) in t('sessions.list.tableHeaders')" :key="i">{{ h }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in data.sessions" :key="s.session_id">
            <td><code class="cell-clip" style="max-width:200px;" :title="s.session_id">{{ s.session_id }}</code></td>
            <td>{{ s.request_count }}</td>
            <td><span class="text-muted">{{ s.model_used || '—' }}</span></td>
            <td>
              <div class="cell-line1">{{ s.time_start }}</div>
              <div class="cell-line2">{{ s.time_end }}</div>
            </td>
            <td><span class="badge badge-gray">{{ s.duration }}</span></td>
            <td>
              <span v-if="s.is_compressed" class="badge badge-blue">{{ t('sessions.list.compressedBadge') }}</span>
              <span v-else class="badge badge-gray">{{ t('sessions.list.notCompressedBadge') }}</span>
            </td>
            <td>
              <span :class="s.success_rate >= 90 ? 'badge badge-green' : s.success_rate >= 50 ? 'badge badge-yellow' : 'badge badge-red'">
                {{ Math.round(s.success_rate) }}%
              </span>
            </td>
            <td>
              <a :href="'/session-compare?session_id=' + encodeURIComponent(s.session_id)" class="trace-link" style="margin-right:8px;">{{ t('sessions.list.actionCompare') }}</a>
              <a :href="'/request-logs?gw_session_id=' + encodeURIComponent(s.session_id)" class="trace-link">{{ t('sessions.list.actionLogs') }}</a>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-else-if="data && !data.sessions.length" class="empty" style="padding: 60px;">
      <p>{{ t('sessions.list.empty') }}</p>
      <p class="text-muted">{{ t('sessions.list.emptyHint') }}</p>
    </div>

    <!-- Pagination -->
    <div v-if="data && data.pages > 1" class="pagination-bar">
      <div class="text-muted" style="font-size:12px;">{{ t('sessions.list.paginationTotal', { n: data.total, pages: data.pages }) }}</div>
      <div style="display:flex;gap:8px;">
        <button class="btn btn-sm" :disabled="page <= 1" @click="page--; loadData()">{{ t('sessions.list.previous') }}</button>
        <span style="display:flex;align-items:center;font-size:12px;">{{ t('sessions.list.paginationPage', { current: page, total: data.pages }) }}</span>
        <button class="btn btn-sm" :disabled="page >= data.pages" @click="page++; loadData()">{{ t('sessions.list.next') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { getSessionList } from '../api'
import type { SessionListResponse } from '../api'

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const data = ref<SessionListResponse | null>(null)
const page = ref(1)
const hours = ref(72)
const searchQ = ref('')

const compressedCount = computed(() => data.value?.sessions.filter(s => s.is_compressed).length || 0)

async function loadData() {
  loading.value = true
  error.value = ''
  try {
    data.value = await getSessionList({
      page: page.value,
      size: 20,
      hours: hours.value,
      q: searchQ.value.trim() || undefined,
    })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(loadData)
</script>

<style scoped>
.page-layout { padding: 16px; max-width: 1400px; margin: 0 auto; }
.header-actions { display: flex; gap: 8px; align-items: center; }
.cf-input { background: var(--card); border: 1px solid var(--border); border-radius: var(--radius); padding: 6px 12px; color: var(--text); font-size: 13px; width: 200px; }
.cf-select { background: var(--card); border: 1px solid var(--border); border-radius: var(--radius); padding: 6px 8px; color: var(--text); font-size: 12px; }
.text-muted { color: var(--muted); }
.trace-link { color: var(--accent); cursor: pointer; text-decoration: none; font-size: 12px; }
.trace-link:hover { text-decoration: underline; }
.badge { font-size: 10px; padding: 1px 6px; border-radius: 4px; }
.badge-blue { background: color-mix(in srgb, var(--accent) 20%, transparent); color: var(--accent-h); }
.badge-green { background: color-mix(in srgb, var(--success) 20%, transparent); color: var(--success); }
.badge-red { background: color-mix(in srgb, var(--danger) 20%, transparent); color: var(--danger); }
.badge-yellow { background: color-mix(in srgb, var(--warning) 20%, transparent); color: var(--warning); }
.badge-gray { background: var(--border); color: var(--muted); }
code { font-size: 12px; background: var(--bg); padding: 1px 4px; border-radius: 3px; }

/* 加载态：spinner + 居中文案 */
.loading-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 60px 20px;
  color: var(--muted);
  font-size: 13px;
}

.spinner {
  display: inline-block;
  width: 16px;
  height: 16px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 错误态增强：图标 + 文本 + 重试按钮 */
.alert {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.alert-icon { font-size: 16px; flex-shrink: 0; }
.alert-text { flex: 1 1 auto; min-width: 0; word-break: break-word; }
.alert-retry { flex-shrink: 0; margin-left: auto; }
</style>