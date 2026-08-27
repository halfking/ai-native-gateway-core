<script setup lang="ts">
// SessionTurnsSyncPane — left timeline + right facet; syncs turn ↔ request_id.
import { computed, ref, watch } from 'vue'
import {
  fetchSessionTurnsTree,
  type SessionChildRequest,
  type SessionTurnTreeItem,
} from '../../api/sessionTurnsTree'
import ConversationMessagesPanel from './ConversationMessagesPanel.vue'
import { statusToneClass } from './statusTone'
import type { RoleFilter } from './messageHelpers'

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

const turns = ref<SessionTurnTreeItem[]>([])
const loading = ref(false)
const error = ref('')
const facet = ref<Facet>('integrated')
const selectedTurn = ref<number | null>(null)

watch(
  () => props.sessionId,
  async (id) => {
    turns.value = []
    error.value = ''
    selectedTurn.value = null
    if (!id) return
    loading.value = true
    try {
      const r = await fetchSessionTurnsTree(id, { limit: 50 })
      turns.value = r.turns
      const match = r.turns.find((t) => t.request_id === props.activeRequestId)
      selectedTurn.value = match?.turn_number ?? r.turns[0]?.turn_number ?? null
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  },
  { immediate: true },
)

const current = computed(() =>
  turns.value.find((t) => t.turn_number === selectedTurn.value) || null,
)

function latencyLabel(ms: number | null): string {
  return ms == null ? '—' : `${ms}ms`
}

function selectTurn(t: SessionTurnTreeItem) {
  selectedTurn.value = t.turn_number
  emit('selectRequest', t.request_id, t.turn_number)
}

function selectChild(c: SessionChildRequest) {
  emit('openAsRequest', c.request_id)
}

const facetRole = computed((): RoleFilter | undefined => {
  if (facet.value === 'system' || facet.value === 'user' || facet.value === 'tool' || facet.value === 'assistant') {
    return facet.value
  }
  return undefined
})
</script>

<template>
  <div class="sync-pane" :class="{ 'sync-pane--timeline': timelineOnly }">
    <aside class="left">
      <div v-if="loading" class="muted">加载轮次…</div>
      <div v-else-if="error" class="err">{{ error }}</div>
      <button
        v-for="t in turns"
        :key="t.turn_number"
        type="button"
        class="turn-row"
        :class="[{ active: selectedTurn === t.turn_number }, statusToneClass(t.status, 'turn')]"
        @click="selectTurn(t)"
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
    <section v-if="!timelineOnly" class="right">
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
      <div v-if="current" class="sync-bar">
        对应 request_logs:
        <code>{{ current.request_id }}</code>
        <button type="button" class="btn btn-sm" @click="emit('openAsRequest', current.request_id)">
          在单请求模式打开
        </button>
      </div>
      <template v-if="facet === 'children'">
        <ul v-if="current?.child_requests?.length" class="child-list">
          <li v-for="c in current.child_requests" :key="c.request_id">
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
  </div>
</template>

<style scoped>
.sync-pane { display: grid; grid-template-columns: minmax(200px, 32%) 1fr; gap: 12px; min-height: 320px; }
.sync-pane--timeline { grid-template-columns: 1fr; min-height: 0; height: 100%; }
.left { border-right: 1px solid var(--border); padding-right: 8px; overflow: auto; max-height: 60vh; }
.sync-pane--timeline .left { border-right: none; max-height: none; height: 100%; padding: 8px; }
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
.tn { font-weight: 600; margin-right: 6px; }
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
  .sync-pane { grid-template-columns: 1fr; }
  .left { border-right: none; border-bottom: 1px solid var(--border); max-height: 200px; }
}
</style>
