<script setup lang="ts">
// ManualInline.vue — renders the inline spans produced by lib/markdown.ts.
//
// The manual content is parsed into a structured tree (never raw HTML), so
// this component only has to switch on the span kind. Links are restricted
// to http(s) and site-relative targets: anything else degrades to plain text,
// so a future content edit can never introduce a javascript: URL.

import { computed } from 'vue'
import type { InlineSegment } from '../../lib/markdown'

const props = defineProps<{
  segments: InlineSegment[]
}>()

function isSafeHref(href: string): boolean {
  return href.startsWith('http://') || href.startsWith('https://') || href.startsWith('/')
}

const spans = computed(() =>
  props.segments.map((segment) => ({
    ...segment,
    safe: segment.kind === 'link' ? isSafeHref(segment.href) : false,
  })),
)
</script>

<template>
  <template v-for="(span, index) in spans" :key="index">
    <code v-if="span.kind === 'code'" class="manual-inline-code">{{ span.text }}</code>
    <strong v-else-if="span.kind === 'bold'">{{ span.text }}</strong>
    <em v-else-if="span.kind === 'italic'">{{ span.text }}</em>
    <a
      v-else-if="span.kind === 'link' && span.safe"
      :href="span.href"
      target="_blank"
      rel="noopener noreferrer"
      class="manual-inline-link"
    >{{ span.text }}</a>
    <template v-else>{{ span.text }}</template>
  </template>
</template>

<style scoped>
.manual-inline-code {
  padding: 1px 4px;
  border-radius: 4px;
  background: var(--surface-soft);
  border: 1px solid var(--border-muted);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text-primary);
}

.manual-inline-link {
  color: var(--accent);
  text-decoration: underline;
  text-underline-offset: 2px;
}
</style>
