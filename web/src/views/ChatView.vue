<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { deleteGatewaySession, getAvailableModels, type PopularModel } from '../api'
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
  isSessionForbiddenError,
} from '../composables/useChatCompletions'
import { formatSessionModelLabel, useChatSessions } from '../composables/useChatSessions'
import { useGatewayApiKey } from '../composables/useGatewayApiKey'
import ApiKeySelectModal from '../components/ApiKeySelectModal.vue'
import GatewayApiKeyPicker from '../components/GatewayApiKeyPicker.vue'

interface SendOptions {
  text?: string
  skipAppendUser?: boolean
  isAutoRetry?: boolean
  /** Skip LLM title generation (e.g. resend) */
  skipTitleGen?: boolean
}

const {
  apiKey,
  loading: keyLoading,
  error: keyError,
  showPicker,
  showKeyModal,
  keyModalReason,
  candidateKeys,
  unrevealableKeyIds,
  hasNoKeys,
  picking,
  selectedKeyId,
  selectedKeyMeta,
  resolve: resolveApiKey,
  selectKey,
  openKeyModal,
  closeKeyModal,
  formatApiKeyLabel,
} = useGatewayApiKey()
const {
  sessions,
  activeId,
  activeSession,
  switchSession,
  startNewSession,
  updateActive,
  accumulateUsage,
  deleteSession,
  setGwSessionId,
  ensureSessionApiKey,
  clearAllGwSessionIds,
} = useChatSessions()

const popularModels = ref<PopularModel[]>([])
const modelDisplayMap = computed(() => {
  const m = new Map<string, string>()
  for (const p of popularModels.value) {
    m.set(p.canonical_name, p.display_name || p.canonical_name)
  }
  return m
})

const input = ref('')
const sending = ref(false)
const summarizing = ref(false)
const sendError = ref('')
const messagesEl = ref<HTMLElement | null>(null)
const pendingRetryText = ref<string | null>(null)
const autoRetriedForMessage = ref(false)
const copiedKey = ref<string | null>(null)
const showSummaryModal = ref(false)
const summaryText = ref('')
const titleGenInFlight = ref(false)
/** Current model picker value — synced to session but captured per send turn */
const pickerModel = ref('auto')

watch(
  () => activeSession.value?.model,
  (m) => {
    if (m != null) pickerModel.value = m
  },
  { immediate: true },
)

watch(pickerModel, (v) => {
  if (activeSession.value && activeSession.value.model !== v) {
    updateActive({ model: v })
  }
})

const messages = computed(() => activeSession.value?.messages ?? [])

const sessionModelLabel = computed(() => {
  if (!activeSession.value) return '—'
  return formatSessionModelLabel(activeSession.value, modelDisplayMap.value)
})

const sessionUsage = computed(() => activeSession.value?.usage)

const currentKeyLabel = computed(() => {
  if (selectedKeyMeta.value) return formatApiKeyLabel(selectedKeyMeta.value)
  const match = candidateKeys.value.find((k) => k.id === selectedKeyId.value)
  if (match) return formatApiKeyLabel(match)
  if (selectedKeyId.value) return `密钥 #${selectedKeyId.value}`
  if (keyLoading.value) return '加载中…'
  return '未选择'
})

const hasMessages = computed(() => messages.value.length > 0)

