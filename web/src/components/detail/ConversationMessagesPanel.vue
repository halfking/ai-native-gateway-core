<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  extractMessagesFromBody,
  filterMessages,
  formatJson,
  previewText,
  roleColor,
  type RoleFilter,
} from './messageHelpers'
import { extractMultimodalFromBody } from './multimodalHelpers'

const props = defineProps<{
  body: unknown
  emptyHint?: string
}>()

const roleFilter = ref<RoleFilter>('all')
const expanded = ref<Set<number>>(new Set())

const messages = computed(() =>
  filterMessages(extractMessagesFromBody(props.body), roleFilter.value),
)

const allMedia = computed(() => extractMultimodalFromBody(props.body))

function mediaForMessage(i: number) {
  return allMedia.value.filter((m) => m.messageIndex === i)
}

function toggle(i: number) {
  const next = new Set(expanded.value)
  if (next.has(i)) next.delete(i)
  else next.add(i)
  expanded.value = next
}

function contentOf(msg: Record<string, unknown>): unknown {
  return msg.content ?? msg
}
</script>

<template>
  <div class="conv-panel">
    <div class="conv-filters">
      <button
        v-for="f in (['all', 'system', 'user', 'tool', 'assistant'] as RoleFilter[])"
        :key="f"
        type="button"
        class="btn btn-sm"
        :class="{ 'btn-primary': roleFilter === f }"
        @click="roleFilter = f"
      >
        {{ f === 'all' ? '全部' : f }}
      </button>
      <span v-if="allMedia.length" class="media-hint">含媒体 {{ allMedia.length }}</span>
    </div>
    <template v-if="messages.length">
      <div v-for="(msg, i) in messages" :key="i" class="msg-block">
        <div class="msg-role" :style="{ color: roleColor(String(msg.role || '')) }">
          [{{ msg.role || 'unknown' }}]
        </div>
        <div v-if="mediaForMessage(i).length" class="inline-media">
          <template v-for="(m, j) in mediaForMessage(i)" :key="j">
            <img v-if="m.kind === 'image' && m.url" :src="m.url" :alt="m.label || 'image'" loading="lazy" />
            <audio v-else-if="m.kind === 'audio' && m.url" :src="m.url" controls preload="metadata" />
            <video v-else-if="m.kind === 'video' && m.url" :src="m.url" controls preload="metadata" />
            <a v-else-if="m.url" :href="m.url" target="_blank" rel="noopener">{{ m.label || m.kind }}</a>
          </template>
        </div>
        <pre class="msg-pre">{{
          expanded.has(i)
            ? formatJson(contentOf(msg))
            : previewText(contentOf(msg)).text
        }}</pre>
        <button
          v-if="previewText(contentOf(msg)).truncated || expanded.has(i)"
          type="button"
          class="btn btn-sm linkish"
          @click="toggle(i)"
        >
          {{ expanded.has(i) ? '收起' : '展开' }}
        </button>
        <div v-if="msg.tool_calls" class="tool-block">
          <div class="tool-label">工具调用:</div>
          <pre
            v-for="(tc, j) in (msg.tool_calls as unknown[])"
            :key="j"
            class="tool-pre"
          >{{ formatJson(tc) }}</pre>
        </div>
      </div>
    </template>
    <div v-else class="text-muted">{{ emptyHint || '(无消息)' }}</div>
  </div>
</template>

<style scoped>
.conv-filters { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 10px; align-items: center; }
.media-hint { font-size: 11px; color: var(--muted); margin-left: 4px; }
.msg-block { margin-bottom: 12px; }
.msg-role { font-size: 12px; font-weight: 600; margin-bottom: 4px; }
.inline-media {
  display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 6px;
}
.inline-media img, .inline-media video {
  max-width: 180px; max-height: 140px; object-fit: contain;
  border-radius: 6px; border: 1px solid var(--border);
}
.inline-media audio { max-width: 100%; }
.msg-pre, .tool-pre {
  margin: 0; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.45; max-height: 320px; overflow: auto;
  background: var(--bg-subtle, var(--overlay-light)); padding: 8px; border-radius: 4px;
}
.tool-label { font-size: 11px; color: var(--muted); margin: 6px 0 2px; }
.linkish { margin-top: 4px; }
.text-muted { color: var(--muted); font-size: 13px; }
</style>
