<script setup lang="ts">
import { computed } from 'vue'

interface SlotDetail {
  index: number
  holder: string
  ttl_seconds: number
  expired: boolean
  session_title?: string
  session_id?: string
}

interface Props {
  details: SlotDetail[]
  slotLimit: number
}

const props = defineProps<Props>()

const grid = computed(() => {
  // Pad to slotLimit so the grid always shows slotLimit cells.
  const slots: (SlotDetail | null)[] = [...props.details]
  while (slots.length < props.slotLimit) {
    slots.push(null)
  }
  return slots
})

const stats = computed(() => {
  const occupied = props.details.filter(d => d.holder && !d.expired).length
  const free = props.slotLimit - occupied
  return { occupied, free, total: props.slotLimit }
})

function slotClass(d: SlotDetail | null) {
  if (!d) return 'fp-cell fp-cell--free'
  if (!d.holder) return 'fp-cell fp-cell--free'
  if (d.expired) return 'fp-cell fp-cell--expired'
  return 'fp-cell fp-cell--occupied'
}

function holderLabel(d: SlotDetail): string {
  return d.session_title || d.holder
}

function ttlLabel(d: SlotDetail | null): string {
  if (!d || !d.holder) return ''
  const secs = d.ttl_seconds
  if (secs <= 0) return '已过期'
  const h = Math.floor(secs / 3600)
  const m = Math.floor((secs % 3600) / 60)
  if (h >= 1) return `${h}h${m}m`
  return `${m}m`
}

function isLongHeld(d: SlotDetail | null): boolean {
  // Highlight slots held for > 12 hours as "long-term"
  if (!d || !d.holder) return false
  return d.ttl_seconds > 12 * 3600
}
</script>

<template>
  <div class="fp-visualizer">
    <div class="fp-header">
      <div class="fp-stat">
        <span class="fp-stat-num">{{ stats.occupied }}</span>
        <span class="fp-stat-label">已占用</span>
      </div>
      <div class="fp-stat">
        <span class="fp-stat-num">{{ stats.free }}</span>
        <span class="fp-stat-label">空闲</span>
      </div>
      <div class="fp-stat">
        <span class="fp-stat-num">{{ stats.total }}</span>
        <span class="fp-stat-label">总槽位</span>
      </div>
    </div>

    <div class="fp-grid">
      <div
        v-for="(d, idx) in grid"
        :key="idx"
        :class="slotClass(d)"
        :class="{ 'fp-cell--long': isLongHeld(d) }"
      >
        <template v-if="d && d.holder">
          <div class="fp-cell-num">#{{ d.index }}</div>
          <div class="fp-cell-icon">●</div>
          <div class="fp-cell-ttl">{{ ttlLabel(d) }}</div>
          <div class="fp-tooltip">
            <div class="fp-tooltip-title">{{ holderLabel(d) }}</div>
            <div class="fp-tooltip-meta">
              <div>槽位 #{{ d.index }} · TTL {{ ttlLabel(d) }}</div>
              <div class="fp-tooltip-session">会话: {{ d.session_id || d.holder }}</div>
              <div v-if="d.session_title" class="fp-tooltip-hint">
                (ID 为内部 session_id，标题是自动生成的会话主题)
              </div>
            </div>
          </div>
        </template>
        <template v-else>
          <div class="fp-cell-num">#{{ idx }}</div>
          <div class="fp-cell-empty">空闲</div>
        </template>
      </div>
    </div>

    <div class="fp-legend">
      <span class="fp-legend-item"><span class="fp-legend-dot fp-legend-dot--occupied"></span>占用中</span>
      <span class="fp-legend-item"><span class="fp-legend-dot fp-legend-dot--long"></span>长期持有 (&gt;12h)</span>
      <span class="fp-legend-item"><span class="fp-legend-dot fp-legend-dot--free"></span>空闲</span>
    </div>
  </div>
</template>

<style scoped>
.fp-visualizer {
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 14px;
}

.fp-header {
  display: flex;
  gap: 18px;
  margin-bottom: 12px;
  padding-bottom: 10px;
  border-bottom: 1px solid var(--border);
}
.fp-stat {
  display: flex;
  flex-direction: column;
  align-items: center;
  flex: 1;
}
.fp-stat-num {
  font-size: 18px;
  font-weight: 600;
  color: var(--text);
  line-height: 1.1;
}
.fp-stat-label {
  font-size: 11px;
  color: var(--muted);
  margin-top: 2px;
}

.fp-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(72px, 1fr));
  gap: 8px;
}

.fp-cell {
  position: relative;
  height: 72px;
  border-radius: 8px;
  border: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 6px;
  cursor: default;
  transition: transform 0.1s, box-shadow 0.1s;
}
.fp-cell:hover {
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0,0,0,0.3);
  z-index: 5;
}

.fp-cell--free {
  background: transparent;
  border-style: dashed;
  color: var(--muted);
}
.fp-cell--free .fp-cell-empty {
  font-size: 11px;
  opacity: 0.6;
}

.fp-cell--occupied {
  background: linear-gradient(135deg, #6366f1 0%, #4f46e5 100%);
  border-color: #4f46e5;
  color: white;
}
.fp-cell--long {
  background: linear-gradient(135deg, #f59e0b 0%, #d97706 100%);
  border-color: #d97706;
  color: white;
}
.fp-cell--expired {
  background: rgba(239, 68, 68, 0.15);
  border-color: var(--danger, #ef4444);
  color: var(--danger, #ef4444);
}

.fp-cell-num {
  font-family: ui-monospace, monospace;
  font-size: 10px;
  opacity: 0.8;
}
.fp-cell-icon {
  font-size: 18px;
  line-height: 1;
  margin: 2px 0;
}
.fp-cell-ttl {
  font-family: ui-monospace, monospace;
  font-size: 10px;
  opacity: 0.85;
}

.fp-tooltip {
  position: absolute;
  bottom: calc(100% + 6px);
  left: 50%;
  transform: translateX(-50%);
  background: #0d1117;
  border: 1px solid #4f46e5;
  border-radius: 6px;
  padding: 8px 10px;
  min-width: 180px;
  max-width: 280px;
  font-size: 12px;
  color: var(--text);
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.15s;
  z-index: 100;
  box-shadow: 0 4px 16px rgba(0,0,0,0.5);
  white-space: normal;
}
.fp-cell:hover .fp-tooltip {
  opacity: 1;
}
.fp-tooltip-title {
  font-weight: 600;
  margin-bottom: 4px;
  word-break: break-word;
}
.fp-tooltip-meta {
  font-size: 11px;
  color: var(--muted);
  font-family: ui-monospace, monospace;
  word-break: break-all;
}
.fp-tooltip-session {
  margin-top: 2px;
}
.fp-tooltip-hint {
  font-family: inherit;
  margin-top: 4px;
  font-style: italic;
  opacity: 0.8;
}

.fp-legend {
  display: flex;
  gap: 14px;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--border);
  font-size: 11px;
  color: var(--muted);
}
.fp-legend-item {
  display: flex;
  align-items: center;
  gap: 4px;
}
.fp-legend-dot {
  width: 8px;
  height: 8px;
  border-radius: 2px;
}
.fp-legend-dot--occupied { background: #4f46e5; }
.fp-legend-dot--long { background: #d97706; }
.fp-legend-dot--free {
  background: transparent;
  border: 1px dashed var(--muted);
}
</style>