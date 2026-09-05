<script setup lang="ts">
// RequestLogDrawer.vue — compatibility shell (2026-08-25).
// Delegates to UnifiedRequestSessionDrawer while preserving the public
// props/emits used by Dashboard / RequestLogs / NodeDetailDrawer.
import UnifiedRequestSessionDrawer from './detail/UnifiedRequestSessionDrawer.vue'

withDefaults(defineProps<{
  requestId: string | null
  mode?: 'default' | 'request-logs'
  initialTraceOpen?: boolean
  stackLevel?: 'default' | 'nested'
  initialViewMode?: 'request' | 'session-turns'
}>(), {
  mode: 'default',
  initialTraceOpen: false,
  stackLevel: 'default',
  initialViewMode: 'request',
})

defineEmits<{
  close: []
  generateSessionSummary: [sessionId: string]
  filterSession: [sessionId: string]
  openRequest: [requestId: string]
  sessionTitleChanged: [{ taskId: string; sessionId: string | null; title: string | null }]
}>()
</script>

<template>
  <UnifiedRequestSessionDrawer
    :request-id="requestId"
    :mode="mode"
    :initial-trace-open="initialTraceOpen"
    :stack-level="stackLevel"
    :initial-view-mode="initialViewMode"
    @close="$emit('close')"
    @filter-session="$emit('filterSession', $event)"
    @open-request="$emit('openRequest', $event)"
    @session-title-changed="$emit('sessionTitleChanged', $event)"
  />
</template>
