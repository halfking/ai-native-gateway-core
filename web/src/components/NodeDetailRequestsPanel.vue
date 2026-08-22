<script setup lang="ts">
import type { CredentialRoutingDecision } from '../api/credential-monitor'
import { fmtTime } from '../utils/nodeDetailFormat'

defineProps<{
  requestsLoading: boolean
  requestsLoaded: boolean
  decisions: CredentialRoutingDecision[]
}>()

const emit = defineEmits<{
  openRequest: [requestId: string]
}>()
</script>

<template>
  <div v-if="requestsLoading && !requestsLoaded" class="nd-seg-loading" role="status">最近路由请求加载中…</div>
  <section v-else class="nd-section">
    <h3>最近路由请求 <small v-if="decisions.length">({{ decisions.length }})</small></h3>
    <p v-if="!decisions.length" class="nd-muted">暂无路由请求记录。</p>
    <div v-else class="nd-table-wrap">
      <table class="nd-table">
        <thead>
          <tr><th>时间</th><th>请求</th><th>模型</th><th>结果</th><th>延迟</th><th>错误</th></tr>
        </thead>
        <tbody>
          <tr v-for="decision in decisions" :key="decision.request_id">
            <td>{{ fmtTime(decision.ts) }}</td>
            <td>
              <button type="button" class="nd-link" @click="emit('openRequest', decision.request_id)">
                {{ decision.request_id.slice(0, 8) }}
              </button>
            </td>
            <td>{{ decision.client_model || decision.model }}</td>
            <td :class="decision.success ? 'is-ok' : 'is-bad'">{{ decision.success ? '成功' : '失败' }}</td>
            <td>{{ decision.latency_ms == null ? '—' : `${decision.latency_ms}ms` }}</td>
            <td>{{ decision.error_class || '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
