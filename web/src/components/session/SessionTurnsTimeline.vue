<script setup lang="ts">
/**
 * SessionTurnsTimeline — 会话轮次时间线（V3.3-OBS OBS-FE5, 26 号 §5）
 *
 * 数据源：GET /api/admin/sessions/{id}/turns?limit=20&cursor=（OBS-BE6,
 * admin/session_turns_tree.go）。每轮显示主请求卡（轮次号/请求 ID/状态/模型/
 * 延迟）+ 内联子请求树（title/summary/sensitive_word/compression/other 徽标
 * + 状态 + 彩色延迟）。
 *
 * 契约说明（字段名以 session_turns_tree.go json tag 为准）：
 *   - latency 为毫秒、null 表示未知 → 渲染「未知」，禁止 0ms 冒充。
 *   - model 为 omitempty → 无值不渲染该列内容。
 *   - 404 = 会话不存在（含无任何请求记录）；403 = 跨租户；401 = 未认证。
 *
 * 分页：cursor 游标（has_more / next_cursor），「加载更多」按钮追加。
 * 轮询：手动加载（mount / sessionId 变化 / 手动刷新），无轮询，无需
 * visibility 门控。
 *
 * 设计约束：颜色只用 var(--kx-*)；三态（Skeleton / Empty / Error 区分码）。
 */
import { onMounted, ref, watch } from 'vue'
import {
  fetchSessionTurnsTree,
  SessionObsApiError,
  type SessionTurnTreeItem,
} from '../../api/sessionTurnsTree'

const props = defineProps<{ sessionId: string }>()
const emit = defineEmits<{
  openRequest: [payload: { requestId: string; turnNumber: number }]
}>()

