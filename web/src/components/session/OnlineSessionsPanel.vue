<script setup lang="ts">
/**
 * OnlineSessionsPanel — 在线会话列表（V3.3-OBS OBS-FE5, 26 号 §5）
 *
 * 数据源：GET /api/admin/sessions/online（admin/session_online.go）。
 * 展示：会话键 / 标题 / 最近请求状态 / 最近模型 / 最近延迟 / 最近活动 /
 * freshness 过期标记。点击会话行 emit('select', sessionId) 进入轮次时间线。
 *
 * 契约说明（禁止零值冒充）：
 *   - 后端未提供「用户/项目」「轮次数」「健康度」字段，本列表不显示这些列；
 *     等后端补齐后再接入。
 *   - last_latency_ms 为 null 表示未知，渲染为「—」，绝不显示 0ms。
 *
 * 轮询说明：本面板为手动加载（mount 时一次 + 手动刷新按钮 + 游标加载更多），
 * 不做轮询，因此无需 probeStreamStore 式的 visibility 门控（该门控仅用于
 * SSE/轮询长连接，页面不可见时本面板本来就没有任何后台流量）。
 *
 * 设计约束：颜色只用 var(--kx-*)；三态（Skeleton / Empty / Error）。
 */
import { onMounted, ref } from 'vue'
import {
  fetchOnlineSessions,
  SessionObsApiError,
  type OnlineSessionItem,
} from '../../api/sessionTurnsTree'

const emit = defineEmits<{ (e: 'select', sessionId: string): void }>()

