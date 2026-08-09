<script setup lang="ts">
// TurnsListView.vue — 跨会话轮次列表页（2026-08-09）
// 展示所有会话的轮次记录，按时间倒序分页，点击跳转到会话详情页。
// 与 /request-logs（原始请求日志）并存，数据来自 gateway.session_turns 表。

import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listTurns, type TurnInList } from '../api/turns'

const router = useRouter()
const loading = ref(false)
const items = ref<TurnInList[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const error = ref('')

// 筛选参数
const modelFilter = ref('')
const providerFilter = ref('')
const statusCodeFilter = ref('')

async function load(reset = true) {
  loading.value = true
  error.value = ''
  try {
    if (reset) {
      items.value = []
      nextCursor.value = ''
    }
    const params: Parameters<typeof listTurns>[0] = { limit: 50 }
    if (nextCursor.value) params.cursor = nextCursor.value
    if (modelFilter.value) params.model = modelFilter.value
    if (providerFilter.value) params.provider = providerFilter.value
    if (statusCodeFilter.value) params.status_code = parseInt(statusCodeFilter.value, 10)

    const res = await listTurns(params)
    items.value = [...items.value, ...res.items]
    hasMore.value = res.has_more
    nextCursor.value = res.next_cursor
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function openTurn(turn: TurnInList) {
  router.push({
    path: `/admin/sessions/${turn.session_id}`,
    query: { turn: String(turn.turn_no), focus: '1' }
  })
}

function resetAndLoad() {
  load(true)
}

onMounted(() => {
  load(true)
})
</script>

<template>
  <div class="turns-list-view">
    <div class="header">
      <h1>轮次列表</h1>
      <p class="subtitle">所有会话的轮次记录，按时间倒序</p>
    </div>

    <!-- 筛选区 -->
    <div class="filter-bar">
      <input
        v-model="modelFilter"
        type="text"
        placeholder="按模型筛选"
        class="filter-input"
        @keyup.enter="resetAndLoad"
      />
      <input
        v-model="providerFilter"
        type="text"
        placeholder="按供应商筛选"
        class="filter-input"
        @keyup.enter="resetAndLoad"
      />
      <input
        v-model="statusCodeFilter"
        type="text"
        placeholder="状态码"
        class="filter-input"
        @keyup.enter="resetAndLoad"
      />
      <button class="btn btn-primary" @click="resetAndLoad">查询</button>
    </div>

    <!-- 错误提示 -->
    <div v-if="error" class="error-banner">
      {{ error }}
    </div>

    <!-- 列表 -->
    <div class="list">
      <div
        v-for="turn in items"
        :key="`${turn.session_id}-${turn.turn_no}`"
        class="turn-row"
        @click="openTurn(turn)"
      >
        <div class="col col-req">
          <div class="meta">
            <span class="turn-no">#{{ turn.turn_no }}</span>
            <span class="ts">{{ new Date(turn.ts).toLocaleString() }}</span>
            <span :class="['verdict', `tag-${turn.injection_verdict || 'skip'}`]">
              injection: {{ turn.injection_verdict || 'skip' }}
            </span>
          </div>
          <div class="preview">{{ turn.title || '(无请求摘要)' }}</div>
          <div class="badges">
            <span class="badge">Δ {{ turn.request_tokens }} tok</span>
            <span v-if="turn.attachment_count > 0" class="badge">
              &#128206; {{ turn.attachment_count }}
            </span>
            <span class="badge model">{{ turn.model }}</span>
            <span class="badge session-id" :title="turn.session_id">{{ turn.session_id.slice(0, 12) }}...</span>
          </div>
        </div>
        <div class="col col-resp">
          <div class="meta">
            <span class="turn-no">#{{ turn.turn_no }}</span>
            <span :class="['verdict', `tag-${turn.output_verdict || 'skip'}`]">
              output: {{ turn.output_verdict || 'skip' }}
            </span>
          </div>
          <div class="preview">{{ turn.summary || '(无回复摘要)' }}</div>
          <div class="badges">
            <span class="badge">Δ {{ turn.response_tokens }} tok</span>
            <span class="badge cost">${{ turn.cost_usd.toFixed(4) }}</span>
            <span
              class="badge status"
              :data-ok="turn.status_code < 400 ? 'true' : 'false'"
            >{{ turn.status_code }}</span>
          </div>
        </div>
      </div>

      <div v-if="!loading && items.length === 0" class="empty">暂无轮次记录</div>
      <div v-if="hasMore" class="load-more">
        <button class="btn btn-secondary" :disabled="loading" @click="load(false)">
          {{ loading ? '加载中...' : '加载更早' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.turns-list-view {
  background: #f3f4f6;
  min-height: 100vh;
  padding: 16px 24px;
  max-width: 1400px;
  margin: 0 auto;
}
.header {
  margin-bottom: 16px;
}
.header h1 {
  font-size: 24px;
  font-weight: 600;
  color: var(--text);
  margin: 0 0 8px 0;
}
.subtitle {
  font-size: 14px;
  color: var(--text-secondary);
  margin: 0;
}
.filter-bar {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.filter-input {
  height: 36px;
  padding: 4px 12px;
  background: white;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
  min-width: 140px;
}
.filter-input:focus {
  outline: none;
  border-color: #3b82f6;
  box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.1);
}
.btn {
  height: 36px;
  padding: 0 16px;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.15s;
}
.btn-primary {
  background: #3b82f6;
  color: white;
}
.btn-primary:hover {
  background: #2563eb;
}
.btn-secondary {
  background: white;
  color: #374151;
  border: 1px solid #d1d5db;
}
.btn-secondary:hover {
  background: #f9fafb;
}
.btn-secondary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.error-banner {
  background: #fee;
  border: 1px solid #fcc;
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 16px;
  color: #c33;
  font-size: 14px;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.turn-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 12px;
  cursor: pointer;
  transition: background 0.15s;
  background: white;
}
.turn-row:hover {
  background: #f9fafb;
}
.col {
  padding: 0 8px;
}
.col + .col {
  border-left: 1px dashed #e5e7eb;
}
.meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.turn-no {
  font-weight: 600;
  color: #374151;
}
.ts {
  color: #9ca3af;
}
.verdict {
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
}
.tag-pass {
  background: #d1fae5;
  color: #065f46;
}
.tag-warn {
  background: #fef3c7;
  color: #92400e;
}
.tag-block {
  background: #fee2e2;
  color: #991b1b;
}
.tag-skip {
  background: #f3f4f6;
  color: #6b7280;
}
.preview {
  font-size: 13px;
  color: #374151;
  margin-bottom: 6px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.badges {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.badge {
  background: #f3f4f6;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: #6b7280;
}
.badge.model {
  background: #eff6ff;
  color: #1e40af;
}
.badge.session-id {
  background: #f0fdf4;
  color: #166534;
  font-family: monospace;
}
.badge.cost {
  background: #fef3c7;
  color: #92400e;
}
.badge.status[data-ok="true"] {
  background: #d1fae5;
  color: #065f46;
}
.badge.status[data-ok="false"] {
  background: #fee2e2;
  color: #991b1b;
}
.empty {
  text-align: center;
  color: #6b7280;
  padding: 48px 24px;
  font-size: 14px;
}
.load-more {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}
</style>
