<script setup lang="ts">
// SessionDetailPage.vue — V2-P4 (2026-07-24)
// New admin session detail page. Replaces legacy SessionTurnsPanel:
//   - sticky summary bar with instant summary trigger (V2-P5)
//   - cursor-paginated dual-column turn list
//   - right-side drawer with five tabs (request/response/meta/...
//     governance/attachments) when a row is clicked
//   - `?turn=N&focus=1` deep-link support for cross-page handoff

import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  listSessionTurns,
  getSessionSnapshot,
  type TurnListItem,
} from '../../api/sessions_v2'
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
const drawerTurnNo = ref<number | null>(focusTurn.value || null)
const snapshot = ref<Record<string, unknown> | null>(null)

async function load(reset = true) {
  loading.value = true
  try {
    if (reset) {
      turns.value = []
      nextCursor.value = ''
    }
    const params: { cursor?: string; limit: number } = { limit: 50 }
    if (nextCursor.value) params.cursor = nextCursor.value
    const r = await listSessionTurns(sessionId.value, params)
    turns.value = [...turns.value, ...r.turns]
    hasMore.value = r.has_more
    nextCursor.value = r.next_cursor
  } catch (e) {
    console.error('list turns failed', e)
  } finally {
    loading.value = false
  }
}

async function loadSnapshot() {
  try {
    snapshot.value = (await getSessionSnapshot(sessionId.value)) as Record<
      string,
      unknown
    >
  } catch (e) {
    snapshot.value = null
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
    drawerTurnNo.value = focusTurn.value || null
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
    />
    <div class="list">
      <SessionTurnListItem
        v-for="t in turns"
        :key="t.turn_no"
        :turn="t"
        :active="t.turn_no === drawerTurnNo"
        @open="openDrawer"
      />
      <div v-if="!loading && turns.length === 0" class="empty">暂无 turn 记录</div>
      <div v-if="hasMore" class="load-more">
        <el-button :loading="loading" @click="load(false)">加载更早</el-button>
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
.empty {
  text-align: center;
  color: #6b7280;
  padding: 32px;
}
.load-more {
  display: flex;
  justify-content: center;
  margin-top: 12px;
}
</style>
