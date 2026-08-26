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
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 8px;
  cursor: pointer;
  transition: background 0.15s;
  background: white;
}
.turn-row:hover { background: #f9fafb; }
.turn-row.active { background: #eff6ff; border-color: #3b82f6; }
.col { padding: 0 8px; }
.col + .col { border-left: 1px dashed #e5e7eb; }
.meta {
  display: flex;
  gap: 8px;
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 6px;
  flex-wrap: wrap;
}
.turn-no { font-weight: 600; color: #111827; }
.verdict { padding: 2px 6px; border-radius: 4px; font-size: 11px; }
.tag-pass { background: #d1fae5; color: #065f46; }
.tag-warn { background: #fef3c7; color: #92400e; }
.tag-block { background: #fee2e2; color: #991b1b; }
.tag-skip { background: #f3f4f6; color: #4b5563; }
.preview { color: #111827; margin-bottom: 6px; }
.badges { display: flex; gap: 6px; flex-wrap: wrap; }
.badge {
  background: #f3f4f6;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  color: #4b5563;
}
.badge.cost { background: #ecfdf5; color: #065f46; }
.badge.status[data-ok="true"] { background: #d1fae5; color: #065f46; }
.badge.status[data-ok="false"] { background: #fee2e2; color: #991b1b; }
</style>
