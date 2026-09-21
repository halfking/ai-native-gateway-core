<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { PopularModel } from '../../api'
import type { ChatResponseMode } from '../../composables/useChatSessions'
import ChatModeToggle from './ChatModeToggle.vue'

defineProps<{
  modelValue: string
  model: string
  mode: ChatResponseMode
  models: PopularModel[]
  sending: boolean
  disabled: boolean
  waitingChat?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [string]
  'update:model': [string]
  'update:mode': [ChatResponseMode]
  send: []
  stop: []
  openParams: []
}>()

const { t } = useI18n()

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    emit('send')
  }
}
</script>

<template>
  <div class="composer">
    <div class="composer__toolbar">
      <label class="composer__model">
        <span>{{ t('chat.page.modelLabel') }}</span>
        <select
          :value="model"
          class="composer__select"
          :disabled="sending || disabled"
          @change="emit('update:model', ($event.target as HTMLSelectElement).value)"
        >
          <option value="auto">{{ t('chat.page.autoRoute') }}</option>
          <option
            v-for="m in models"
            :key="m.canonical_name"
            :value="m.canonical_name"
          >
            {{ m.display_name || m.canonical_name }}
          </option>
        </select>
      </label>
      <ChatModeToggle
        :model-value="mode"
        :disabled="sending || disabled"
        @update:model-value="emit('update:mode', $event)"
      />
      <button
        type="button"
        class="btn btn-ghost btn-sm"
        :disabled="disabled"
        @click="emit('openParams')"
      >
        {{ t('chat.params.open') }}
      </button>
    </div>
    <div class="composer__row">
      <textarea
        :value="modelValue"
        class="composer__input"
        rows="3"
        :placeholder="t('chat.input.placeholder')"
        :disabled="sending || disabled"
        @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
        @keydown="onKeydown"
      />
      <button
        v-if="sending"
        type="button"
        class="btn btn-ghost send-btn"
        @click="emit('stop')"
      >
        {{ t('chat.input.stop') }}
      </button>
      <button
        v-else
        type="button"
        class="btn btn-primary send-btn"
        :disabled="disabled || !modelValue.trim()"
        @click="emit('send')"
      >
        {{ t('chat.input.send') }}
      </button>
    </div>
    <p v-if="waitingChat" class="composer__waiting">{{ t('chat.input.waitingChat') }}</p>
  </div>
</template>

<style scoped>
.composer {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px 12px;
  border-top: 1px solid var(--kx-border, var(--border));
}

.composer__toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.composer__model {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--kx-muted, var(--muted));
}

.composer__select {
  min-width: 160px;
  max-width: 240px;
  padding: 4px 8px;
  border-radius: 6px;
  border: 1px solid var(--kx-border, var(--border));
  background: var(--kx-bg, var(--bg));
  color: var(--kx-text, var(--text));
  font-size: 12px;
}

.composer__row {
  display: flex;
  gap: 10px;
  align-items: flex-end;
}

.composer__input {
  flex: 1;
  padding: 10px 12px;
  border-radius: 8px;
  border: 1px solid var(--kx-border, var(--border));
  background: var(--kx-bg, var(--bg));
  color: var(--kx-text, var(--text));
  font-size: 14px;
  font-family: inherit;
  resize: vertical;
  min-height: 72px;
}

.composer__input:disabled {
  opacity: 0.7;
}

.send-btn {
  flex-shrink: 0;
  min-width: 72px;
  height: 40px;
}

.composer__waiting {
  margin: 0;
  font-size: 12px;
  color: var(--kx-muted, var(--muted));
}
</style>
