<script setup lang="ts">
import type { WaterfallRequest } from '../api/dispatch'

defineProps<{
  selected: WaterfallRequest
}>()

const emit = defineEmits<{
  close: []
  'open-session': []
}>()
</script>

<template>
  <aside class="dw-detail">
    <header>
      <h3>请求详情</h3>
      <div class="dw-detail-actions">
        <button
          v-if="selected.session_id"
          class="btn ghost"
          type="button"
          @click="emit('open-session')"
        >打开会话</button>
        <button class="btn ghost" type="button" @click="emit('close')">关闭</button>
      </div>
    </header>
    <dl>
      <div><dt>request_id</dt><dd><code>{{ selected.request_id }}</code></dd></div>
      <div><dt>session_id</dt><dd><code>{{ selected.session_id || '—' }}</code></dd></div>
      <div><dt>model</dt><dd>{{ selected.model || '—' }}</dd></div>
      <div><dt>credential</dt><dd>{{ selected.credential_id ?? '—' }}</dd></div>
      <div><dt>result</dt><dd>{{ selected.result }}</dd></div>
      <div><dt>arrived_at</dt><dd>{{ selected.arrived_at || '—' }}</dd></div>
      <div><dt>queue wait</dt><dd>{{ selected.queue_wait_ms }} ms</dd></div>
      <div><dt>total queue</dt><dd>{{ selected.waiting_in_total_ms }} ms</dd></div>
      <div><dt>model queue</dt><dd>{{ selected.waiting_in_model_ms }} ms</dd></div>
      <div><dt>cred queue</dt><dd>{{ selected.waiting_in_node_ms }} ms</dd></div>
      <div><dt>routing</dt><dd>{{ selected.routing_ms }} ms</dd></div>
      <div><dt>upstream TTFB</dt><dd>{{ selected.upstream_latency_ms }} ms</dd></div>
      <div><dt>streaming</dt><dd>{{ selected.streaming_duration_ms }} ms</dd></div>
      <div><dt>total</dt><dd>{{ selected.total_ms }} ms</dd></div>
    </dl>
    <div v-if="selected.attempts?.length" class="dw-attempts">
      <h4>Attempts ({{ selected.attempts.length }})</h4>
      <ul>
        <li v-for="a in selected.attempts" :key="a.attempt_id || a.attempt_no">
          <span>#{{ a.attempt_no }}</span>
          <span>{{ a.model || '—' }}</span>
          <span>cred {{ a.credential_id }}</span>
          <span>{{ a.outcome || '—' }}</span>
          <span v-if="a.error_kind" class="err">{{ a.error_kind }}</span>
        </li>
      </ul>
    </div>
  </aside>
</template>

<style scoped>
.btn {
  border: 1px solid var(--kx-primary);
  background: var(--kx-primary);
  color: #fff;
  border-radius: 6px;
  padding: 6px 12px;
  cursor: pointer;
  font-size: 13px;
}
.btn.ghost {
  background: transparent;
  color: var(--kx-primary);
}
.dw-detail {
  margin-top: 14px;
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 12px 14px;
}
.dw-detail header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 10px;
}
.dw-detail-actions { display: flex; gap: 8px; }
.dw-detail h3 { margin: 0; font-size: 14px; }
.dw-detail dl {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 8px 16px;
  margin: 0;
}
.dw-detail dt {
  font-size: 11px;
  color: var(--kx-muted);
}
.dw-detail dd {
  margin: 2px 0 0;
  font-size: 13px;
}
.dw-detail code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.dw-attempts {
  margin-top: 14px;
  border-top: 1px dashed var(--kx-border);
  padding-top: 10px;
}
.dw-attempts h4 {
  margin: 0 0 8px;
  font-size: 13px;
}
.dw-attempts ul {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: 4px;
}
.dw-attempts li {
  display: grid;
  grid-template-columns: 40px 1fr 90px 100px auto;
  gap: 10px;
  font-size: 12px;
  padding: 4px 6px;
  border-radius: 4px;
  background: var(--kx-bg);
}
.dw-attempts .err { color: var(--kx-danger); }
</style>
