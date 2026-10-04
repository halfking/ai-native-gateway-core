<script setup lang="ts">
/**
 * Unified model identity chip: client → canonical → outbound/raw.
 * Used in request logs, node detail, and model tables.
 *
 * 2026-10-04：三个身份标签改为 i18n（原先是模板里的硬编码中文，
 * 英文/日文界面会直接露出「客户端 / 标准 / 出站」）。
 * 词条是这三个术语的**唯一真源** —— LogsTab 的表头也从这里取，
 * 两处各写一份必然漂移（表头写「标准」而 chip 里写「Standard」）。
 */
import { useI18n } from 'vue-i18n'

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

const { t } = useI18n()
const mi = (k: string): string => t(`models.modelIdentity.${k}` as never) as string
</script>

<template>
  <div class="mic" :class="{ 'mic--compact': compact }">
    <span v-if="clientModel" class="mic-part" :title="mi('titleClient')">
      <span class="mic-label">{{ mi('client') }}</span>
      <code>{{ clientModel }}</code>
    </span>
    <span v-if="canonicalName" class="mic-part mic-part--link" :title="mi('titleCanonical')" @click.stop="emit('clickCanonical')">
      <span class="mic-label">{{ mi('canonical') }}</span>
      <code>{{ canonicalName }}</code>
    </span>
    <span
      v-if="outboundModel || rawModel"
      class="mic-part"
      :class="{ 'mic-part--link': !!outboundModel || !!rawModel }"
      :title="mi('titleOutbound')"
      @click.stop="emit('clickOutbound')"
    >
      <span class="mic-label">{{ mi('outbound') }}</span>
      <code>{{ outboundModel || rawModel }}</code>
      <span
        v-if="outboundModel && rawModel && outboundModel !== rawModel"
        class="mic-raw"
        :title="mi('titleRaw')"
      >≠raw</span>
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
.mic-raw { color: var(--warning, var(--warning-dark)); font-size: 10px; }
.mic-empty { color: var(--muted); }
</style>
