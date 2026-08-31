<script setup lang="ts">
// SessionTurnsSyncPane — session turns view.
//
// Two sub-views:
//   - "turns" (default): derive turns from the conversation's message list by
//     splitting on each user message (system + user + assistant reply per turn).
//     Left = per-turn user-message summary (collapsible), click a turn to show
//     only its messages on the right. A draggable divider adjusts the width.
//   - "tree": the original request/child-request tree (metadata only) that
//     syncs a turn to its request_id.
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  fetchSessionTurnsTree,
  type SessionChildRequest,
  type SessionTurnTreeItem,
} from '../../api/sessionTurnsTree'
import ConversationMessagesPanel from './ConversationMessagesPanel.vue'
import { statusToneClass } from './statusTone'
import {
  deriveConversationTurns,
  extractMessagesFromBody,
  splitTurnMessages,
  summarizeTurnUserInstruction,
  type ConversationTurn,
  type RoleFilter,
} from './messageHelpers'
import {
  fetchSessionTurnsBodies,
  type SessionTurnBodiesResponse,
} from '../../api/sessions_v2'
import TurnDigestDrawer from '../session/TurnDigestDrawer.vue'

const props = defineProps<{
  sessionId: string
  activeRequestId: string | null
  requestBody: unknown
  responseBody: unknown
  /** When true, only render the turns timeline (fullscreen shell owns facets). */
  timelineOnly?: boolean
}>()

const emit = defineEmits<{
  selectRequest: [requestId: string, turnNumber: number]
  openAsRequest: [requestId: string]
}>()

const { t } = useI18n()

// Digest drawer state — opened from a left-rail turn card in the "turns" sub-view.
const digestDrawerOpen = ref(false)
const digestDrawerTurnNo = ref<number | null>(null)

function showDigest(turnNo: number | null | undefined) {
  if (turnNo == null) return
  digestDrawerTurnNo.value = turnNo
  digestDrawerOpen.value = true
}

function closeDigest() {
  digestDrawerOpen.value = false
  digestDrawerTurnNo.value = null
}

type Facet = 'integrated' | 'system' | 'user' | 'tool' | 'assistant' | 'children' | 'compress' | 'security'
type SubView = 'turns' | 'tree'

const turns = ref<SessionTurnTreeItem[]>([])
const loading = ref(false)
const error = ref('')
const facet = ref<Facet>('integrated')
const treeSelectedTurn = ref<number | null>(null)

// Derived-turns view state.
const subView = ref<SubView>('turns')
const selectedIndex = ref(0)
const derivedExpanded = ref<Set<number>>(new Set())
const showAllTurns = ref(false)
const leftWidth = ref(36) // percentage width of the left panel

// V2 per-turn bodies (session_bodies) — the primary source once the gateway
// dual-writes sessions. Empty when the session has no V2 bodies yet, in which
// case we fall back to deriving turns from the request_logs body.
const v2Bodies = ref<SessionTurnBodiesResponse | null>(null)
const v2Error = ref('')
const treeHasMore = ref(false)
let sessionLoadSeq = 0

watch(
  () => props.sessionId,
  async (id) => {
    const seq = ++sessionLoadSeq
    // Session switched: close any open digest drawer so it doesn't leak the
    // previous session's turnNo into the new session.
    closeDigest()
    turns.value = []
    error.value = ''
    treeSelectedTurn.value = null
    v2Bodies.value = null
    v2Error.value = ''
    treeHasMore.value = false
    if (!id) return
    loading.value = true
    try {
      const r = await fetchSessionTurnsTree(id, { limit: 50 })
      if (seq !== sessionLoadSeq) return
      turns.value = r.turns
      treeHasMore.value = r.has_more
      const match = r.turns.find((t) => t.request_id === props.activeRequestId)
      treeSelectedTurn.value = match?.turn_number ?? r.turns[0]?.turn_number ?? null
    } catch (e: unknown) {
      if (seq !== sessionLoadSeq) return
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === sessionLoadSeq) loading.value = false
    }
    // Best-effort: V2 bodies are optional until the cutover completes.
    try {
      const bodies = await fetchSessionTurnsBodies(id, { limit: 200 })
      if (seq !== sessionLoadSeq) return
      v2Bodies.value = bodies
    } catch (e: unknown) {
      if (seq !== sessionLoadSeq) return
      v2Error.value = e instanceof Error ? e.message : String(e)
      v2Bodies.value = null
    }
  },
  { immediate: true },
)

