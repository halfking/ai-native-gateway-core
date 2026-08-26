<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  extractAssistantReply,
  extractMessagesFromBody,
  filterMessages,
  formatJson,
  previewText,
  roleColor,
  type RoleFilter,
} from './messageHelpers'
import { extractMultimodalFromBody } from './multimodalHelpers'
import { splitSensitivePlaceholders, truncateForPreview } from '../../utils/sensitivePlaceholders'

const props = defineProps<{
  body: unknown
  /** When set, show a dedicated assistant reply block below request messages. */
  responseBody?: unknown
  emptyHint?: string
  /** When set, lock the role filter (used by session SyncPane facets). */
  lockedRole?: RoleFilter
}>()

const roleFilter = ref<RoleFilter>('all')
const expanded = ref<Set<number>>(new Set())
const replyExpanded = ref(false)

watch(
  () => props.lockedRole,
  (r) => {
    if (r) roleFilter.value = r
  },
  { immediate: true },
)

const effectiveRole = computed(() => props.lockedRole || roleFilter.value)

const messages = computed(() =>
  filterMessages(extractMessagesFromBody(props.body), effectiveRole.value),
)

const allMedia = computed(() => extractMultimodalFromBody(props.body))

const replyText = computed(() =>
  props.responseBody == null ? '' : extractAssistantReply(props.responseBody),
)

const replyPreview = computed(() => previewText(replyText.value || '(无回复)'))

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

function contentSegments(msg: Record<string, unknown>) {
  const raw = formatJson(contentOf(msg))
  const { text } = truncateForPreview(raw)
  return splitSensitivePlaceholders(text)
}

function replySegments() {
  const { text } = truncateForPreview(replyText.value || '')
  return splitSensitivePlaceholders(text)
}

function roleTone(role: unknown): string {
  switch (String(role || '')) {
    case 'user': return 'msg-block--user'
    case 'assistant': return 'msg-block--assistant'
    case 'system': return 'msg-block--system'
    case 'tool': return 'msg-block--tool'
    default: return ''
  }
}
</script>

<template>
  <div class="conv-panel">
    <div v-if="!lockedRole" class="conv-filters">
      <button
        v-for="f in (['all', 'system', 'user', 'tool', 'assistant'] as RoleFilter[])"
        :key="f"
        type="button"
        class="btn btn-sm"
        :class="{ 'btn-primary': effectiveRole === f }"
        @click="roleFilter = f"
      >
        {{ f === 'all' ? '全部' : f }}
      </button>
      <span v-if="allMedia.length" class="media-hint">含媒体 {{ allMedia.length }}</span>
    </div>
    <div v-else-if="allMedia.length" class="conv-filters">
      <span class="media-hint">含媒体 {{ allMedia.length }}</span>
    </div>
    <template v-if="messages.length">
      <div v-for="(msg, i) in messages" :key="i" class="msg-block" :class="roleTone(msg.role)">
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
        <pre class="msg-pre"><template v-if="expanded.has(i)"><template v-for="(seg, si) in contentSegments(msg)" :key="si"><span v-if="seg.kind === 'ph'" class="ph-badge">{{ seg.value }}</span><template v-else>{{ seg.value }}</template></template></template><template v-else>{{ previewText(contentOf(msg)).text }}</template></pre>
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

    <section v-if="responseBody !== undefined" class="reply-block msg-block--assistant" data-testid="chat-reply">
      <div class="msg-role" :style="{ color: roleColor('assistant') }">[assistant 回复]</div>
      <pre class="msg-pre">{{ replyExpanded ? (replyText || '(无回复)') : replyPreview.text }}</pre>
      <button
        v-if="replyPreview.truncated || replyExpanded"
        type="button"
        class="btn btn-sm linkish"
        @click="replyExpanded = !replyExpanded"
      >{{ replyExpanded ? '收起' : '展开' }}</button>
    </section>
  </div>
</template>

<style scoped>
.conv-filters { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 10px; align-items: center; }
.media-hint { font-size: 11px; color: var(--muted); margin-left: 4px; }
.msg-block {
  margin-bottom: 12px;
  padding: 8px 10px;
  border-radius: 6px;
  border-left: 3px solid var(--border);
  background: var(--bg-subtle, var(--surface-secondary));
}
.msg-block--user { border-left-color: var(--kx-primary); background: color-mix(in srgb, var(--kx-primary) 7%, transparent); }
.msg-block--assistant { border-left-color: var(--kx-success); background: color-mix(in srgb, var(--kx-success) 7%, transparent); }
.msg-block--system { border-left-color: var(--kx-warning); background: color-mix(in srgb, var(--kx-warning) 8%, transparent); }
.msg-block--tool { border-left-color: var(--muted); }
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
  background: transparent; padding: 4px 0; border-radius: 4px;
}
.tool-label { font-size: 11px; color: var(--muted); margin: 6px 0 2px; }
.linkish { margin-top: 4px; }
.reply-block {
  margin-top: 16px; padding: 8px 10px; border-radius: 6px;
  border-left: 3px solid var(--kx-success);
  background: color-mix(in srgb, var(--kx-success) 7%, transparent);
}
.text-muted { color: var(--muted); font-size: 13px; }
.ph-badge {
  display: inline;
  padding: 0 4px;
  margin: 0 1px;
  border-radius: 4px;
  background: color-mix(in srgb, var(--kx-warning) 18%, transparent);
  color: var(--kx-warning);
  font-weight: 600;
}
</style>
