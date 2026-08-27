<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { deleteGatewaySession, getAvailableModels, type PopularModel } from '../api'
import { projectAvailableModels } from '../utils/availableModels'
import {
  copyToClipboard,
  downloadTextFile,
  formatSessionExport,
  generateSessionTitle,
  safeExportFilename,
  summarizeConversation,
} from '../composables/useChatActions'
import {
  chatCompletion,
  formatTokenCount,
  isAbortError,
  isSessionForbiddenError,
} from '../composables/useChatCompletions'
import {
  defaultChatSessionSettings,
  formatSessionModelLabel,
  useChatSessions,
  type ChatResponseMode,
  type ChatSessionSettings,
} from '../composables/useChatSessions'
import { useGatewayApiKey } from '../composables/useGatewayApiKey'
import ApiKeySelectModal from '../components/ApiKeySelectModal.vue'
import ChatComposer from '../components/chat/ChatComposer.vue'
import ChatMessageList from '../components/chat/ChatMessageList.vue'
import ChatParamsDrawer from '../components/chat/ChatParamsDrawer.vue'
import GatewayApiKeyPicker from '../components/GatewayApiKeyPicker.vue'

const { t } = useI18n()

interface SendOptions {
  text?: string
  skipAppendUser?: boolean
  isAutoRetry?: boolean
  skipTitleGen?: boolean
}

const {
  apiKey, loading: keyLoading, error: keyError, showPicker, showKeyModal, keyModalReason,
  candidateKeys, unrevealableKeyIds, hasNoKeys, picking, selectedKeyId, selectedKeyMeta,
  resolve: resolveApiKey, selectKey, openKeyModal, closeKeyModal, formatApiKeyLabel,
} = useGatewayApiKey()

const {
  sessions, activeId, activeSession, switchSession, startNewSession, updateActive,
  accumulateUsage, deleteSession, setGwSessionId, ensureSessionApiKey, clearAllGwSessionIds,
} = useChatSessions()

const popularModels = ref<PopularModel[]>([])
const modelDisplayMap = computed(() => {
  const m = new Map<string, string>()
  for (const p of popularModels.value) m.set(p.canonical_name, p.display_name || p.canonical_name)
  return m
})

const input = ref('')
const sending = ref(false)
const waitingChat = ref(false)
const summarizing = ref(false)
const sendError = ref('')
const messagesEl = ref<HTMLElement | null>(null)
const pendingRetryText = ref<string | null>(null)
const autoRetriedForMessage = ref(false)
const copiedKey = ref<string | null>(null)
const showSummaryModal = ref(false)
const summaryText = ref('')
const titleGenInFlight = ref(false)
const showParams = ref(false)
const pickerModel = ref('auto')
let abortCtrl: AbortController | null = null
let availableModelsGeneration = 0

const messages = computed(() => activeSession.value?.messages ?? [])
const sessionSettings = computed(() => activeSession.value?.settings ?? defaultChatSessionSettings())
const sessionUsage = computed(() => activeSession.value?.usage)
const hasMessages = computed(() => messages.value.length > 0)
const composerDisabled = computed(() => keyLoading.value || showPicker.value || hasNoKeys.value)

const currentKeyLabel = computed(() => {
  if (selectedKeyMeta.value) return formatApiKeyLabel(selectedKeyMeta.value)
  const match = candidateKeys.value.find((k) => k.id === selectedKeyId.value)
  if (match) return formatApiKeyLabel(match)
  if (selectedKeyId.value) return t('chat.page.keyIdPrefix', { id: selectedKeyId.value })
  if (keyLoading.value) return t('chat.loading')
  return t('chat.keyNotSelected')
})

watch(() => activeSession.value?.model, (m) => { if (m != null) pickerModel.value = m }, { immediate: true })
watch(pickerModel, (v) => {
  if (activeSession.value && activeSession.value.model !== v) updateActive({ model: v })
})

