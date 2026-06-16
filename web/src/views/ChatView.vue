<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { getAvailableModels, type PopularModel } from '../api'
import { useGatewayApiKey } from '../composables/useGatewayApiKey'

interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string
}

const { apiKey, loading: keyLoading, error: keyError, resolve: resolveApiKey } = useGatewayApiKey()

const selectedModel = ref('auto')
const popularModels = ref<PopularModel[]>([])
const messages = ref<ChatMessage[]>([])
const input = ref('')
const sending = ref(false)
const sendError = ref('')
const messagesEl = ref<HTMLElement | null>(null)

const baseUrl = computed(() => `${window.location.origin}/v1`)

onMounted(async () => {
  try {
    const data = await getAvailableModels()
    popularModels.value = data.popular ?? []
  } catch {
    popularModels.value = []
  }
})

async function scrollToBottom() {
  await nextTick()
  messagesEl.value?.scrollTo({ top: messagesEl.value.scrollHeight, behavior: 'smooth' })
}

async function send() {
  const text = input.value.trim()
  if (!text || sending.value) return

  sendError.value = ''
  const key = apiKey.value || (await resolveApiKey())
  if (!key) {
    sendError.value = keyError.value || '无法获取 API 密钥'
    return
  }

  messages.value.push({ role: 'user', content: text })
  input.value = ''
  await scrollToBottom()

  sending.value = true
  const assistantIdx = messages.value.length
  messages.value.push({ role: 'assistant', content: '' })

  const payloadMessages = messages.value
    .filter((_, i) => i !== assistantIdx)
    .map(({ role, content }) => ({ role, content }))

  const payload = {
    model: selectedModel.value,
    messages: payloadMessages,
    max_tokens: 2048,
  }

  try {
    const resp = await fetch(`${baseUrl.value}/chat/completions`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${key}`,
      },
      body: JSON.stringify({ ...payload, stream: false }),
    })

    const raw = await resp.text()
    if (!resp.ok) {
      let msg = `HTTP ${resp.status}`
      try {
        const j = JSON.parse(raw)
        msg = j?.error?.message || j?.error || raw || msg
      } catch {
        msg = raw || msg
      }
      throw new Error(msg)
    }

    try {
      const data = JSON.parse(raw)
      const content = data?.choices?.[0]?.message?.content
      messages.value[assistantIdx].content = content || raw.slice(0, 2000) || '（空响应）'
    } catch {
      messages.value[assistantIdx].content = raw.slice(0, 2000) || '（空响应）'
    }
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : '发送失败'
    sendError.value = msg
    if (!messages.value[assistantIdx].content) {
      messages.value[assistantIdx].content = `错误：${msg}`
    }
  } finally {
    sending.value = false
    await scrollToBottom()
  }
}

function clearChat() {
  messages.value = []
  sendError.value = ''
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
    <div v-else-if="keyError" class="alert alert-danger">
      {{ keyError }}
      <RouterLink to="/keys" class="link-inline">前往 API 密钥</RouterLink>
    </div>

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
          <div class="bubble-content">{{ msg.content }}</div>
        </div>
      </div>

      <div v-if="sendError" class="alert alert-danger chat-error">{{ sendError }}</div>

      <div class="chat-input-row">
        <textarea
          v-model="input"
          class="chat-input"
          rows="3"
          placeholder="输入消息…（Enter 发送，Shift+Enter 换行）"
          :disabled="sending || keyLoading"
          @keydown="onKeydown"
        />
        <button
          type="button"
          class="btn btn-primary send-btn"
          :disabled="sending || keyLoading || !input.trim()"
          @click="send"
        >
          {{ sending ? '生成中…' : '发送' }}
        </button>
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
</style>