// V2 turns built from session_bodies. Each turn's request-side messages are
// drawn from outbound_body (the full prompt the gateway actually sent) so
// system context survives across turns; the assistant reply block uses
// response_delta independently so we don't conflate request and response.
const v2Turns = computed<ConversationTurn[]>(() => {
  const list = v2Bodies.value?.turns
  if (!list?.length) return []
  return list.map((t, i) => {
    const { requestMessages, responseMessages } = splitTurnMessages(
      t.request_delta,
      t.response_delta,
      t.outbound_body,
    )
    const deltaMessages = [
      ...extractMessagesFromBody(t.request_delta),
      ...responseMessages,
    ]
    const summary = summarizeTurnUserInstruction(t.request_delta, t.outbound_body)
    const messages = [...requestMessages, ...responseMessages]
    return {
      index: i,
      number: t.turn_no,
      turnNo: t.turn_no,
      requestId: typeof t.request_id === 'string' ? t.request_id : null,
      requestMessages,
      responseMessages,
      allTurnMessages: deltaMessages.length ? deltaMessages : messages,
      messages,
      userPreview: summary.text,
      userPreviewFull: summary.full,
      truncated: summary.truncated,
      assistantCount: messages.filter((m) => String(m.role || '') === 'assistant').length,
    }
  })
})

// Fallback: derive turns from the accumulated request_logs body.
const derivedTurns = computed(() =>
  deriveConversationTurns(extractMessagesFromBody(props.requestBody)),
)

// V2 bodies win when present; otherwise fall back to request_logs derivation.
const displayTurns = computed<ConversationTurn[]>(() => {
  if (!v2Turns.value.length) return derivedTurns.value
  // Keep a usable card for V2 rows whose body was scrubbed/NULL by falling
  // back to the corresponding request_logs-derived turn when available.
  return v2Turns.value.map((turn, i) => {
    if (turn.messages.length || !derivedTurns.value[i]) return turn
    const fallback = derivedTurns.value[i]
    return {
      ...fallback,
      index: turn.index,
      number: turn.number,
      turnNo: turn.turnNo,
      requestId: turn.requestId,
    }
  })
})

// Reset selection when the displayed turn set changes.
watch(
  displayTurns,
  (list) => {
    if (selectedIndex.value >= list.length) selectedIndex.value = 0
  },
  { immediate: true },
)

// When the route's active request changes, align the selected turn card so
// the left rail reflects the request the user is currently looking at
// instead of always defaulting to the first card.
watch(
  [() => props.activeRequestId, displayTurns],
  ([activeId, list]) => {
    if (!list.length) {
      selectedIndex.value = 0
      return
    }
    if (activeId) {
      const match = list.findIndex((t) => t.requestId === activeId)
      if (match >= 0) {
        selectedIndex.value = match
        return
      }
    }
    if (selectedIndex.value >= list.length) selectedIndex.value = 0
  },
  { immediate: true },
)

const selectedTurn = computed(() => displayTurns.value[selectedIndex.value] || null)
const selectedTurnNumber = computed(() => selectedTurn.value?.number ?? 0)

// Body fed to the right panel: the full conversation, or just the selected turn.
const rightBody = computed(() => {
  if (showAllTurns.value) {
    if (v2Turns.value.length) {
      // Use incremental request/response messages for the all-turn view.
      // Each outbound_body is a full snapshot and would repeat the entire
      // conversation once per later turn.
      const messages = displayTurns.value.flatMap((t) => t.allTurnMessages || t.messages)
      return { messages }
    }
    return props.requestBody
  }
  return selectedTurn.value ? { messages: selectedTurn.value.messages } : props.requestBody
})

const treeCurrent = computed(() =>
  turns.value.find((t) => t.turn_number === treeSelectedTurn.value) || null,
)
const hasMoreTurns = computed(() => treeHasMore.value || Boolean(v2Bodies.value?.has_more))

watch(
  () => props.activeRequestId,
  (requestId) => {
    const match = turns.value.find((t) => t.request_id === requestId)
    if (match) treeSelectedTurn.value = match.turn_number
  },
)

function latencyLabel(ms: number | null): string {
  return ms == null ? '—' : `${ms}ms`
}

function selectTreeTurn(t: SessionTurnTreeItem) {
  treeSelectedTurn.value = t.turn_number
  emit('selectRequest', t.request_id, t.turn_number)
}

function selectChild(c: SessionChildRequest) {
  emit('openAsRequest', c.request_id)
}

function selectDerived(i: number) {
  selectedIndex.value = i
  showAllTurns.value = false
  const rid = displayTurns.value[i]?.requestId
  const turnNo = displayTurns.value[i]?.turnNo ?? displayTurns.value[i]?.number ?? 0
  if (rid) emit('selectRequest', rid, turnNo)
}

