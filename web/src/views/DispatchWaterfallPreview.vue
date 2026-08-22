<script setup lang="ts">
/**
 * DEV-only visual of QueueWaterfallTimeline with fixture rows (no API).
 */
import QueueWaterfallTimeline from '../components/QueueWaterfallTimeline.vue'
import type { WaterfallRequest } from '../api/dispatch'

const T0 = '2026-08-21T10:00:00.000Z'

function req(id: string, model: string, offsets: number[], total: number): WaterfallRequest {
  const t = (ms: number) => new Date(Date.parse(T0) + ms).toISOString()
  const [a, b, c, d, e, f, g, h, i] = offsets
  return {
    request_id: id,
    result: 'success',
    model,
    arrived_at: T0,
    total_enqueued_at: t(a),
    total_dequeued_at: t(b),
    model_enqueued_at: t(c),
    model_dequeued_at: t(d),
    cred_enqueued_at: t(e),
    cred_dequeued_at: t(f),
    forward_start_at: t(g),
    response_start_at: t(h),
    response_end_at: t(i),
    waiting_in_total_ms: b - a,
    waiting_in_model_ms: d - c,
    waiting_in_node_ms: f - e,
    routing_ms: e - b,
    acquire_ms: g - f,
    upstream_latency_ms: h - g,
    streaming_duration_ms: i - h,
    queue_wait_ms: f,
    total_ms: total,
  }
}

const requests: WaterfallRequest[] = [
  req('req-long-0001', 'glm-4.7', [8, 40, 45, 145, 150, 170, 175, 255, 400], 400),
  req('req-mid-0002', 'gpt-4', [5, 20, 22, 70, 72, 80, 82, 120, 200], 200),
]
</script>

<template>
  <div class="preview">
    <h1>队列瀑布图 · 本地预览</h1>
    <p class="sub">相对 T0 · 图例在表上方 · fixture 两条请求</p>
    <QueueWaterfallTimeline :requests="requests" :wired="true" source="memory" />
    <h2>空态</h2>
    <QueueWaterfallTimeline :requests="[]" :wired="false" source="none" />
  </div>
</template>

<style scoped>
.preview { padding: 16px 20px; color: var(--kx-text); background: var(--kx-bg); min-height: 100%; }
h1 { font-size: 20px; margin: 0; }
h2 { font-size: 14px; margin: 20px 0 8px; }
.sub { margin: 4px 0 16px; color: var(--kx-muted); font-size: 13px; }
</style>
