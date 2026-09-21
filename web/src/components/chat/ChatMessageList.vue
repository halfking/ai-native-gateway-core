<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatTokenCount } from '../../composables/useChatCompletions'
import type { ChatMessage } from '../../composables/useChatSessions'

defineProps<{
  messages: ChatMessage[]
  sending: boolean
  waitingChat: boolean
  hasNoKeys: boolean
  modelDisplayMap: Map<string, string>
  copiedKey: string | null
}>()

const emit = defineEmits<{
  copy: [text: string, key: string]
  resend: [userIdx: number]
}>()

const { t } = useI18n()

function modelLabel(name: string | undefined, map: Map<string, string>) {
  if (!name) return ''
  if (name === 'auto') return t('chat.auto')
  return map.get(name) || name
}
</script>

<template>
  <div class="chat-messages">
    <div v-if="!messages.length" class="chat-empty">
      <p v-if="hasNoKeys">{{ t('chat.session.noKeyHint') }}</p>
      <p v-else>{{ t('chat.session.emptyHint') }}</p>
    </div>
    <div
      v-for="(msg, i) in messages"
      :key="i"
      class="chat-bubble"
      :class="msg.role"
    >
      <div class="bubble-head">
        <span class="bubble-role">
          {{ msg.role === 'user' ? t('chat.roleUser') : t('chat.roleAssistant') }}
          <template v-if="msg.role === 'user' && msg.requestedModel">
            · {{ modelLabel(msg.requestedModel, modelDisplayMap) }}
          </template>
          <template v-if="msg.role === 'assistant' && msg.resolvedModel">
            · {{ modelLabel(msg.resolvedModel, modelDisplayMap) }}
          </template>
          <template v-if="msg.role === 'assistant' && msg.usage">
            · {{ formatTokenCount(msg.usage.totalTokens) }} tok
          </template>
          <template v-if="msg.role === 'assistant' && msg.resumed">
            <span class="resumed-badge" :title="t('chat.session.resumedTitle')">
              {{ t('chat.session.resumedBadge') }}
            </span>
          </template>
        </span>
        <span class="bubble-actions">
          <button
            type="button"
            class="bubble-btn"
            :title="copiedKey === `copy-${i}` ? t('chat.copied') : t('chat.copy')"
            @click="emit('copy', msg.content, `copy-${i}`)"
          >
            {{ copiedKey === `copy-${i}` ? '✓' : t('chat.copy') }}
          </button>
          <button
            v-if="msg.role === 'user'"
            type="button"
            class="bubble-btn"
            :disabled="sending"
            :title="t('chat.session.resendTitle')"
            @click="emit('resend', i)"
          >
            {{ t('chat.session.resend') }}
          </button>
        </span>
      </div>
      <div class="bubble-content">
        <template v-if="waitingChat && i === messages.length - 1 && msg.role === 'assistant' && !msg.content">
          <span class="waiting-pulse">{{ t('chat.input.waitingChat') }}</span>
        </template>
        <template v-else>
          {{ msg.content }}<span
            v-if="sending && !waitingChat && i === messages.length - 1 && msg.role === 'assistant'"
            class="cursor-blink"
          >▍</span>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-messages {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  min-height: 100%;
}

.chat-empty {
  margin: auto;
  text-align: center;
  color: var(--kx-muted, var(--muted));
  font-size: 14px;
  max-width: 360px;
}

.chat-empty code {
  font-size: 12px;
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--kx-primary-soft, var(--bg-subtle));
}

.chat-bubble {
  max-width: 92%;
  align-self: flex-start;
}

.chat-bubble.user {
  align-self: flex-end;
}

.bubble-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.bubble-role {
  font-size: 11px;
  color: var(--kx-muted, var(--muted));
}

.bubble-actions {
  display: flex;
  gap: 4px;
}

.bubble-btn {
  border: none;
  background: transparent;
  color: var(--kx-muted, var(--muted));
  font-size: 11px;
  cursor: pointer;
  padding: 2px 4px;
}

.bubble-btn:hover:not(:disabled) {
  color: var(--kx-primary, var(--accent));
}

.bubble-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.bubble-content {
  padding: 10px 12px;
  border-radius: 10px;
  font-size: 14px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
  background: var(--kx-surface-soft, var(--bg-secondary));
  color: var(--kx-text, var(--text));
  border: 1px solid var(--kx-border, var(--border));
}

.chat-bubble.user .bubble-content {
  background: var(--kx-primary-soft, var(--bg-subtle));
  border-color: color-mix(in srgb, var(--kx-primary, var(--accent)) 25%, transparent);
}

.cursor-blink {
  display: inline-block;
  animation: blink 1s step-end infinite;
  color: var(--kx-primary, var(--accent));
}

.waiting-pulse {
  color: var(--kx-muted, var(--muted));
  animation: pulse 1.2s ease-in-out infinite;
}

.resumed-badge {
  margin-left: 4px;
  font-size: 10px;
  color: var(--kx-success, var(--success));
}

@keyframes blink {
  50% { opacity: 0; }
}

@keyframes pulse {
  0%, 100% { opacity: 0.45; }
  50% { opacity: 1; }
}
</style>
