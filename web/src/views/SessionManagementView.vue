<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

interface Session {
  session_id: string
  tenant_id: string
  api_key_id: number
  status: string
  created_at: string
  last_active: string
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

const statusColors: Record<string, string> = {
  active: 'badge-success',
  stopped: 'badge-warning',
  recovered: 'badge-info',
  expired: 'badge-error'
}

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
    await loadSessions()
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
    await loadSessions()
  } catch (e: unknown) {
    alert('恢复会话失败: ' + (e instanceof Error ? e.message : 'Unknown error'))
  }
}

function formatTime(ts: string) {
  if (!ts) return '-'
  return new Date(ts).toLocaleString()
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
            <th>Session ID</th>
            <th>状态</th>
            <th>租户</th>
            <th>轮次</th>
            <th>Tokens</th>
            <th>费用</th>
            <th>当前凭据</th>
            <th>当前模型</th>
            <th>创建时间</th>
            <th>最后活跃</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in sessions" :key="s.session_id">
            <td>
              <code class="text-xs">{{ s.session_id.substring(0, 16) }}...</code>
            </td>
            <td>
              <span :class="['badge', statusColors[s.status] || 'badge-ghost']">
                {{ s.status }}
              </span>
            </td>
            <td>{{ s.tenant_id }}</td>
            <td>{{ s.total_turns }}</td>
            <td class="text-xs">
              {{ s.total_prompt_tokens }} / {{ s.total_completion_tokens }}
            </td>
            <td>{{ formatCost(s.total_cost_usd_cents) }}</td>
            <td>{{ s.current_credential_id }}</td>
            <td class="text-xs">{{ s.current_model }}</td>
            <td class="text-xs">{{ formatTime(s.created_at) }}</td>
            <td class="text-xs">{{ formatTime(s.last_active) }}</td>
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
                  v-if="s.status === 'stopped'" 
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
              <span :class="['badge', statusColors[selectedSession.status]]">
                {{ selectedSession.status }}
              </span>
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
                  <tr v-for="(r, idx) in rotations" :key="idx">
                    <td>{{ r.credential_id }}</td>
                    <td class="text-xs">{{ r.model }}</td>
                    <td>{{ r.provider }}</td>
                    <td class="text-xs">{{ formatTime(r.started_at) }}</td>
                    <td class="text-xs">{{ r.ended_at ? formatTime(r.ended_at) : '进行中' }}</td>
                    <td>{{ r.turns }}</td>
                    <td class="text-xs">{{ r.prompt_tokens }}/{{ r.completion_tokens }}</td>
                    <td>{{ formatCost(r.cost_usd_cents) }}</td>
                    <td>
                      <span class="badge badge-sm">{{ r.switch_reason }}</span>
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
</style>