async function refreshAvailableModels() {
  const generation = ++availableModelsGeneration
  try {
    const data = await getAvailableModels()
    if (generation !== availableModelsGeneration) return
    popularModels.value = projectAvailableModels(data)
  } catch {
    if (generation === availableModelsGeneration) popularModels.value = []
  }
}

onMounted(async () => {
  window.addEventListener('llm-gateway:models-updated', refreshAvailableModels)
  await refreshAvailableModels()
})
onBeforeUnmount(() => {
  window.removeEventListener('llm-gateway:models-updated', refreshAvailableModels)
  abortCtrl?.abort()
})

watch(activeId, async () => {
  sendError.value = ''
  pendingRetryText.value = null
  autoRetriedForMessage.value = false
  showSummaryModal.value = false
  await scrollToBottom()
})

async function scrollToBottom() {
  await nextTick()
  const el = messagesEl.value
  el?.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
}

function formatSessionTime(ts: number): string {
  const d = new Date(ts)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString('zh-CN', { month: 'short', day: 'numeric' })
}

function stripFailedAssistantTail<T extends { role: string; content: string }>(msgs: T[]): T[] {
  const copy = [...msgs]
  const last = copy[copy.length - 1]
  if (last?.role === 'assistant' && (!last.content || last.content.startsWith(t('chat.errorPrefix')))) {
    copy.pop()
  }
  return copy
}

async function flashCopied(key: string) {
  copiedKey.value = key
  await new Promise((r) => setTimeout(r, 1500))
  if (copiedKey.value === key) copiedKey.value = null
}

async function onCopy(text: string, key: string) {
  if (await copyToClipboard(text)) await flashCopied(key)
}

function onSettingsPatch(patch: Partial<ChatSessionSettings>) {
  updateActive({ settings: { ...sessionSettings.value, ...patch } })
}

function onModeChange(mode: ChatResponseMode) {
  if (sending.value) return
  updateActive({ settings: { ...sessionSettings.value, mode } })
}

async function onSelectApiKey(id: number, opts?: { autoRetry?: boolean }) {
  const prevId = selectedKeyId.value
  if (!(await selectKey(id))) return false
  if (prevId != null && prevId !== id) clearAllGwSessionIds()
  sendError.value = ''
  closeKeyModal()
  if (opts?.autoRetry && pendingRetryText.value && !autoRetriedForMessage.value) {
    const retryText = pendingRetryText.value
    pendingRetryText.value = null
    autoRetriedForMessage.value = true
    await send({ text: retryText, skipAppendUser: true, isAutoRetry: true })
  }
  return true
}

async function onKeySelectorChange(e: Event) {
  const id = Number.parseInt((e.target as HTMLSelectElement).value, 10)
  if (Number.isFinite(id) && id > 0) await onSelectApiKey(id)
}

async function onKeyModalSelect(id: number) {
  const shouldAutoRetry =
    keyModalReason.value === 'session-forbidden' &&
    pendingRetryText.value != null &&
    !autoRetriedForMessage.value
  await onSelectApiKey(id, { autoRetry: shouldAutoRetry })
}

function onKeyModalClose() {
  if (keyModalReason.value === 'session-forbidden') return
  closeKeyModal()
}

watch(selectedKeyId, (id, prev) => {
  if (id != null && prev != null && id !== prev) clearAllGwSessionIds()
})

async function maybeGenerateTitle(
  sessionId: string, firstUserText: string, key: string, model: string,
  taskId: string, gwSessionId: string | null,
) {
  if (titleGenInFlight.value) return
  titleGenInFlight.value = true
  try {
    const { title, usage, resolvedModel } = await generateSessionTitle({
      apiKey: key, model, firstUserMessage: firstUserText, taskId, gwSessionId,
    })
    if (activeSession.value?.id !== sessionId || !title) return
    accumulateUsage({ title, ...(resolvedModel ? { lastResolvedModel: resolvedModel } : {}) }, usage)
  } catch { /* non-critical */ } finally {
    titleGenInFlight.value = false
  }
}

function stopGeneration() {
  abortCtrl?.abort()
}

