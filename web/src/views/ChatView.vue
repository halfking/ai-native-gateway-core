<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { getAvailableModels, type PopularModel } from '../api'
import { chatCompletion } from '../composables/useChatCompletions'
import { useChatSessions } from '../composables/useChatSessions'
import { useGatewayApiKey } from '../composables/useGatewayApiKey'
import GatewayApiKeyPicker from '../components/GatewayApiKeyPicker.vue'

const {
  apiKey,
  loading: keyLoading,
  error: keyError,
  showPicker,
  candidateKeys,
  picking,
  selectedKeyId,
  selectedKeyMeta,
  resolve: resolveApiKey,
  selectKey,
  openPicker,
  formatApiKeyLabel,
} = useGatewayApiKey()
const {
  sessions,
  activeId,
  activeSession,
  switchSession,
  startNewSession,
  updateActive,
  setGwSessionId,
  ensureSessionApiKey,
  clearAllGwSessionIds,
} = useChatSessions()

const popularModels = ref<PopularModel[]>([])
const input = ref('')
const sending = ref(false)
const sendError = ref('')
const messagesEl = ref<HTMLElement | null>(null)

const selectedModel = computed({
  get: () => activeSession.value?.model ?? 'auto',
  set: (v: string) => updateActive({ model: v }),
})

const messages = computed(() => activeSession.value?.messages ?? [])

const currentKeyLabel = computed(() => {
  if (selectedKeyMeta.value) return formatApiKeyLabel(selectedKeyMeta.value)
  const match = candidateKeys.value.find((k) => k.id === selectedKeyId.value)
  return match ? formatApiKeyLabel(match) : selectedKeyId.value ? `密钥 #${selectedKeyId.value}` : ''
})

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

async function onSelectApiKey(id: number) {
  const prevId = selectedKeyId.value
  const ok = await selectKey(id)
  if (ok) {
    if (prevId != null && prevId !== id) {
      clearAllGwSessionIds()
    }
    sendError.value = ''
  }
}

async function onKeySelectorChange(e: Event) {
  const raw = (e.target as HTMLSelectElement).value
  const id = Number.parseInt(raw, 10)
  if (!Number.isFinite(id) || id <= 0) return
  await onSelectApiKey(id)
}

watch(selectedKeyId, (id, prev) => {
  if (id != null && prev != null && id !== prev) {
    clearAllGwSessionIds()
  }
})

