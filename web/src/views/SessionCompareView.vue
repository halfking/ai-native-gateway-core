<template>
  <div class="session-compare">
    <!-- Header -->
    <div class="compare-header">
      <div class="header-left">
        <h2>会话对比</h2>
        <span class="session-id">会话: {{ sessionId }}</span>
        <span v-if="data?.model_used" class="model-badge">{{ data.model_used }}</span>
      </div>
      <div class="header-right">
        <span :class="['strategy-badge', data?.stats.compression_strategy]">
          {{ strategyLabel }}
        </span>
        <button class="btn btn-sm" @click="refresh">刷新</button>
      </div>
    </div>

    <!-- Context Usage Bar -->
    <div class="context-usage-bar" v-if="data">
      <div class="usage-header">
        <span>上下文使用率</span>
        <span>{{ Math.round(data.context_usage) }}% / {{ data.context_window.toLocaleString() }} tokens</span>
      </div>
      <div class="usage-track">
        <div class="usage-fill" :style="{ width: Math.min(data.context_usage, 100) + '%' }"
             :class="{ warning: data.context_usage >= 70, danger: data.context_usage >= 85 }">
        </div>
      </div>
      <div class="usage-threshold-marker" v-if="data.context_usage >= 80">
        ⚠️ 上下文使用 &ge; 80%，建议执行 Handoff
      </div>
    </div>

    <!-- Stats Summary -->
    <div class="stats-bar" v-if="data">
      <div class="stat-item">
        <label>原始 Token</label>
        <span class="stat-value">{{ data.stats.original_tokens.toLocaleString() }}</span>
      </div>
      <div class="stat-item">
        <label>压缩后 Token</label>
        <span class="stat-value">{{ data.stats.compressed_tokens.toLocaleString() }}</span>
      </div>
      <div class="stat-item saved" v-if="data.is_compressed">
        <label>节约</label>
        <span class="stat-value">-{{ data.stats.saved_tokens.toLocaleString() }} ({{ Math.round(data.stats.saved_percent) }}%)</span>
      </div>
      <div class="stat-item">
        <label>消息数</label>
        <span class="stat-value">{{ data.msg_count }}</span>
      </div>
      <div class="stat-item">
        <label>缓存</label>
        <span class="stat-value">
          {{ data.cache_info.l1_hit ? 'L1' : '' }}
          {{ data.cache_info.l2_hit ? 'L2' : '' }}
          {{ data.cache_info.l3_fallback ? 'L3(DB)' : '' }}
        </span>
      </div>
    </div>

    <!-- Error / Loading -->
    <div v-if="error" class="error-message">{{ error }}</div>
    <div v-if="loading" class="loading">加载中...</div>

    <!-- Four Column Comparison -->
    <div class="four-panel" v-if="data" ref="panelsContainer">
      <!-- Panel 1: Original Session -->
      <div class="panel" ref="originalPanel">
        <div class="panel-header">
          <h3>原会话</h3>
          <span class="panel-count">{{ data.original_msgs.length }} 条消息</span>
          <span v-if="data.cache_info.l1_hit" class="cache-badge">L1✓</span>
          <span v-else-if="data.cache_info.l2_hit" class="cache-badge l2">L2✓</span>
          <span v-else-if="data.cache_info.l3_fallback" class="cache-badge l3">L3(DB)</span>
        </div>
        <div class="panel-body" ref="originalBody" @scroll="syncScroll('original')">
          <div v-for="msg in data.original_msgs" :key="'orig-' + msg.index"
               :class="['msg', msg.role]">
            <div class="msg-role">{{ roleLabel(msg.role) }}</div>
            <div class="msg-content">{{ msg.content }}</div>
            <div v-if="msg.tool_calls" class="msg-tools">{{ msg.tool_calls }}</div>
            <div class="msg-tokens">{{ msg.token_count }} tok</div>
          </div>
          <div v-if="data.original_msgs.length === 0" class="empty-msg">暂无消息</div>
        </div>
      </div>

      <!-- Panel 2: Compressed Session -->
      <div class="panel compressed" ref="compressedPanel">
        <div class="panel-header">
          <h3>压缩后</h3>
          <span class="panel-count">{{ data.compressed_msgs.length }} 条消息</span>
          <span class="compression-badge">{{ strategyLabel }}</span>
        </div>
        <div class="panel-body" ref="compressedBody" @scroll="syncScroll('compressed')">
          <div v-if="!data.is_compressed" class="no-compression-note">
            此会话未压缩（直接转发原会话）
          </div>
          <div v-for="msg in data.compressed_msgs" :key="'comp-' + msg.index"
               :class="['msg', msg.role]">
            <div class="msg-role">{{ roleLabel(msg.role) }}</div>
            <div class="msg-content">{{ msg.content }}</div>
            <div v-if="msg.tool_calls" class="msg-tools">{{ msg.tool_calls }}</div>
            <div class="msg-tokens">{{ msg.token_count }} tok</div>
          </div>
          <div v-if="data.compressed_msgs.length === 0" class="empty-msg">暂无压缩消息</div>
        </div>
      </div>

      <!-- Panel 3: LLM Response -->
      <div class="panel response" ref="responsePanel">
        <div class="panel-header">
          <h3>大模型返回</h3>
          <span class="panel-count">{{ data.response_msgs.length }} 条响应</span>
        </div>
        <div class="panel-body" ref="responseBody" @scroll="syncScroll('response')">
          <div v-for="msg in data.response_msgs" :key="'resp-' + msg.index"
               :class="['msg', msg.role]">
            <div class="msg-role">{{ roleLabel(msg.role) }}</div>
            <div class="msg-content">{{ msg.content }}</div>
            <div class="msg-tokens">{{ msg.token_count }} tok</div>
          </div>
          <div v-if="data.response_msgs.length === 0" class="empty-msg">暂无响应</div>
        </div>
      </div>

      <!-- Panel 4: Cache & Savings -->
      <div class="panel stats-panel" ref="statsPanel">
        <div class="panel-header">
          <h3>缓存 & 节约</h3>
        </div>
        <div class="panel-body stats-body">
          <div class="cache-section">
            <h4>缓存状态</h4>
            <table class="cache-table">
              <tr>
                <td>L1 (内存)</td>
                <td><span :class="data.cache_info.l1_hit ? 'hit' : 'miss'">{{ data.cache_info.l1_hit ? '✓ 命中' : '✗ 未命中' }}</span></td>
              </tr>
              <tr>
                <td>L2 (Redis)</td>
                <td><span :class="data.cache_info.l2_hit ? 'hit' : 'miss'">{{ data.cache_info.l2_hit ? '✓ 命中' : '✗ 未命中' }}</span></td>
              </tr>
              <tr>
                <td>L3 (DB)</td>
                <td><span :class="data.cache_info.l3_fallback ? 'hit' : 'miss'">{{ data.cache_info.l3_fallback ? '✓ 回退' : '—' }}</span></td>
              </tr>
            </table>
          </div>

          <div class="savings-section">
            <h4>Token 节约</h4>
            <table class="savings-table">
              <tr>
                <td>原始</td>
                <td class="num">{{ data.stats.original_tokens.toLocaleString() }}</td>
              </tr>
              <tr>
                <td>压缩后</td>
                <td class="num">{{ data.stats.compressed_tokens.toLocaleString() }}</td>
              </tr>
              <tr v-if="data.is_compressed" class="saved-row">
                <td>节约</td>
                <td class="num saved">-{{ data.stats.saved_tokens.toLocaleString() }} ({{ Math.round(data.stats.saved_percent) }}%)</td>
              </tr>
            </table>
            <div class="strategy-info">
              压缩策略: {{ strategyLabel }}
            </div>
          </div>

          <div class="handoff-section" v-if="data.context_usage >= 80">
            <h4>Handoff</h4>
            <p class="handoff-hint">会话上下文已使用 {{ Math.round(data.context_usage) }}%，建议执行 handoff。</p>
            <div class="handoff-actions">
              <button class="btn btn-primary btn-sm" @click="executeHandoff" :disabled="handoffLoading">
                {{ handoffLoading ? '执行中...' : '执行 Handoff' }}
              </button>
              <button class="btn btn-sm" @click="showNewSessionHint = !showNewSessionHint">
                新会话提示词
              </button>
            </div>
            <div v-if="showNewSessionHint" class="new-session-hint">
              <p>新会话提示词：</p>
              <pre>{{ newSessionHintText }}</pre>
              <button class="btn btn-sm" @click="copyHint">复制</button>
            </div>
            <div v-if="handoffResult" class="handoff-result">
              <h5>Handoff 结果</h5>
              <pre>{{ handoffResult.handoff_summary }}</pre>
              <div v-if="handoffResult.new_session_id" class="new-session">
                新会话: <code>{{ handoffResult.new_session_id }}</code>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import { useRoute } from 'vue-router'
