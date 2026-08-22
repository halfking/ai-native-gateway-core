<script setup lang="ts">
/**
 * Unified model identity chip: client → canonical → outbound/raw.
 * Used in request logs, node detail, and model tables.
 */
defineProps<{
  clientModel?: string | null
  canonicalName?: string | null
  outboundModel?: string | null
  rawModel?: string | null
  compact?: boolean
}>()

const emit = defineEmits<{
  clickCanonical: []
  clickOutbound: []
}>()
</script>

<template>
  <div class="mic" :class="{ 'mic--compact': compact }">
    <span v-if="clientModel" class="mic-part" title="客户端请求名">
      <span class="mic-label">客户端</span>
      <code>{{ clientModel }}</code>
    </span>
    <span v-if="canonicalName" class="mic-part mic-part--link" title="标准名" @click.stop="emit('clickCanonical')">
      <span class="mic-label">标准</span>
      <code>{{ canonicalName }}</code>
    </span>
    <span
      v-if="outboundModel || rawModel"
      class="mic-part"
      :class="{ 'mic-part--link': !!outboundModel || !!rawModel }"
      title="出站 / 上游原名"
      @click.stop="emit('clickOutbound')"
    >
      <span class="mic-label">出站</span>
      <code>{{ outboundModel || rawModel }}</code>
      <span v-if="outboundModel && rawModel && outboundModel !== rawModel" class="mic-raw" :title="'原名: ' + rawModel">≠raw</span>
    </span>
    <span v-if="!clientModel && !canonicalName && !outboundModel && !rawModel" class="mic-empty">—</span>
  </div>
</template>

<style scoped>
.mic { display: flex; flex-wrap: wrap; gap: 6px 10px; align-items: baseline; font-size: 12px; }
.mic--compact { gap: 4px 8px; font-size: 11px; }
.mic-part { display: inline-flex; gap: 4px; align-items: baseline; }
.mic-part--link { cursor: pointer; }
.mic-part--link:hover code { color: var(--accent); }
.mic-label { color: var(--muted); font-size: 10px; text-transform: uppercase; letter-spacing: 0.02em; }
.mic code { font-size: inherit; }
.mic-raw { color: var(--warning, #c97800); font-size: 10px; }
.mic-empty { color: var(--muted); }
</style>
