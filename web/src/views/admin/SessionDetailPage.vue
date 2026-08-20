<script setup lang="ts">
// SessionDetailPage.vue — V2-P4 (2026-07-24)
// New admin session detail page. Replaces legacy SessionTurnsPanel:
//   - sticky summary bar with instant summary trigger (V2-P5)
//   - cursor-paginated dual-column turn list
//   - right-side drawer with five tabs (request/response/meta/...
//     governance/attachments) when a row is clicked
//   - `?turn=N&focus=1` deep-link support for cross-page handoff

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  listSessionTurns,
  getSessionSnapshot,
  type TurnListItem,
} from '../../api/sessions_v2'
import { ApiError } from '../../api/_core'
import SessionSummaryBar from '../../components/SessionSummaryBar.vue'
import SessionTurnListItem from '../../components/SessionTurnListItem.vue'
import SessionTurnDrawer from '../../components/SessionTurnDrawer.vue'

const route = useRoute()
const router = useRouter()
const sessionId = computed(() => String(route.params.id || ''))
const focusTurn = computed(() => Number(route.query.turn || 0))

const turns = ref<TurnListItem[]>([])
const hasMore = ref(false)
const nextCursor = ref('')
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const snapshotError = ref('')
const drawerTurnNo = ref<number | null>(focusTurn.value || null)
const snapshot = ref<Record<string, unknown> | null>(null)
const requestVersion = ref(0)
let listController: AbortController | null = null
let snapshotController: AbortController | null = null

async function load(reset = true) {
  if (!sessionId.value || loadingMore.value || (!reset && (loading.value || !hasMore.value))) return
  const id = sessionId.value
  const version = ++requestVersion.value
  if (reset) {
    listController?.abort()
    listController = new AbortController()
    loading.value = true
    error.value = ''
    turns.value = []
    nextCursor.value = ''
    hasMore.value = false
  } else {
    loadingMore.value = true
    error.value = ''
  }
  try {
    const params: { cursor?: string; limit: number } = { limit: 50 }
    if (nextCursor.value) params.cursor = nextCursor.value
    const r = await listSessionTurns(id, params, { signal: listController?.signal })
    if (version !== requestVersion.value || id !== sessionId.value) return
    turns.value = reset ? r.turns : [...turns.value, ...r.turns]
    hasMore.value = r.has_more
    nextCursor.value = r.next_cursor
  } catch (e) {
    if (version !== requestVersion.value || id !== sessionId.value || (e instanceof DOMException && e.name === 'AbortError')) return
    error.value = e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e)
    if (reset) turns.value = []
  } finally {
    if (version === requestVersion.value && id === sessionId.value) {
      loading.value = false
      loadingMore.value = false
    }
  }
}

async function loadSnapshot() {
  const id = sessionId.value
  const version = requestVersion.value
  snapshotController?.abort()
  snapshotController = new AbortController()
  snapshot.value = null
  snapshotError.value = ''
  try {
    const value = (await getSessionSnapshot(id, { signal: snapshotController.signal })) as Record<string, unknown>
    if (version !== requestVersion.value || id !== sessionId.value) return
    snapshot.value = value
  } catch (e) {
    if (version !== requestVersion.value || id !== sessionId.value || (e instanceof DOMException && e.name === 'AbortError')) return
    snapshotError.value = e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e)
  }
}

function openDrawer(t: TurnListItem) {
  drawerTurnNo.value = t.turn_no
  router.replace({
    query: { ...route.query, turn: String(t.turn_no), focus: '1' },
  })
}

function closeDrawer() {
  drawerTurnNo.value = null
  const { turn: _t, focus: _f, ...rest } = route.query
  router.replace({ query: rest })
}

onMounted(() => {
  load(true)
  loadSnapshot()
})

watch(
  () => String(route.params.id || ''),
  (id, previousId) => {
    if (id === previousId) return
    requestVersion.value++
    listController?.abort()
    snapshotController?.abort()
    drawerTurnNo.value = focusTurn.value || null
    snapshot.value = null
    load(true)
    loadSnapshot()
  }
)

watch(
  () => route.query.turn,
  turn => {
    drawerTurnNo.value = Number(turn || 0) || null
  }
)

onBeforeUnmount(() => {
  requestVersion.value++
  listController?.abort()
  snapshotController?.abort()
})
</script>

<template>
  <div class="session-detail">
    <SessionSummaryBar
      :session-id="sessionId"
      :title="snapshot?.title as string | undefined"
      :summary="snapshot?.summary as string | undefined"
      :total-turns="snapshot?.total_turns as number | undefined"
      :total-cost="snapshot?.total_cost_usd as number | undefined"
      :summary-generated-at="snapshot?.summary_generated_at as string | undefined"
      @summary-updated="snapshot = $event"
    />
    <div class="list">
      <div v-if="loading && turns.length === 0" class="loading">加载轮次中…</div>
      <div v-else-if="error && turns.length === 0" class="error" role="alert">
        {{ error }}
        <el-button size="small" @click="load(true)">重试</el-button>
      </div>
      <div v-if="snapshotError" class="error snapshot-error" role="alert">
        会话摘要加载失败：{{ snapshotError }}
      </div>
      <SessionTurnListItem
        v-for="t in turns"
        :key="t.turn_no"
        :turn="t"
        :active="t.turn_no === drawerTurnNo"
        @open="openDrawer"
      />
      <div v-if="!loading && !error && turns.length === 0" class="empty">暂无 turn 记录</div>
      <div v-if="error && turns.length > 0" class="error" role="alert">
        {{ error }}
        <el-button size="small" @click="load(false)">重试加载更早轮次</el-button>
      </div>
      <div v-if="hasMore" class="load-more">
        <el-button :loading="loadingMore" :disabled="loading || loadingMore" @click="load(false)">加载更早</el-button>
      </div>
    </div>
    <SessionTurnDrawer
      :session-id="sessionId"
      :turn-no="drawerTurnNo"
      @close="closeDrawer"
    />
  </div>
</template>

<style scoped>
.session-detail {
  background: #f3f4f6;
  min-height: 100vh;
}
.list {
  padding: 16px 24px;
  max-width: 1400px;
  margin: 0 auto;
}
.loading { color: #6b7280; padding: 24px; text-align: center; }
.error { color: #b42318; background: #fff1f0; border: 1px solid #f3b4b0; padding: 10px 12px; border-radius: 6px; margin-bottom: 10px; }
.load-more {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}
</style>
