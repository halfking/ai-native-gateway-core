<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

interface Session {
  session_id: string
  tenant_id: string
  api_key_id: number
  status: string
  created_at: string
  last_active: string
  last_request_at?: string
  total_turns: number
  total_prompt_tokens: number
  total_completion_tokens: number
  total_cost_usd_cents: number
  current_credential_id: number
  current_model: string
  current_provider: string
  title?: string
  annotation?: string
  tags?: string[]
  health_grade?: string
  health_score?: number
}

interface CredRotation {
  credential_id: number
  model: string
  provider: string
  started_at: string
  ended_at?: string
  turns: number
  prompt_tokens: number
  completion_tokens: number
  cost_usd_cents: number
  switch_reason: string
  fp_slot_index?: number
}

const sessions = ref<Session[]>([])
const loading = ref(false)
const error = ref('')
const selectedSession = ref<Session | null>(null)
const rotations = ref<CredRotation[]>([])
const showDetail = ref(false)
const errorHighlightedSessions = ref<Set<string>>(new Set())

// SSE EventSource
let eventSource: EventSource | null = null

// 状态颜色映射（6 个对外状态）
const statusConfig: Record<string, { color: string; label: string; icon: string }> = {
  active: { color: 'badge-success', label: '运行中', icon: '🟢' },
  stopped: { color: 'badge-neutral', label: '已停止', icon: '⚫' },
  recovered: { color: 'badge-info', label: '已恢复', icon: '🔵' },
  waiting: { color: 'badge-warning', label: '等待中', icon: '🟡' },
  error: { color: 'badge-error', label: '异常', icon: '🔴' },
  expired: { color: 'badge-ghost', label: '已过期', icon: '⚪' }
}

// 健康等级颜色映射（A-F）
const healthGradeConfig: Record<string, { color: string; label: string }> = {
  A: { color: 'badge-success', label: '优秀' },
  B: { color: 'badge-primary', label: '良好' },
  C: { color: 'badge-warning', label: '一般' },
  D: { color: 'badge-warning', label: '较差' },
  F: { color: 'badge-error', label: '异常' }
}

// 判断是否为僵尸会话（>1h 无活动且仍在运行）
function isZombieSession(session: Session): boolean {
  if (session.status !== 'active') return false
  const lastActive = session.last_request_at || session.last_active
  if (!lastActive) return false
  
  const lastActiveTime = new Date(lastActive).getTime()
  const now = Date.now()
  const hourInMs = 60 * 60 * 1000
  
  return (now - lastActiveTime) > hourInMs
}

// 计算排序后的会话列表（异常 > 等待 > 运行中按成本降序 > 已停止/恢复）
const sortedSessions = computed(() => {
  const priorityOrder = { error: 1, waiting: 2, active: 3, recovered: 4, stopped: 5, expired: 6 }
  
  return [...sessions.value].sort((a, b) => {
    const priorityA = priorityOrder[a.status as keyof typeof priorityOrder] || 99
    const priorityB = priorityOrder[b.status as keyof typeof priorityOrder] || 99
    
    if (priorityA !== priorityB) {
      return priorityA - priorityB
    }
    
    // 同优先级按成本降序
    return (b.total_cost_usd_cents || 0) - (a.total_cost_usd_cents || 0)
  })
})

