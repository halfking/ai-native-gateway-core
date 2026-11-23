<script setup lang="ts">
import { computed } from 'vue'
import AppSheet from '@/components/common/AppSheet.vue'
import { t, locale } from '@/i18n'

const props = defineProps<{ modelValue: boolean; contentId: string; contextKey: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

// Consume the desktop help source and shared standards directly. Vite bundles these
// markdown files at build time; no duplicated glossary copy or runtime request.
const helpFiles = import.meta.glob<string>(
  '../../../../web/content/page-help/{usage-cost,usage-enhanced,usage-trends,reconciliation,request-logs,session-detail,session-analytics-overview,request-detail}.{zh-CN,en-US}.md', {
  eager: true, query: '?raw', import: 'default',
})
const standardsFiles = import.meta.glob<string>('../../../../web/content/statistics-standards/{zh-CN,en-US}.md', {
  eager: true, query: '?raw', import: 'default',
})
const rawHelp = computed(() => {
  const lang = locale.value
  return helpFiles[`../../../../web/content/page-help/${props.contentId}.${lang}.md`]
    ?? helpFiles[`../../../../web/content/page-help/${props.contentId}.zh-CN.md`]
    ?? helpFiles[`../../../../web/content/page-help/${props.contentId}.en-US.md`]
    ?? ''
})
const rawStandards = computed(() => standardsFiles[`../../../../web/content/statistics-standards/${locale.value}.md`]
  ?? standardsFiles['../../../../web/content/statistics-standards/zh-CN.md'] ?? '')
type HelpBlock = { kind: 'heading' | 'paragraph' | 'list' | 'table'; text: string }
function plainInline(value: string): string {
  return value.replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/\*([^*]+)\*/g, '$1').replace(/`([^`]+)`/g, '$1')
}
function parseHelpText(source: string): HelpBlock[] {
  const blocks: HelpBlock[] = []
  for (const rawLine of source.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || /^\|\s*:?-{2,}/.test(line)) continue
    const heading = /^(#{1,3})\s+(.+)$/.exec(line)
    if (heading) blocks.push({ kind: 'heading', text: plainInline(heading[2] ?? '') })
    else if (line.startsWith('|') && line.endsWith('|')) {
      blocks.push({ kind: 'table', text: line.slice(1, -1).split('|').map(cell => plainInline(cell.trim())).join(' · ') })
    } else if (/^([-*+]\s+|\d+\.\s+)/.test(line)) {
      blocks.push({ kind: 'list', text: plainInline(line.replace(/^([-*+]\s+|\d+\.\s+)/, '')) })
    } else if (line.startsWith('> ')) blocks.push({ kind: 'paragraph', text: plainInline(line.slice(2)) })
    else blocks.push({ kind: 'paragraph', text: plainInline(line) })
  }
  return blocks
}
const blocks = computed(() => [...parseHelpText(rawHelp.value), ...parseHelpText(rawStandards.value)])
const pageTitle = computed(() => t(`${props.contextKey}.title`))
</script>

<template>
  <AppSheet :model-value="modelValue" :title="pageTitle" @update:model-value="emit('update:modelValue', $event)">
    <article class="stats-help" :aria-label="pageTitle">
      <p class="stats-help__context">{{ t(`${contextKey}.context`) }}</p>
      <template v-for="(block, index) in blocks" :key="`${block.kind}-${index}`">
        <h3 v-if="block.kind === 'heading'">{{ block.text }}</h3>
        <p v-else-if="block.kind === 'paragraph'">{{ block.text }}</p>
        <ul v-else-if="block.kind === 'list'"><li>{{ block.text }}</li></ul>
        <p v-else class="stats-help__table-row">{{ block.text }}</p>
      </template>
      <h2>{{ t('statisticsHelp.contractTitle') }}</h2>
      <p>{{ t('statisticsHelp.contract') }}</p>
    </article>
  </AppSheet>
</template>

<style scoped>
.stats-help { padding: 0 var(--app-space-3) var(--app-space-5); color: var(--app-text); line-height: 1.55; }
.stats-help__context { padding: var(--app-space-3); border-left: 3px solid var(--app-primary); background: var(--app-surface-muted); }
.stats-help h2 { margin: var(--app-space-4) 0 var(--app-space-2); font-size: 1.05rem; }
.stats-help h3 { margin: var(--app-space-4) 0 var(--app-space-2); font-size: .95rem; }
.stats-help p, .stats-help li { margin: var(--app-space-2) 0; }
.stats-help__table-row { padding: var(--app-space-2); border-bottom: 1px solid var(--app-border); }
.stats-help code { overflow-wrap: anywhere; }
</style>