const turns = ref<SessionTurnTreeItem[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const hasMore = ref(false)
const nextCursor = ref('')
const error = ref<SessionObsApiError | null>(null)
const loaded = ref(false)

const PAGE_LIMIT = 20

async function load(reset = true) {
  if (reset) {
    loading.value = true
    error.value = null
  } else {
    loadingMore.value = true
  }
  try {
    const r = await fetchSessionTurnsTree(props.sessionId, {
      limit: PAGE_LIMIT,
      cursor: reset ? undefined : nextCursor.value || undefined,
    })
    turns.value = reset ? r.turns : [...turns.value, ...r.turns]
    hasMore.value = r.has_more
    nextCursor.value = r.next_cursor || ''
    loaded.value = true
  } catch (e) {
    error.value =
      e instanceof SessionObsApiError ? e : new SessionObsApiError('network', 0, String(e))
    if (reset) turns.value = []
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

onMounted(() => load(true))
watch(
  () => props.sessionId,
  () => {
    turns.value = []
    hasMore.value = false
    nextCursor.value = ''
    loaded.value = false
    load(true)
  }
)

function statusClass(status: string): string {
  const s = status.toLowerCase()
  if (s === 'success' || s === 'ok' || s === 'completed') return 'stt-status--ok'
  if (s === 'error' || s === 'failed' || s === 'timeout') return 'stt-status--err'
  return 'stt-status--unknown'
}

/** 延迟分级着色：<3s 正常 / <10s 偏慢 / ≥10s 异常。 */
function latencyClass(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return 'stt-latency--unknown'
  if (ms < 3000) return 'stt-latency--ok'
  if (ms < 10000) return 'stt-latency--slow'
  return 'stt-latency--bad'
}

function fmtLatency(ms: number | null | undefined): string {
  // null = 未知，禁止渲染成 0ms
  if (ms === null || ms === undefined) return '未知'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

/** 子请求类型徽标缩写（26 号 §5：T=title / S=summary / SW=sensitive_word）。 */
const TYPE_ABBR: Record<string, string> = {
  title: 'T',
  summary: 'S',
  sensitive_word: 'SW',
  compression: 'C',
  other: '其他',
}

function typeBadge(t: string): string {
  return TYPE_ABBR[t] ?? '其他'
}

function typeTitle(t: string): string {
  return TYPE_ABBR[t] && t !== 'other' ? t : t
}

function errorText(e: SessionObsApiError | null): string {
  if (!e) return ''
  switch (e.kind) {
    case 'not_found':
      return '会话不存在或暂无任何请求记录（404）'
    case 'forbidden':
      return '无权限访问该会话（可能属于其他租户，403）'
    case 'unauthorized':
      return '未认证或登录已过期，请重新登录（401）'
    case 'network':
      return '网络错误，请检查连接后重试'
    default:
      return `加载失败（HTTP ${e.status}）：${e.message}`
  }
}
</script>

<template>
  <div class="stt" data-testid="session-turns-timeline">
    <div class="stt-header">
      <span class="stt-title">轮次时间线</span>
      <span class="stt-sid" :title="sessionId">{{ sessionId }}</span>
      <button
        type="button"
        class="stt-refresh"
        :disabled="loading"
        @click="load(true)"
      >
        {{ loading ? '刷新中…' : '刷新' }}
      </button>
    </div>

    <!-- 首载 Skeleton -->
    <div v-if="loading && !loaded" class="stt-skeleton" data-testid="stt-skeleton">
      <div v-for="i in 4" :key="i" class="stt-skeleton-card" />
    </div>

    <!-- 错误态（区分 404/403/401/网络/其他） -->
    <div
      v-else-if="error"
      class="stt-error"
      role="alert"
      :data-error-kind="error.kind"
    >
      <span class="stt-error-text">{{ errorText(error) }}</span>
      <button type="button" class="stt-retry" :disabled="loading" @click="load(true)">
        重试
      </button>
    </div>

    <!-- 空态：200 但无轮次 -->
    <div v-else-if="loaded && turns.length === 0" class="stt-empty">
      该会话暂无轮次记录
    </div>

    <!-- 轮次时间线 -->
    <div v-else class="stt-timeline">
      <div
        v-for="t in turns"
        :key="t.request_id"
        class="stt-turn"
        :data-turn-number="t.turn_number"
      >
        <!-- 主请求卡 -->
        <button
          type="button"
          class="stt-turn-main stt-turn-main--clickable"
          @click="emit('openRequest', { requestId: t.request_id, turnNumber: t.turn_number })"
        >
          <span class="stt-turn-no">#{{ t.turn_number }}</span>
          <span class="stt-status" :class="statusClass(t.status)">{{ t.status }}</span>
          <span v-if="t.model" class="stt-turn-model">{{ t.model }}</span>
          <span class="stt-turn-latency" :class="latencyClass(t.latency)">
            {{ fmtLatency(t.latency) }}
          </span>
          <span class="stt-turn-rid" :title="t.request_id">{{ t.request_id }}</span>
        </button>

        <!-- 内联子请求树 -->
        <div
          v-if="t.child_requests && t.child_requests.length > 0"
          class="stt-children"
          :data-child-count="t.child_requests.length"
        >
          <div
            v-for="c in t.child_requests"
            :key="c.request_id"
            class="stt-child"
          >
            <span class="stt-child-tree" aria-hidden="true">└</span>
            <span
              class="stt-child-type"
              :class="`stt-child-type--${c.request_type}`"
              :title="typeTitle(c.request_type)"
            >{{ typeBadge(c.request_type) }}</span>
            <span class="stt-status stt-status--sm" :class="statusClass(c.status)">
              {{ c.status }}
            </span>
            <span class="stt-turn-latency" :class="latencyClass(c.latency)">
              {{ fmtLatency(c.latency) }}
            </span>
            <span class="stt-child-rid" :title="c.request_id">{{ c.request_id }}</span>
          </div>
        </div>
      </div>

      <!-- 游标分页：加载更多 -->
      <div v-if="hasMore" class="stt-more">
        <button
          type="button"
          class="stt-more-btn"
          :disabled="loadingMore"
          data-testid="stt-load-more"
          @click="load(false)"
        >
          {{ loadingMore ? '加载中…' : '加载更多轮次' }}
        </button>
      </div>
      <div v-else-if="loaded && turns.length > 0" class="stt-end">
        共 {{ turns.length }} 轮，已全部加载
      </div>
    </div>
  </div>
</template>

<style scoped>
.stt {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 12px 16px;
}
.stt-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.stt-title {
  font-weight: 600;
  color: var(--kx-text);
}
.stt-sid {
  font-family: monospace;
  font-size: 12px;
  color: var(--kx-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.stt-refresh {
  margin-left: auto;
  flex-shrink: 0;
  font-size: 12px;
  padding: 3px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  cursor: pointer;
}
.stt-refresh:hover:not(:disabled) {
  border-color: var(--kx-primary);
  color: var(--kx-primary);
}
.stt-refresh:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Skeleton（仅 opacity 动画） */
.stt-skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.stt-skeleton-card {
  height: 44px;
  border-radius: 6px;
  background: var(--kx-bg-accent);
  animation: stt-pulse 1.2s ease-in-out infinite;
}
@keyframes stt-pulse {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
}

/* 错误态 */
.stt-error {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--kx-danger);
  border-radius: 6px;
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
}
.stt-error-text {
  flex: 1;
  font-size: 13px;
}
.stt-retry {
  font-size: 12px;
  padding: 3px 10px;
  border: 1px solid var(--kx-danger);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-danger);
  cursor: pointer;
}

/* 空态 */
.stt-empty {
  padding: 24px;
  text-align: center;
  color: var(--kx-muted);
  font-size: 13px;
}

/* 时间线 */
.stt-timeline {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.stt-turn {
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  padding: 8px 10px;
}
.stt-turn-main {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.stt-turn-main--clickable {
  width: 100%;
  text-align: left;
  border: none;
  background: transparent;
  color: inherit;
  cursor: pointer;
  padding: 0;
  font: inherit;
}
.stt-turn-main--clickable:hover .stt-turn-rid {
  color: var(--kx-primary, var(--accent));
  text-decoration: underline;
}
.stt-turn-no {
  font-weight: 600;
  color: var(--kx-primary);
  font-size: 13px;
  min-width: 32px;
}
.stt-turn-model {
  font-size: 12px;
  color: var(--kx-text);
  background: var(--kx-bg-accent);
  padding: 1px 8px;
  border-radius: 10px;
}
.stt-turn-rid,
.stt-child-rid {
  font-family: monospace;
  font-size: 11px;
  color: var(--kx-muted);
  margin-left: auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.stt-child-rid {
  margin-left: 8px;
}

/* 状态徽标 */
.stt-status {
  display: inline-block;
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 10px;
}
.stt-status--sm {
  font-size: 11px;
  padding: 0 6px;
}
.stt-status--ok {
  background: var(--kx-success-soft);
  color: var(--kx-success);
}
.stt-status--err {
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
}
.stt-status--unknown {
  background: var(--kx-bg-accent);
  color: var(--kx-muted);
}

/* 彩色延迟 */
.stt-turn-latency {
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}
.stt-latency--ok {
  color: var(--kx-success);
}
.stt-latency--slow {
  color: var(--kx-warning);
}
.stt-latency--bad {
  color: var(--kx-danger);
}
.stt-latency--unknown {
  color: var(--kx-muted);
}

/* 子请求树 */
.stt-children {
  margin-top: 6px;
  padding-left: 18px;
  border-left: 2px solid var(--kx-border);
  margin-left: 14px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.stt-child {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.stt-child-tree {
  color: var(--kx-muted);
  font-size: 12px;
}
.stt-child-type {
  display: inline-block;
  font-size: 11px;
  font-weight: 600;
  padding: 0 6px;
  border-radius: 4px;
  min-width: 20px;
  text-align: center;
  background: var(--kx-primary-soft);
  color: var(--kx-primary);
}
.stt-child-type--sensitive_word {
  background: var(--kx-warning-soft);
  color: var(--kx-warning);
}
.stt-child-type--compression {
  background: var(--kx-bg-accent);
  color: var(--kx-muted);
}
.stt-child-type--other {
  background: var(--kx-bg-accent);
  color: var(--kx-muted);
}

/* 分页 */
.stt-more {
  display: flex;
  justify-content: center;
  padding: 6px 0 0;
}
.stt-more-btn {
  font-size: 13px;
  padding: 4px 16px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-primary);
  cursor: pointer;
}
.stt-more-btn:hover:not(:disabled) {
  border-color: var(--kx-primary);
}
.stt-more-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.stt-end {
  text-align: center;
  font-size: 12px;
  color: var(--kx-muted);
  padding-top: 4px;
}
</style>
