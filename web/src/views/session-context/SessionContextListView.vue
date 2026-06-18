<script setup lang="ts">
import { computed, inject, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { MemoraSession } from '../../api'
import {
  displayKey,
  displayTitle,
  displayUser,
  displayMemoraPreview,
  hasMemoraPreview,
  fmtDate,
  sessionRowKey,
  buildSessionQueryParams,
  type useSessionFilters,
  type useSessionList,
} from '../../composables/useSessionContext'

const route = useRoute()
const router = useRouter()

const filters = inject<ReturnType<typeof useSessionFilters>>('sessionContextFilters')!
const list = inject<ReturnType<typeof useSessionList>>('sessionContextList')!

const loading = list.loading
const error = list.error
const topicSessions = list.topicSessions
const noTopicSessions = list.noTopicSessions
const sessions = list.sessions

const noTopicOnly = computed(() => route.query.section === 'no-topic')
const rows = computed(() => (noTopicOnly.value ? noTopicSessions.value : topicSessions.value))

async function applyFilters() {
  try {
    await list.loadSessions({
      q: filters.searchQ.value || undefined,
      owner_user: filters.searchOwner.value || undefined,
      hours: filters.hours.value,
      no_topic_window: filters.noTopicWindow.value,
    })
  } catch { /* list.error */ }
}

function openSession(s: MemoraSession) {
  if (s.no_topic || !s.task_id || s.task_id === '[空]') {
    router.push({
      path: '/session-context/_no-topic',
      query: {
        label: s.no_topic_label || '',
        prefix: s.api_key_prefix || '',
        hours: String(filters.hours.value),
      },
    })
    return
  }
  router.push({
    path: `/session-context/${encodeURIComponent(s.task_id)}`,
    query: buildSessionQueryParams({
      hours: filters.hours.value,
      no_topic_window: filters.noTopicWindow.value,
      section: noTopicOnly.value ? 'no-topic' : 'topic',
      session_id: s.session_id && s.session_id !== '[空]' ? s.session_id : undefined,
      rc: s.request_count,
    }),
  })
}

onMounted(() => {
  if (sessions.value.length === 0 && !loading.value) applyFilters()
})
</script>

<template>
  <div class="tab-content">
    <div v-if="error" class="alert alert-danger compact-alert">{{ error }}</div>

    <div class="card compact-card">
      <div class="toolbar-row">
        <input
          v-model="filters.searchQ.value"
          class="filter-input"
          placeholder="搜索 task / session / model…"
          @keyup.enter="applyFilters"
        />
        <input
          v-model="filters.searchOwner.value"
          class="filter-input narrow"
          placeholder="用户模糊匹配…"
          @keyup.enter="applyFilters"
        />
        <select v-model.number="filters.hours.value" class="filter-select" @change="applyFilters">
          <option :value="6">近 6 小时</option>
          <option :value="24">近 24 小时</option>
          <option :value="72">近 3 天</option>
          <option :value="168">近 7 天</option>
        </select>
        <select
          v-model.number="filters.noTopicWindow.value"
          class="filter-select narrow"
          title="无主题会话按小时聚合窗口"
          @change="applyFilters"
        >
          <option :value="1">无主题 1h 窗</option>
          <option :value="2">无主题 2h 窗</option>
          <option :value="6">无主题 6h 窗</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="applyFilters">
          {{ loading ? '加载中…' : '查询' }}
        </button>
      </div>
    </div>

    <div class="card compact-card">
      <div v-if="loading" class="state-box">正在加载会话列表…</div>
      <div v-else-if="error" class="state-box">
        <p>无法加载会话数据</p>
        <p class="text-muted">{{ error }}</p>
        <button class="btn btn-ghost btn-sm" @click="applyFilters">重试</button>
      </div>
      <div v-else-if="rows.length === 0" class="state-box">
        <p>{{ noTopicOnly ? '该时间窗内暂无无主题会话' : '该时间窗内暂无有主题会话' }}</p>
        <p class="text-muted">
          {{ noTopicOnly
            ? '可扩大时间范围，或在请求日志中按 Key 前缀筛选'
            : '客户端需在请求头传入 gw_task_id 才会归入有主题会话' }}
        </p>
      </div>
      <div v-else class="table-wrap">
        <table class="dense-table">
          <thead>
            <tr>
              <th class="col-type">类型</th>
              <th>用户</th>
              <th>Key</th>
              <th>标题 / 会话</th>
              <th>可读摘要</th>
              <th class="num">消息</th>
              <th>模型</th>
              <th>开始时间</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="s in rows"
              :key="sessionRowKey(s)"
              class="clickable"
              :class="{ 'no-topic-row': s.no_topic }"
              @click="openSession(s)"
            >
              <td>
                <span class="badge" :class="s.no_topic ? 'badge-yellow' : 'badge-blue'">
                  {{ s.no_topic ? '无主题' : '主题' }}
                </span>
              </td>
              <td class="ellipsis">{{ displayUser(s) }}</td>
              <td class="mono ellipsis">{{ displayKey(s) }}</td>
              <td class="ellipsis" :title="displayTitle(s)">{{ displayTitle(s) }}</td>
              <td
                class="ellipsis preview-col"
                :class="{ 'preview-empty': !hasMemoraPreview(s) }"
                :title="displayMemoraPreview(s)"
              >{{ displayMemoraPreview(s) }}</td>
              <td class="num">
                <span class="count-chip">{{ s.request_count }}</span>
                <span v-if="s.fail_count > 0" class="fail-chip">{{ s.fail_count }} 失败</span>
              </td>
              <td>
                <span v-if="s.latest_model && s.latest_model !== '[空]'" class="badge badge-gray">{{ s.latest_model }}</span>
                <span v-else class="text-muted">—</span>
              </td>
              <td class="text-muted">{{ fmtDate(s.first_activity) }}</td>
              <td class="text-muted">▶</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tab-content { display: flex; flex-direction: column; gap: 8px; }
.compact-card { padding: 8px 10px; }
.compact-alert { padding: 6px 10px; font-size: 12px; }
.toolbar-row { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.filter-input {
  flex: 1;
  min-width: 140px;
  max-width: 220px;
  padding: 4px 8px;
  font-size: 12px;
}
.filter-input.narrow { max-width: 140px; flex: 0 1 140px; }
.filter-select { width: auto; padding: 4px 8px; font-size: 11px; }
.filter-select.narrow { max-width: 110px; }
.table-wrap { overflow-x: auto; }
.dense-table { width: 100%; border-collapse: collapse; font-size: 12px; }
.dense-table th, .dense-table td { padding: 5px 8px; border-bottom: 1px solid var(--border); text-align: left; }
.dense-table th { font-size: 10px; color: var(--muted); font-weight: 600; }
.dense-table tbody tr.clickable { cursor: pointer; }
.dense-table tbody tr:hover { background: var(--bg-subtle); }
.dense-table tbody tr.no-topic-row { background: rgba(210, 153, 34, 0.06); }
.col-type { width: 56px; }
.num { text-align: right; white-space: nowrap; }
.mono { font-family: ui-monospace, monospace; font-size: 11px; }
.ellipsis { max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preview-col { max-width: 280px; font-size: 11px; line-height: 1.4; }
.preview-empty { color: var(--muted); font-style: italic; }
.count-chip {
  display: inline-block;
  background: rgba(99, 102, 241, 0.15);
  color: var(--accent-h);
  border-radius: 10px;
  padding: 0 6px;
  font-size: 10px;
}
.fail-chip {
  display: inline-block;
  margin-left: 4px;
  background: rgba(248, 81, 73, 0.15);
  color: var(--danger);
  border-radius: 10px;
  padding: 0 6px;
  font-size: 10px;
}
.state-box { padding: 28px 12px; text-align: center; font-size: 12px; color: var(--muted); }
.state-box p { margin: 0 0 6px; }
.text-muted { color: var(--muted); font-size: 11px; }
.badge-yellow { background: rgba(210, 153, 34, 0.2); color: var(--warning); }
</style>