async function send() {
  const text = input.value.trim()
  if (!text || sending.value || !activeSession.value) return

  sendError.value = ''
  const key = apiKey.value || (await resolveApiKey())
  if (!key) {
    sendError.value = keyError.value || '无法获取 API 密钥'
    return
  }
  if (!selectedKeyId.value) {
    sendError.value = '无法确定 API 密钥，请切换或重新选择'
    openPicker()
    return
  }

  const { gwSessionId, taskId } = ensureSessionApiKey(selectedKeyId.value)
  const session = activeSession.value!
  const nextMessages = [...session.messages, { role: 'user' as const, content: text }]
  updateActive({ messages: nextMessages })
  input.value = ''
  await scrollToBottom()

  sending.value = true
  const assistantIdx = nextMessages.length
  const withPlaceholder = [...nextMessages, { role: 'assistant' as const, content: '' }]
  updateActive({ messages: withPlaceholder })

  try {
    const result = await chatCompletion({
      apiKey: key,
      model: selectedModel.value,
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
      finalMsgs[assistantIdx] = { role: 'assistant', content: result.content }
      updateActive({ messages: finalMsgs })
    }
    if (result.gwSessionId) {
      setGwSessionId(result.gwSessionId, selectedKeyId.value)
    }
  } catch (e: unknown) {
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

function clearChat() {
  startNewSession(selectedModel.value)
  sendError.value = ''
  input.value = ''
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
        <div v-if="apiKey && !keyLoading" class="key-indicator">
          <span class="key-indicator__label">密钥</span>
          <span class="key-indicator__value" :title="currentKeyLabel">{{ currentKeyLabel || '已加载' }}</span>
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="sending || picking"
            @click="openPicker"
          >
            切换密钥
          </button>
        </div>
        <label v-if="apiKey && candidateKeys.length > 1" class="model-label key-label">
          快速切换
          <select
            class="model-select key-select"
            :value="selectedKeyId ?? ''"
            :disabled="sending || picking || keyLoading"
            @change="onKeySelectorChange"
          >
            <option v-if="!selectedKeyId" disabled value="">选择密钥…</option>
            <option v-for="k in candidateKeys" :key="k.id" :value="k.id">
              {{ formatApiKeyLabel(k) }}
            </option>
          </select>
        </label>
        <label class="model-label">
          模型
          <select v-model="selectedModel" class="model-select" :disabled="sending">
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
          清空
        </button>
      </div>
    </div>

    <div v-if="keyLoading" class="alert alert-info">正在加载 API 密钥…</div>
    <GatewayApiKeyPicker
      v-else-if="showPicker"
      :keys="candidateKeys"
      :loading="picking"
      :error="keyError"
      @select="onSelectApiKey"
    />
    <div v-else-if="keyError" class="alert alert-danger">
      {{ keyError }}
      <RouterLink to="/keys?redirect=/chat" class="link-inline">前往 API 密钥</RouterLink>
    </div>

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
              <span class="session-item__title">{{ s.title }}</span>
              <span class="session-item__meta">{{ formatSessionTime(s.updatedAt) }}</span>
            </button>
          </li>
          <li v-if="!sessions.length" class="session-empty">暂无会话</li>
        </ul>
      </aside>

      <div class="chat-layout card">
        <div ref="messagesEl" class="chat-messages">
          <div v-if="!messages.length" class="chat-empty">
            <p>输入消息开始对话。选择 <code>auto</code> 将由网关自动挑选合适模型。</p>
          </div>
          <div
            v-for="(msg, i) in messages"
            :key="i"
            class="chat-bubble"
            :class="msg.role"
          >
            <div class="bubble-role">{{ msg.role === 'user' ? '你' : '助手' }}</div>
            <div class="bubble-content">{{ msg.content }}<span v-if="sending && i === messages.length - 1 && msg.role === 'assistant' && !msg.content" class="cursor-blink">▍</span></div>
          </div>
        </div>

        <div v-if="sendError" class="alert alert-danger chat-error">{{ sendError }}</div>

        <div class="chat-input-row">
          <textarea
            v-model="input"
            class="chat-input"
            rows="3"
            placeholder="输入消息…（Enter 发送，Shift+Enter 换行）"
            :disabled="sending || keyLoading || showPicker"
            @keydown="onKeydown"
          />
          <button
            type="button"
            class="btn btn-primary send-btn"
            :disabled="sending || keyLoading || showPicker || !input.trim()"
            @click="send"
          >
            {{ sending ? '生成中…' : '发送' }}
          </button>
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
}

.model-label {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--muted);
}

.model-select {
  min-width: 200px;
  padding: 6px 10px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text);
  font-size: 13px;
}

.key-label {
  flex-wrap: wrap;
}

.key-select {
  min-width: 180px;
  max-width: 280px;
}

.key-indicator {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  border-radius: 8px;
  border: 1px solid rgba(99, 102, 241, 0.35);
  background: rgba(99, 102, 241, 0.08);
  max-width: min(360px, 100%);
}

.key-indicator__label {
  font-size: 12px;
  color: var(--muted);
  flex-shrink: 0;
}

.key-indicator__value {
  font-size: 13px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  min-width: 0;
}

.chat-body {
  flex: 1;
  display: flex;
  gap: 12px;
  min-height: 0;
}

.session-sidebar {
  width: 220px;
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
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.session-item__btn:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.05);
}

.session-item.active .session-item__btn {
  background: rgba(99, 102, 241, 0.18);
  border: 1px solid rgba(99, 102, 241, 0.35);
}

.session-item__title {
  font-size: 13px;
  line-height: 1.35;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-item__meta {
  font-size: 11px;
  color: var(--muted);
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

.chat-messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.chat-empty {
  color: var(--muted);
  font-size: 14px;
  text-align: center;
  margin: auto;
  max-width: 420px;
}

.chat-bubble {
  max-width: 85%;
  padding: 10px 14px;
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

.bubble-role {
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 4px;
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

@media (max-width: 768px) {
  .chat-body {
    flex-direction: column;
  }

  .session-sidebar {
    width: 100%;
    max-height: 140px;
  }
}
</style>