import { getSessionCompare, executeHandoff as callHandoff } from '../api'
import type { SessionCompareData, HandoffResponse } from '../api'

const route = useRoute()
const sessionId = ref((route.query.session_id as string) || '')
const tenantId = ref((route.query.tenant_id as string) || 'default')

const loading = ref(false)
const error = ref('')
const data = ref<SessionCompareData | null>(null)

const showNewSessionHint = ref(false)
const handoffLoading = ref(false)
const handoffResult = ref<HandoffResponse | null>(null)

const panelsContainer = ref<HTMLElement | null>(null)
const originalBody = ref<HTMLElement | null>(null)
const compressedBody = ref<HTMLElement | null>(null)
const responseBody = ref<HTMLElement | null>(null)

const strategyLabel = computed(() => {
  if (!data.value) return ''
  const s = data.value.stats.compression_strategy
  const labels: Record<string, string> = {
    'delta_append': '增量追加',
    'sliding_window_token': '滑动窗口-Token',
    'sliding_window_count': '滑动窗口-消息数',
    'sliding_window_idle': '滑动窗口-闲置',
    'mechanical_trim': '机械裁剪',
    'llm_summary': 'LLM 总结',
    'memora_l1_inject': 'Memora 注入',
  }
  return labels[s] || s
})