function toggleDerivedExpand(i: number) {
  const next = new Set(derivedExpanded.value)
  if (next.has(i)) next.delete(i)
  else next.add(i)
  derivedExpanded.value = next
}

const facetRole = computed((): RoleFilter | undefined => {
  if (facet.value === 'system' || facet.value === 'user' || facet.value === 'tool' || facet.value === 'assistant') {
    return facet.value
  }
  return undefined
})

// --- Movable divider ---------------------------------------------------------
const paneEl = ref<HTMLElement | null>(null)
let dragging = false

function onDividerDown(e: PointerEvent) {
  dragging = true
  window.addEventListener('pointermove', onDividerMove)
  window.addEventListener('pointerup', onDividerUp)
  e.preventDefault()
}

function onDividerMove(e: PointerEvent) {
  if (!dragging || !paneEl.value) return
  const body = paneEl.value.querySelector('.pane-body') as HTMLElement | null
  if (!body) return
  const rect = body.getBoundingClientRect()
  const pct = ((e.clientX - rect.left) / rect.width) * 100
  leftWidth.value = Math.min(62, Math.max(20, pct))
}

function onDividerUp() {
  dragging = false
  window.removeEventListener('pointermove', onDividerMove)
  window.removeEventListener('pointerup', onDividerUp)
}

function onDividerKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowLeft') leftWidth.value = Math.max(20, leftWidth.value - 2)
  else if (e.key === 'ArrowRight') leftWidth.value = Math.min(62, leftWidth.value + 2)
  else return
  e.preventDefault()
}
</script>

<template>
  <div ref="paneEl" class="sync-pane" :class="{ 'sync-pane--timeline': timelineOnly }">
    <div class="view-switch">
      <button
        type="button"
        class="btn btn-sm"
        :class="{ 'btn-primary': subView === 'turns' }"
        :disabled="timelineOnly"
        @click="subView = 'turns'"
      >{{ t('requestDetail.turns.viewLabel') }}</button>
      <button
        type="button"
        class="btn btn-sm"
        :class="{ 'btn-primary': subView === 'tree' }"
        :disabled="timelineOnly"
        @click="subView = 'tree'"
      >{{ t('requestDetail.turns.rawTree') }}</button>
    </div>

    <!-- 轮次视图：左=用户指令摘要，右=该轮消息，中间分隔条可拖动 -->
    <div v-if="subView === 'turns'" class="pane-body" :class="{ 'pane-body--single': timelineOnly }">
      <aside
        class="left"
        :class="{ 'left--single': timelineOnly }"
        :style="timelineOnly ? undefined : { width: leftWidth + '%' }"
      >
        <div v-if="hasMoreTurns" class="warn">{{ t('requestDetail.turns.truncatedHint') }}</div>
        <div v-if="!displayTurns.length" class="muted">{{ t('requestDetail.turns.empty') }}</div>
        <div
          v-for="turn in displayTurns"
          :key="turn.index"
          class="turn-card"
          :class="{ active: selectedIndex === turn.index }"
          role="group"
          :aria-label="`${t('requestDetail.turns.viewLabel')} #${turn.number}`"
        >
          <button
            type="button"
            class="turn-card-select"
            :class="{ active: selectedIndex === turn.index }"
            :aria-pressed="selectedIndex === turn.index"
            @click="selectDerived(turn.index)"
          >
            <span class="turn-card-head">
              <span class="tn">#{{ turn.number }}</span>
              <span v-if="turn.assistantCount" class="badge">{{ t('requestDetail.turns.badgeReply', { count: turn.assistantCount }) }}</span>
            </span>
            <span v-if="derivedExpanded.has(turn.index)" class="turn-preview">{{ turn.userPreviewFull }}</span>
            <span v-else class="turn-preview">{{ turn.userPreview }}</span>
          </button>
          <button
            v-if="turn.truncated"
            type="button"
            class="btn btn-sm linkish"