onMounted(async () => {
  try {
    const data = await getAvailableModels()
    popularModels.value = data.popular ?? []
  } catch {
    popularModels.value = []
  }
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
  messagesEl.value?.scrollTo({ top: messagesEl.value.scrollHeight, behavior: 'smooth' })
}

function formatSessionTime(ts: number): string {
  const d = new Date(ts)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString('zh-CN', { month: 'short', day: 'numeric' })
}

function sessionListModelLabel(s: (typeof sessions.value)[0]): string {
  return formatSessionModelLabel(s, modelDisplayMap.value)
}

function stripFailedAssistantTail<T extends { role: string; content: string }>(msgs: T[]): T[] {
  const copy = [...msgs]
  const last = copy[copy.length - 1]
  if (last?.role === 'assistant' && (!last.content || last.content.startsWith('错误：'))) {
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
  const ok = await copyToClipboard(text)
  if (ok) await flashCopied(key)
}

async function onSelectApiKey(id: number, opts?: { autoRetry?: boolean }) {
  const prevId = selectedKeyId.value
  const ok = await selectKey(id)
  if (!ok) return false

  if (prevId != null && prevId !== id) {
    clearAllGwSessionIds()
  }
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
  const raw = (e.target as HTMLSelectElement).value
  const id = Number.parseInt(raw, 10)
  if (!Number.isFinite(id) || id <= 0) return
  await onSelectApiKey(id)
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
  if (id != null && prev != null && id !== prev) {
    clearAllGwSessionIds()
  }
})

async function maybeGenerateTitle(
  sessionId: string,
  firstUserText: string,
  key: string,
  model: string,
  taskId: string,
  gwSessionId: string | null,
) {
  if (titleGenInFlight.value) return
  titleGenInFlight.value = true
  try {
    const { title, usage, resolvedModel } = await generateSessionTitle({
      apiKey: key,
      model,
      firstUserMessage: firstUserText,
      taskId,
      gwSessionId,
    })
    if (activeSession.value?.id !== sessionId || !title) return
    accumulateUsage(
      {
        title,
        ...(resolvedModel ? { lastResolvedModel: resolvedModel } : {}),
      },
      usage,
    )
  } catch {
    // non-critical
  } finally {
    titleGenInFlight.value = false
  }
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
    sendError.value = keyError.value || '无法获取 API 密钥'
    openKeyModal('manual')
    return
  }
  if (!selectedKeyId.value) {
    sendError.value = '无法确定 API 密钥，请选择一把密钥'
    openKeyModal('manual')
    return
  }

  const { gwSessionId, taskId } = ensureSessionApiKey(selectedKeyId.value)
  const session = activeSession.value!
  const modelForTurn = pickerModel.value
  const userMsgCountBefore = session.messages.filter((m) => m.role === 'user').length
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
        // Track C: persist the resumed flag so the bubble can render
        // a "已从缓存恢复" badge after the user reloads the page.
        // Falsy values are omitted so existing messages (without the
        // field) do not pick up `undefined` in the JSON serialisation.
        ...(result.resumed ? { resumed: true } : {}),
      }
    }

    accumulateUsage(
      {
        messages: finalMsgs,
        ...(result.resolvedModel ? { lastResolvedModel: result.resolvedModel } : {}),
      },
      result.usage,
    )

    if (result.gwSessionId) {
      setGwSessionId(result.gwSessionId, selectedKeyId.value)
    }
    pendingRetryText.value = null

    const isFirstExchange = userMsgCountBefore === 0 && !opts?.skipTitleGen
    if (isFirstExchange && activeSession.value?.title === titleFromTruncated(text)) {
      void maybeGenerateTitle(session.id, text, key, modelForTurn, taskId, result.gwSessionId)
    }
  } catch (e: unknown) {
    if (isSessionForbiddenError(e) && !opts?.isAutoRetry) {
      pendingRetryText.value = text
      sendError.value = '当前 API 密钥无法访问此会话，请选择正确的密钥'
      const errMsgs = stripFailedAssistantTail(activeSession.value?.messages ?? withPlaceholder)
      updateActive({ messages: errMsgs })
      openKeyModal('session-forbidden')
      return
    }

    const msg = e instanceof Error ? e.message : '发送失败'
    sendError.value = msg
    const errMsgs = [...(activeSession.value?.messages ?? withPlaceholder)]
    if (errMsgs[assistantIdx] && !errMsgs[assistantIdx].content) {
      errMsgs[assistantIdx] = { role: 'assistant', content: `错误：${msg}` }
      updateActive({ messages: errMsgs })
    }
  } finally {
    sending.value = false
    await scrollToBottom()
  }
}