const newSessionHintText = `## 新会话提示

前一会话已通过 Handoff 完成压缩和总结。

建议开始新会话：
1. 确认上一步的任务结果
2. 继续未完成的工作
3. 开始新的任务`

let syncing = false

function syncScroll(source: string) {
  if (syncing) return
  syncing = true

  const bodies = [originalBody.value, compressedBody.value, responseBody.value]
  const srcIdx = { original: 0, compressed: 1, response: 2 }[source]
  const srcEl = bodies[srcIdx as number]

  if (srcEl) {
    const ratio = srcEl.scrollTop / (srcEl.scrollHeight - srcEl.clientHeight)
    for (let i = 0; i < bodies.length; i++) {
      if (i !== srcIdx && bodies[i]) {
        const el = bodies[i]!
        el.scrollTop = ratio * (el.scrollHeight - el.clientHeight)
      }
    }
  }

  requestAnimationFrame(() => { syncing = false })
}

function roleLabel(role: string): string {
  const labels: Record<string, string> = {
    'user': '用户',
    'assistant': '助手',
    'system': '系统',
    'tool': '工具',
  }
  return labels[role] || role
}

async function loadData() {
  if (!sessionId.value) return
  loading.value = true
  error.value = ''
  try {
    data.value = await getSessionCompare(sessionId.value, tenantId.value)
  } catch (e: any) {
    error.value = e?.message || '加载失败'
  } finally {
    loading.value = false
  }
}

async function executeHandoff() {
  if (!sessionId.value) return
  handoffLoading.value = true
  try {
    handoffResult.value = await callHandoff({
      session_id: sessionId.value,
      tenant_id: tenantId.value,
      create_new: true,
    })
  } catch (e: any) {
    error.value = 'Handoff 执行失败: ' + (e?.message || '')
  } finally {
    handoffLoading.value = false
  }
}

function copyHint() {
  navigator.clipboard.writeText(newSessionHintText)
}

async function refresh() {
  await loadData()
}

onMounted(async () => {
  // If session_id is in route params instead of query, check that too
  if (!sessionId.value && route.params.sessionId) {
    sessionId.value = route.params.sessionId as string
  }
  if (sessionId.value) {
    await loadData()
  }
})
</script>

<style scoped>
.session-compare {
  padding: 16px;
  height: 100%;
  display: flex;
  flex-direction: column;
  background: #0f172a;
  color: #e2e8f0;
}

