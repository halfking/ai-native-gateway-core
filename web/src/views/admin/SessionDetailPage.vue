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
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getSessionDetailV2, getSessionSnapshot } from '../../api/sessions_v2'
import { ApiError } from '../../api/_core'
import SessionSummaryBar from '../../components/SessionSummaryBar.vue'
import SessionTurnsTimeline from '../../components/session/SessionTurnsTimeline.vue'
import TurnDigestDrawer from '../../components/session/TurnDigestDrawer.vue'
import { useTurnTitleSummary } from '../../composables/useTurnTitleSummary'
import { openRequestDetailPage } from '../../utils/openRequestDetailPage'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const sessionId = computed(() => String(route.params.id || ''))

const snapshotError = ref('')
const snapshot = ref<Record<string, unknown> | null>(null)
let snapshotController: AbortController | null = null

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

// ── body_status 横幅 (Subtask 3, handoff §5) ─────────────────────────────
// 数据源：/api/admin/sessions/detail 的 turns[].body_status。
// 不要改用 /snapshot（不带 turns）。
//
// 后端只发两态 available | unavailable，**不发 dropped**：当前 schema 里
// 没有 session_bodies 的保留期开关，也没有任何清理任务会删 session_bodies
// 的行（唯一的 body 清理器只处理 V1 的 request_logs_bodies_hot）。此时若
// 报 dropped，等于告诉运维「保留期把数据清了」，而实际多半是这一轮根本没
// 采集到正文。完整论证见 admin/body_status.go 顶部 CONTRACT 注释。
// 后端将来真的引入保留期后再加第三态，本组件届时同步。
//
// 横幅只在有 turn 真的 unavailable 时出现。详情端默认分页 limit=50，若
// unavailable 的轮次恰好在第 51 轮之后，本页不会提示——这是分页的固有
// 边界，不是本组件的判断。
const bodyStatusBanner = ref<{ kind: 'unavailable'; turns: number[] } | null>(null)
const bodyStatusDismissed = ref(false)

function collectBodyStatus(detail: { turns?: { turn_no?: number; body_status?: string }[] }) {
  const unavailable: number[] = []
  for (const turn of detail.turns ?? []) {
    // 只认后端会发的两态。'dropped' 分支刻意不实现：后端一旦真的引入保留期
    // 并发出该值，届时再补，不要提前编造文案。
    if (turn.body_status === 'unavailable' && typeof turn.turn_no === 'number') {
      unavailable.push(turn.turn_no)
    }
  }
  bodyStatusBanner.value = unavailable.length ? { kind: 'unavailable', turns: unavailable } : null
}

const bodyStatusList = computed(() =>
  bodyStatusBanner.value ? bodyStatusBanner.value.turns.join(', ') : ''
)

async function loadBodyStatus() {
  const id = sessionId.value
  if (!id) return
  bodyStatusBanner.value = null
  bodyStatusDismissed.value = false
  try {
    const detail = await getSessionDetailV2(id)
    // 用户在请求返回前已经切走会话：丢弃这次结果。
    if (id !== sessionId.value) return
    collectBodyStatus(detail)
  } catch {
    // 横幅是增强信息，取不到就不显示；不能让它把整个详情页带崩。
    bodyStatusBanner.value = null
  }
}

onMounted(() => {
  loadSnapshot()
  void loadBodyStatus()
})

watch(sessionId, () => {
  // Session navigation: drop any open drawer before loading the new snapshot.
  closeDigest()
  void loadBodyStatus()
  loadSnapshot()
})

onBeforeUnmount(() => {
  closeDigest()
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
      <!-- Subtask 3 (handoff §5): body_status 横幅。role=status 而非 alert
           —— 这是常驻说明，不是需要打断操作的错误。 -->
      <div
        v-if="bodyStatusBanner && !bodyStatusDismissed"
        class="body-status-banner body-status-unavailable"
        role="status"
      >
        <div class="body-status-head">
          <strong>{{ t('requestDetail.bodyStatus.unavailableTitle') }}</strong>
          <button type="button" class="body-status-dismiss" @click="bodyStatusDismissed = true">
            {{ t('requestDetail.bodyStatus.dismiss') }}
          </button>
        </div>
        <p class="body-status-body">
          {{ t('requestDetail.bodyStatus.unavailableBody') }}
        </p>
        <p class="body-status-meta">
          {{ t('requestDetail.bodyStatus.affectedTurns', { list: bodyStatusList }) }}
        </p>
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
}
.error {
  color: var(--kx-danger, var(--danger));
  background: var(--kx-danger-soft, var(--danger-bg));
  border: 1px solid color-mix(in srgb, var(--kx-danger, var(--danger)) 40%, var(--kx-border, var(--danger-bg)));
  padding: 10px 12px;
  border-radius: 6px;
  margin-bottom: 10px;
}
/* Subtask 3: body_status 横幅。中性提示色，不是错误态——正文不可用是
   常态（未采集 / 尚未写入），不该用红色吓人。 */
.body-status-banner {
  padding: 10px 12px;
  border-radius: 6px;
  margin-bottom: 12px;
  border: 1px solid var(--kx-border, var(--border-color));
  background: var(--kx-surface, var(--surface-primary));
}
.body-status-unavailable {
  border-color: var(--kx-border, var(--border-color));
  background: var(--kx-surface-secondary, var(--surface-secondary));
}
.body-status-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
}
.body-status-dismiss {
  border: 1px solid var(--kx-border, var(--border-color));
  background: transparent;
  color: inherit;
  border-radius: 4px;
  padding: 2px 8px;
  font-size: 12px;
  cursor: pointer;
}
.body-status-body,
.body-status-meta {
  margin: 6px 0 0;
  font-size: 13px;
  line-height: 1.5;
}
.body-status-meta {
  font-size: 12px;
  opacity: 0.75;
}
</style>
