<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getSessionCompare, type TurnView, type SessionCompareData, type SessionTagView } from '../api/session'
import TurnStageCard from './TurnStageCard.vue'

const props = defineProps<{
  sessionId: string
  tenantId?: string
  /** Optional up-to-now summary to show at the top (from session analytics). */
  summary?: string
  title?: string
}>()

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const data = ref<SessionCompareData | null>(null)
const showHistory = ref(false)

async function load() {
  if (!props.sessionId) return
  loading.value = true
  error.value = ''
  try {
    data.value = await getSessionCompare(props.sessionId, props.tenantId)
  } catch (e: any) {
    data.value = null
    error.value = e.message || t('sessions.turns.loadError')
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.sessionId, load)

const turns = computed<TurnView[]>(() => data.value?.turns ?? [])
const currentTurn = computed<TurnView | null>(() => turns.value.length ? turns.value[turns.value.length - 1] : null)
const historyTurns = computed<TurnView[]>(() => turns.value.length > 1 ? turns.value.slice(0, -1) : [])

// Session-level tags (security/compliance/pii/approval/optimization + OLAP).
const sessionTags = computed<SessionTagView[]>(() => data.value?.session_tags ?? [])
// Security/compliance-ish tags shown as colored chips at the top.
const securityTags = computed(() =>
  sessionTags.value.filter(t =>
    ['security', 'compliance', 'pii', 'approval', 'optimization', 'risk_action'].includes(t.tag_key)
  )
)
function tagChipClass(key: string, value: string): string {
  if (key === 'pii' && value === 'stripped') return 'chip-pii'
  if (key === 'approval' && value === 'pending') return 'chip-warn'
  if (key === 'security' && value.includes('high')) return 'chip-danger'
  if (key === 'compliance') return 'chip-warn'
  return 'chip-neutral'
}

function tagLabel(tag: string): string {
  if (tag === 'pii_strip') return t('sessions.turns.tagPiiStrip')
  if (tag === 'strip_tools' || tag === 'compress_thinking' || tag === 'summarize') return t('sessions.turns.tagStripTools')
  if (tag === 'sensitive_detected') return t('sessions.turns.tagSensitive')
  if (tag.startsWith('audit:')) return t('sessions.turns.tagAudit') + ' ' + tag.slice(6)
  return tag
}
function savedPct(turn: TurnView): string {
  const o = turn.original.tokens
  const c = turn.compressed.tokens
  if (o <= 0 || c >= o) return ''
  return Math.round((1 - c / o) * 100) + '%'
}
function preview(s: string, max = 320): string {
  if (!s) return '—'
  return s.length > max ? s.slice(0, max) + '…' : s
}
const loadingText = computed(() => {
  // reuse a sensible "loading" string; fall back to a literal if missing
  return t('sessions.management.loading') || 'Loading…'
})
</script>

<template>
  <div class="turns-panel">
    <div class="turns-head">
      <h4 class="turns-title">{{ t('sessions.turns.title') }}</h4>
      <span v-if="title" class="turns-session-title">{{ title }}</span>
    </div>

    <!-- Session-level security/compliance/pii chips -->
    <div v-if="securityTags.length" class="tag-bar">
      <span v-for="tag in securityTags" :key="tag.tag_key + tag.tag_value"
        class="stag" :class="tagChipClass(tag.tag_key, tag.tag_value)"
        :title="`${tag.tag_key}: ${tag.tag_value} (${tag.tag_source})`">
        {{ tag.tag_key }}: {{ tag.tag_value }}
      </span>
    </div>

    <!-- Up-to-now summary -->
    <div class="summary-row">
      <span class="summary-label">{{ t('sessions.turns.summaryLabel') }}</span>
      <span v-if="summary" class="summary-text">{{ summary }}</span>
      <span v-else class="summary-empty">{{ t('sessions.turns.summaryEmpty') }}</span>
    </div>

    <p v-if="loading" class="state">{{ loadingText }}</p>
    <p v-else-if="error" class="state err">{{ error }}</p>
    <p v-else-if="!currentTurn" class="state muted">{{ t('sessions.turns.empty') }}</p>

    <template v-else>
      <!-- Current turn: three-column three-stage view -->
      <div class="current-block">
        <div class="current-label">{{ t('sessions.turns.currentTurn') }} #{{ currentTurn.turn }}</div>
        <TurnStageCard
          :stage-label="t('sessions.turns.stageOriginal')"
          :send-label="t('sessions.turns.directionSend') + ' ' + t('sessions.turns.sendHint')"
          :receive-label="t('sessions.turns.directionReceive')"
          :send="preview(currentTurn.original.send)"
          :receive="preview(currentTurn.original.receive)"
          :tokens="currentTurn.original.tokens"
        />
        <TurnStageCard
          :stage-label="t('sessions.turns.stageCompressed')"
          :send-label="t('sessions.turns.directionSend') + ' ' + t('sessions.turns.sendHintCompressed')"
          :receive-label="t('sessions.turns.directionReceive')"
          :send="preview(currentTurn.compressed.send)"
          :receive="preview(currentTurn.compressed.receive)"
          :tokens="currentTurn.compressed.tokens"
          :saved="savedPct(currentTurn)"
          :extra="currentTurn.summary_marker ? t('sessions.turns.summaryMarker') : ''"
          :range="currentTurn.compressed.range_start && currentTurn.compressed.range_end
            ? t('sessions.turns.rangeLabel', { s: currentTurn.compressed.range_start, e: currentTurn.compressed.range_end })
            : ''"
          :strategy="currentTurn.strategy"
        />
        <TurnStageCard
          :stage-label="t('sessions.turns.stageSecured')"
          :send-label="t('sessions.turns.directionSend')"
          :receive-label="t('sessions.turns.directionReceive')"
          :send="preview(currentTurn.secured.send)"
          :receive="preview(currentTurn.secured.receive)"
          :tokens="currentTurn.secured.tokens"
          :tags="currentTurn.secured.applied_tags"
          :no-change-hint="(!currentTurn.secured.applied_tags || currentTurn.secured.applied_tags.length === 0) ? t('sessions.turns.noSecurityChange') : ''"
        />
      </div>

      <!-- History (collapsed by default) -->
      <div v-if="historyTurns.length" class="history-block">
        <button class="history-toggle" @click="showHistory = !showHistory">
          <span class="caret" :class="{ open: showHistory }">▶</span>
          {{ showHistory ? t('sessions.turns.collapseHistory') : t('sessions.turns.expandHistory', { n: historyTurns.length }) }}
        </button>
        <div v-if="showHistory" class="history-list">
          <div v-for="ht in historyTurns" :key="ht.turn" class="history-row">
            <div class="history-turn-head">
              <span class="seq">#{{ ht.turn }}</span>
              <span class="ts">{{ ht.ts }}</span>
              <span v-if="ht.strategy" class="strat-badge">{{ ht.strategy }}</span>
              <span v-if="savedPct(ht)" class="saved-badge">{{ t('sessions.turns.saved', { pct: savedPct(ht) }) }}</span>
            </div>
            <div class="history-cols">
              <div class="history-col">
                <span class="col-lbl">{{ t('sessions.turns.stageOriginal') }}</span>
                <span class="col-send">👤 {{ preview(ht.original.send, 120) }}</span>
              </div>
              <div class="history-col">
                <span class="col-lbl">{{ t('sessions.turns.stageCompressed') }}</span>
                <span class="col-send">📦 {{ preview(ht.compressed.send, 120) }}</span>
                <span v-if="ht.summary_marker" class="marker-flag">⭐</span>
              </div>
              <div class="history-col">
                <span class="col-lbl">{{ t('sessions.turns.stageSecured') }}</span>
                <span class="col-tags">
                  <span v-if="!ht.secured.applied_tags || !ht.secured.applied_tags.length" class="text-muted">—</span>
                  <span v-for="tag in (ht.secured.applied_tags || [])" :key="tag" class="mini-tag">{{ tagLabel(tag) }}</span>
                </span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.turns-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.turns-head { display: flex; align-items: baseline; gap: 10px; }
.turns-title { margin: 0; font-size: 13px; font-weight: 600; color: var(--text-primary, #e6edf3); }
.turns-session-title { font-size: 12px; color: var(--text-secondary, #8b949e); }

.tag-bar { display: flex; flex-wrap: wrap; gap: 5px; }
.stag {
  display: inline-block; padding: 1px 8px; border-radius: 999px;
  font-size: 10px; font-weight: 500; border: 1px solid var(--border, #30363d);
}
.chip-neutral { background: var(--bg-hover, #2a2a3e); color: var(--text-secondary, #8b949e); }
.chip-pii { background: rgba(52,211,153,.12); color: #34d399; border-color: rgba(52,211,153,.3); }
.chip-warn { background: rgba(251,191,36,.12); color: #fbbf24; border-color: rgba(251,191,36,.3); }
.chip-danger { background: rgba(248,113,113,.12); color: #f87171; border-color: rgba(248,113,113,.3); }

.summary-row {
  display: flex;
  gap: 8px;
  padding: 8px 10px;
  background: var(--bg-subtle, rgba(99,102,241,.05));
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
}
.summary-label { color: var(--text-secondary, #8b949e); flex-shrink: 0; font-weight: 600; }
.summary-text { color: var(--text-primary, #e6edf3); white-space: pre-wrap; word-break: break-word; }
.summary-empty { color: var(--text-muted, #6e7681); font-style: italic; }

.state { font-size: 12px; color: var(--text-secondary, #8b949e); padding: 8px; }
.state.err { color: var(--danger, #f87171); }
.state.muted { color: var(--text-muted, #6e7681); }

.current-block {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 10px;
}
.current-label {
  grid-column: 1 / -1;
  font-size: 12px;
  font-weight: 600;
  color: var(--accent-h, #818cf8);
  margin-bottom: -4px;
}

.history-block { margin-top: 6px; }
.history-toggle {
  background: none; border: none; cursor: pointer;
  color: var(--text-secondary, #8b949e); font-size: 12px;
  display: flex; align-items: center; gap: 6px; padding: 4px 0;
}
.history-toggle:hover { color: var(--text-primary, #e6edf3); }
.caret { font-size: 9px; transition: transform .2s; display: inline-block; }
.caret.open { transform: rotate(90deg); }
.history-list { display: flex; flex-direction: column; gap: 6px; margin-top: 6px; }
.history-row {
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  padding: 6px 8px;
}
.history-turn-head { display: flex; align-items: center; gap: 8px; font-size: 11px; margin-bottom: 4px; }
.seq { font-weight: 600; color: var(--accent-h, #818cf8); }
.ts { color: var(--text-muted, #6e7681); }
.strat-badge {
  padding: 0 6px; border-radius: 4px; font-size: 10px;
  background: var(--bg-hover, #2a2a3e); color: var(--text-secondary, #8b949e);
}
.saved-badge { color: var(--success, #34d399); font-weight: 600; margin-left: auto; }
.history-cols { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.history-col { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.col-lbl { font-size: 10px; color: var(--text-muted, #6e7681); text-transform: uppercase; }
.col-send { font-size: 11px; color: var(--text-primary, #e6edf3); white-space: pre-wrap; word-break: break-word; line-height: 1.4; }
.marker-flag { font-size: 10px; }
.col-tags { display: flex; flex-wrap: wrap; gap: 3px; }
.mini-tag {
  padding: 0 5px; border-radius: 4px; font-size: 10px;
  background: rgba(99,102,241,.12); color: var(--accent-h, #818cf8);
}
.text-muted { color: var(--text-muted, #6e7681); font-size: 11px; }

@media (max-width: 820px) {
  .current-block, .history-cols { grid-template-columns: 1fr; }
}
</style>