async function loadSessions() {
  loading.value = true
  error.value = ''
  try {
    const resp = await fetch('/api/admin/sessions', {
      credentials: 'include'
    })
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}`)
    }
    const data = await resp.json()
    sessions.value = data.sessions || []
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load sessions'
  } finally {
    loading.value = false
  }
}

// 局部刷新单个会话行
async function refreshSessionRow(sessionId: string) {
  try {
    const resp = await fetch(`/api/admin/sessions/${sessionId}`, {
      credentials: 'include'
    })
    if (!resp.ok) return
    
    const data = await resp.json()
    const session = data.session
    
    // 更新现有会话或添加新会话
    const index = sessions.value.findIndex(s => s.session_id === sessionId)
    if (index !== -1) {
      sessions.value[index] = session
    } else {
      sessions.value.push(session)
    }
  } catch (e) {
    console.error('Failed to refresh session row:', e)
  }
}

// 高亮异常会话
function highlightSessionError(sessionId: string) {
  errorHighlightedSessions.value.add(sessionId)
  
  // 3秒后移除高亮
  setTimeout(() => {
    errorHighlightedSessions.value.delete(sessionId)
  }, 3000)
}

// 初始化 SSE 连接
function initSSE() {
  if (eventSource) {
    eventSource.close()
  }
  
  eventSource = new EventSource('/api/admin/live-stream')
  
  eventSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data)
      
      if (!data.gw_session_id) return
      
      // 处理不同类型的 SSE 事件
      switch (data.type) {
        case 'session.error':
          highlightSessionError(data.gw_session_id)
          break
        case 'session.stopped':
        case 'session.recovered':
        case 'session.waiting':
        case 'request.completed':
          // 所有事件都触发局部刷新
          break
      }
      
      // 局部刷新该会话
      refreshSessionRow(data.gw_session_id)
    } catch (e) {
      console.error('Failed to parse SSE event:', e)
    }
  }
  
  eventSource.onerror = (e) => {
    console.error('SSE connection error:', e)
    // EventSource 会自动重连
  }
}

// 关闭 SSE 连接
function closeSSE() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
}

async function viewDetail(session: Session) {
  selectedSession.value = session
  showDetail.value = true
  
  // 加载凭据轮换历史
  try {
    const resp = await fetch(`/api/admin/sessions/${session.session_id}/cred-rotations`, {
      credentials: 'include'
    })
    if (resp.ok) {
      const data = await resp.json()
      rotations.value = data.rotations || []
    }
  } catch (e) {
    console.error('Failed to load rotations:', e)
  }
}

async function stopSession(sessionId: string, reason: string = 'admin_stop') {
  if (!confirm('确定要停止此会话吗？')) return
  
  try {
    const resp = await fetch(`/api/admin/sessions/${sessionId}/stop?reason=${reason}`, {
      method: 'POST',
      credentials: 'include'
    })
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}`)
    }
    // SSE 会自动推送更新，不需要手动重载
  } catch (e: unknown) {
    alert('停止会话失败: ' + (e instanceof Error ? e.message : 'Unknown error'))
  }
}