function titleFromTruncated(text: string): string {
  const t = text.trim().replace(/\s+/g, ' ')
  return t.length <= 24 ? t : `${t.slice(0, 24)}…`
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
  if (s.messages.length > 0 && !window.confirm(`删除会话「${s.title}」？此操作不可恢复。`)) return

  const removed = deleteSession(id)
  if (!removed) return

  if (removed.gwSessionId) {
    const key = apiKey.value || (await resolveApiKey().catch(() => null))
    if (key) {
      deleteGatewaySession(key, removed.gwSessionId).catch(() => {})
    }
  }
}

function exportSession() {
  const s = activeSession.value
  if (!s || !s.messages.length) return
  const content = formatSessionExport({
    title: s.title,
    modelLabel: formatSessionModelLabel(s, modelDisplayMap.value),
    messages: s.messages,
    summary: s.summary,
    usage: s.usage,
  })
  downloadTextFile(safeExportFilename(s.title), content)
}

async function runSummarize() {
  const s = activeSession.value
  if (!s || !s.messages.length || summarizing.value || sending.value) return

  const key = apiKey.value || (await resolveApiKey())
  if (!key || !selectedKeyId.value) {
    sendError.value = '请先选择 API 密钥'
    return
  }

  const { gwSessionId, taskId } = ensureSessionApiKey(selectedKeyId.value)
  summarizing.value = true
  sendError.value = ''

  try {
    const result = await summarizeConversation({
      apiKey: key,
      model: pickerModel.value,
      messages: s.messages,
      taskId,
      gwSessionId,
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
    sendError.value = e instanceof Error ? e.message : '总结失败'
  } finally {
    summarizing.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    void send()
  }
}
</script>

<template>
  <div class="chat-page">
    <div class="page-header chat-header">
      <div>
        <h2>对话</h2>
        <p class="chat-subtitle">通过 OpenAI 兼容接口直接与网关模型对话</p>
      </div>
      <div class="chat-controls">
        <label class="model-label key-label key-label--primary">
          <span class="key-label__text">API 密钥</span>
          <select
            class="model-select key-select"
            :value="selectedKeyId ?? ''"
            :disabled="sending || picking || keyLoading || hasNoKeys"
            :title="currentKeyLabel"
            @change="onKeySelectorChange"
          >
            <option value="" disabled>
              {{
                keyLoading
                  ? '加载中…'
                  : hasNoKeys
                    ? '无可用密钥'
                    : candidateKeys.length
                      ? '选择密钥…'
                      : '无可用密钥'
              }}
            </option>
            <option
              v-for="k in candidateKeys"
              :key="k.id"
              :value="k.id"
              :disabled="unrevealableKeyIds.has(k.id)"
            >
              {{ formatApiKeyLabel(k) }}{{ unrevealableKeyIds.has(k.id) ? '（无法还原）' : '' }}
            </option>
          </select>
        </label>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="sending || picking || hasNoKeys"
          @click="openKeyModal('manual')"
        >
          管理密钥
        </button>
        <label class="model-label">
          <span>模型</span>
          <select v-model="pickerModel" class="model-select" :disabled="sending || hasNoKeys">
            <option value="auto">自动路由 (auto)</option>
            <option
              v-for="m in popularModels"
              :key="m.canonical_name"
              :value="m.canonical_name"
            >
              {{ m.display_name || m.canonical_name }}
            </option>
          </select>
        </label>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="sending" @click="clearChat">
          新建
        </button>
      </div>
    </div>

    <div v-if="keyLoading" class="alert alert-info">正在加载 API 密钥…</div>
    <div v-else-if="hasNoKeys" class="alert alert-warning no-keys-banner">
      <div class="no-keys-banner__body">
        <strong>请先申请 API 密钥</strong>
        <p>对话需要一把属于您且已启用的 API 密钥。您当前没有可用密钥，请先申请或创建。</p>
      </div>
      <RouterLink
        to="/keys?redirect=/chat&action=create"
        class="btn btn-primary btn-sm no-keys-banner__cta"
      >
        前往申请密钥
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
      <RouterLink to="/keys?redirect=/chat" class="link-inline">前往 API 密钥</RouterLink>
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
          <span class="session-sidebar__title">会话</span>
          <button type="button" class="btn btn-ghost btn-sm" :disabled="sending" @click="clearChat">
            + 新建
          </button>
        </div>
        <ul class="session-list">
          <li
            v-for="s in sessions"
            :key="s.id"
            class="session-item"
            :class="{ active: s.id === activeId }"
          >
            <button
              type="button"
              class="session-item__btn"
              :disabled="sending"
              @click="switchSession(s.id)"
            >
              <span class="session-item__row">
                <span class="session-item__title">{{ s.title }}</span>
                <button
                  type="button"
                  class="session-item__del"
                  title="删除会话"
                  :disabled="sending || summarizing"
                  @click="removeSession(s.id, $event)"
                >
                  ×
                </button>
              </span>
              <span class="session-item__meta">
                {{ formatSessionTime(s.updatedAt) }}
                · {{ sessionListModelLabel(s) }}
                <template v-if="s.usage && s.usage.totalTokens > 0">
                  · {{ formatTokenCount(s.usage.totalTokens) }} tok
                </template>
              </span>
            </button>
          </li>
          <li v-if="!sessions.length" class="session-empty">暂无会话</li>
        </ul>
      </aside>

      <div class="chat-layout card">
        <div v-if="activeSession" class="chat-session-bar">
          <div class="chat-session-bar__info">
            <span class="chat-session-bar__title">{{ activeSession.title }}</span>
            <span class="chat-session-bar__model" :title="sessionModelLabel">
              当前: {{ pickerModel === 'auto' ? '自动' : (modelDisplayMap.get(pickerModel) || pickerModel) }}
              <template v-if="activeSession.lastResolvedModel">
                · 最近实际: {{ modelDisplayMap.get(activeSession.lastResolvedModel) || activeSession.lastResolvedModel }}
              </template>
            </span>
            <span
              v-if="sessionUsage && sessionUsage.totalTokens > 0"
              class="chat-session-bar__tokens"
              :title="`输入 ${sessionUsage.promptTokens} / 输出 ${sessionUsage.completionTokens} / 合计 ${sessionUsage.totalTokens}`"
            >
              {{ formatTokenCount(sessionUsage.promptTokens) }} in /
              {{ formatTokenCount(sessionUsage.completionTokens) }} out /
              {{ formatTokenCount(sessionUsage.totalTokens) }} total
            </span>
          </div>
          <div v-if="hasMessages" class="chat-session-bar__actions">
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              :disabled="sending || summarizing"
              @click="exportSession"
            >
              导出
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              :disabled="sending || summarizing"
              @click="runSummarize"
            >
              {{ summarizing ? '总结中…' : '总结' }}
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-sm btn-danger-text"
              :disabled="sending || summarizing"
              @click="removeSession(activeSession.id)"
            >
              删除
            </button>
          </div>
        </div>

        <div ref="messagesEl" class="chat-messages">
          <div v-if="!messages.length" class="chat-empty">
            <p v-if="hasNoKeys">申请 API 密钥后即可开始对话。</p>
            <p v-else>输入消息开始对话。选择 <code>auto</code> 将由网关自动挑选合适模型。</p>
          </div>
          <div
            v-for="(msg, i) in messages"
            :key="i"
            class="chat-bubble"
            :class="msg.role"
          >
            <div class="bubble-head">
              <span class="bubble-role">
                {{ msg.role === 'user' ? '你' : '助手' }}
                <template v-if="msg.role === 'user' && msg.requestedModel">
                  · {{ msg.requestedModel === 'auto' ? '自动' : (modelDisplayMap.get(msg.requestedModel) || msg.requestedModel) }}
                </template>
                <template v-if="msg.role === 'assistant' && msg.resolvedModel">
                  · {{ modelDisplayMap.get(msg.resolvedModel) || msg.resolvedModel }}
                </template>
                <template v-if="msg.role === 'assistant' && msg.usage">
                  · {{ formatTokenCount(msg.usage.totalTokens) }} tok
                </template>
                <!-- Track C: badge when this reply came from the gateway
                     pending-response cache instead of a fresh upstream
                     call. Helps the user understand why the model did
                     not spend new tokens for this turn. -->
                <template v-if="msg.role === 'assistant' && msg.resumed">
                  <span class="resumed-badge" title="从网关缓存恢复（无新 LLM 调用）">↻ 已恢复</span>
                </template>
              </span>
              <span class="bubble-actions">
                <button
                  type="button"
                  class="bubble-btn"
                  :title="copiedKey === `copy-${i}` ? '已复制' : '复制'"
                  @click="onCopy(msg.content, `copy-${i}`)"
                >
                  {{ copiedKey === `copy-${i}` ? '✓' : '复制' }}
                </button>
                <button
                  v-if="msg.role === 'user'"
                  type="button"
                  class="bubble-btn"
                  :disabled="sending"
                  title="重发此问题"
                  @click="resendUserMessage(i)"
                >
                  重发
                </button>
              </span>
            </div>
            <div class="bubble-content">
              {{ msg.content }}<span
                v-if="sending && i === messages.length - 1 && msg.role === 'assistant' && !msg.content"
                class="cursor-blink"
              >▍</span>
            </div>
          </div>
        </div>

        <div v-if="sendError" class="alert alert-danger chat-error">{{ sendError }}</div>

        <div class="chat-input-row">
          <textarea
            v-model="input"
            class="chat-input"
            rows="3"
            placeholder="输入消息…（Enter 发送，Shift+Enter 换行）"
            :disabled="sending || keyLoading || showPicker || hasNoKeys"
            @keydown="onKeydown"
          />
          <button
            type="button"
            class="btn btn-primary send-btn"
            :disabled="sending || keyLoading || showPicker || hasNoKeys || !input.trim()"
            @click="send()"
          >
            {{ sending ? '生成中…' : '发送' }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="showSummaryModal" class="modal-overlay" @click.self="showSummaryModal = false">
      <div class="modal-card">
        <div class="modal-head">
          <h3>会话总结</h3>
          <button type="button" class="modal-close" @click="showSummaryModal = false">×</button>
        </div>
        <div class="modal-body">{{ summaryText }}</div>
        <div class="modal-foot">
          <button type="button" class="btn btn-ghost btn-sm" @click="onCopy(summaryText, 'summary')">
            {{ copiedKey === 'summary' ? '已复制' : '复制总结' }}
          </button>
          <button type="button" class="btn btn-primary btn-sm" @click="showSummaryModal = false">关闭</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-page {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 48px);
  max-height: calc(100vh - 48px);
}

.chat-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}

.chat-subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--muted);
}

