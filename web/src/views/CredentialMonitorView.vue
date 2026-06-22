<script setup lang="ts">
import { ref, onMounted, computed, onUnmounted } from 'vue'
import { getCredentialMonitorSummary, getSlidingWindow, promoteCredential, demoteCredential, setConcurrencyAuto, type CredentialMonitorSummary, type CallEntry } from '../api'
import { Chart, registerables } from 'chart.js'

Chart.register(...registerables)

const loading = ref(false)
const credentials = ref<CredentialMonitorSummary[]>([])
const selectedCred = ref<CredentialMonitorSummary | null>(null)
const windowEntries = ref<CallEntry[]>([])
const windowLoading = ref(false)
const windowModel = ref('')

const providerFilter = ref(0)
const availStateFilter = ref('')
const healthFilter = ref('')

const demoteDialogOpen = ref(false)
const demoteReason = ref('')
const demoteHours = ref(2)

const promoteDialogOpen = ref(false)
const promoteReason = ref('')

const concurrencyDialogOpen = ref(false)
const concurrencyValue = ref(5)
const concurrencyReason = ref('')

// Auto refresh
const autoRefresh = ref(false)
const refreshInterval = ref(30) // seconds
let refreshTimer: number | null = null

// Batch operations
const selectedIds = ref<Set<number>>(new Set())
const batchDialogOpen = ref(false)
const batchAction = ref<'promote' | 'demote'>('promote')
const batchReason = ref('')
const batchHours = ref(2)

// Error pie chart
let errorPieChart: Chart | null = null

async function load() {
  loading.value = true
  try {
    const res = await getCredentialMonitorSummary({
      provider_id: providerFilter.value || undefined,
      include_window_stats: true,
    })
    credentials.value = res.credentials
  } catch (e) {
    console.error('load failed', e)
  } finally {
    loading.value = false
  }
}

const filteredCreds = computed(() => {
  let result = credentials.value
  if (availStateFilter.value) {
    result = result.filter(c => c.availability_state === availStateFilter.value)
  }
  if (healthFilter.value) {
    result = result.filter(c => c.health_status === healthFilter.value)
  }
  return result
})

const allSelected = computed(() => {
  return filteredCreds.value.length > 0 && filteredCreds.value.every(c => selectedIds.value.has(c.id))
})

function toggleSelectAll() {
  if (allSelected.value) {
    selectedIds.value.clear()
  } else {
    filteredCreds.value.forEach(c => selectedIds.value.add(c.id))
  }
}

function toggleSelect(id: number) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id)
  } else {
    selectedIds.value.add(id)
  }
}

function openDetail(cred: CredentialMonitorSummary) {
  selectedCred.value = cred
  windowModel.value = cred.recent_window_stats?.sample_model || ''
  if (windowModel.value) {
    loadSlidingWindow(cred.id, windowModel.value)
  }
}

async function loadSlidingWindow(credId: number, model: string) {
  if (!model) return
  windowLoading.value = true
  try {
    const res = await getSlidingWindow(credId, model, 60)
    windowEntries.value = res.entries
    // Render error pie chart
    setTimeout(() => renderErrorPieChart(res.stats.error_kinds), 100)
  } catch (e) {
    console.error('sliding window failed', e)
  } finally {
    windowLoading.value = false
  }
}

function renderErrorPieChart(errorKinds: Record<string, number>) {
  const canvas = document.getElementById('errorPieChart') as HTMLCanvasElement
  if (!canvas) return

  if (errorPieChart) {
    errorPieChart.destroy()
  }

  const labels = Object.keys(errorKinds)
  const data = Object.values(errorKinds)

  if (labels.length === 0) return

  errorPieChart = new Chart(canvas, {
    type: 'pie',
    data: {
      labels: labels,
      datasets: [{
        data: data,
        backgroundColor: [
          '#ef4444', '#f97316', '#f59e0b', '#eab308', '#84cc16',
          '#22c55e', '#10b981', '#14b8a6', '#06b6d4', '#0ea5e9',
        ],
      }],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          position: 'right',
        },
        title: {
          display: true,
          text: '错误类型分布',
        },
      },
    },
  })
}

function startAutoRefresh() {
  if (refreshTimer) return
  autoRefresh.value = true
  refreshTimer = window.setInterval(() => {
    load()
  }, refreshInterval.value * 1000)
}

function stopAutoRefresh() {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
  autoRefresh.value = false
}

function toggleAutoRefresh() {
  if (autoRefresh.value) {
    stopAutoRefresh()
  } else {
    startAutoRefresh()
  }
}

