<script setup lang="ts">
import type { RequestLogDetail } from '../../api/logs'
import type { UnifiedRequestDetail } from '../../api/requestDetail'
import type { WaterfallAttempt, WaterfallRequest } from '../../api/dispatch'
import type { DetailSection } from '../../composables/useRequestDetailLoader'
import RequestOverviewPanel from './RequestOverviewPanel.vue'
import ConversationMessagesPanel from './ConversationMessagesPanel.vue'
import FlowTimingPanel from './FlowTimingPanel.vue'
import CompressionRedactionPanel from './CompressionRedactionPanel.vue'
import RequestWaterfallPanel from './RequestWaterfallPanel.vue'
import MultimodalAttachmentsPanel from './MultimodalAttachmentsPanel.vue'
import { formatJson } from './messageHelpers'

defineProps<{
  section: DetailSection
  requestId: string
  log: RequestLogDetail | null
  unified: UnifiedRequestDetail | null
  sessionSnap: Record<string, unknown> | null
  sessionId: string | null
  requestBody: unknown
  responseBody: unknown
  outboundBody: unknown
  waterfall: WaterfallRequest | null
  attempts: WaterfallAttempt[]
  waterfallLoading: boolean
  waterfallError: string
  waterfallSource: string
}>()

const emit = defineEmits<{
  (e: 'goto', section: DetailSection): void
}>()
</script>

<template>
  <main class="main">
    <RequestOverviewPanel
      v-if="section === 'overview'"
      :log="log"
      :unified="unified"
      :request-body="requestBody"
      :response-body="responseBody"
      :session-snap="sessionSnap"
      @goto="emit('goto', $event)"
    />
    <ConversationMessagesPanel
      v-else-if="section === 'chat'"
      :body="requestBody"
      :response-body="responseBody"
    />
    <RequestWaterfallPanel
      v-else-if="section === 'waterfall'"
      :selected="waterfall"
      :attempts="attempts"
      :loading="waterfallLoading"
      :error="waterfallError"
      :source="waterfallSource"
    />
    <RequestWaterfallPanel
      v-else-if="section === 'attempts'"
      :selected="null"
      :attempts="attempts"
      :loading="waterfallLoading"
      :error="waterfallError"
    />
    <FlowTimingPanel
      v-else-if="section === 'flow'"
      :request-id="requestId"
      @goto="emit('goto', $event)"
    />
    <CompressionRedactionPanel
      v-else-if="section === 'compress'"
      :session-id="sessionId"
      :request-id="requestId"
      :request-body="requestBody"
      :outbound-body="outboundBody"
      :response-body="responseBody"
    />
    <MultimodalAttachmentsPanel
      v-else-if="section === 'attachments'"
      :request-id="requestId"
      :request-body="requestBody"
      :attachments="log?.attachments"
    />
    <pre v-else class="raw">{{
      formatJson({
        unified,
        log_meta: log,
        waterfall,
        request_body: requestBody,
        outbound_body: outboundBody,
        response_body: responseBody,
      })
    }}</pre>
  </main>
</template>

<style scoped>
.main { padding: 14px; overflow: auto; min-height: 0; }
.raw {
  font-size: 11px; white-space: pre-wrap; word-break: break-word;
  background: var(--bg-subtle); padding: 10px; border-radius: 6px;
}
</style>