async function send(opts?: SendOptions) {
  const text = (opts?.text ?? input.value).trim()
  if (!text || sending.value || !activeSession.value) return
  if (!opts?.isAutoRetry) {
    autoRetriedForMessage.value = false
    pendingRetryText.value = null
  }
  sendError.value = ''
  const key = apiKey.value || (await resolveApiKey())
  if (!key) {
    sendError.value = keyError.value || t('chat.fetchKeyFailed')
    openKeyModal('manual')
    return
  }
  if (!selectedKeyId.value) {
    sendError.value = t('chat.selectKeyRequired')
    openKeyModal('manual')
    return
  }

  const { gwSessionId, taskId } = ensureSessionApiKey(selectedKeyId.value)
  const session = activeSession.value!
  const settings = session.settings
  const modelForTurn = pickerModel.value
  const userMsgCountBefore = session.messages.filter((m) => m.role === 'user').length
  const stream = settings.mode !== 'chat'
  let nextMessages = session.messages

  if (opts?.skipAppendUser) {
    nextMessages = stripFailedAssistantTail(session.messages)
    updateActive({ messages: nextMessages })
  } else {
    nextMessages = [...session.messages, { role: 'user' as const, content: text, requestedModel: modelForTurn }]
    updateActive({ messages: nextMessages })
    input.value = ''
  }

  await scrollToBottom()
  sending.value = true
  waitingChat.value = !stream
  abortCtrl = new AbortController()
  const assistantIdx = nextMessages.length
  const withPlaceholder = [...nextMessages, { role: 'assistant' as const, content: '' }]
  updateActive({ messages: withPlaceholder })

  try {
    const result = await chatCompletion({
      apiKey: key,
      model: modelForTurn,
      messages: nextMessages,
      taskId,
      gwSessionId,
      stream,
      signal: abortCtrl.signal,
      maxTokens: settings.maxTokens,
      temperature: settings.temperature,
      topP: settings.topP,
      presencePenalty: settings.presencePenalty,
      frequencyPenalty: settings.frequencyPenalty,
      stop: settings.stop,
      systemPrompt: settings.systemPrompt,
      onDelta: (delta) => {
        const current = activeSession.value
        if (!current || current.id !== session.id) return
        const msgs = [...current.messages]
        if (msgs[assistantIdx]) {
          msgs[assistantIdx] = { ...msgs[assistantIdx], content: msgs[assistantIdx].content + delta }
          updateActive({ messages: msgs })
        }
      },
    })

    const finalMsgs = [...(activeSession.value?.messages ?? withPlaceholder)]
    if (finalMsgs[assistantIdx]) {
      finalMsgs[assistantIdx] = {
        role: 'assistant',
        content: result.content,
        ...(result.usage ? { usage: result.usage } : {}),
        ...(result.resolvedModel ? { resolvedModel: result.resolvedModel } : {}),
        ...(result.resumed ? { resumed: true } : {}),
      }
    }
    accumulateUsage(
      { messages: finalMsgs, ...(result.resolvedModel ? { lastResolvedModel: result.resolvedModel } : {}) },
      result.usage,
    )
    if (result.gwSessionId) setGwSessionId(result.gwSessionId, selectedKeyId.value)
    pendingRetryText.value = null
    const isFirstExchange = userMsgCountBefore === 0 && !opts?.skipTitleGen
    if (isFirstExchange && activeSession.value?.title === titleFromTruncated(text)) {
      void maybeGenerateTitle(session.id, text, key, modelForTurn, taskId, result.gwSessionId)
    }
  } catch (e: unknown) {
    if (isAbortError(e)) {
      sendError.value = t('chat.aborted')
      const errMsgs = [...(activeSession.value?.messages ?? withPlaceholder)]
      if (errMsgs[assistantIdx] && !errMsgs[assistantIdx].content) {
        updateActive({ messages: stripFailedAssistantTail(errMsgs) })
      }
      return
    }
    if (isSessionForbiddenError(e) && !opts?.isAutoRetry) {
      pendingRetryText.value = text
      sendError.value = t('chat.sessionForbidden')
      updateActive({ messages: stripFailedAssistantTail(activeSession.value?.messages ?? withPlaceholder) })
      openKeyModal('session-forbidden')
      return
    }
    const msg = e instanceof Error ? e.message : t('chat.sendFailed')
    const lower = msg.toLowerCase()
    sendError.value =
      !stream && (lower.includes('timeout') || lower.includes('timed out') || lower.includes('abort'))
        ? `${msg} — ${t('chat.suggestStream')}`
        : msg
    const errMsgs = [...(activeSession.value?.messages ?? withPlaceholder)]
    if (errMsgs[assistantIdx] && !errMsgs[assistantIdx].content) {
      errMsgs[assistantIdx] = { role: 'assistant', content: `${t('chat.errorPrefix')}${msg}` }
      updateActive({ messages: errMsgs })
    }
  } finally {
    sending.value = false
    waitingChat.value = false
    abortCtrl = null
    await scrollToBottom()
  }
}

