<script setup lang="ts">
// SessionTurnListItem.vue — V2-P4 (2026-07-24)
// Single-row representation of a turn in the dual-column list. Left half
// shows the request (injection verdict, title preview, request tokens),
// right half shows the response (output verdict, summary preview, cost +
// status). Whole row is clickable; emits `open` with the turn itself.

import { computed } from 'vue'
import type { TurnListItem } from '../api/sessions_v2'

const props = defineProps<{ turn: TurnListItem; active: boolean }>()
const emit = defineEmits<{ (e: 'open', turn: TurnListItem): void }>()

const tagClass = (v: string) => `tag-${v || 'skip'}`
const ts = computed(() => {
  try { return new Date(props.turn.ts).toLocaleString() }
  catch { return props.turn.ts }
})
</script>

<template>
  <div class="turn-row" :class="{ active }" @click="emit('open', turn)">
    <div class="col col-req">
      <div class="meta">
        <span class="turn-no">#{{ turn.turn_no }}</span>
        <span class="ts">{{ ts }}</span>
        <span :class="['verdict', tagClass(turn.injection_verdict)]">
          injection: {{ turn.injection_verdict || 'skip' }}
        </span>
      </div>
      <div class="preview">{{ turn.title || '(无请求摘要)' }}</div>
      <div class="badges">
        <span class="badge">&Delta; {{ turn.request_tokens }} tok</span>
        <span v-if="turn.attachment_count > 0" class="badge">
          &#128206; {{ turn.attachment_count }}
        </span>
        <span class="badge model">{{ turn.model }}</span>
      </div>
    </div>
    <div class="col col-resp">
      <div class="meta">
        <span class="turn-no">#{{ turn.turn_no }}</span>
        <span :class="['verdict', tagClass(turn.output_verdict)]">
          output: {{ turn.output_verdict || 'skip' }}
        </span>
      </div>
      <div class="preview">{{ turn.summary || '(无回复摘要)' }}</div>
      <div class="badges">
        <span class="badge">&Delta; {{ turn.response_tokens }} tok</span>
        <span class="badge cost">${{ turn.cost_usd.toFixed(4) }}</span>
        <span
          class="badge status"
          :data-ok="turn.status_code < 400 ? 'true' : 'false'"
        >{{ turn.status_code }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.turn-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  border: 1px solid var(--surface-secondary);
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 8px;
  cursor: pointer;
  transition: background 0.15s;
  background: white;
}
.turn-row:hover { background: var(--surface-secondary); }
.turn-row.active { background: var(--info-bg); border-color: var(--accent); }
.col { padding: 0 8px; }
.col + .col { border-left: 1px dashed var(--surface-secondary); }
.meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.turn-no { font-weight: 600; color: var(--kx-text); }
.verdict { padding: 2px 6px; border-radius: 4px; font-size: 11px; }
.tag-pass { background: var(--success-bg); color: var(--success-strong); }
.tag-warn { background: var(--warning-bg); color: var(--warning-dark); }
.tag-block { background: var(--danger-bg); color: var(--danger-dark); }
.tag-skip { background: var(--surface-secondary); color: var(--muted); }
.preview { color: var(--kx-text); margin-bottom: 6px; }
.badges { display: flex; gap: 6px; flex-wrap: wrap; }
.badge {
  background: var(--surface-secondary);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--muted);
}
.badge.cost { background: var(--success-bg); color: var(--success-strong); }
.badge.status[data-ok="true"] { background: var(--success-bg); color: var(--success-strong); }
.badge.status[data-ok="false"] { background: var(--danger-bg); color: var(--danger-dark); }
</style>