@click="toggleDerivedExpand(turn.index)"
        >{{ derivedExpanded.has(turn.index) ? t('requestDetail.turns.collapse') : t('requestDetail.turns.expand') }}</button>
          <button
            v-if="turn.turnNo != null"
            type="button"
            class="btn btn-sm stsp-digest"
            data-testid="stsp-show-digest"
            :title="t('turnDigest.view')"
            :aria-label="`${t('turnDigest.view')} #${turn.number}`"
            @click.stop="showDigest(turn.turnNo)"
          >{{ t('turnDigest.view') }}</button>
        </div>
      </aside>

      <template v-if="!timelineOnly">
        <div
          class="divider"
          role="separator"
          tabindex="0"
          :aria-valuenow="Math.round(leftWidth)"
          aria-valuemin="20"
          aria-valuemax="62"
          aria-label="divider"
          @pointerdown="onDividerDown"
          @keydown="onDividerKeydown"
        >
          <span class="divider-grip" />
        </div>

        <section class="right">
        <div class="facet-row">
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': showAllTurns }"
            @click="showAllTurns = true"
          >{{ t('requestDetail.turns.allTurns') }}</button>
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': !showAllTurns && selectedTurnNumber }"
            :disabled="!selectedTurnNumber"
            @click="showAllTurns = false"
          >{{ t('requestDetail.turns.onlyThisTurn', { number: selectedTurnNumber || '—' }) }}</button>
        </div>
        <ConversationMessagesPanel
          :body="rightBody"
          :empty-hint="t('requestDetail.turns.emptyHint')"
        />
        </section>
      </template>
    </div>

    <!-- 原始树：仅元数据，可下钻到 request_id -->
    <template v-else>
      <aside class="left">
        <div v-if="loading" class="muted">{{ t('requestDetail.turns.loading') }}</div>
        <div v-else-if="error" class="err">{{ error }}</div>
        <div
          v-for="t in turns"
          :key="t.turn_number"
          class="turn-row"
          :class="[{ active: treeSelectedTurn === t.turn_number }, statusToneClass(t.status, 'turn')]"
          role="button"
          tabindex="0"
          :aria-pressed="treeSelectedTurn === t.turn_number"
          @click="selectTreeTurn(t)"
          @keydown.enter.prevent="selectTreeTurn(t)"
          @keydown.space.prevent="selectTreeTurn(t)"
        >
          <span class="tn">#{{ t.turn_number }}</span>
          <span class="st pill" :class="statusToneClass(t.status, 'pill')">{{ t.status }}</span>
          <span class="lat">{{ latencyLabel(t.latency) }}</span>
          <span v-if="t.model" class="mdl">{{ t.model }}</span>
          <ul v-if="t.child_requests?.length" class="children">
            <li
              v-for="c in t.child_requests"
              :key="c.request_id"
              tabindex="0"
              role="button"
              @click.stop="selectChild(c)"
              @keydown.enter.stop.prevent="selectChild(c)"
              @keydown.space.stop.prevent="selectChild(c)"
            >
              {{ c.request_type }} ·
              <span class="pill pill--sm" :class="statusToneClass(c.status, 'pill')">{{ c.status }}</span>
              · {{ latencyLabel(c.latency) }}
            </li>
          </ul>
        </div>
      </aside>
      <section class="right">
        <div class="facet-row">
          <button
            v-for="f in ([
              'integrated',
              'system',
              'user',
              'tool',
              'assistant',
              'children',
              'compress',
              'security',
            ] as Facet[])"
            :key="f"
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': facet === f }"
            @click="facet = f"
          >
            {{ t(`requestDetail.turns.facet.${f}`) }}
          </button>
        </div>
        <div v-if="treeCurrent" class="sync-bar">
          {{ t('requestDetail.turns.mappedToLogs') }}
          <code>{{ treeCurrent.request_id }}</code>
          <button type="button" class="btn btn-sm" @click="emit('openAsRequest', treeCurrent.request_id)">
            {{ t('requestDetail.turns.openSingle') }}
          </button>
        </div>
        <template v-if="facet === 'children'">
          <ul v-if="treeCurrent?.child_requests?.length" class="child-list">
            <li v-for="c in treeCurrent.child_requests" :key="c.request_id">
              <button type="button" class="btn btn-sm" @click="selectChild(c)">
                {{ c.request_type }} · {{ c.request_id }}
              </button>
            </li>
          </ul>
          <p v-else class="muted">{{ t('requestDetail.turns.noChildren') }}</p>
        </template>
        <template v-else-if="facet === 'compress' || facet === 'security'">
          <p class="muted">{{ t('requestDetail.turns.switchHint') }}</p>
        </template>
        <ConversationMessagesPanel
          v-else
          :body="facet === 'assistant' ? responseBody : requestBody"
          :response-body="facet === 'integrated' ? responseBody : undefined"
          :locked-role="facetRole"
          :empty-hint="facet === 'integrated' ? t('requestDetail.turns.empty') : t('requestDetail.turns.noFacetMessages', { facet: t(`requestDetail.turns.facet.${facet}`) })"
        />
      </section>
    </template>

    <TurnDigestDrawer
      :model-value="digestDrawerOpen"
      :session-id="sessionId"
      :turn-no="digestDrawerTurnNo"
      @update:model-value="(value: boolean) => { if (!value) closeDigest() }"
      @close="closeDigest"
    />
  </div>