const sessions = ref<OnlineSessionItem[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const hasMore = ref(false)
const nextCursor = ref('')
const error = ref<SessionObsApiError | null>(null)
const loaded = ref(false) // 是否完成过一次成功加载（区分 Skeleton 与空态）

const PAGE_LIMIT = 20

async function load(reset = true) {
  if (reset) {
    loading.value = true
    error.value = null
  } else {
    loadingMore.value = true
  }
  try {
    const r = await fetchOnlineSessions({
      limit: PAGE_LIMIT,
      cursor: reset ? undefined : nextCursor.value || undefined,
    })
    sessions.value = reset ? r.sessions : [...sessions.value, ...r.sessions]
    hasMore.value = r.has_more
    nextCursor.value = r.next_cursor || ''
    loaded.value = true
  } catch (e) {
    error.value =
      e instanceof SessionObsApiError ? e : new SessionObsApiError('network', 0, String(e))
    if (reset) sessions.value = []
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

onMounted(() => load(true))

function statusClass(status: string | undefined): string {
  if (!status) return 'osp-status--unknown'
  const s = status.toLowerCase()
  if (s === 'success' || s === 'ok' || s === 'completed') return 'osp-status--ok'
  if (s === 'error' || s === 'failed' || s === 'timeout') return 'osp-status--err'
  return 'osp-status--unknown'
}

function fmtLatency(ms: number | null | undefined): string {
  // null/undefined = 未知，禁止渲染成 0ms
  if (ms === null || ms === undefined) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function fmtTime(iso: string | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString()
}

function errorText(e: SessionObsApiError | null): string {
  if (!e) return ''
  switch (e.kind) {
    case 'unauthorized':
      return '未认证或登录已过期，请重新登录'
    case 'forbidden':
      return '无权限访问在线会话列表（租户隔离）'
    case 'network':
      return '网络错误，请检查连接后重试'
    default:
      return `加载失败（HTTP ${e.status}）：${e.message}`
  }
}
</script>

<template>
  <div class="osp" data-testid="online-sessions-panel">
    <div class="osp-header">
      <span class="osp-title">在线会话</span>
      <span class="osp-count">{{ sessions.length }} 个会话</span>
      <button
        type="button"
        class="osp-refresh"
        :disabled="loading"
        @click="load(true)"
      >
        {{ loading ? '刷新中…' : '刷新' }}
      </button>
    </div>

    <!-- 首载 Skeleton -->
    <div v-if="loading && !loaded" class="osp-skeleton" data-testid="osp-skeleton">
      <div v-for="i in 5" :key="i" class="osp-skeleton-row" />
    </div>

    <!-- 错误态（区分 401/403/网络/其他） -->
    <div
      v-else-if="error"
      class="osp-error"
      role="alert"
      :data-error-kind="error.kind"
    >
      <span class="osp-error-text">{{ errorText(error) }}</span>
      <button type="button" class="osp-retry" :disabled="loading" @click="load(true)">
        重试
      </button>
    </div>

    <!-- 空态 -->
    <div v-else-if="loaded && sessions.length === 0" class="osp-empty">
      当前没有在线会话
    </div>

    <!-- 会话列表 -->
    <div v-else class="osp-list">
      <div class="osp-row osp-row--head">
        <span class="osp-cell osp-cell--sid">会话键</span>
        <span class="osp-cell osp-cell--title">标题</span>
        <span class="osp-cell osp-cell--status">最近状态</span>
        <span class="osp-cell osp-cell--model">最近模型</span>
        <span class="osp-cell osp-cell--latency">最近延迟</span>
        <span class="osp-cell osp-cell--time">最近活动</span>
      </div>
      <button
        v-for="s in sessions"
        :key="s.session_id"
        type="button"
        class="osp-row"
        :data-session-id="s.session_id"
        @click="emit('select', s.session_id)"
      >
        <span class="osp-cell osp-cell--sid" :title="s.session_id">{{ s.session_id }}</span>
        <span class="osp-cell osp-cell--title" :title="s.title">
          <template v-if="s.title">{{ s.title }}</template>
          <template v-else>—</template>
        </span>
        <span class="osp-cell osp-cell--status">
          <span v-if="s.last_request_status" class="osp-status" :class="statusClass(s.last_request_status)">
            {{ s.last_request_status }}
          </span>
          <span v-else class="osp-status osp-status--unknown">未知</span>
          <span
            v-if="s.freshness?.stale"
            class="osp-stale-badge"
            title="数据已过期"
          >过期</span>
        </span>
        <span class="osp-cell osp-cell--model">
          <template v-if="s.last_model">{{ s.last_model }}</template>
          <template v-else>—</template>
        </span>
        <span class="osp-cell osp-cell--latency">{{ fmtLatency(s.last_latency_ms) }}</span>
        <span class="osp-cell osp-cell--time">{{ fmtTime(s.last_active_at) }}</span>
      </button>

      <!-- 游标分页：加载更多 -->
      <div v-if="hasMore" class="osp-more">
        <button
          type="button"
          class="osp-more-btn"
          :disabled="loadingMore"
          data-testid="osp-load-more"
          @click="load(false)"
        >
          {{ loadingMore ? '加载中…' : '加载更多' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.osp {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 12px 16px;
  margin-bottom: 20px;
}
.osp-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.osp-title {
  font-weight: 600;
  color: var(--kx-text);
}
.osp-count {
  font-size: 12px;
  color: var(--kx-muted);
}
.osp-refresh {
  margin-left: auto;
  font-size: 12px;
  padding: 3px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  cursor: pointer;
}
.osp-refresh:hover:not(:disabled) {
  border-color: var(--kx-primary);
  color: var(--kx-primary);
}
.osp-refresh:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* Skeleton（仅 transform/opacity 动画） */
.osp-skeleton {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.osp-skeleton-row {
  height: 28px;
  border-radius: 6px;
  background: var(--kx-bg-accent);
  animation: osp-pulse 1.2s ease-in-out infinite;
}
@keyframes osp-pulse {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
}

/* 错误态 */
.osp-error {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--kx-danger);
  border-radius: 6px;
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
}
.osp-error-text {
  flex: 1;
  font-size: 13px;
}
.osp-retry {
  font-size: 12px;
  padding: 3px 10px;
  border: 1px solid var(--kx-danger);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-danger);
  cursor: pointer;
}

/* 空态 */
.osp-empty {
  padding: 24px;
  text-align: center;
  color: var(--kx-muted);
  font-size: 13px;
}

/* 列表 */
.osp-list {
  display: flex;
  flex-direction: column;
}
.osp-row {
  display: grid;
  grid-template-columns: minmax(160px, 2fr) minmax(120px, 2fr) 110px minmax(120px, 1.5fr) 90px 160px;
  gap: 8px;
  align-items: center;
  width: 100%;
  padding: 8px 6px;
  border: 0;
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
  text-align: left;
  font: inherit;
  color: var(--kx-text);
}
button.osp-row {
  cursor: pointer;
}
button.osp-row:hover {
  background: var(--kx-bg-accent);
}
.osp-row--head {
  font-size: 12px;
  color: var(--kx-muted);
  cursor: default;
}
.osp-row--head:hover {
  background: var(--kx-surface);
}
.osp-cell {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}
.osp-cell--sid {
  font-family: monospace;
  font-size: 12px;
}
.osp-status {
  display: inline-block;
  font-size: 12px;
  padding: 1px 8px;
  border-radius: 10px;
}
.osp-status--ok {
  background: var(--kx-success-soft);
  color: var(--kx-success);
}
.osp-status--err {
  background: var(--kx-danger-soft);
  color: var(--kx-danger);
}
.osp-status--unknown {
  background: var(--kx-bg-accent);
  color: var(--kx-muted);
}
.osp-stale-badge {
  margin-left: 6px;
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 10px;
  background: var(--kx-warning-soft);
  color: var(--kx-warning);
}
.osp-more {
  display: flex;
  justify-content: center;
  padding: 10px 0 2px;
}
.osp-more-btn {
  font-size: 13px;
  padding: 4px 16px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-primary);
  cursor: pointer;
}
.osp-more-btn:hover:not(:disabled) {
  border-color: var(--kx-primary);
}
.osp-more-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
