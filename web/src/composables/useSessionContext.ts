import { ref, computed } from 'vue'
import type { RouteLocationNormalizedLoaded } from 'vue-router'
import {
  getMemoraSessions,
  type MemoraSession,
  type MemoraSessionsResponse,
  type SessionScopeParams,
} from '../api'

export type SessionScope = SessionScopeParams & {
  no_topic_window?: number
  /** 列表行 request_count，用于详情页核对 */
  rc?: number
  section?: 'topic' | 'no-topic'
}

export function buildSessionQueryParams(
  scope: SessionScope,
  extra?: Record<string, string | undefined>,
): Record<string, string> {
  const q: Record<string, string> = {}
  if (scope.hours != null) q.hours = String(scope.hours)
  if (scope.no_topic_window != null) q.no_topic_window = String(scope.no_topic_window)
  if (scope.session_id) q.session_id = scope.session_id
  if (scope.rc != null) q.rc = String(scope.rc)
  if (scope.section) q.section = scope.section
  if (extra) {
    for (const [k, v] of Object.entries(extra)) {
      if (v != null && v !== '') q[k] = v
    }
  }
  return q
}

export function parseSessionScopeFromRoute(
  route: RouteLocationNormalizedLoaded,
  fallbackHours = 24,
): SessionScope {
  const hours = parseInt(String(route.query.hours ?? ''), 10)
  const noTopicWindow = parseInt(String(route.query.no_topic_window ?? ''), 10)
  const rc = parseInt(String(route.query.rc ?? ''), 10)
  const sessionId = String(route.query.session_id ?? '').trim()
  return {
    hours: Number.isFinite(hours) && hours > 0 ? hours : fallbackHours,
    no_topic_window: Number.isFinite(noTopicWindow) && noTopicWindow > 0 ? noTopicWindow : undefined,
    session_id: sessionId && sessionId !== '[空]' ? sessionId : undefined,
    rc: Number.isFinite(rc) && rc >= 0 ? rc : undefined,
    section: route.query.section === 'no-topic' ? 'no-topic' : 'topic',
  }
}

export function sessionScopeToParams(scope: SessionScope): SessionScopeParams {
  return {
    hours: scope.hours,
    session_id: scope.session_id,
  }
}

export function listBackQueryFromRoute(route: RouteLocationNormalizedLoaded): Record<string, string> {
  const q: Record<string, string> = {}
  if (route.query.hours) q.hours = String(route.query.hours)
  if (route.query.no_topic_window) q.no_topic_window = String(route.query.no_topic_window)
  if (route.query.section) q.section = String(route.query.section)
  return q
}

export function useSessionFilters() {
  const searchQ = ref('')
  const searchOwner = ref('')
  const hours = ref(24)
  const noTopicWindow = ref(1)
  const showTopic = ref(true)
  const showNoTopic = ref(true)
  return { searchQ, searchOwner, hours, noTopicWindow, showTopic, showNoTopic }
}

export function useSessionList() {
  const sessions = ref<MemoraSession[]>([])
  const meta = ref<Pick<MemoraSessionsResponse, 'hours' | 'no_topic_window' | 'topic_count' | 'no_topic_count'> | null>(null)
  const loading = ref(false)
  const error = ref('')

  const topicSessions = computed(() => sessions.value.filter(s => !s.no_topic))
  const noTopicSessions = computed(() => sessions.value.filter(s => s.no_topic))

  async function loadSessions(filters: {
    q?: string
    owner_user?: string
    hours: number
    no_topic_window: number
  }) {
    loading.value = true
    error.value = ''
    try {
      const resp = await getMemoraSessions({
        ...filters,
        include_memora: true,
        limit: 20,
      })
      sessions.value = resp.sessions
      meta.value = {
        hours: resp.hours,
        no_topic_window: resp.no_topic_window,
        topic_count: resp.topic_count,
        no_topic_count: resp.no_topic_count,
      }
    } catch (e: unknown) {
      sessions.value = []
      meta.value = null
      const msg = e instanceof Error ? e.message : '加载失败'
      error.value = msg === 'Unauthorized' ? '登录已过期，请重新登录' : msg
      throw e
    } finally {
      loading.value = false
    }
  }

  return {
    sessions,
    meta,
    loading,
    error,
    topicSessions,
    noTopicSessions,
    loadSessions,
  }
}

export function displayTitle(s: MemoraSession): string {
  if (s.no_topic) return s.no_topic_label || '[无主题会话]'
  if (s.title && s.title.trim()) return s.title.trim()
  return s.task_id || '[无标题]'
}

export function displayMemoraPreview(s: MemoraSession): string {
  if (s.no_topic) return '—'
  const preview = s.memora_preview?.trim()
  if (preview) return preview
  return 'Memora中没有'
}

export function hasMemoraPreview(s: MemoraSession): boolean {
  return Boolean(s.memora_preview?.trim())
}

export function displayUser(s: MemoraSession): string {
  const v = s.api_key_owner_user
  return !v || v === '[空]' ? '[空]' : v
}

export function displayKey(s: MemoraSession): string {
  const v = s.api_key_prefix
  return !v || v === '[空]' ? '[空]' : v
}

export function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

export function fmtTime(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

export function fmtDateFull(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN')
}

export function fmtScore(v: number) {
  return (v * 100).toFixed(0) + '%'
}

export function fmtCost(v: number) {
  return '$' + v.toFixed(4)
}

export function tagClass(tags: string[] | null): string {
  if (!tags || tags.length === 0) return 'badge-blue'
  const t = tags[0].toLowerCase()
  if (t.includes('pref') || t.includes('preference')) return 'badge-purple'
  if (t.includes('fact')) return 'badge-green'
  if (t.includes('context')) return 'badge-blue'
  return 'badge-blue'
}

export function sessionRowKey(s: MemoraSession): string {
  if (s.no_topic) return `nt:${s.no_topic_label || ''}:${s.first_activity}`
  return s.task_id || `unknown:${s.first_activity}`
}
