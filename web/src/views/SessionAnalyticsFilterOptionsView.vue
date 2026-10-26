<script setup lang="ts">
// SessionAnalyticsFilterOptionsView — 批 158a（2026-10-17）
// P1 sessionAnalyticsFilterOptions 孤儿接前端：3 个下拉框（clients /
// tasks / users），后端 listSessionAnalyticsFilterOptions 聚合 6 端点
// 数据（29 跳已验证全有 12-18 命中租户隔离）。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  listSessionAnalyticsFilterOptions,
  type SessionAnalyticsFilterOptions,
} from '../api/sessionAnalyticsFilterOptions'

const router = useRouter()

const loading = ref(false)
const error = ref('')
const options = ref<SessionAnalyticsFilterOptions>({
  clients: [],
  tasks: [],
  users: [],
  refreshedAt: '',
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    options.value = await listSessionAnalyticsFilterOptions({ hours: 24, limit: 20 })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function goToClient(clientId: string) {
  router.push({
    path: `/admin/session-analytics/clients/${encodeURIComponent(clientId)}`,
  })
}

function goToTask(taskId: string) {
  router.push({
    path: `/admin/session-analytics/tasks/${encodeURIComponent(taskId)}`,
  })
}

function goToUser(user: string) {
  router.push({
    path: `/admin/session-analytics/users/${encodeURIComponent(user)}`,
  })
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="session-analytics-filter-options-view">
    <header class="filter-options-header">
      <h1 class="filter-options-title">会话分析筛选</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="filter-options-error">{{ error }}</p>

    <div v-if="loading && options.clients.length === 0" class="filter-options-loading">
      {{ $t('common.feedback.loading') }}
    </div>

    <div v-else class="filter-options-grid">
      <section class="filter-options-section">
        <h2 class="filter-options-section-title">{{ $t('common.terms.client') }}</h2>
        <ul v-if="options.clients.length" class="filter-options-list">
          <li v-for="c in options.clients" :key="c" class="filter-options-item">
            <button class="filter-options-link" @click="goToClient(c)">{{ c }}</button>
          </li>
        </ul>
        <p v-else class="filter-options-empty">{{ $t('common.terms.noData') }}</p>
      </section>

      <section class="filter-options-section">
        <h2 class="filter-options-section-title">{{ $t('common.terms.task') }}</h2>
        <ul v-if="options.tasks.length" class="filter-options-list">
          <li v-for="t_id in options.tasks" :key="t_id" class="filter-options-item">
            <button class="filter-options-link" @click="goToTask(t_id)">{{ t_id }}</button>
          </li>
        </ul>
        <p v-else class="filter-options-empty">{{ $t('common.terms.noData') }}</p>
      </section>

      <section class="filter-options-section">
        <h2 class="filter-options-section-title">{{ $t('common.terms.user') }}</h2>
        <ul v-if="options.users.length" class="filter-options-list">
          <li v-for="u in options.users" :key="u" class="filter-options-item">
            <button class="filter-options-link" @click="goToUser(u)">{{ u }}</button>
          </li>
        </ul>
        <p v-else class="filter-options-empty">{{ $t('common.terms.noData') }}</p>
      </section>
    </div>

    <footer v-if="options.refreshedAt" class="filter-options-footer">
      最近刷新: {{ options.refreshedAt }}
    </footer>
  </div>
</template>

<style scoped>
.session-analytics-filter-options-view {
  padding: var(--space-4);
}
.filter-options-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-3);
}
.filter-options-title {
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
}
.filter-options-error {
  color: var(--danger);
  padding: var(--space-2);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}
.filter-options-loading {
  color: var(--text-muted);
  padding: var(--space-3);
}
.filter-options-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: var(--space-4);
}
.filter-options-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
}
.filter-options-section-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-2);
}
.filter-options-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.filter-options-item {
  padding: var(--space-1) 0;
  border-bottom: 1px solid var(--border-muted);
}
.filter-options-item:last-child {
  border-bottom: none;
}
.filter-options-link {
  background: none;
  border: none;
  color: var(--app-primary);
  cursor: pointer;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  padding: 0;
  text-align: left;
}
.filter-options-link:hover {
  text-decoration: underline;
}
.filter-options-empty {
  color: var(--text-muted);
  font-style: italic;
  margin: var(--space-2) 0;
}
.filter-options-footer {
  margin-top: var(--space-3);
  color: var(--text-muted);
  font-size: var(--text-sm);
}
@media (max-width: 768px) {
  .filter-options-grid {
    grid-template-columns: 1fr;
  }
}
</style>
