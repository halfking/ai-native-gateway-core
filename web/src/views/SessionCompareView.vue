<template>
  <div class="page-layout">
    <div class="page-header">
      <div>
        <h2>会话对比</h2>
        <p class="text-muted" v-if="sessionId">会话: {{ sessionId }} · {{ data?.model_used || '' }}</p>
        <p class="text-muted" v-else>请输入会话 ID 查看对比</p>
      </div>
      <div class="header-actions">
        <input
          v-model="sessionIdInput"
          class="cf-input"
          placeholder="输入会话 ID..."
          @keyup.enter="loadByInput"
        />
        <button class="btn btn-primary" @click="loadByInput">查看</button>
      </div>
    </div>

    <!-- Context Usage Bar -->
    <div class="card" v-if="data" style="margin-bottom: 12px;">
      <div style="display: flex; justify-content: space-between; font-size: 12px; color: var(--muted); margin-bottom: 6px;">
        <span>上下文使用率</span>
        <span>{{ Math.round(data.context_usage) }}% / {{ (data.context_window || 128000).toLocaleString() }} tokens</span>
      </div>
      <div style="height: 8px; background: var(--border); border-radius: 4px; overflow: hidden;">
        <div :style="{ width: Math.min(data.context_usage, 100) + '%', height: '100%', background: data.context_usage >= 85 ? 'var(--danger)' : data.context_usage >= 70 ? 'var(--warning)' : 'var(--accent)', borderRadius: '4px', transition: 'width 0.3s' }"></div>
      </div>
      <div v-if="data.context_usage >= 80" class="alert alert-warning" style="margin-top: 8px; font-size: 12px;">
        上下文使用率 ≥ 80%，建议执行 Handoff
      </div>
    </div>

    <!-- Stats Bar -->
    <div class="card" v-if="data" style="margin-bottom: 12px; display: flex; gap: 32px; flex-wrap: wrap;">
      <div><div class="cell-line2">原始 Token</div><div class="cell-line1">{{ data.stats.original_tokens.toLocaleString() }}</div></div>
      <div><div class="cell-line2">压缩后 Token</div><div class="cell-line1">{{ data.stats.compressed_tokens.toLocaleString() }}</div></div>
      <div v-if="data.is_compressed"><div class="cell-line2">节约</div><div class="cell-line1" style="color: var(--success);">-{{ data.stats.saved_tokens.toLocaleString() }} ({{ Math.round(data.stats.saved_percent) }}%)</div></div>
      <div><div class="cell-line2">消息数</div><div class="cell-line1">{{ data.msg_count }}</div></div>
      <div><div class="cell-line2">策略</div><div class="cell-line1"><span class="badge badge-blue">{{ strategyLabel }}</span></div></div>
      <div><div class="cell-line2">缓存</div><div class="cell-line1">{{ cacheLabel }}</div></div>
    </div>

    <!-- Loading & Error -->
    <div v-if="loading" class="empty" style="padding: 60px;">加载中...</div>
    <div v-if="error" class="alert alert-danger" style="margin-bottom: 12px;">{{ error }}</div>
    <div v-if="!sessionId && !loading" class="empty" style="padding: 60px;">在上方输入会话 ID 查看对比</div>

    <!-- Four Panel Comparison -->
    <div class="four-panel" v-if="data">
      <!-- Panel 1: Original -->
      <div class="card" style="display: flex; flex-direction: column; min-width: 0; padding: 0; overflow: hidden;">
        <div class="card-header" style="flex-shrink: 0;">
          <span style="font-weight: 600;">原会话</span>
          <span class="text-muted" style="font-size: 12px;">{{ data.original_msgs.length }} 条</span>
        </div>
        <div class="msg-scroll" ref="p1" @scroll="syncScroll(1)">
          <div v-for="msg in data.original_msgs" :key="'o'+msg.index" :class="['msg-row', msg.role]">
            <div class="role-tag">{{ roleLabel(msg.role) }}</div>
            <div class="msg-text">{{ msg.content || '(空)' }}</div>
            <div v-if="msg.tool_calls" class="tool-preview">{{ msg.tool_calls }}</div>
            <div class="text-muted" style="text-align:right;font-size:10px;">{{ msg.token_count }} tok</div>
          </div>
          <div v-if="!data.original_msgs.length" class="empty">无原始消息</div>
        </div>
      </div>

      <!-- Panel 2: Compressed -->
      <div class="card" style="display: flex; flex-direction: column; min-width: 0; padding: 0; overflow: hidden;">
        <div class="card-header" style="flex-shrink: 0;">
          <span style="font-weight: 600;">压缩后</span>
          <span class="text-muted" style="font-size: 12px;">{{ data.compressed_msgs.length }} 条</span>
          <span v-if="data.is_compressed" class="badge badge-blue">{{ strategyLabel }}</span>
          <span v-else class="badge badge-gray">未压缩</span>
        </div>
        <div class="msg-scroll" ref="p2" @scroll="syncScroll(2)">
          <div v-if="!data.is_compressed" class="empty" style="padding: 24px;">此会话未压缩，转发原会话</div>
          <div v-for="msg in data.compressed_msgs" :key="'c'+msg.index" :class="['msg-row', msg.role]">
            <div class="role-tag">{{ roleLabel(msg.role) }}</div>
            <div class="msg-text">{{ msg.content || '(空)' }}</div>
            <div v-if="msg.tool_calls" class="tool-preview">{{ msg.tool_calls }}</div>
            <div class="text-muted" style="text-align:right;font-size:10px;">{{ msg.token_count }} tok</div>
          </div>
          <div v-if="!data.compressed_msgs.length && data.is_compressed" class="empty">无压缩消息</div>
        </div>
      </div>

      <!-- Panel 3: Response -->
      <div class="card" style="display: flex; flex-direction: column; min-width: 0; padding: 0; overflow: hidden;">
        <div class="card-header" style="flex-shrink: 0;">
          <span style="font-weight: 600;">大模型返回</span>
          <span class="text-muted" style="font-size: 12px;">{{ data.response_msgs.length }} 条</span>
        </div>
        <div class="msg-scroll" ref="p3" @scroll="syncScroll(3)">
          <div v-for="msg in data.response_msgs" :key="'r'+msg.index" :class="['msg-row', msg.role]">
            <div class="role-tag">{{ roleLabel(msg.role) }}</div>
            <div class="msg-text">{{ msg.content || '(空)' }}</div>
            <div class="text-muted" style="text-align:right;font-size:10px;">{{ msg.token_count }} tok</div>
          </div>
          <div v-if="!data.response_msgs.length" class="empty">暂无响应</div>
        </div>
      </div>

      <!-- Panel 4: Cache & Handoff -->
      <div class="card" style="display: flex; flex-direction: column; min-width: 0; padding: 0; overflow: hidden;">
        <div class="card-header" style="flex-shrink: 0;">
          <span style="font-weight: 600;">缓存 & 节约</span>
        </div>
        <div class="msg-scroll" style="padding: 12px;">
          <!-- Cache -->
          <div class="card-title" style="margin-bottom: 8px;">缓存状态</div>
          <table class="data-table" style="margin-bottom: 16px;">
            <tr><td>L1 (内存)</td><td style="text-align:right"><span :class="data.cache_info.l1_hit ? 'badge badge-green' : 'badge badge-red'">{{ data.cache_info.l1_hit ? '✓ 命中' : '✗ 未命中' }}</span></td></tr>
            <tr><td>L2 (Redis)</td><td style="text-align:right"><span :class="data.cache_info.l2_hit ? 'badge badge-green' : 'badge badge-red'">{{ data.cache_info.l2_hit ? '✓ 命中' : '✗ 未命中' }}</span></td></tr>
            <tr><td>L3 (DB)</td><td style="text-align:right"><span :class="data.cache_info.l3_fallback ? 'badge badge-yellow' : 'badge badge-gray'">{{ data.cache_info.l3_fallback ? '✓ 回退' : '—' }}</span></td></tr>
          </table>

          <!-- Token Savings -->
          <div class="card-title" style="margin-bottom: 8px;">Token 节约</div>
          <table class="data-table" style="margin-bottom: 16px;">
            <tr><td>原始</td><td style="text-align:right;font-family:monospace">{{ data.stats.original_tokens.toLocaleString() }}</td></tr>
            <tr><td>压缩后</td><td style="text-align:right;font-family:monospace">{{ data.stats.compressed_tokens.toLocaleString() }}</td></tr>
            <tr v-if="data.is_compressed"><td style="color:var(--success)">节约</td><td style="text-align:right;font-family:monospace;color:var(--success)">-{{ data.stats.saved_tokens.toLocaleString() }} ({{ Math.round(data.stats.saved_percent) }}%)</td></tr>
            <tr><td>策略</td><td style="text-align:right"><span class="badge badge-blue">{{ strategyLabel }}</span></td></tr>
          </table>

          <!-- Handoff -->
          <div v-if="data.context_usage >= 80" style="border-top: 1px solid var(--border); padding-top: 12px;">
            <div class="card-title" style="margin-bottom: 8px; color: var(--warning);">⚠️ Handoff 建议</div>
            <p style="font-size:12px;color:var(--muted);margin-bottom:8px;">
              会话上下文已使用 {{ Math.round(data.context_usage) }}%。执行 Handoff 会压缩当前会话并生成新会话提示词。
            </p>
            <div style="display:flex;gap:8px;flex-wrap:wrap;">
              <button class="btn btn-warning btn-sm" @click="execHandoff" :disabled="handoffLoading">
                {{ handoffLoading ? '执行中...' : '执行 Handoff' }}
              </button>
              <button class="btn btn-sm" @click="showHint = !showHint">新会话提示词</button>
              <button class="btn btn-sm" @click="copyCompareUrl">复制链接</button>
            </div>
            <div v-if="showHint" class="card" style="margin-top:8px;padding:8px;font-size:12px;">
              <p style="margin-bottom:4px;">新会话提示词（可复制给客户端）：</p>
              <pre style="background:var(--bg);padding:8px;border-radius:4px;white-space:pre-wrap;">{{ newSessionHint }}</pre>
              <button class="btn btn-sm" @click="copyHint">复制</button>
            </div>
            <div v-if="handoffResult" class="card" style="margin-top:8px;padding:8px;">
              <div class="card-title" style="margin-bottom:4px;">Handoff 结果</div>
              <pre style="font-size:11px;white-space:pre-wrap;">{{ handoffResult.handoff_summary }}</pre>
              <div v-if="handoffResult.new_session_id" style="margin-top:4px;">
                新会话: <code style="background:var(--bg);padding:1px 4px;border-radius:3px;">{{ handoffResult.new_session_id }}</code>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { getSessionCompare, executeHandoff as callHandoff } from '../api'
