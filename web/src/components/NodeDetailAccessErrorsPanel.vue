<script setup lang="ts">
/** 明细 tab：失败滑动窗口 + 失败路由决策，可点开原始请求详情。 */
defineProps<{
  failedWindowEntries: Array<{ rid: string; ts: number; ok: boolean; lat: number; err?: string }>
  failedDecisions: Array<{
    request_id: string
    ts: string
    latency_ms: number | null
    error_class: string | null
  }>
  fmtTime: (value: string | number | null | undefined) => string
}>()

const emit = defineEmits<{
  open: [requestId: string]
}>()
</script>

<template>
  <section class="nd-section">
    <h3>最近访问与异常 <small>失败调用 + 路由错误 · 点击打开原始请求详情</small></h3>
    <p v-if="!failedWindowEntries.length && !failedDecisions.length" class="nd-muted">
      近窗无失败样本；路由异常见「最近路由请求」tab。
    </p>
    <div v-else class="nd-table-wrap">
      <table class="nd-table">
        <thead>
          <tr><th>来源</th><th>时间</th><th>请求</th><th>延迟/错误</th></tr>
        </thead>
        <tbody>
          <tr v-for="entry in failedWindowEntries" :key="`w-${entry.rid}-${entry.ts}`">
            <td>滑动窗口</td>
            <td>{{ fmtTime(entry.ts) }}</td>
            <td>
              <button v-if="entry.rid" type="button" class="nd-link" @click="emit('open', entry.rid)">{{ entry.rid.slice(0, 8) }}</button>
              <span v-else>—</span>
            </td>
            <td class="is-bad">{{ entry.lat }}ms · {{ entry.err || '失败' }}</td>
          </tr>
          <tr v-for="decision in failedDecisions" :key="`d-${decision.request_id}`">
            <td>路由决策</td>
            <td>{{ fmtTime(decision.ts) }}</td>
            <td>
              <button type="button" class="nd-link" @click="emit('open', decision.request_id)">{{ decision.request_id.slice(0, 8) }}</button>
            </td>
            <td class="is-bad">{{ decision.latency_ms == null ? '—' : `${decision.latency_ms}ms` }} · {{ decision.error_class || '失败' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.nd-section { border: 1px solid var(--kx-border); border-radius: 8px; padding: 14px; margin-bottom: 12px; }
.nd-section h3 { margin: 0 0 12px; font-size: 14px; }
.nd-muted, small { color: var(--kx-muted); font-size: 12px; }
.nd-table-wrap { overflow: auto; }
.nd-table { border-collapse: collapse; width: 100%; font-size: 11px; }
.nd-table th, .nd-table td { text-align: left; padding: 6px; border-bottom: 1px solid var(--kx-border); white-space: nowrap; }
.nd-link { border: 0; background: transparent; color: var(--kx-primary); cursor: pointer; padding: 0; font: inherit; text-decoration: underline; }
.is-bad { color: var(--kx-danger); }
</style>
