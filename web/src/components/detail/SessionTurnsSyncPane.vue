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
  type RoleFilter,
} from './messageHelpers'

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

type Facet = 'integrated' | 'system' | 'user' | 'tool' | 'assistant' | 'children' | 'compress' | 'security'
type SubView = 'turns' | 'tree'

const turns = ref<SessionTurnTreeItem[]>([])
const loading = ref(false)
const error = ref('')
const facet = ref<Facet>('integrated')
const treeSelectedTurn = ref<number | null>(null)

// Derived-turns view state.
const subView = ref<SubView>('turns')
const derivedSelected = ref(0)
const derivedExpanded = ref<Set<number>>(new Set())
const showAllTurns = ref(false)
const leftWidth = ref(36) // percentage width of the left panel

watch(
  () => props.sessionId,
  async (id) => {
    turns.value = []
    error.value = ''
    treeSelectedTurn.value = null
    if (!id) return
    loading.value = true
    try {
      const r = await fetchSessionTurnsTree(id, { limit: 50 })
      turns.value = r.turns
      const match = r.turns.find((t) => t.request_id === props.activeRequestId)
      treeSelectedTurn.value = match?.turn_number ?? r.turns[0]?.turn_number ?? null
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const derivedTurns = computed(() =>
  deriveConversationTurns(extractMessagesFromBody(props.requestBody)),
)

// Reset selection when the conversation body changes.
watch(
  derivedTurns,
  (list) => {
    if (derivedSelected.value >= list.length) derivedSelected.value = 0
  },
  { immediate: true },
)

const selectedTurn = computed(() =>
  derivedTurns.value[derivedSelected.value] || null,
)
const selectedTurnNumber = computed(() => selectedTurn.value?.number ?? 0)

// Body fed to the right panel: the full conversation, or just the selected turn.
const rightBody = computed(() => {
  if (showAllTurns.value || !selectedTurn.value) return props.requestBody
  return { messages: selectedTurn.value.messages }
})

const treeCurrent = computed(() =>
  turns.value.find((t) => t.turn_number === treeSelectedTurn.value) || null,
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
  derivedSelected.value = i
  showAllTurns.value = false
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
      >轮次视图</button>
      <button
        type="button"
        class="btn btn-sm"
        :class="{ 'btn-primary': subView === 'tree' }"
        :disabled="timelineOnly"
        @click="subView = 'tree'"
      >原始树</button>
    </div>

    <!-- 轮次视图：左=用户指令摘要，右=该轮消息，中间分隔条可拖动 -->
    <div v-if="subView === 'turns'" class="pane-body" :class="{ 'pane-body--single': timelineOnly }">
      <aside
        class="left"
        :class="{ 'left--single': timelineOnly }"
        :style="timelineOnly ? undefined : { width: leftWidth + '%' }"
      >
        <div v-if="!derivedTurns.length" class="muted">无对话数据</div>
        <button
          v-for="t in derivedTurns"
          :key="t.index"
          type="button"
          class="turn-card"
          :class="{ active: derivedSelected === t.index }"
          @click="selectDerived(t.index)"
        >
          <div class="turn-card-head">
            <span class="tn">#{{ t.number }}</span>
            <span v-if="t.assistantCount" class="badge">回复 {{ t.assistantCount }}</span>
          </div>
          <pre v-if="derivedExpanded.has(t.index)" class="turn-preview">{{ t.userPreviewFull }}</pre>
          <pre v-else class="turn-preview">{{ t.userPreview }}</pre>
          <button
            v-if="t.truncated"
            type="button"
            class="btn btn-sm linkish"
            @click.stop="toggleDerivedExpand(t.index)"
          >{{ derivedExpanded.has(t.index) ? '收起' : '展开' }}</button>
        </button>
      </aside>

      <template v-if="!timelineOnly">
        <div class="divider" role="separator" @pointerdown="onDividerDown">
          <span class="divider-grip" />
        </div>

        <section class="right">
        <div class="facet-row">
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': showAllTurns }"
            @click="showAllTurns = true"
          >全部轮次</button>
          <button
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': !showAllTurns && selectedTurnNumber }"
            :disabled="!selectedTurnNumber"
            @click="showAllTurns = false"
          >仅本轮 #{{ selectedTurnNumber || '—' }}</button>
        </div>
        <ConversationMessagesPanel
          :body="rightBody"
          :empty-hint="'(无消息)'"
        />
        </section>
      </template>
    </div>

    <!-- 原始树：仅元数据，可下钻到 request_id -->
    <template v-else>
      <aside class="left">
        <div v-if="loading" class="muted">加载轮次…</div>
        <div v-else-if="error" class="err">{{ error }}</div>
        <button
          v-for="t in turns"
          :key="t.turn_number"
          type="button"
          class="turn-row"
          :class="[{ active: treeSelectedTurn === t.turn_number }, statusToneClass(t.status, 'turn')]"
          @click="selectTreeTurn(t)"
        >
          <span class="tn">#{{ t.turn_number }}</span>
          <span class="st pill" :class="statusToneClass(t.status, 'pill')">{{ t.status }}</span>
          <span class="lat">{{ latencyLabel(t.latency) }}</span>
          <span v-if="t.model" class="mdl">{{ t.model }}</span>
          <ul v-if="t.child_requests?.length" class="children">
            <li
              v-for="c in t.child_requests"
              :key="c.request_id"
              @click.stop="selectChild(c)"
            >
              {{ c.request_type }} ·
              <span class="pill pill--sm" :class="statusToneClass(c.status, 'pill')">{{ c.status }}</span>
              · {{ latencyLabel(c.latency) }}
            </li>
          </ul>
        </button>
      </aside>
      <section class="right">
        <div class="facet-row">
          <button
            v-for="f in ([
              ['integrated', '整合'],
              ['system', '系统'],
              ['user', '用户'],
              ['tool', '工具'],
              ['assistant', '模型'],
              ['children', '子请求'],
              ['compress', '压缩'],
              ['security', '安全脱敏'],
            ] as [Facet, string][])"
            :key="f[0]"
            type="button"
            class="btn btn-sm"
            :class="{ 'btn-primary': facet === f[0] }"
            @click="facet = f[0]"
          >
            {{ f[1] }}
          </button>
        </div>
        <div v-if="treeCurrent" class="sync-bar">
          对应 request_logs:
          <code>{{ treeCurrent.request_id }}</code>
          <button type="button" class="btn btn-sm" @click="emit('openAsRequest', treeCurrent.request_id)">
            在单请求模式打开
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
          <p v-else class="muted">无子请求</p>
        </template>
        <template v-else-if="facet === 'compress' || facet === 'security'">
          <p class="muted">请切换到「单请求」模式的「压缩与脱敏」Tab 查看三阶段对比。</p>
        </template>
        <ConversationMessagesPanel
          v-else
          :body="facet === 'assistant' ? responseBody : requestBody"
          :response-body="facet === 'integrated' ? responseBody : undefined"
          :locked-role="facetRole"
          :empty-hint="facet === 'integrated' ? '(无对话数据)' : `(无 ${facet} 消息)`"
        />
      </section>
    </template>
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
  border-radius: 6px; padding: 8px; margin-bottom: 6px; cursor: pointer; color: inherit;
}
.turn-card.active { border-color: var(--accent, var(--kx-primary)); box-shadow: inset 3px 0 0 var(--accent, var(--kx-primary)); }
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
.pill {
  display: inline-block; padding: 1px 7px; border-radius: 999px;
  font-size: 11px; font-weight: 600; color: inherit;
}
.pill--sm { padding: 0 6px; font-size: 10px; }
.pill--ok { color: var(--kx-success); background: color-mix(in srgb, var(--kx-success) 14%, transparent); }
.pill--err { color: var(--kx-error); background: color-mix(in srgb, var(--kx-error) 14%, transparent); }
.pill--warn { color: var(--kx-warning); background: color-mix(in srgb, var(--kx-warning) 16%, transparent); }
.pill--info { color: var(--kx-primary); background: color-mix(in srgb, var(--kx-primary) 14%, transparent); }
.pill--muted { color: var(--muted); background: var(--bg-subtle, var(--surface-secondary)); }
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