.compare-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding-bottom: 12px;
  border-bottom: 1px solid #1e293b;
  margin-bottom: 12px;
}
.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}
.header-left h2 { margin: 0; font-size: 18px; }
.session-id { color: #94a3b8; font-size: 13px; }
.model-badge {
  background: #1e293b;
  color: #60a5fa;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
}
.strategy-badge {
  background: #1e293b;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  color: #34d399;
}

.context-usage-bar {
  margin-bottom: 12px;
}
.usage-header {
  display: flex;
  justify-content: space-between;
  font-size: 12px;
  color: #94a3b8;
  margin-bottom: 4px;
}
.usage-track {
  height: 8px;
  background: #1e293b;
  border-radius: 4px;
  overflow: hidden;
}
.usage-fill {
  height: 100%;
  background: #2563eb;
  border-radius: 4px;
  transition: width 0.3s;
}
.usage-fill.warning { background: #f59e0b; }
.usage-fill.danger { background: #ef4444; }
.usage-threshold-marker {
  font-size: 12px;
  color: #f59e0b;
  margin-top: 4px;
}

.stats-bar {
  display: flex;
  gap: 24px;
  padding: 12px;
  background: #1e293b;
  border-radius: 8px;
  margin-bottom: 12px;
}
.stat-item {
  display: flex;
  flex-direction: column;
}
.stat-item label { font-size: 11px; color: #94a3b8; }
.stat-value { font-size: 16px; font-weight: 600; }
.stat-item.saved .stat-value { color: #34d399; }

.four-panel {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr 320px;
  gap: 12px;
  flex: 1;
  min-height: 0;
}

.panel {
  display: flex;
  flex-direction: column;
  background: #1e293b;
  border-radius: 8px;
  overflow: hidden;
  min-width: 0;
}
.panel-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-bottom: 1px solid #334155;
  background: #1a2332;
  flex-shrink: 0;
}
.panel-header h3 { margin: 0; font-size: 13px; }
.panel-count { font-size: 11px; color: #94a3b8; }
.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 8px;
  scroll-behavior: smooth;
}
.panel-body::-webkit-scrollbar { width: 6px; }
.panel-body::-webkit-scrollbar-track { background: transparent; }
.panel-body::-webkit-scrollbar-thumb { background: #334155; border-radius: 3px; }

.msg {
  padding: 8px;
  margin-bottom: 6px;
  border-radius: 6px;
  font-size: 12px;
  line-height: 1.5;
}
.msg.user { background: #1e3a5f; border-left: 3px solid #3b82f6; }
.msg.assistant { background: #1a2e1a; border-left: 3px solid #22c55e; }
.msg.system { background: #2d1b69; border-left: 3px solid #8b5cf6; }
.msg.tool { background: #3b2f1a; border-left: 3px solid #f59e0b; }

.msg-role { font-size: 10px; color: #94a3b8; margin-bottom: 4px; text-transform: uppercase; }
.msg-content { white-space: pre-wrap; word-break: break-word; }
.msg-tools { margin-top: 4px; padding: 4px; background: #0f172a; border-radius: 4px; font-family: monospace; font-size: 10px; color: #f59e0b; }
.msg-tokens { font-size: 10px; color: #64748b; text-align: right; margin-top: 2px; }
.empty-msg { color: #64748b; text-align: center; padding: 40px; }

.cache-badge {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 3px;
  background: #065f46;
  color: #6ee7b7;
}
.cache-badge.l2 { background: #1e3a5f; color: #60a5fa; }
.cache-badge.l3 { background: #3b2f1a; color: #fbbf24; }

.compression-badge {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 3px;
  background: #1e3a5f;
  color: #60a5fa;
}

.no-compression-note {
  padding: 24px;
  text-align: center;
  color: #94a3b8;
  font-size: 13px;
}

.stats-panel .panel-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.cache-section h4, .savings-section h4, .handoff-section h4 {
  margin: 0 0 8px 0;
  font-size: 12px;
  color: #94a3b8;
}
.cache-table, .savings-table {
  width: 100%;
  font-size: 12px;
}
.cache-table td, .savings-table td {
  padding: 3px 0;
}
.cache-table td:last-child { text-align: right; }
.hit { color: #34d399; }
.miss { color: #ef4444; }
.num { text-align: right; font-family: monospace; }
.saved-row .saved { color: #34d399; }
.strategy-info { margin-top: 8px; font-size: 11px; color: #94a3b8; }

.handoff-hint {
  font-size: 12px;
  color: #f59e0b;
  margin: 0 0 8px 0;
}
.handoff-actions { display: flex; gap: 8px; }
.new-session-hint {
  margin-top: 8px;
  padding: 8px;
  background: #0f172a;
  border-radius: 6px;
}
.new-session-hint pre {
  font-size: 11px;
  white-space: pre-wrap;
  color: #e2e8f0;
}
.handoff-result {
  margin-top: 8px;
  padding: 8px;
  background: #0f172a;
  border-radius: 6px;
}
.handoff-result pre { font-size: 11px; white-space: pre-wrap; }
.handoff-result .new-session { margin-top: 4px; font-size: 12px; }
.handoff-result code { background: #1e293b; padding: 1px 4px; border-radius: 3px; }

.btn {
  padding: 6px 16px;
  border: 1px solid #334155;
  border-radius: 6px;
  background: transparent;
  color: #e2e8f0;
  cursor: pointer;
  font-size: 12px;
}
.btn-primary { background: #2563eb; border-color: #2563eb; }
.btn-sm { padding: 4px 10px; font-size: 11px; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

.loading { text-align: center; padding: 60px; color: #94a3b8; }
.error-message { padding: 12px; background: #451a1a; color: #fca5a5; border-radius: 6px; margin-bottom: 12px; }
</style>