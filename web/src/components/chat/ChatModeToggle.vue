<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ChatResponseMode } from '../../composables/useChatSessions'

defineProps<{
  modelValue: ChatResponseMode
  disabled?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [ChatResponseMode]
}>()

const { t } = useI18n()

function setMode(mode: ChatResponseMode) {
  emit('update:modelValue', mode)
}
</script>

<template>
  <div class="mode-toggle" role="group" :aria-label="t('chat.mode.label')">
    <button
      type="button"
      class="mode-toggle__btn"
      :class="{ active: modelValue === 'stream' }"
      :disabled="disabled"
      @click="setMode('stream')"
    >
      {{ t('chat.mode.stream') }}
    </button>
    <button
      type="button"
      class="mode-toggle__btn"
      :class="{ active: modelValue === 'chat' }"
      :disabled="disabled"
      @click="setMode('chat')"
    >
      {{ t('chat.mode.chat') }}
    </button>
  </div>
</template>

<style scoped>
.mode-toggle {
  display: inline-flex;
  border: 1px solid var(--kx-border, var(--border));
  border-radius: 6px;
  overflow: hidden;
  flex-shrink: 0;
}

.mode-toggle__btn {
  padding: 4px 10px;
  font-size: 12px;
  border: none;
  background: var(--kx-surface, var(--card));
  color: var(--kx-muted, var(--muted));
  cursor: pointer;
}

.mode-toggle__btn + .mode-toggle__btn {
  border-left: 1px solid var(--kx-border, var(--border));
}

.mode-toggle__btn.active {
  background: var(--kx-primary-soft, var(--bg-subtle));
  color: var(--kx-primary, var(--accent));
  font-weight: 600;
}

.mode-toggle__btn:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}
</style>