.chat-controls {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.model-label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--muted);
}

.model-select {
  min-width: 180px;
  padding: 6px 10px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
}

.key-label--primary .key-label__text {
  font-weight: 600;
  color: var(--text);
}

.key-select {
  min-width: 180px;
  max-width: 260px;
}

.no-keys-banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.no-keys-banner__body { flex: 1; min-width: 200px; }
.no-keys-banner__body p { margin: 4px 0 0; font-size: 13px; }
.no-keys-banner__cta { flex-shrink: 0; text-decoration: none; }

.chat-body {
  flex: 1;
  display: flex;
  gap: 12px;
  min-height: 0;
}

.session-sidebar {
  width: 240px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: 0;
  overflow: hidden;
}

.session-sidebar__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
}

.session-sidebar__title {
  font-size: 13px;
  font-weight: 600;
  color: var(--muted);
}

.session-list {
  list-style: none;
  margin: 0;
  padding: 6px;
  overflow-y: auto;
  flex: 1;
}

.session-item__btn {
  width: 100%;
  text-align: left;
  padding: 8px 10px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--text);
  cursor: pointer;
}

.session-item__btn:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.05);
}

.session-item.active .session-item__btn {
  background: rgba(99, 102, 241, 0.18);
  border: 1px solid rgba(99, 102, 241, 0.35);
}