function openBatchDialog(action: 'promote' | 'demote') {
  if (selectedIds.value.size === 0) {
    alert('请先选择凭据')
    return
  }
  batchAction.value = action
  batchReason.value = ''
  batchHours.value = 2
  batchDialogOpen.value = true
}

async function submitBatch() {
  const ids = Array.from(selectedIds.value)
  const promises = ids.map(id => {
    if (batchAction.value === 'promote') {
      return promoteCredential(id, batchReason.value)
    } else {
      return demoteCredential(id, batchReason.value, batchHours.value)
    }
  })

  try {
    await Promise.all(promises)
    batchDialogOpen.value = false
    selectedIds.value.clear()
    load()
  } catch (e) {
    alert('批量操作失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openDemoteDialog() {
  demoteDialogOpen.value = true
  demoteReason.value = ''
  demoteHours.value = 2
}

async function submitDemote() {
  if (!selectedCred.value) return
  try {
    await demoteCredential(selectedCred.value.id, demoteReason.value, demoteHours.value)
    demoteDialogOpen.value = false
    load()
    selectedCred.value = null
  } catch (e) {
    alert('降级失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openPromoteDialog() {
  promoteDialogOpen.value = true
  promoteReason.value = ''
}

async function submitPromote() {
  if (!selectedCred.value) return
  try {
    await promoteCredential(selectedCred.value.id, promoteReason.value)
    promoteDialogOpen.value = false
    load()
    selectedCred.value = null
  } catch (e) {
    alert('升级失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function openConcurrencyDialog() {
  concurrencyDialogOpen.value = true
  concurrencyValue.value = selectedCred.value?.concurrency_limit_auto || selectedCred.value?.effective_concurrency || 5
  concurrencyReason.value = ''
}

async function submitConcurrency() {
  if (!selectedCred.value) return
  try {
    await setConcurrencyAuto(selectedCred.value.id, concurrencyValue.value, concurrencyReason.value)
    concurrencyDialogOpen.value = false
    load()
  } catch (e) {
    alert('设置失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

function statusBadge(state: string) {
  if (state === 'ready') return 'badge-green'
  if (state === 'degraded' || state === 'cooling') return 'badge-amber'
  if (state === 'unreachable' || state === 'auth_failed') return 'badge-red'
  return 'badge-gray'
}

function healthBadge(h: string) {
  if (h === 'healthy') return 'badge-green'
  if (h === 'warning') return 'badge-amber'
  if (h === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

onMounted(() => {
  load()
})

onUnmounted(() => {
  stopAutoRefresh()
  if (errorPieChart) {
    errorPieChart.destroy()
  }
})
</script>

<template>
  <div class="page-container">
    <div class="page-header">
      <h1>凭据监控</h1>
      <div style="display:flex;gap:8px;align-items:center">
        <label style="display:flex;align-items:center;gap:4px;font-size:14px">
          <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
          自动刷新
        </label>
        <select v-model.number="refreshInterval" class="field-input" style="width:auto">
          <option :value="10">10秒</option>
          <option :value="30">30秒</option>
          <option :value="60">60秒</option>
        </select>
        <button class="btn btn-primary btn-sm" @click="load">手动刷新</button>
      </div>
    </div>

    <div class="card" style="margin-bottom:16px">
      <div style="display:flex;gap:12px;align-items:center;flex-wrap:wrap">
        <label>可用性状态:</label>
        <select v-model="availStateFilter" class="field-input" style="width:auto">
          <option value="">全部</option>
          <option value="ready">ready</option>
          <option value="degraded">degraded</option>
          <option value="cooling">cooling</option>
          <option value="unreachable">unreachable</option>
        </select>
        <label>健康状态:</label>
        <select v-model="healthFilter" class="field-input" style="width:auto">
          <option value="">全部</option>
          <option value="healthy">healthy</option>
          <option value="warning">warning</option>
          <option value="unreachable">unreachable</option>
        </select>
        <div style="flex:1"></div>
        <button class="btn btn-sm btn-success" :disabled="selectedIds.size === 0" @click="openBatchDialog('promote')">
          批量恢复 ({{ selectedIds.size }})
        </button>
        <button class="btn btn-sm btn-danger" :disabled="selectedIds.size === 0" @click="openBatchDialog('demote')">
          批量降级 ({{ selectedIds.size }})
        </button>
      </div>
    </div>

    <div v-if="loading" style="text-align:center;padding:32px">加载中...</div>
    <div v-else-if="!filteredCreds.length" style="text-align:center;padding:32px">暂无凭据</div>

    <div v-else class="card" style="overflow-x:auto">
      <table class="data-table">
        <thead>
          <tr>
            <th style="width:40px">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll" />
            </th>
            <th>凭据</th>
            <th>供应商</th>
            <th>可用性</th>
            <th>健康</th>
            <th>并发</th>
            <th>失败率 (1h)</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in filteredCreds" :key="c.id">
            <td>
              <input type="checkbox" :checked="selectedIds.has(c.id)" @change="toggleSelect(c.id)" />
            </td>
            <td>
              <div>{{ c.label || `#${c.id}` }}</div>
              <div class="cell-sub">ID: {{ c.id }}</div>
            </td>
            <td>{{ c.provider_name }}</td>
            <td>
              <span class="badge" :class="statusBadge(c.availability_state)">{{ c.availability_state }}</span>
              <div v-if="c.state_reason_code" class="cell-sub">{{ c.state_reason_code }}</div>
            </td>
            <td>
              <span class="badge" :class="healthBadge(c.health_status)">{{ c.health_status }}</span>
            </td>
            <td>
              <div>手动: {{ c.concurrency_limit || '—' }}</div>
              <div class="cell-sub">自动: {{ c.concurrency_limit_auto || '—' }}</div>
              <div class="cell-sub">生效: {{ c.effective_concurrency }}</div>
            </td>
            <td>
              <div v-if="c.recent_window_stats">
                {{ (c.recent_window_stats.failure_rate * 100).toFixed(1) }}%
                <span class="cell-sub">({{ c.recent_window_stats.failed }}/{{ c.recent_window_stats.total }})</span>
              </div>
              <div v-else class="cell-muted">—</div>
            </td>
            <td>
              <button class="btn btn-sm btn-ghost" @click="openDetail(c)">详情</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Detail Drawer -->
    <div v-if="selectedCred" class="drawer-backdrop" @click="selectedCred = null">
      <div class="drawer-panel card drawer-panel-wide" @click.stop style="max-width:1000px">
        <div class="drawer-header">
          <div>
            <h3 style="margin:0">{{ selectedCred.label || `凭据 #${selectedCred.id}` }}</h3>
            <div class="drawer-sub">{{ selectedCred.provider_name }}</div>
          </div>
          <button class="btn btn-ghost btn-sm" @click="selectedCred = null">关闭</button>
        </div>

        <div class="drawer-body">
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:16px">
            <!-- Left column -->
            <div>
              <div class="drawer-section">
                <div class="drawer-section-title">状态概览</div>
                <div style="display:grid;grid-template-columns:repeat(2,1fr);gap:12px">
                  <div>
                    <label class="field-label">可用性状态</label>
                    <span class="badge" :class="statusBadge(selectedCred.availability_state)">{{ selectedCred.availability_state }}</span>
                  </div>
                  <div>
                    <label class="field-label">健康状态</label>
                    <span class="badge" :class="healthBadge(selectedCred.health_status)">{{ selectedCred.health_status }}</span>
                  </div>
                  <div>
                    <label class="field-label">配额状态</label>
                    <span>{{ selectedCred.quota_state }}</span>
                  </div>
                  <div>
                    <label class="field-label">连续失败</label>
                    <span>{{ selectedCred.consecutive_failures }}</span>
                  </div>
                </div>
                <div v-if="selectedCred.state_reason_detail" class="cell-sub" style="margin-top:8px">
                  {{ selectedCred.state_reason_detail }}
                </div>
              </div>

              <div class="drawer-section">
                <div class="drawer-section-title">并发限流</div>
                <div style="display:grid;grid-template-columns:repeat(3,1fr);gap:12px">
                  <div>
                    <label class="field-label">手动设置</label>
                    <div>{{ selectedCred.concurrency_limit || '未设置' }}</div>
                  </div>
                  <div>
                    <label class="field-label">自动调整</label>
                    <div>{{ selectedCred.concurrency_limit_auto || '未设置' }}</div>
                  </div>
                  <div>
                    <label class="field-label">实际生效</label>
                    <div class="badge badge-blue">{{ selectedCred.effective_concurrency }}</div>
                  </div>
                </div>
                <button class="btn btn-sm" style="margin-top:8px" @click="openConcurrencyDialog">手动调整自动值</button>
              </div>

              <div class="drawer-section">
                <div class="drawer-section-title">手动升降级</div>
                <div style="display:flex;gap:8px">
                  <button class="btn btn-sm btn-danger" @click="openDemoteDialog">临时降级</button>
                  <button class="btn btn-sm btn-success" @click="openPromoteDialog">恢复上线</button>
                </div>
              </div>
            </div>

            <!-- Right column -->
            <div>
              <div class="drawer-section">
                <div class="drawer-section-title">滑动窗口 (最近 1 小时)</div>
                <div v-if="!windowModel" class="cell-muted">无可用模型数据</div>
                <div v-else>
                  <div style="margin-bottom:8px">
                    <label class="field-label">模型:</label>
                    <code class="mono-sm">{{ windowModel }}</code>
                  </div>
                  <div v-if="windowLoading">加载中...</div>
                  <div v-else-if="!windowEntries.length" class="cell-muted">无数据</div>
                  <div v-else>
                    <div style="display:flex;gap:4px;overflow-x:auto;padding:8px 0">
                      <div
                        v-for="(e, i) in windowEntries.slice(0, 100)"
                        :key="i"
                        :style="{
                          width: '4px',
                          height: '40px',
                          background: e.ok ? '#10b981' : '#ef4444',
                          opacity: 0.8,
                        }"
                        :title="`${e.ok ? '✓' : '✗'} ${e.lat}ms ${e.err || ''}`"
                      ></div>
                    </div>
                    <div style="display:flex;gap:16px;margin-top:8px;font-size:13px">
                      <span>总计: {{ windowEntries.length }}</span>
                      <span style="color:#10b981">成功: {{ windowEntries.filter(e => e.ok).length }}</span>
                      <span style="color:#ef4444">失败: {{ windowEntries.filter(e => !e.ok).length }}</span>
                      <span>失败率: {{ ((windowEntries.filter(e => !e.ok).length / windowEntries.length) * 100).toFixed(1) }}%</span>
                    </div>
                  </div>
                </div>
              </div>

              <div class="drawer-section">
                <div class="drawer-section-title">错误分布</div>
                <div style="height:200px;position:relative">
                  <canvas id="errorPieChart"></canvas>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Batch Dialog -->
    <div v-if="batchDialogOpen" class="drawer-backdrop" @click="batchDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">批量{{ batchAction === 'promote' ? '恢复' : '降级' }} ({{ selectedIds.size }} 个凭据)</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">原因</label>
          <input v-model="batchReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div v-if="batchAction === 'demote'" style="margin-bottom:16px">
          <label class="field-label">自动恢复时间 (小时)</label>
          <input v-model.number="batchHours" type="number" min="0.5" step="0.5" class="field-input" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="batchDialogOpen = false">取消</button>
          <button
            :class="batchAction === 'promote' ? 'btn btn-success' : 'btn btn-danger'"
            @click="submitBatch"
          >
            确认{{ batchAction === 'promote' ? '恢复' : '降级' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Demote Dialog -->
    <div v-if="demoteDialogOpen" class="drawer-backdrop" @click="demoteDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">临时降级</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">降级原因</label>
          <input v-model="demoteReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div style="margin-bottom:16px">
          <label class="field-label">自动恢复时间 (小时)</label>
          <input v-model.number="demoteHours" type="number" min="0.5" step="0.5" class="field-input" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="demoteDialogOpen = false">取消</button>
          <button class="btn btn-danger" @click="submitDemote">确认降级</button>
        </div>
      </div>
    </div>

    <!-- Promote Dialog -->
    <div v-if="promoteDialogOpen" class="drawer-backdrop" @click="promoteDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">恢复上线</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">恢复原因</label>
          <input v-model="promoteReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="promoteDialogOpen = false">取消</button>
          <button class="btn btn-success" @click="submitPromote">确认恢复</button>
        </div>
      </div>
    </div>

    <!-- Concurrency Dialog -->
    <div v-if="concurrencyDialogOpen" class="drawer-backdrop" @click="concurrencyDialogOpen = false">
      <div class="card" @click.stop style="max-width:500px;margin:auto;margin-top:100px;padding:24px">
        <h3 style="margin-top:0">手动调整并发自动值</h3>
        <div style="margin-bottom:16px">
          <label class="field-label">并发上限</label>
          <input v-model.number="concurrencyValue" type="number" min="1" class="field-input" />
        </div>
        <div style="margin-bottom:16px">
          <label class="field-label">调整原因</label>
          <input v-model="concurrencyReason" class="field-input" placeholder="请输入原因" />
        </div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button class="btn btn-ghost" @click="concurrencyDialogOpen = false">取消</button>
          <button class="btn btn-primary" @click="submitConcurrency">确认</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-container {
  padding: 24px;
  max-width: 1400px;
  margin: 0 auto;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0;
  font-size: 24px;
  font-weight: 600;
}

.drawer-panel-wide {
  width: 90vw;
}
</style>