function titleFromTruncated(text: string): string {
  const x = text.trim().replace(/\s+/g, ' ')
  return x.length <= 24 ? x : `${x.slice(0, 24)}…`
}

async function resendUserMessage(userIdx: number) {
  const msg = messages.value[userIdx]
  if (!msg || msg.role !== 'user' || sending.value) return
  const kept = messages.value.slice(0, userIdx + 1).map((m, i) =>
    i === userIdx ? { ...m, requestedModel: pickerModel.value } : m,
  )
  updateActive({ messages: kept })
  await send({ text: msg.content, skipAppendUser: true, skipTitleGen: true })
}

function clearChat() {
  startNewSession(pickerModel.value)
  sendError.value = ''
  input.value = ''
  pendingRetryText.value = null
  autoRetriedForMessage.value = false
}

async function removeSession(id: string, e?: Event) {
  e?.stopPropagation()
  if (sending.value || summarizing.value) return
  const s = sessions.value.find((x) => x.id === id)
  if (!s) return
  if (s.messages.length > 0 && !window.confirm(t('chat.sidebar.confirmDelete', { title: s.title }))) return
  const removed = deleteSession(id)
  if (!removed) return
  if (removed.gwSessionId) {
    const key = apiKey.value || (await resolveApiKey().catch(() => null))
    if (key) deleteGatewaySession(key, removed.gwSessionId).catch(() => {})
  }
}

function exportSession() {
  const s = activeSession.value
  if (!s?.messages.length) return
  downloadTextFile(
    safeExportFilename(s.title),
    formatSessionExport({
      title: s.title,
      modelLabel: formatSessionModelLabel(s, modelDisplayMap.value),
      messages: s.messages,
      summary: s.summary,
      usage: s.usage,
    }),
  )
}

async function runSummarize() {
  const s = activeSession.value
  if (!s?.messages.length || summarizing.value || sending.value) return
  const key = apiKey.value || (await resolveApiKey())
  if (!key || !selectedKeyId.value) {
    sendError.value = t('chat.selectKeyRequired')
    return
  }
  const { gwSessionId, taskId } = ensureSessionApiKey(selectedKeyId.value)
  summarizing.value = true
  sendError.value = ''
  try {
    const result = await summarizeConversation({
      apiKey: key, model: pickerModel.value, messages: s.messages, taskId, gwSessionId,
    })
    summaryText.value = result.summary
    showSummaryModal.value = true
    accumulateUsage(
      {
        summary: result.summary,
        ...(result.title ? { title: result.title } : {}),
        ...(result.resolvedModel ? { lastResolvedModel: result.resolvedModel } : {}),
      },
      result.usage,
    )
  } catch (e: unknown) {
    sendError.value = e instanceof Error ? e.message : t('chat.summarizeFailed')
  } finally {
    summarizing.value = false
  }
}
</script>

