import { ref, computed } from 'vue'
import {
  getMemoraSessions,
  type MemoraSession,
  type MemoraSessionsResponse,
} from '../api'

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
      const resp = await getMemoraSessions(filters)
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
  return s.task_id || '[无标题]'
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
