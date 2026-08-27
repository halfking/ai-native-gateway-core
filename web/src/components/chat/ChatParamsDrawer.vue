<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  defaultChatSessionSettings,
  type ChatSessionSettings,
} from '../../composables/useChatSessions'

const props = defineProps<{
  open: boolean
  settings: ChatSessionSettings
  disabled?: boolean
}>()

const emit = defineEmits<{
  close: []
  'update:settings': [Partial<ChatSessionSettings>]
}>()

const { t } = useI18n()
const showAdvanced = ref(false)

const stopText = computed({
  get: () => props.settings.stop.join(', '),
  set: (v: string) => {
    const stop = v
      .split(/[,，]/)
      .map((s) => s.trim())
      .filter(Boolean)
    emit('update:settings', { stop })
  },
})

function patch<K extends keyof ChatSessionSettings>(key: K, value: ChatSessionSettings[K]) {
  emit('update:settings', { [key]: value } as Partial<ChatSessionSettings>)
}

function resetDefaults() {
  emit('update:settings', defaultChatSessionSettings())
}
</script>

<template>
  <div v-if="open" class="drawer-overlay" @click.self="emit('close')">
    <aside class="drawer" role="dialog" :aria-label="t('chat.params.title')">
      <div class="drawer__head">
        <h3>{{ t('chat.params.title') }}</h3>
        <button type="button" class="drawer__close" @click="emit('close')">×</button>
      </div>
      <p class="drawer__hint">{{ t('chat.params.nextTurnHint') }}</p>
      <div class="drawer__body">
        <label class="field">
          <span>{{ t('chat.params.systemPrompt') }}</span>
          <textarea
            :value="settings.systemPrompt"
            rows="4"
            :disabled="disabled"
            :placeholder="t('chat.params.systemPromptPlaceholder')"
            @input="patch('systemPrompt', ($event.target as HTMLTextAreaElement).value)"
          />
        </label>

        <label class="field">
          <span>{{ t('chat.params.temperature') }} · {{ settings.temperature.toFixed(2) }}</span>
          <input
            type="range"
            min="0"
            max="2"
            step="0.05"
            :value="settings.temperature"
            :disabled="disabled"
            @input="patch('temperature', Number(($event.target as HTMLInputElement).value))"
          />
        </label>

        <label class="field">
          <span>{{ t('chat.params.maxTokens') }}</span>
          <input
            type="number"
            min="1"
            max="128000"
            :value="settings.maxTokens"
            :disabled="disabled"
            @change="patch('maxTokens', Number(($event.target as HTMLInputElement).value) || 2048)"
          />
        </label>

        <label class="field">
          <span>{{ t('chat.params.topP') }} · {{ settings.topP.toFixed(2) }}</span>
          <input
            type="range"
            min="0"
            max="1"
            step="0.01"
            :value="settings.topP"
            :disabled="disabled"
            @input="patch('topP', Number(($event.target as HTMLInputElement).value))"
          />
        </label>

        <button type="button" class="adv-toggle" @click="showAdvanced = !showAdvanced">
          {{ showAdvanced ? t('chat.params.hideAdvanced') : t('chat.params.showAdvanced') }}
        </button>

        <template v-if="showAdvanced">
          <label class="field">
            <span>{{ t('chat.params.presencePenalty') }} · {{ settings.presencePenalty.toFixed(2) }}</span>
            <input
              type="range"
              min="-2"
              max="2"
              step="0.05"
              :value="settings.presencePenalty"
              :disabled="disabled"
              @input="patch('presencePenalty', Number(($event.target as HTMLInputElement).value))"
            />
          </label>
          <label class="field">
            <span>{{ t('chat.params.frequencyPenalty') }} · {{ settings.frequencyPenalty.toFixed(2) }}</span>
            <input
              type="range"
              min="-2"
              max="2"
              step="0.05"
              :value="settings.frequencyPenalty"
              :disabled="disabled"
              @input="patch('frequencyPenalty', Number(($event.target as HTMLInputElement).value))"
            />
          </label>
          <label class="field">
            <span>{{ t('chat.params.stop') }}</span>
            <input
              v-model="stopText"
              type="text"
              :disabled="disabled"
              :placeholder="t('chat.params.stopPlaceholder')"
            />
          </label>
        </template>
      </div>
      <div class="drawer__foot">
        <button type="button" class="btn btn-ghost btn-sm" :disabled="disabled" @click="resetDefaults">
          {{ t('chat.params.reset') }}
        </button>
        <button type="button" class="btn btn-primary btn-sm" @click="emit('close')">
          {{ t('chat.params.done') }}
        </button>
      </div>
    </aside>
  </div>
</template>

<style scoped>
.drawer-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay-medium, rgba(0, 0, 0, 0.4));
  z-index: 80;
  display: flex;
  justify-content: flex-end;
}

.drawer {
  width: min(360px, 100vw);
  height: 100%;
  background: var(--kx-surface, var(--card));
  border-left: 1px solid var(--kx-border, var(--border));
  display: flex;
  flex-direction: column;
  box-shadow: var(--kx-shadow-md);
}

.drawer__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px;
  border-bottom: 1px solid var(--kx-border, var(--border));
}

.drawer__head h3 {
  margin: 0;
  font-size: 15px;
  color: var(--kx-text, var(--text));
}

.drawer__close {
  border: none;
  background: transparent;
  font-size: 20px;
  cursor: pointer;
  color: var(--kx-muted, var(--muted));
}

.drawer__hint {
  margin: 0;
  padding: 8px 16px;
  font-size: 12px;
  color: var(--kx-muted, var(--muted));
  border-bottom: 1px solid var(--kx-border, var(--border));
}

.drawer__body {
  flex: 1;
  overflow-y: auto;
  padding: 12px 16px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
  color: var(--kx-muted, var(--muted));
}

.field textarea,
.field input[type='number'],
.field input[type='text'] {
  padding: 8px 10px;
  border-radius: 6px;
  border: 1px solid var(--kx-border, var(--border));
  background: var(--kx-bg, var(--bg));
  color: var(--kx-text, var(--text));
  font-size: 13px;
  font-family: inherit;
  resize: vertical;
}

.field input[type='range'] {
  width: 100%;
}

.adv-toggle {
  align-self: flex-start;
  border: none;
  background: transparent;
  color: var(--kx-primary, var(--accent));
  font-size: 12px;
  cursor: pointer;
  padding: 0;
}

.drawer__foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--kx-border, var(--border));
}
</style>