.session-item__row {
  display: flex;
  align-items: center;
  gap: 4px;
}

.session-item__title {
  flex: 1;
  font-size: 13px;
  line-height: 1.35;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-item__del {
  flex-shrink: 0;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  opacity: 0;
}

.session-item:hover .session-item__del,
.session-item.active .session-item__del {
  opacity: 1;
}

.session-item__del:hover {
  color: var(--danger);
  background: rgba(248, 81, 73, 0.12);
}

.session-item__meta {
  display: block;
  margin-top: 2px;
  font-size: 10px;
  color: var(--muted);
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-empty {
  padding: 12px;
  font-size: 13px;
  color: var(--muted);
  text-align: center;
}

.chat-layout {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: 0;
  overflow: hidden;
}

.chat-session-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 14px;
  border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}

.chat-session-bar__info {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  min-width: 0;
}

.chat-session-bar__title {
  font-size: 13px;
  font-weight: 600;
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chat-session-bar__model {
  font-size: 11px;
  color: var(--accent-h);
  background: rgba(99, 102, 241, 0.12);
  padding: 2px 8px;
  border-radius: 4px;
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chat-session-bar__tokens {
  font-size: 11px;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

.chat-session-bar__actions {
  display: flex;
  gap: 6px;
  flex-shrink: 0;
}

.btn-danger-text {
  color: var(--danger);
}

.btn-danger-text:hover {
  border-color: var(--danger);
}

.chat-messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.chat-empty {
  color: var(--muted);
  font-size: 14px;
  text-align: center;
  margin: auto;
  max-width: 420px;
}

.chat-bubble {
  max-width: 88%;
  padding: 8px 12px;
  border-radius: 10px;
  font-size: 14px;
  line-height: 1.55;
}

.chat-bubble.user {
  align-self: flex-end;
  background: rgba(99, 102, 241, 0.2);
  border: 1px solid rgba(99, 102, 241, 0.35);
}

.chat-bubble.assistant {
  align-self: flex-start;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid var(--border);
}

.bubble-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
}

.bubble-role {
  font-size: 11px;
  color: var(--muted);
}

.bubble-actions {
  display: flex;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.15s;
}

.chat-bubble:hover .bubble-actions {
  opacity: 1;
}

/* Track C client-side resume (2026-06-21): the ↻ 已恢复 badge
   signals that the assistant reply came from the gateway pending-
   response cache, not a fresh upstream call. Tinted subtly so it
   doesn't compete with the resolved-model / token-count chips. */
.resumed-badge {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 6px;
  font-size: 11px;
  border-radius: 8px;
  background: rgba(64, 158, 255, 0.12);
  color: var(--primary, #409eff);
  border: 1px solid rgba(64, 158, 255, 0.3);
  white-space: nowrap;
  cursor: help;
}

.bubble-btn {
  padding: 1px 6px;
  font-size: 11px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
}

.bubble-btn:hover:not(:disabled) {
  color: var(--text);
  border-color: var(--accent);
}

.bubble-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.bubble-content {
  white-space: pre-wrap;
  word-break: break-word;
}

.cursor-blink {
  animation: blink 1s step-end infinite;
}

@keyframes blink {
  50% { opacity: 0; }
}

.chat-error {
  margin: 0 16px 8px;
}

.chat-input-row {
  display: flex;
  gap: 12px;
  padding: 12px 16px 16px;
  border-top: 1px solid var(--border);
  align-items: flex-end;
}

.chat-input {
  flex: 1;
  resize: vertical;
  min-height: 72px;
  max-height: 200px;
  padding: 10px 12px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 14px;
  font-family: inherit;
}

.send-btn {
  flex-shrink: 0;
  min-width: 88px;
}

.link-inline {
  margin-left: 8px;
  color: var(--accent-h);
}

.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  padding: 16px;
}

.modal-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 10px;
  width: min(560px, 100%);
  max-height: 80vh;
  display: flex;
  flex-direction: column;
}

.modal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
}

.modal-head h3 {
  font-size: 15px;
  font-weight: 600;
}

.modal-close {
  border: none;
  background: transparent;
  color: var(--muted);
  font-size: 20px;
  cursor: pointer;
  line-height: 1;
}

.modal-body {
  padding: 16px;
  overflow-y: auto;
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
}

.modal-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--border);
}

@media (max-width: 768px) {
  .chat-body {
    flex-direction: column;
  }

  .session-sidebar {
    width: 100%;
    max-height: 160px;
  }

  .bubble-actions {
    opacity: 1;
  }
}
</style>