</template>

<style scoped>
.sync-pane { display: flex; flex-direction: column; min-height: 320px; }
.sync-pane--timeline { min-height: 0; height: 100%; }
.view-switch { display: flex; gap: 6px; margin-bottom: 10px; }
.pane-body { display: flex; align-items: stretch; gap: 0; min-height: 320px; flex: 1; }
.pane-body--single { display: block; }
.left {
  flex: 0 0 auto; min-width: 200px; max-width: 62%;
  border-right: 1px solid var(--border); padding-right: 8px;
  overflow: auto; max-height: 64vh;
}
.left--single { width: 100%; max-width: none; border: none; padding: 8px; max-height: none; height: 100%; }
.sync-pane--timeline .left { border-right: none; max-height: none; height: 100%; padding: 8px; }
.divider {
  flex: 0 0 8px; align-self: stretch; cursor: col-resize;
  display: flex; align-items: center; justify-content: center;
  background: transparent; touch-action: none;
}
.divider:hover { background: color-mix(in srgb, var(--accent, var(--kx-primary)) 12%, transparent); }
.divider-grip {
  width: 3px; height: 38px; border-radius: 999px;
  background: var(--border); transition: background .15s;
}
.divider:hover .divider-grip { background: var(--accent, var(--kx-primary)); }
.right { flex: 1 1 auto; min-width: 0; padding-left: 12px; overflow: auto; max-height: 64vh; }
.sync-pane--timeline .right { max-height: none; }

.turn-card {
  display: block; width: 100%; text-align: left;
  border: 1px solid var(--border); background: var(--bg-card, transparent);
  border-radius: 6px; padding: 0; margin-bottom: 6px; color: inherit;
}
.turn-card.active { border-color: var(--accent, var(--kx-primary)); box-shadow: inset 3px 0 0 var(--accent, var(--kx-primary)); }
.turn-card-select {
  display: block; width: 100%; text-align: left; border: 0;
  background: transparent; padding: 8px; cursor: pointer; color: inherit;
}
.turn-card-select.active { box-shadow: inset 3px 0 0 var(--accent, var(--kx-primary)); }
.turn-card-head { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.turn-card .tn { font-weight: 600; }
.turn-card .badge {
  font-size: 10px; color: var(--kx-success);
  background: color-mix(in srgb, var(--kx-success) 14%, transparent);
  padding: 0 6px; border-radius: 999px;
}
.turn-preview {
  margin: 0; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.45; color: var(--fg, inherit);
  max-height: 180px; overflow: auto; background: transparent;
}
.linkish { margin-top: 4px; }
.stsp-digest {
  margin-top: 6px;
  border: 1px solid var(--border, var(--kx-border));
  background: transparent;
  color: var(--accent, var(--kx-primary));
  font-size: 11px;
}
.stsp-digest:hover { border-color: var(--accent, var(--kx-primary)); }

.turn-row {
  display: block; width: 100%; text-align: left;
  border: 1px solid var(--border); background: var(--bg-card, transparent);
  border-radius: 6px; padding: 8px; margin-bottom: 6px; cursor: pointer; color: inherit;
}
.turn-row.active { border-color: var(--accent); box-shadow: inset 3px 0 0 var(--accent); }
.turn--ok { border-left: 3px solid var(--kx-success); }
.turn--err { border-left: 3px solid var(--kx-error); background: color-mix(in srgb, var(--kx-error) 5%, transparent); }
.turn--warn { border-left: 3px solid var(--kx-warning); }
.turn--info { border-left: 3px solid var(--kx-primary); }
.left .tn { font-weight: 600; margin-right: 6px; }
.st, .lat, .mdl { font-size: 11px; color: var(--muted); margin-right: 6px; }
/* .pill / .pill--* 全部从全局 styles/pill-chip.css 继承（P1-8）。 */
.children { margin: 6px 0 0; padding-left: 14px; font-size: 11px; color: var(--muted); }
.children li { cursor: pointer; }
.children li:hover { color: var(--accent); }
.facet-row { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 8px; }
.sync-bar { font-size: 12px; margin-bottom: 8px; display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.child-list { list-style: none; padding: 0; }
.muted { color: var(--muted); font-size: 12px; }
.err { color: var(--danger); font-size: 12px; }
@media (max-width: 800px) {
  .pane-body { flex-direction: column; }
  .left { border-right: none; border-bottom: 1px solid var(--border); max-height: 200px; width: 100% !important; max-width: none; }
  .divider { display: none; }
  .right { padding-left: 0; max-height: none; }
}
</style>
