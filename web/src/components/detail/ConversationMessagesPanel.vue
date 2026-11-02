<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
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
  /**
   * ★ 2026-11-02：正文可用性（后端 meta.body_status）。
   *   'unavailable' ⇒ 正文**没落库**（不是「这条请求没消息」）⇒
   *   空态必须说真话，不能显示「无消息」。
   */
  bodyStatus?: 'available' | 'unavailable'
  /**
   * ★ 该租户的会话正文录制同意状态。false = 正文压根没落库。
   */
  recordingConsent?: boolean
}>()

const roleFilter = ref<RoleFilter>('all')
const expanded = ref<Set<number>>(new Set())
const replyExpanded = ref(false)

const { t } = useI18n()

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

// ★ 2026-11-02：正文**没落库**（body_status=unavailable）与「这条请求没有消息」
//   是两件不同的事 ⇒ 空态必须分开说，否则把「未落库」读成「无对话」＝错的量具。
const bodyUnavailable = computed(() => props.bodyStatus === 'unavailable')

const replyPreview = computed(() => previewText(replyText.value || t('requestDetail.conversation.noReply')))

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
        {{ f === 'all' ? t('requestDetail.conversation.allFilter') : f }}
      </button>
      <span v-if="allMedia.length" class="media-hint">{{ t('requestDetail.conversation.mediaHint', { count: allMedia.length }) }}</span>
    </div>
    <div v-else-if="allMedia.length" class="conv-filters">
      <span class="media-hint">{{ t('requestDetail.conversation.mediaHint', { count: allMedia.length }) }}</span>
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
        <pre class="msg-pre" :class="{ 'msg-pre--expanded': expanded.has(i) }"><template v-if="expanded.has(i)"><template v-for="(seg, si) in contentSegments(msg)" :key="si"><span v-if="seg.kind === 'ph'" class="ph-badge">{{ seg.value }}</span><template v-else>{{ seg.value }}</template></template></template><template v-else>{{ previewText(contentOf(msg)).text }}</template></pre>
        <button
          v-if="previewText(contentOf(msg)).truncated || expanded.has(i)"
          type="button"
          class="btn btn-sm linkish"
          @click="toggle(i)"
        >
          {{ expanded.has(i) ? t('requestDetail.conversation.collapse') : t('requestDetail.conversation.expand') }}
        </button>
        <div v-if="msg.tool_calls" class="tool-block">
          <div class="tool-label">{{ t('requestDetail.conversation.toolCall') }}</div>
          <pre
            v-for="(tc, j) in (msg.tool_calls as unknown[])"
            :key="j"
            class="tool-pre"
          >{{ formatJson(tc) }}</pre>
        </div>
      </div>
    </template>
    <!-- ★ 2026-11-02 空态说真话：正文没落库 ≠ 这条请求没有对话。
         ★ 此前一律显示「(无消息)」⇒ 把「正文未落库」读成「没有对话」＝错的量具
           （本次报障：overview 有 token/预览，chat 四个页签全空）。 -->
    <div
      v-else-if="bodyUnavailable"
      class="body-status"
      role="status"
      data-testid="conversation-body-unavailable"
    >
      <template v-if="recordingConsent === false">
        <div class="body-status-title">{{ t('requestDetail.bodyStatus.consentOffTitle') }}</div>
        <div class="body-status-body">{{ t('requestDetail.bodyStatus.consentOffBody') }}</div>
      </template>
      <template v-else>
        <div class="body-status-body">{{ t('requestDetail.conversation.bodyUnavailable') }}</div>
      </template>
    </div>
    <div v-else class="text-muted">{{ emptyHint || t('requestDetail.conversation.noMessages') }}</div>

    <!-- 正文没落库时**不**渲染 assistant 回复块：否则会显示「(无回复)」，
         读起来像「模型没回」，而真相是「响应正文压根没采到」。 -->
    <section v-if="responseBody !== undefined && !bodyUnavailable" class="reply-block msg-block--assistant" data-testid="chat-reply">
      <div class="msg-role" :style="{ color: roleColor('assistant') }">{{ t('requestDetail.conversation.assistantReply') }}</div>
      <pre class="msg-pre" :class="{ 'msg-pre--expanded': replyExpanded }">{{ replyExpanded ? (replyText || t('requestDetail.conversation.noReply')) : replyPreview.text }}</pre>
      <button
        v-if="replyPreview.truncated || replyExpanded"
        type="button"
        class="btn btn-sm linkish"
        @click="replyExpanded = !replyExpanded"
      >{{ replyExpanded ? t('requestDetail.conversation.collapse') : t('requestDetail.conversation.expand') }}</button>
    </section>
  </div>
</template>

<style scoped>
.conv-filters { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 8px; align-items: center; }
.media-hint { font-size: 11px; color: var(--muted); margin-left: 4px; }
.msg-block {
  margin-bottom: 12px;
  padding: 8px 8px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg-subtle, var(--surface-secondary));
}
.msg-block--user { border-color: var(--kx-primary); background: color-mix(in srgb, var(--kx-primary) 7%, transparent); }
.msg-block--assistant { border-color: var(--kx-success); background: color-mix(in srgb, var(--kx-success) 7%, transparent); }
.msg-block--system { border-color: var(--kx-warning); background: color-mix(in srgb, var(--kx-warning) 8%, transparent); }
.msg-block--tool { border-color: var(--muted); }
.msg-role { font-size: 12px; font-weight: 600; margin-bottom: 4px; }
.inline-media {
  display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 4px;
}
.inline-media img, .inline-media video {
  max-width: 180px; max-height: 140px; object-fit: contain;
  border-radius: 6px; border: 1px solid var(--border);
}
.inline-media audio { max-width: 100%; }
.msg-pre, .tool-pre {
  margin: 0; white-space: pre-wrap; word-break: break-word;
  font-size: 12px; line-height: 1.45;
  background: transparent; padding: 4px 0; border-radius: 4px;
}
.msg-pre:not(.msg-pre--expanded) {
  max-height: 320px;
  overflow: auto;
}
.msg-pre--expanded {
  max-height: none;
  overflow: visible;
}
.tool-label { font-size: 11px; color: var(--muted); margin: 4px 0 2px; }
.linkish { margin-top: 4px; }
.reply-block {
  margin-top: 16px; padding: 8px 8px; border-radius: 6px;
  border: 1px solid var(--kx-success);
  background: color-mix(in srgb, var(--kx-success) 7%, transparent);
}
.text-muted { color: var(--muted); font-size: 13px; }
.body-status {
  margin-bottom: 12px;
  padding: 8px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg-subtle, var(--surface-secondary));
}
.body-status-title { font-size: 12px; font-weight: 600; margin-bottom: 4px; }
.body-status-body { font-size: 12px; line-height: 1.45; color: var(--muted); }
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