import type { SessionCompareData, HandoffResponse } from '../api'

const route = useRoute()
const sessionId = ref('')
const sessionIdInput = ref('')
const data = ref<SessionCompareData | null>(null)
const loading = ref(false)
const error = ref('')
const showHint = ref(false)
const handoffLoading = ref(false)
const handoffResult = ref<HandoffResponse | null>(null)

const p1 = ref<HTMLElement | null>(null)
const p2 = ref<HTMLElement | null>(null)
const p3 = ref<HTMLElement | null>(null)

const strategyLabel = computed(() => {
  const s = data.value?.stats?.compression_strategy || ''
  const m: Record<string, string> = {
    delta_append: '增量追加', sliding_window_token: '滑动窗口', sliding_window_count: '消息数触发',
    sliding_window_idle: '闲置触发', mechanical_trim: '机械裁剪', llm_summary: 'LLM 总结', memora_l1_inject: 'Memora'
  }
  return m[s] || s || '—'
})
const cacheLabel = computed(() => {
  const c = data.value?.cache_info
  if (!c) return '—'
  return [c.l1_hit && 'L1✓', c.l2_hit && 'L2✓', c.l3_fallback && 'DB✓'].filter(Boolean).join(' ') || '未缓存'
})

const newSessionHint = `提示客户端开始新会话：
前一会话已执行 Handoff，上下文已压缩。
可以确认上一步结果后继续，或开始全新任务。`

