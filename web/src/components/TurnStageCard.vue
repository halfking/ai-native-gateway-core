<script setup lang="ts">
// TurnStageCard — one column of the three-stage per-turn view.
// Kept as a separate SFC (rather than a render function) so the template
// is declarative and easy to read.
defineProps<{
  stageLabel: string
  sendLabel: string
  receiveLabel: string
  send: string
  receive: string
  tokens?: number
  saved?: string
  extra?: string
  range?: string
  strategy?: string
  tags?: string[]
  noChangeHint?: string
}>()
</script>

<template>
  <div class="stage-card">
    <div class="stage-head">
      <span class="stage-name">{{ stageLabel }}</span>
      <span v-if="tokens != null" class="stage-meta">
        {{ tokens }} tok<span v-if="saved"> · {{ saved }}</span>
      </span>
    </div>

    <div class="stage-dir">
      <span class="dir-lbl">{{ sendLabel }}</span>
      <div class="dir-body send">{{ send }}</div>
    </div>
    <div class="stage-dir">
      <span class="dir-lbl">{{ receiveLabel }}</span>
      <div class="dir-body receive">{{ receive }}</div>
    </div>

    <div v-if="range" class="stage-extra range">{{ range }}</div>
    <div v-if="strategy" class="stage-extra strat">{{ strategy }}</div>
    <div v-if="extra" class="stage-extra marker">{{ extra }}</div>

    <div v-if="tags && tags.length" class="stage-tags">
      <span
        v-for="tag in tags"
        :key="tag"
        class="tag"
        :class="tag.startsWith('audit:') ? 'tag-audit' : 'tag-pos'"
      >{{ tag }}</span>
    </div>
    <div v-else-if="noChangeHint" class="no-change">{{ noChangeHint }}</div>
  </div>
</template>

<style scoped>
.stage-card {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  padding: 10px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.stage-head { display: flex; justify-content: space-between; align-items: baseline; }
.stage-name { font-size: 12px; font-weight: 600; color: var(--text-primary, #e6edf3); }
.stage-meta { font-size: 11px; color: var(--text-muted, #6e7681); font-variant-numeric: tabular-nums; }
.stage-dir { display: flex; flex-direction: column; gap: 3px; }
.dir-lbl { font-size: 10px; color: var(--text-secondary, #8b949e); text-transform: uppercase; }
.dir-body {
  font-size: 11px; line-height: 1.5; white-space: pre-wrap; word-break: break-word;
  padding: 6px 8px; border-radius: 4px; background: var(--bg-subtle, rgba(0,0,0,.15));
  color: var(--text-primary, #e6edf3);
  max-height: 220px; overflow-y: auto;
}
.stage-extra { font-size: 10px; }
.stage-extra.range { color: var(--accent-h, #818cf8); }
.stage-extra.strat { color: var(--text-muted, #6e7681); }
.stage-extra.marker { color: #f59e0b; }
.stage-tags { display: flex; flex-wrap: wrap; gap: 4px; }
.tag { padding: 1px 6px; border-radius: 4px; font-size: 10px; }
.tag-pos { background: rgba(52,211,153,.12); color: #34d399; }
.tag-audit { background: rgba(99,102,241,.12); color: #818cf8; }
.no-change { font-size: 10px; color: var(--text-muted, #6e7681); font-style: italic; }
</style>
