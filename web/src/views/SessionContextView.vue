<script setup lang="ts">
import { ref, onMounted } from 'vue'
import {
  getMemoraSessions,
  getMemoraContext,
  type MemoraSession,
  type MemoraContextResponse,
} from '../api'

const sessions = ref<MemoraSession[]>([])
const selectedTask = ref<MemoraSession | null>(null)
const contextData = ref<MemoraContextResponse | null>(null)
const searchQ = ref('')
const hours = ref(24)
const loading = ref(false)
const contextLoading = ref(false)
const error = ref('')
const activeTab = ref<'facts' | 'timeline'>('facts')

async function loadSessions() {
  loading.value = true
  error.value = ''
  try {
    const resp = await getMemoraSessions({ q: searchQ.value || undefined, hours: hours.value })
    sessions.value = resp.sessions
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function selectSession(s: MemoraSession) {
  selectedTask.value = s
  contextLoading.value = true
  try {
    contextData.value = await getMemoraContext(s.task_id)
  } catch (e: unknown) {
    contextData.value = null
    error.value = e instanceof Error ? e.message : '加载上下文失败'
  } finally {
    contextLoading.value = false
  }
}

function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function fmtScore(v: number) {
  return (v * 100).toFixed(0) + '%'
}

onMounted(loadSessions)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>会话上下文</h2>
      <span class="sub">浏览 Memora L1 会话记忆 &amp; 对话线索</span>
    </div>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div class="sc-layout">
      <!-- Left: session list -->
      <div class="sc-sidebar card">
        <div class="sc-search">
          <input
            v-model="searchQ"
            placeholder="搜索 task / session / model…"
            @keyup.enter="loadSessions"
          />
          <select v-model.number="hours" @change="loadSessions" style="width:80px">
            <option :value="6">6h</option>
            <option :value="24">24h</option>
            <option :value="72">3d</option>
            <option :value="168">7d</option>
          </select>
          <button class="btn btn-ghost btn-sm" @click="loadSessions" :disabled="loading">
            {{ loading ? '…' : '刷新' }}
          </button>
        </div>

        <div v-if="loading" class="empty">加载中…</div>
        <div v-else-if="sessions.length === 0" class="empty">暂无会话数据</div>
        <div v-else class="sc-list">
          <div
            v-for="s in sessions"
            :key="s.task_id"
            class="sc-item"
            :class="{ active: selectedTask?.task_id === s.task_id }"
            @click="selectSession(s)"
          >
            <div class="sc-item-title">
              <code>{{ (s.task_id || '').slice(0, 24) }}</code>
              <span v-if="s.latest_model" class="badge badge-blue" style="font-size:11px">{{ s.latest_model }}</span>
            </div>
            <div class="sc-item-meta">
              {{ s.request_count }} 请求
              <template v-if="s.fail_count > 0"> · <span style="color:#dc2626">{{ s.fail_count }} 失败</span></template>
              · {{ fmtDate(s.last_activity) }}
            </div>
          </div>
        </div>
      </div>

      <!-- Right: context detail -->
      <div class="sc-detail card">
        <div v-if="!selectedTask" class="empty" style="padding:40px">
          ← 选择一个会话查看上下文
        </div>
        <div v-else>
          <div class="sc-detail-header">
            <div>
              <strong>Task:</strong> <code>{{ selectedTask.task_id }}</code>
              <span v-if="selectedTask.session_id" style="margin-left:12px">
                <strong>Session:</strong> <code>{{ selectedTask.session_id }}</code>
              </span>
            </div>
            <div v-if="contextData" style="margin-top:4px;font-size:13px;color:#666">
              {{ contextData.request_count }} 请求
              <template v-if="contextData.latest_model"> · 模型 {{ contextData.latest_model }}</template>
              <template v-if="contextData.user_id"> · User ID <code>{{ contextData.user_id }}</code></template>
            </div>
          </div>

          <div class="sc-tabs">
            <button
              class="sc-tab"
              :class="{ active: activeTab === 'facts' }"
              @click="activeTab = 'facts'"
            >
              Memora 事实
              <span v-if="contextData" class="badge">{{ contextData.facts.length }}</span>
            </button>
            <button
              class="sc-tab"
              :class="{ active: activeTab === 'timeline' }"
              @click="activeTab = 'timeline'"
            >
              对话线索
            </button>
          </div>

          <div v-if="contextLoading" class="empty">加载中…</div>

          <template v-else-if="contextData">
            <!-- Facts tab -->
            <div v-if="activeTab === 'facts'" class="sc-facts">
              <div v-if="contextData.facts.length === 0" class="empty">
                该会话暂无 Memora 记忆事实
              </div>
              <div v-else>
                <div v-for="(f, i) in contextData.facts" :key="f.id" class="sc-fact">
                  <div class="sc-fact-header">
                    <span class="sc-fact-idx">#{{ i + 1 }}</span>
                    <span v-if="f.score" class="badge badge-green">{{ fmtScore(f.score) }}</span>
                    <span v-for="t in (f.tags || [])" :key="t" class="badge badge-blue">{{ t }}</span>
                  </div>
                  <div class="sc-fact-text">{{ f.memory }}</div>
                </div>
              </div>
            </div>

            <!-- Timeline tab -->
            <div v-if="activeTab === 'timeline'" class="sc-timeline">
              <div class="empty" style="padding:20px">
                对话线索需通过
                <a :href="`/request-logs?gw_task=${selectedTask.task_id}`" target="_blank">请求日志</a>
                页面查看（按 task 过滤，chrono 模式）
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sc-layout {
  display: flex;
  gap: 16px;
  min-height: 60vh;
}
.sc-sidebar {
  width: 360px;
  min-width: 280px;
  flex-shrink: 0;
  padding: 12px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.sc-search {
  display: flex;
  gap: 6px;
  margin-bottom: 10px;
}
.sc-search input {
  flex: 1;
  min-width: 0;
}
.sc-list {
  overflow-y: auto;
  flex: 1;
}
.sc-item {
  padding: 8px 10px;
  border-radius: 6px;
  cursor: pointer;
  margin-bottom: 4px;
  border: 1px solid transparent;
}
.sc-item:hover { background: #f5f5f5; }
.sc-item.active { background: #eff6ff; border-color: #93c5fd; }
.sc-item-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.sc-item-title code { font-size: 12px; }
.sc-item-meta {
  font-size: 12px;
  color: #888;
  margin-top: 2px;
}

.sc-detail {
  flex: 1;
  min-width: 0;
  padding: 16px;
}
.sc-detail-header {
  margin-bottom: 12px;
  font-size: 14px;
}
.sc-detail-header code { font-size: 13px; }

.sc-tabs {
  display: flex;
  gap: 0;
  border-bottom: 2px solid #e5e7eb;
  margin-bottom: 16px;
}
.sc-tab {
  padding: 8px 16px;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 13px;
  color: #666;
  border-bottom: 2px solid transparent;
  margin-bottom: -2px;
}
.sc-tab.active {
  color: #1d4ed8;
  border-bottom-color: #1d4ed8;
  font-weight: 600;
}
.sc-tab .badge { margin-left: 4px; }

.sc-facts { display: flex; flex-direction: column; gap: 10px; }
.sc-fact {
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 10px 12px;
}
.sc-fact-header {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}
.sc-fact-idx { font-weight: 600; color: #6b7280; font-size: 12px; }
.sc-fact-text {
  font-size: 13px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
