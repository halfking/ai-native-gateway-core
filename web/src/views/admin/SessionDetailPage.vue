<script setup lang="ts">
/**
 * SessionDetailPage — admin session detail (summary + request_logs turn tree).
 *
 * Turn list uses SessionTurnsTimeline (GET …/turns → session_turns_tree.go).
 * Clicking a turn opens the fullscreen request detail in session-turns mode.
 * The "view digest" button on each turn opens a side drawer showing the
 * generated turn digest (user input, assistant output, metrics, events, tools).
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { getSessionSnapshot, listSessionTurns, type TurnListItem } from '../../api/sessions_v2'
import { ApiError } from '../../api/_core'
import SessionSummaryBar from '../../components/SessionSummaryBar.vue'
import SessionTurnsTimeline from '../../components/session/SessionTurnsTimeline.vue'
import TurnDigestDrawer from '../../components/session/TurnDigestDrawer.vue'
import { useTurnTitleSummary } from '../../composables/useTurnTitleSummary'
import { openRequestDetailPage } from '../../utils/openRequestDetailPage'

const route = useRoute()
const router = useRouter()
const sessionId = computed(() => String(route.params.id || ''))
const { t } = useI18n()

const snapshotError = ref('')
const snapshot = ref<Record<string, unknown> | null>(null)
let snapshotController: AbortController | null = null

// Subtask 3 body_status banner: pull the V2 turns list once per session
// (metadata-only endpoint, never decodes body bytes) and aggregate the
// unavailable count. The banner is shown only when at least one turn
// reports body_status === 'unavailable'; turns without the field
// (older producers) are NOT counted as unavailable.
const unavailableCount = ref(0)
const unavailableLoaded = ref(false)
let turnsController: AbortController | null = null
async function loadUnavailableCount() {
  const id = sessionId.value
  if (!id) return
  turnsController?.abort()
  turnsController = new AbortController()
  unavailableLoaded.value = false
  try {
    const r = await listSessionTurns(id, { limit: 200 }, { signal: turnsController.signal })
    unavailableCount.value = countUnavailable(r.turns)
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') return
    // Best-effort: leave count at 0; banner stays hidden.
    unavailableCount.value = 0
  } finally {
    unavailableLoaded.value = true
  }
}
function countUnavailable(turns: TurnListItem[]): number {
  let n = 0
  for (const t of turns) {
    if (t.body_status === 'unavailable') n++
  }
  return n
}

// Digest drawer state — opened from the inline "view digest" button on each
// turn card. Reset on sessionId change / unmount so the drawer never points
// at a stale turn after navigating between sessions.
const digestOpen = ref(false)
const digestTurnNo = ref<number | null>(null)
// 2026-09-05 audit F2-#9: the turn detail response has no title/summary, so
// the drawer's fallbacks come from the turns list (cached per session).
const { ensureTurnTitleSummary, lookupTurnTitleSummary } = useTurnTitleSummary()
const digestTitle = ref('')
const digestSummary = ref('')

function showDigest(payload: { turnNumber: number }) {
  digestTurnNo.value = payload.turnNumber
  digestTitle.value = ''
  digestSummary.value = ''
  digestOpen.value = true
  void ensureTurnTitleSummary(sessionId.value).then(() => {
    // Ignore the resolved lookup if the user already switched turns.
    if (digestTurnNo.value !== payload.turnNumber) return
    const item = lookupTurnTitleSummary(sessionId.value, payload.turnNumber)
    digestTitle.value = item.title
    digestSummary.value = item.summary
  })
}

function closeDigest() {
  digestOpen.value = false
  digestTurnNo.value = null
  digestTitle.value = ''
  digestSummary.value = ''
}

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
  openRequestDetailPage(payload.requestId, { mode: 'session-turns' }, router)
}

onMounted(() => {
  loadSnapshot()
  void loadUnavailableCount()
})

watch(sessionId, () => {
  // Session navigation: drop any open drawer before loading the new snapshot.
  closeDigest()
  loadSnapshot()
  void loadUnavailableCount()
})

onBeforeUnmount(() => {
  closeDigest()
  snapshotController?.abort()
  turnsController?.abort()
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
      <!--
        Subtask 3 body_status banner (admin/body_status.go): surfaces how many
        turns in this session have lost their original request/response bodies
        (already pruned by retention, or never captured by the body-writer
        FF). Hidden when the count is zero so sessions with all-available
        bodies render exactly like before.
      -->
      <div
        v-if="unavailableLoaded && unavailableCount > 0"
        class="body-status-banner"
        role="status"
        data-testid="session-body-status-banner"
      >
        {{ t('requestDetail.compress.bodyStatusUnavailable', { count: unavailableCount }) }}
      </div>
      <SessionTurnsTimeline
        v-if="sessionId"
        :key="sessionId"
        :session-id="sessionId"
        @open-request="openTurn"
        @show-digest="showDigest"
      />
      <TurnDigestDrawer
        v-if="sessionId"
        :model-value="digestOpen"
        :session-id="sessionId"
        :turn-no="digestTurnNo"
        :title="digestTitle"
        :summary="digestSummary"
        @update:model-value="(value: boolean) => { if (!value) closeDigest() }"
        @close="closeDigest"
      />
    </div>
  </div>
</template>

<style scoped>
.session-detail {
  background: var(--kx-bg, var(--surface-secondary));
  min-height: 100vh;
  min-height: 100dvh;
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
/*
 * Subtask 3 body_status banner: warning tone — informational, not blocking.
 * Operators are told that some turns lack original bodies; the page itself
 * still works (compressed/redacted views fall back gracefully).
 */
.body-status-banner {
  color: var(--kx-warning, var(--warning, #8a6d3b));
  background: var(--kx-warning-soft, var(--warning-bg, #fff7e6));
  border: 1px solid color-mix(in srgb, var(--kx-warning, var(--warning)) 35%, var(--kx-border));
  padding: 10px 12px;
  border-radius: 6px;
  margin-bottom: 10px;
  font-size: 13px;
}
</style>
