<script setup lang="ts">
/**
 * SessionDetailPage — admin session detail (summary + request_logs turn tree).
 *
 * Turn list uses SessionTurnsTimeline (GET …/turns → session_turns_tree.go).
 * Clicking a turn opens the fullscreen request detail in session-turns mode.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getSessionSnapshot } from '../../api/sessions_v2'
import { ApiError } from '../../api/_core'
import SessionSummaryBar from '../../components/SessionSummaryBar.vue'
import SessionTurnsTimeline from '../../components/session/SessionTurnsTimeline.vue'

const route = useRoute()
const router = useRouter()
const sessionId = computed(() => String(route.params.id || ''))

const snapshotError = ref('')
const snapshot = ref<Record<string, unknown> | null>(null)
let snapshotController: AbortController | null = null

async function loadSnapshot() {
  const id = sessionId.value
  if (!id) return
  snapshotController?.abort()
  snapshotController = new AbortController()
  snapshot.value = null
  snapshotError.value = ''
  try {
    snapshot.value = (await getSessionSnapshot(id, {
      signal: snapshotController.signal,
    })) as Record<string, unknown>
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') return
    snapshotError.value = e instanceof ApiError ? e.detail : e instanceof Error ? e.message : String(e)
  }
}

function openTurn(payload: { requestId: string; turnNumber: number }) {
  void router.push({
    name: 'request-detail',
    params: { requestId: payload.requestId },
    query: { mode: 'session-turns' },
  })
}

onMounted(loadSnapshot)

watch(sessionId, () => {
  loadSnapshot()
})

onBeforeUnmount(() => {
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
      <div v-if="snapshotError" class="error snapshot-error" role="alert">
        会话摘要加载失败：{{ snapshotError }}
      </div>
      <SessionTurnsTimeline
        v-if="sessionId"
        :key="sessionId"
        :session-id="sessionId"
        @open-request="openTurn"
      />
    </div>
  </div>
</template>

<style scoped>
.session-detail {
  background: var(--kx-bg, var(--surface-secondary));
  min-height: 100vh;
}
.list {
  padding: 16px 24px;
  max-width: 1400px;
  margin: 0 auto;
}
.error {
  color: var(--kx-danger, var(--danger));
  background: var(--kx-danger-soft, var(--danger-bg));
  border: 1px solid color-mix(in srgb, var(--kx-danger, var(--danger)) 40%, var(--kx-border, var(--danger-bg)));
  padding: 10px 12px;
  border-radius: 6px;
  margin-bottom: 10px;
}
</style>
