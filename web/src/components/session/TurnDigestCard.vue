<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TurnDigest, TurnDigestEvent } from '../../api/sessions_v2'

const props = defineProps<{
  digest?: TurnDigest | null
  turnIndex?: number
  loading?: boolean
  summary?: string
  title?: string
}>()

const { t } = useI18n()

const fallbackText = computed(() => {
  const summary = props.summary?.trim()
  if (summary) return summary
  const title = props.title?.trim()
  return title || t('turnDigest.empty')
})

const status = computed(() => {
  const events = props.digest?.events ?? []
  if (events.some((event) => event.type === 'error')) return { type: 'danger', text: t('turnDigest.statusError') }
  if (events.some((event) => event.type === 'warning')) return { type: 'warning', text: t('turnDigest.statusWarning') }
  return props.digest ? { type: 'success', text: t('turnDigest.statusSuccess') } : null
})

function formatTokens(value?: number) {
  if (value == null) return '-'
  return value < 1000 ? String(value) : `${(value / 1000).toFixed(1)}K`
}
function formatCost(value?: number) { return value == null ? '-' : `$${value.toFixed(4)}` }
function formatLatency(value?: number) {
  if (value == null) return '-'
  if (value < 1000) return `${value}ms`
  if (value < 60000) return `${(value / 1000).toFixed(2)}s`
  return `${Math.floor(value / 60000)}m ${((value % 60000) / 1000).toFixed(0)}s`
}
function formatRate(value?: number) { return value == null ? '-' : `${(value * 100).toFixed(0)}%` }
function eventType(event: TurnDigestEvent): 'danger' | 'warning' | 'info' {
  return event.type === 'error' ? 'danger' : event.type === 'warning' ? 'warning' : 'info'
}
</script>

<template>
  <div class="tdc" data-testid="turn-digest-card">
    <div class="tdc-meta">
      <span v-if="turnIndex !== undefined" class="tdc-turn-no">#{{ turnIndex }}</span>
      <el-tag v-if="status" :type="status.type" size="small">{{ status.text }}</el-tag>
      <template v-if="digest">
        <span class="tdc-stat" data-testid="tdc-tokens"><strong>{{ t('turnDigest.metrics.tokens') }}:</strong> {{ formatTokens(digest.metrics.tokens_used) }}</span>
        <span class="tdc-stat" data-testid="tdc-cost"><strong>{{ t('turnDigest.metrics.cost') }}:</strong> {{ formatCost(digest.metrics.cost) }}</span>
        <span class="tdc-stat" data-testid="tdc-latency"><strong>{{ t('turnDigest.metrics.latency') }}:</strong> {{ formatLatency(digest.metrics.latency_ms) }}</span>
        <span v-if="digest.metrics.cache_hit_rate != null" class="tdc-stat" data-testid="tdc-cache"><strong>{{ t('turnDigest.metrics.cacheHitRate') }}:</strong> {{ formatRate(digest.metrics.cache_hit_rate) }}</span>
        <span v-if="digest.metrics.compression_rate != null" class="tdc-stat" data-testid="tdc-compression"><strong>{{ t('turnDigest.metrics.compressionRate') }}:</strong> {{ formatRate(digest.metrics.compression_rate) }}</span>
      </template>
    </div>

    <div v-if="loading && !digest" class="tdc-loading">{{ t('turnDigest.fallback') }}…</div>
    <template v-else-if="!digest">
      <div class="tdc-fallback" data-testid="tdc-fallback">{{ fallbackText }}</div>
    </template>
    <template v-else>
      <section v-if="digest.user_input" class="tdc-section" data-testid="tdc-user-input">
        <div class="tdc-section-label tdc-user">{{ t('turnDigest.userInput') }}</div>
        <div class="tdc-section-body">{{ digest.user_input }}</div>
      </section>
      <section v-if="digest.assistant_output" class="tdc-section" data-testid="tdc-assistant-output">
        <div class="tdc-section-label tdc-assistant">{{ t('turnDigest.assistantOutput') }}</div>
        <div class="tdc-section-body">{{ digest.assistant_output }}</div>
      </section>
      <section v-if="digest.tool_usage?.tools_used?.length" class="tdc-section" data-testid="tdc-tool-usage">
        <div class="tdc-section-label">{{ t('turnDigest.toolUsage') }}</div>
        <div class="tdc-tags">
          <el-tag v-for="tool in digest.tool_usage.tools_used" :key="tool" size="small" effect="plain">{{ tool }}</el-tag>
          <span>{{ t('turnDigest.toolCount', { n: digest.tool_usage.tool_call_count }) }}</span>
        </div>
      </section>
      <section v-if="digest.events?.length" class="tdc-section" data-testid="tdc-events">
        <div class="tdc-section-label">{{ t('turnDigest.events.header') }}</div>
        <ul class="tdc-events"><li v-for="(event, index) in digest.events" :key="index"><el-tag :type="eventType(event)" size="small">{{ event.category }}</el-tag><span>{{ event.message }}</span></li></ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.tdc { background: var(--kx-surface); border: 1px solid var(--kx-border); border-radius: 8px; padding: 12px 16px; color: var(--kx-text); }
.tdc-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-bottom: 10px; font-size: 12px; color: var(--kx-muted); }
.tdc-turn-no { color: var(--kx-primary); font-weight: 600; }
.tdc-stat strong { color: var(--kx-text); }
.tdc-loading, .tdc-fallback { padding: 24px; text-align: center; color: var(--kx-muted); white-space: pre-wrap; word-break: break-word; }
.tdc-section { border-top: 1px dashed var(--kx-border); padding: 10px 0 4px; }
.tdc-section-label { font-size: 12px; font-weight: 600; color: var(--kx-primary); margin-bottom: 6px; }
.tdc-user { color: var(--kx-primary); } .tdc-assistant { color: var(--kx-success); }
.tdc-section-body { font-size: 13px; line-height: 1.6; white-space: pre-wrap; word-break: break-word; }
.tdc-tags { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 12px; color: var(--kx-muted); }
.tdc-events { list-style: none; padding: 0; margin: 0; display: grid; gap: 5px; } .tdc-events li { display: flex; gap: 8px; align-items: flex-start; font-size: 12px; }
</style>
