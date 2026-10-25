<script setup lang="ts">
import { useI18n } from 'vue-i18n'
/** 明细 tab：失败滑动窗口 + 失败路由决策，可点开原始请求详情。 */

const { t } = useI18n()
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
    <h3>最近访问与异常 <small>{{ $t('ops.overview.accessErrorsHint') }}</small></h3>
    <p v-if="!failedWindowEntries.length && !failedDecisions.length" class="nd-muted">
      近窗无失败样本；路由异常见「最近路由请求」tab。
    </p>
    <div v-else class="nd-table-wrap">
      <table class="nd-table">
        <thead>
          <tr><th>{{ $t('common.terms.source') }}</th><th>{{ $t('common.time') }}</th><th>{{ $t('common.terms.request') }}</th><th>{{ $t('common.terms.latencyOrError') }}</th></tr>
        </thead>
        <tbody>
          <tr v-for="entry in failedWindowEntries" :key="`w-${entry.rid}-${entry.ts}`">
            <td>{{ $t('common.terms.slidingWindow') }}</td>
            <td>{{ fmtTime(entry.ts) }}</td>
            <td>
              <button v-if="entry.rid" type="button" class="nd-link" @click="emit('open', entry.rid)">{{ entry.rid.slice(0, 8) }}</button>
              <span v-else>—</span>
            </td>
            <td class="is-bad">{{ entry.lat }}ms · {{ entry.err || $t('common.status.failed') }}</td>
          </tr>
          <tr v-for="decision in failedDecisions" :key="`d-${decision.request_id}`">
            <td>{{ $t('common.terms.routingDecision') }}</td>
            <td>{{ fmtTime(decision.ts) }}</td>
            <td>
              <button type="button" class="nd-link" @click="emit('open', decision.request_id)">{{ decision.request_id.slice(0, 8) }}</button>
            </td>
            <td class="is-bad">{{ decision.latency_ms == null ? '—' : `${decision.latency_ms}ms` }} · {{ decision.error_class || $t('common.status.failed') }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