<template>
  <div class="chat-page">
    <div class="page-header chat-header">
      <div>
        <h2>{{ t('chat.page.title') }}</h2>
        <p class="chat-subtitle">{{ t('chat.page.subtitle') }}</p>
      </div>
      <div class="chat-controls">
        <label class="model-label key-label key-label--primary">
          <span class="key-label__text">{{ t('chat.page.apiKeyLabel') }}</span>
          <select
            class="model-select key-select"
            :value="selectedKeyId ?? ''"
            :disabled="sending || picking || keyLoading || hasNoKeys"
            :title="currentKeyLabel"
            @change="onKeySelectorChange"
          >
            <option value="" disabled>
              {{ keyLoading ? t('chat.loading') : hasNoKeys ? t('chat.noAvailableKeys') : t('chat.selectKey') }}
            </option>
            <option
              v-for="k in candidateKeys"
              :key="k.id"
              :value="k.id"
              :disabled="unrevealableKeyIds.has(k.id)"
            >
              {{ formatApiKeyLabel(k) }}{{ unrevealableKeyIds.has(k.id) ? t('chat.unrevealable') : '' }}
            </option>
          </select>
        </label>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="sending || picking || hasNoKeys"
          @click="openKeyModal('manual')"
        >
          {{ t('chat.page.manageKeys') }}
        </button>
      </div>
    </div>

    <div v-if="keyLoading" class="alert alert-info">{{ t('chat.page.loadingKeys') }}</div>
    <div v-else-if="hasNoKeys" class="alert alert-warning no-keys-banner">
      <div class="no-keys-banner__body">
        <strong>{{ t('chat.page.needsKeyTitle') }}</strong>
        <p>{{ t('chat.page.needsKeyDesc') }}</p>
      </div>
      <RouterLink to="/keys?redirect=/chat&action=create" class="btn btn-primary btn-sm no-keys-banner__cta">
        {{ t('chat.page.applyKeyCta') }}
      </RouterLink>
    </div>
    <GatewayApiKeyPicker
      v-else-if="showPicker"
      :keys="candidateKeys"
      :loading="picking"
      :error="keyError"
      @select="(id) => onSelectApiKey(id)"
    />
    <div v-else-if="keyError && !apiKey" class="alert alert-danger">
      {{ keyError }}
      <RouterLink to="/keys?redirect=/chat" class="link-inline">{{ t('chat.page.goToKeys') }}</RouterLink>
    </div>

    <ApiKeySelectModal
      :visible="showKeyModal"
      :keys="candidateKeys"
      :unrevealable-ids="unrevealableKeyIds"
      :loading="picking"
      :error="keyError"
      :reason="keyModalReason"
      :selected-id="selectedKeyId"
      @select="onKeyModalSelect"
      @close="onKeyModalClose"
    />

    <div class="chat-body">
      <aside class="session-sidebar card">
        <div class="session-sidebar__head">
          <span class="session-sidebar__title">{{ t('chat.sidebar.title') }}</span>
          <button type="button" class="btn btn-ghost btn-sm" :disabled="sending" @click="clearChat">
            {{ t('chat.sidebar.new') }}
          </button>
        </div>
        <ul class="session-list">
          <li
            v-for="s in sessions"
            :key="s.id"
            class="session-item"
            :class="{ active: s.id === activeId }"
          >
            <button type="button" class="session-item__btn" :disabled="sending" @click="switchSession(s.id)">
              <span class="session-item__row">
                <span class="session-item__title">{{ s.title }}</span>
                <button
                  type="button"
                  class="session-item__del"
                  :title="t('chat.sidebar.deleteTitle')"
                  :disabled="sending || summarizing"
                  @click="removeSession(s.id, $event)"
                >
                  ×
                </button>
              </span>
              <span class="session-item__meta">
                {{ formatSessionTime(s.updatedAt) }}
                · {{ formatSessionModelLabel(s, modelDisplayMap) }}
                <template v-if="s.usage && s.usage.totalTokens > 0">
                  · {{ formatTokenCount(s.usage.totalTokens) }} tok
                </template>
              </span>
            </button>
          </li>
          <li v-if="!sessions.length" class="session-empty">{{ t('chat.sidebar.empty') }}</li>
        </ul>
      </aside>

      <div class="chat-layout card">
        <div v-if="activeSession" class="chat-session-bar">
          <div class="chat-session-bar__info">
            <span class="chat-session-bar__title">{{ activeSession.title }}</span>
            <span class="mode-badge">
              {{ sessionSettings.mode === 'chat' ? t('chat.mode.badgeChat') : t('chat.mode.badgeStream') }}
            </span>
            <span class="chat-session-bar__model">
              {{ t('chat.session.current', {
                model: pickerModel === 'auto' ? t('chat.auto') : (modelDisplayMap.get(pickerModel) || pickerModel),
              }) }}
              <template v-if="activeSession.lastResolvedModel">
                · {{ t('chat.session.lastResolved', {
                  model: modelDisplayMap.get(activeSession.lastResolvedModel) || activeSession.lastResolvedModel,
                }) }}
              </template>
            </span>
            <span
              v-if="sessionUsage && sessionUsage.totalTokens > 0"
              class="chat-session-bar__tokens"
              :title="t('chat.session.tokensDetail', {
                in: sessionUsage.promptTokens,
                out: sessionUsage.completionTokens,
                total: sessionUsage.totalTokens,
              })"
            >
              {{ formatTokenCount(sessionUsage.promptTokens) }} in /
              {{ formatTokenCount(sessionUsage.completionTokens) }} out /
              {{ formatTokenCount(sessionUsage.totalTokens) }} total
            </span>
          </div>
          <div v-if="hasMessages" class="chat-session-bar__actions">
            <button type="button" class="btn btn-ghost btn-sm" :disabled="sending || summarizing" @click="exportSession">
              {{ t('chat.session.export') }}
            </button>
            <button type="button" class="btn btn-ghost btn-sm" :disabled="sending || summarizing" @click="runSummarize">
              {{ summarizing ? t('chat.summarizing') : t('chat.summarize') }}
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-sm btn-danger-text"
              :disabled="sending || summarizing"
              @click="removeSession(activeSession.id)"
            >
              {{ t('chat.session.delete') }}
            </button>
          </div>
        </div>

        <div ref="messagesEl" class="messages-wrap">
          <ChatMessageList
            :messages="messages"
            :sending="sending"
            :waiting-chat="waitingChat"
            :has-no-keys="hasNoKeys"
            :model-display-map="modelDisplayMap"
            :copied-key="copiedKey"
            @copy="onCopy"
            @resend="resendUserMessage"
          />
        </div>

        <div v-if="sendError" class="alert alert-danger chat-error">{{ sendError }}</div>

        <ChatComposer
          v-model="input"
          :model="pickerModel"
          :mode="sessionSettings.mode"
          :models="popularModels"
          :sending="sending"
          :disabled="composerDisabled"
          :waiting-chat="waitingChat"
          @update:model="pickerModel = $event"
          @update:mode="onModeChange"
          @send="send()"
          @stop="stopGeneration"
          @open-params="showParams = true"
        />
      </div>
    </div>

    <ChatParamsDrawer
      :open="showParams"
      :settings="sessionSettings"
      :disabled="false"
      @close="showParams = false"
      @update:settings="onSettingsPatch"
    />

    <div v-if="showSummaryModal" class="modal-overlay" @click.self="showSummaryModal = false">
      <div class="modal-card">
        <div class="modal-head">
          <h3>{{ t('chat.modal.summaryTitle') }}</h3>
          <button type="button" class="modal-close" @click="showSummaryModal = false">×</button>
        </div>
        <div class="modal-body">{{ summaryText }}</div>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost btn-sm" @click="onCopy(summaryText, 'summary')">
            {{ copiedKey === 'summary' ? t('chat.copied') : t('chat.copySummary') }}
          </button>
          <button type="button" class="btn btn-primary btn-sm" @click="showSummaryModal = false">
            {{ t('chat.modal.close') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped src="./chat-view.css"></style>