async function recoverSession(sessionId: string) {
  try {
    const resp = await fetch(`/api/admin/sessions/${sessionId}/recover`, {
      method: 'POST',
      credentials: 'include'
    })
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}`)
    }
    // SSE 会自动推送更新，不需要手动重载
  } catch (e: unknown) {
    alert('恢复会话失败: ' + (e instanceof Error ? e.message : 'Unknown error'))
  }
}

function formatTime(ts: string) {
  if (!ts) return '-'
  const date = new Date(ts)
  const now = Date.now()
  const diff = now - date.getTime()
  
  // 相对时间显示
  const minutes = Math.floor(diff / 60000)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)
  
  if (minutes < 1) return '刚刚'
  if (minutes < 60) return `${minutes}分钟前`
  if (hours < 24) return `${hours}小时前`
  if (days < 7) return `${days}天前`
  
  return date.toLocaleString()
}

function formatCost(cents: number) {
  return '$' + (cents / 10000).toFixed(4)
}

function closeDetail() {
  showDetail.value = false
  selectedSession.value = null
  rotations.value = []
}

onMounted(() => {
  loadSessions()
  initSSE()
})

onUnmounted(() => {
  closeSSE()
})
</script>

<template>
  <div class="p-6">
    <div class="flex justify-between items-center mb-6">
      <h1 class="text-2xl font-bold">会话管理</h1>
      <button @click="loadSessions" class="btn btn-primary">
        刷新
      </button>
    </div>

    <div v-if="error" class="alert alert-error mb-4">
      {{ error }}
    </div>

    <div v-if="loading" class="flex justify-center py-8">
      <span class="loading loading-spinner loading-lg"></span>
    </div>

    <div v-else class="overflow-x-auto">
      <table class="table table-zebra w-full">
        <thead>
          <tr>
            <th>状态</th>
            <th>标题</th>
            <th>Session ID</th>
            <th>租户</th>
            <th>轮次</th>
            <th>费用</th>
            <th>Tokens</th>
            <th>当前模型</th>
            <th>健康等级</th>
            <th>最后活跃</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr 
            v-for="s in sortedSessions" 
            :key="s.session_id"
            :class="{
              'bg-error bg-opacity-20': errorHighlightedSessions.has(s.session_id),
              'opacity-50': isZombieSession(s)
            }"
          >
            <td>
              <div class="flex items-center gap-2">
                <span 
                  :class="['badge', statusConfig[s.status]?.color || 'badge-ghost']"
                  :title="statusConfig[s.status]?.label"
                >
                  {{ statusConfig[s.status]?.icon || '⚪' }}
                  {{ statusConfig[s.status]?.label || s.status }}
                </span>
                <span v-if="isZombieSession(s)" class="badge badge-warning badge-sm">
                  ⚠ 僵尸
                </span>
              </div>
            </td>
            <td>
              <div class="max-w-xs truncate" :title="s.title">
                {{ s.title || '（未命名）' }}
              </div>
            </td>
            <td>
              <code class="text-xs">{{ s.session_id.substring(0, 16) }}...</code>
            </td>
            <td class="text-xs">{{ s.tenant_id }}</td>
            <td>{{ s.total_turns }}</td>
            <td>{{ formatCost(s.total_cost_usd_cents) }}</td>
            <td class="text-xs">
              {{ s.total_prompt_tokens }} / {{ s.total_completion_tokens }}
            </td>
            <td class="text-xs">{{ s.current_model }}</td>
            <td>
              <span 
                v-if="s.health_grade"
                :class="['badge', healthGradeConfig[s.health_grade]?.color || 'badge-ghost']"
                :title="`健康分: ${s.health_score || 'N/A'} - ${healthGradeConfig[s.health_grade]?.label || ''}`"
              >
                {{ s.health_grade }}
              </span>
              <span v-else class="badge badge-ghost">-</span>
            </td>
            <td class="text-xs">
              {{ formatTime(s.last_request_at || s.last_active) }}
            </td>
            <td>
              <div class="flex gap-1">
                <button @click="viewDetail(s)" class="btn btn-xs btn-info">
                  详情
                </button>
                <button 
                  v-if="s.status === 'active'" 
                  @click="stopSession(s.session_id)" 
                  class="btn btn-xs btn-warning"
                >
                  停止
                </button>
                <button 
                  v-if="s.status === 'stopped' || s.status === 'error'" 
                  @click="recoverSession(s.session_id)" 
                  class="btn btn-xs btn-success"
                >
                  恢复
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="sortedSessions.length === 0" class="text-center py-8 text-gray-500">
        暂无会话
      </div>
    </div>

    <!-- 详情模态框 -->
    <dialog :open="showDetail" class="modal">
      <div class="modal-box w-11/12 max-w-5xl">
        <h3 class="font-bold text-lg mb-4">
          会话详情: {{ selectedSession?.session_id }}
        </h3>

        <div v-if="selectedSession" class="space-y-4">
          <!-- 基础信息 -->
          <div class="grid grid-cols-2 gap-4">
            <div>
              <label class="label font-bold">状态</label>
              <span :class="['badge', statusConfig[selectedSession.status]?.color]">
                {{ statusConfig[selectedSession.status]?.icon }}
                {{ statusConfig[selectedSession.status]?.label || selectedSession.status }}
              </span>
            </div>
            <div>
              <label class="label font-bold">健康等级</label>
              <span 
                v-if="selectedSession.health_grade"
                :class="['badge', healthGradeConfig[selectedSession.health_grade]?.color]"
              >
                {{ selectedSession.health_grade }} - {{ healthGradeConfig[selectedSession.health_grade]?.label }}
                <span v-if="selectedSession.health_score" class="ml-1">
                  ({{ selectedSession.health_score }}/100)
                </span>
              </span>
              <span v-else>-</span>
            </div>
            <div>
              <label class="label font-bold">租户</label>
              <p>{{ selectedSession.tenant_id }}</p>
            </div>
            <div>
              <label class="label font-bold">API Key ID</label>
              <p>{{ selectedSession.api_key_id }}</p>
            </div>
            <div>
              <label class="label font-bold">总轮次</label>
              <p>{{ selectedSession.total_turns }}</p>
            </div>
            <div>
              <label class="label font-bold">Prompt Tokens</label>
              <p>{{ selectedSession.total_prompt_tokens }}</p>
            </div>
            <div>
              <label class="label font-bold">Completion Tokens</label>
              <p>{{ selectedSession.total_completion_tokens }}</p>
            </div>
            <div>
              <label class="label font-bold">总费用</label>
              <p>{{ formatCost(selectedSession.total_cost_usd_cents) }}</p>
            </div>
            <div>
              <label class="label font-bold">当前凭据</label>
              <p>{{ selectedSession.current_credential_id }}</p>
            </div>
            <div>
              <label class="label font-bold">当前模型</label>
              <p>{{ selectedSession.current_model }}</p>
            </div>
          </div>

          <!-- 标题和标注 -->
          <div v-if="selectedSession.title">
            <label class="label font-bold">标题</label>
            <p>{{ selectedSession.title }}</p>
          </div>
          <div v-if="selectedSession.annotation">
            <label class="label font-bold">标注</label>
            <p>{{ selectedSession.annotation }}</p>
          </div>
          <div v-if="selectedSession.tags && selectedSession.tags.length">
            <label class="label font-bold">标签</label>
            <div class="flex gap-2">
              <span v-for="tag in selectedSession.tags" :key="tag" class="badge badge-outline">
                {{ tag }}
              </span>
            </div>
          </div>

          <!-- 凭据轮换历史 -->
          <div>
            <label class="label font-bold">凭据轮换历史 ({{ rotations.length }})</label>
            <div class="overflow-x-auto">
              <table class="table table-sm">
                <thead>
                  <tr>
                    <th>凭据ID</th>
                    <th>模型</th>
                    <th>供应商</th>
                    <th>开始时间</th>
                    <th>结束时间</th>
                    <th>轮次</th>
                    <th>Tokens</th>
                    <th>费用</th>
                    <th>切换原因</th>
                  </tr>
                </thead>
                <tbody>
                  <tr 
                    v-for="(r, idx) in rotations" 
                    :key="idx"
                    :class="{
                      'bg-error bg-opacity-20': r.turns === 0 && r.switch_reason.includes('failure')
                    }"
                  >
                    <td>{{ r.credential_id }}</td>
                    <td class="text-xs">{{ r.model }}</td>
                    <td>{{ r.provider }}</td>
                    <td class="text-xs">{{ formatTime(r.started_at) }}</td>
                    <td class="text-xs">{{ r.ended_at ? formatTime(r.ended_at) : '进行中' }}</td>
                    <td>{{ r.turns }}</td>
                    <td class="text-xs">{{ r.prompt_tokens }}/{{ r.completion_tokens }}</td>
                    <td>{{ formatCost(r.cost_usd_cents) }}</td>
                    <td>
                      <span 
                        class="badge badge-sm"
                        :class="{
                          'badge-error': r.switch_reason.includes('failure'),
                          'badge-warning': r.switch_reason.includes('quota'),
                          'badge-info': r.switch_reason === 'initial' || r.switch_reason === 'recovery'
                        }"
                      >
                        {{ r.switch_reason }}
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </div>

        <div class="modal-action">
          <button @click="closeDetail" class="btn">关闭</button>
        </div>
      </div>
      <form method="dialog" class="modal-backdrop">
        <button @click="closeDetail">close</button>
      </form>
    </dialog>
  </div>
</template>

<style scoped>
.table {
  @apply text-sm;
}

/* SSE 错误高亮动画 */
.bg-error {
  animation: pulse-error 1s ease-in-out 3;
}

@keyframes pulse-error {
  0%, 100% {
    opacity: 1;
  }
  50% {
    opacity: 0.7;
  }
}
</style>