let syncing = false
function syncScroll(src: number) {
  if (syncing) return
  syncing = true
  const els = [p1.value, p2.value, p3.value]
  const srcEl = els[src - 1]
  if (!srcEl) { syncing = false; return }
  const ratio = srcEl.scrollTop / (srcEl.scrollHeight - srcEl.clientHeight) || 0
  for (let i = 0; i < els.length; i++) {
    if (i !== src - 1 && els[i]) {
      const el = els[i]!
      el.scrollTop = ratio * (el.scrollHeight - el.clientHeight)
    }
  }
  requestAnimationFrame(() => { syncing = false })
}

function roleLabel(r: string) {
  return { user: '用户', assistant: '助手', system: '系统', tool: '工具' }[r] || r
}

async function loadData() {
  if (!sessionId.value) return
  loading.value = true
  error.value = ''
  handoffResult.value = null
  try {
    data.value = await getSessionCompare(sessionId.value)
  } catch (e: any) {
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

function loadByInput() {
  const v = sessionIdInput.value.trim()
  if (v) {
    sessionId.value = v
    loadData()
  }
}

async function execHandoff() {
  if (!sessionId.value) return
  handoffLoading.value = true
  try {
    handoffResult.value = await callHandoff({ session_id: sessionId.value, create_new: true })
  } catch (e: any) {
    error.value = 'Handoff 失败: ' + (e?.message || '')
  } finally {
    handoffLoading.value = false
  }
}

function copyHint() { navigator.clipboard.writeText(newSessionHint) }
function copyCompareUrl() {
  const url = `${location.origin}${location.pathname}?session_id=${sessionId.value}`
  navigator.clipboard.writeText(url)
}

onMounted(() => {
  if (route.query.session_id) {
    sessionId.value = route.query.session_id as string
    sessionIdInput.value = sessionId.value
    loadData()
  }
})
</script>

<style scoped>
.page-layout { padding: 16px; max-width: 1600px; margin: 0 auto; }
.header-actions { display: flex; gap: 8px; align-items: center; }
.cf-input { background: var(--card); border: 1px solid var(--border); border-radius: var(--radius); padding: 6px 12px; color: var(--text); font-size: 13px; width: 280px; }

.four-panel { display: grid; grid-template-columns: 1fr 1fr 1fr 320px; gap: 12px; flex: 1; min-height: 0; min-height: 60vh; }
.card-header { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--border); background: var(--bg-subtle); }
.card-title { font-size: 12px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: 0.05em; }

.msg-scroll { flex: 1; overflow-y: auto; padding: 8px; }
.msg-scroll::-webkit-scrollbar { width: 6px; }
.msg-scroll::-webkit-scrollbar-track { background: transparent; }
.msg-scroll::-webkit-scrollbar-thumb { background: var(--border); border-radius: 3px; }

.msg-row { padding: 8px; margin-bottom: 6px; border-radius: 6px; font-size: 12px; line-height: 1.5; }
.msg-row.user { background: color-mix(in srgb, var(--accent) 10%, var(--card)); border-left: 3px solid var(--accent); }
.msg-row.assistant { background: color-mix(in srgb, var(--success) 8%, var(--card)); border-left: 3px solid var(--success); }
.msg-row.system { background: color-mix(in srgb, #8b5cf6 8%, var(--card)); border-left: 3px solid #8b5cf6; }
.msg-row.tool { background: color-mix(in srgb, var(--warning) 8%, var(--card)); border-left: 3px solid var(--warning); }
.role-tag { font-size: 10px; color: var(--muted); margin-bottom: 4px; text-transform: uppercase; }
.msg-text { white-space: pre-wrap; word-break: break-word; }
.tool-preview { margin-top: 4px; padding: 4px; background: var(--bg); border-radius: 4px; font-family: monospace; font-size: 10px; color: var(--warning); }

.text-muted { color: var(--muted); }
.badge { font-size: 10px; padding: 1px 6px; border-radius: 4px; }
.badge-blue { background: color-mix(in srgb, var(--accent) 20%, transparent); color: var(--accent-h); }
.badge-green { background: color-mix(in srgb, var(--success) 20%, transparent); color: var(--success); }
.badge-red { background: color-mix(in srgb, var(--danger) 20%, transparent); color: var(--danger); }
.badge-yellow { background: color-mix(in srgb, var(--warning) 20%, transparent); color: var(--warning); }
.badge-gray { background: var(--border); color: var(--muted); }

@media (max-width: 1100px) {
  .four-panel { grid-template-columns: 1fr 1fr; }
}
@media (max-width: 700px) {
  .four-panel { grid-template-columns: 1fr; }
}
</style>