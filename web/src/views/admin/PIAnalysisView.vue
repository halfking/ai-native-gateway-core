<template>
  <div class="pi-analysis-view">
    <div class="header">
      <h1>PI 错误分析</h1>
      <div class="actions">
        <button @click="triggerAnalysis" class="btn-primary" :disabled="isTriggering">
          {{ isTriggering ? '分析中...' : '手动触发分析' }}
        </button>
        <button @click="refreshData" class="btn-secondary">
          刷新
        </button>
      </div>
    </div>

    <!--
      ★ 2026-11-18 审计轮 P2：API 不可用时**如实说明**，不再是一张空表。
      后端 /api/admin/pi-analysis/* 只在「有 DB + admin 鉴权材料齐备」时
      注册（fail-closed）⇒ 无 DB / 未配 secret 的部署上这些请求全是 404，
      而页面此前只 console.error ⇒ 运维看到的是「空队列」，分不清
      「没数据」与「功能不存在」。
    -->
    <div class="unavailable-banner" v-if="unavailable">
      <strong>{{ t('common.piAnalysis.unavailableTitle') }}</strong>
      <span>{{ unavailableReason }}</span>
    </div>

    <!-- Chain-not-wired notice: the queue is empty because there is no producer. -->
    <div class="chain-note-banner" v-if="!unavailable && chainNotWired">
      <strong>{{ t('common.piAnalysis.chainNotWiredTitle') }}</strong>
      <span>{{ t('common.piAnalysis.chainNotWiredReason') }}</span>
    </div>

    <!-- 统计卡片 -->
    <div class="stats-cards" v-if="stats">
      <div class="stat-card">
        <div class="stat-label">待分析</div>
        <div class="stat-value">{{ stats.pending || 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已分析</div>
        <div class="stat-value">{{ stats.analyzed || 0 }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">误报率</div>
        <div class="stat-value">{{ stats.falsePositiveRate || '0%' }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">平均耗时</div>
        <div class="stat-value">{{ stats.avgDuration || '0ms' }}</div>
      </div>
    </div>

    <!-- Tab 切换 -->
    <div class="tabs">
      <button 
        :class="['tab', { active: activeTab === 'queue' }]"
        @click="activeTab = 'queue'"
      >
        错误队列
      </button>
      <button 
        :class="['tab', { active: activeTab === 'results' }]"
        @click="activeTab = 'results'"
      >
        分析结果
      </button>
    </div>

    <!-- 错误队列表格 -->
    <div v-if="activeTab === 'queue'" class="table-container">
      <div class="filters">
        <select v-model="queueStatus" @change="loadQueue">
          <option value="">全部状态</option>
          <option value="pending">待处理</option>
          <option value="processing">处理中</option>
          <option value="analyzed">已分析</option>
          <option value="failed">失败</option>
        </select>
      </div>

      <table class="data-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>错误类型</th>
            <th>优先级</th>
            <th>租户</th>
            <th>错误消息</th>
            <th>状态</th>
            <th>重试次数</th>
            <th>创建时间</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="error in queueData" :key="error.id">
            <td>{{ error.id }}</td>
            <td>
              <span class="badge" :class="getErrorTypeClass(error.error_type)">
                {{ formatErrorType(error.error_type) }}
              </span>
            </td>
            <td>
              <span class="priority" :class="getPriorityClass(error.priority)">
                {{ error.priority }}
              </span>
            </td>
            <td>{{ error.tenant_id }}</td>
            <td class="error-message">{{ error.error_message }}</td>
            <td>
              <span class="status" :class="getStatusClass(error.status)">
                {{ formatStatus(error.status) }}
              </span>
            </td>
            <td>{{ error.retry_count }}</td>
            <td>{{ formatTime(error.created_at) }}</td>
            <td>
              <button 
                v-if="error.status === 'analyzed'"
                @click="viewResult(error.id)" 
                class="btn-link"
              >
                查看结果
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="pagination">
        <button @click="prevQueuePage" :disabled="queueOffset === 0">上一页</button>
        <span>{{ queueOffset / queueLimit + 1 }} / {{ Math.ceil(queueTotal / queueLimit) }}</span>
        <button @click="nextQueuePage" :disabled="queueOffset + queueLimit >= queueTotal">下一页</button>
      </div>
    </div>

    <!-- 分析结果表格 -->
    <div v-if="activeTab === 'results'" class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th>分析ID</th>
            <th>错误类型</th>
            <th>根本原因</th>
            <th>误报</th>
            <th>置信度</th>
            <th>严重程度</th>
            <th>建议修复</th>
            <th>创建时间</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="result in resultsData" :key="result.id">
            <td>{{ result.analysis_id.slice(0, 8) }}...</td>
            <td>
              <span class="badge" :class="getErrorTypeClass(result.error_type)">
                {{ formatErrorType(result.error_type) }}
              </span>
            </td>
            <td class="root-cause">{{ result.root_cause }}</td>
            <td>
              <span :class="result.is_false_positive ? 'badge-warning' : 'badge-success'">
                {{ result.is_false_positive ? '是' : '否' }}
              </span>
            </td>
            <td>{{ (result.confidence_score * 100).toFixed(0) }}%</td>
            <td>
              <span class="severity" :class="getSeverityClass(result.severity_level)">
                {{ formatSeverity(result.severity_level) }}
              </span>
            </td>
            <td class="suggested-fix">{{ result.suggested_fix }}</td>
            <td>{{ formatTime(result.created_at) }}</td>
            <td>
              <button @click="viewDetail(result.queue_id)" class="btn-link">
                详情
              </button>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="pagination">
        <button @click="prevResultsPage" :disabled="resultsOffset === 0">上一页</button>
        <span>{{ resultsOffset / resultsLimit + 1 }} / {{ Math.ceil(resultsTotal / resultsLimit) }}</span>
        <button @click="nextResultsPage" :disabled="resultsOffset + resultsLimit >= resultsTotal">下一页</button>
      </div>
    </div>

    <!-- 详情对话框 -->
    <div v-if="showDetailDialog" class="dialog-overlay" @click="closeDetail">
      <div class="dialog" @click.stop>
        <div class="dialog-header">
          <h2>分析详情</h2>
          <button @click="closeDetail" class="btn-close">×</button>
        </div>
        <div class="dialog-content" v-if="detailData">
          <div class="detail-section">
            <h3>基本信息</h3>
            <div class="detail-grid">
              <div class="detail-item">
                <label>分析ID:</label>
                <span>{{ detailData.analysis_id }}</span>
              </div>
              <div class="detail-item">
                <label>错误类型:</label>
                <span>{{ formatErrorType(detailData.error_type) }}</span>
              </div>
              <div class="detail-item">
                <label>租户ID:</label>
                <span>{{ detailData.tenant_id }}</span>
              </div>
              <div class="detail-item">
                <label>置信度:</label>
                <span>{{ (detailData.confidence_score * 100).toFixed(0) }}%</span>
              </div>
            </div>
          </div>

          <div class="detail-section">
            <h3>根本原因</h3>
            <p>{{ detailData.root_cause }}</p>
          </div>

          <div class="detail-section">
            <h3>错误消息</h3>
            <pre>{{ detailData.error_message }}</pre>
          </div>

          <div class="detail-section" v-if="detailData.input_text">
            <h3>输入文本</h3>
            <pre>{{ detailData.input_text }}</pre>
          </div>

          <div class="detail-section">
            <h3>建议修复</h3>
            <p>{{ detailData.suggested_fix }}</p>
          </div>

          <div class="detail-section" v-if="detailData.rule_adjustment">
            <h3>规则调整建议</h3>
            <pre>{{ JSON.stringify(detailData.rule_adjustment, null, 2) }}</pre>
          </div>

          <div class="detail-section" v-if="detailData.code_changes">
            <h3>代码修改建议</h3>
            <pre>{{ JSON.stringify(detailData.code_changes, null, 2) }}</pre>
          </div>

          <div class="detail-section">
            <h3>性能统计</h3>
            <div class="detail-grid">
              <div class="detail-item">
                <label>分析耗时:</label>
                <span>{{ detailData.analysis_duration_ms }}ms</span>
              </div>
              <div class="detail-item">
                <label>Token 消耗:</label>
                <span>{{ detailData.llm_tokens_used }}</span>
              </div>
              <div class="detail-item">
                <label>影响范围:</label>
                <span>{{ formatImpact(detailData.impact_scope) }}</span>
              </div>
              <div class="detail-item">
                <label>预估受影响用户:</label>
                <span>{{ detailData.affected_users_estimate }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

// 状态管理
const activeTab = ref<'queue' | 'results'>('queue')
const queueStatus = ref('')
const isTriggering = ref(false)
const showDetailDialog = ref(false)

// 数据
const stats = ref<any>(null)
const queueData = ref<any[]>([])
const resultsData = ref<any[]>([])
const detailData = ref<any>(null)

// 分页
const queueLimit = ref(20)
const queueOffset = ref(0)
const queueTotal = ref(0)

const resultsLimit = ref(20)
const resultsOffset = ref(0)
const resultsTotal = ref(0)

// 加载统计
async function loadStats() {
  try {
    const response = await fetch('/api/admin/pi-analysis/stats')
    // ★ 2026-11-18 审计轮 P2：非 2xx 必须**如实**判为「不可用」，
    // 不能把 404 的响应体当 JSON 解析（解析失败会伪装成网络错误，
    // 运维看到的提示也会误导）。⇒ 显式带状态码抛错。
    if (!response.ok) {
      throw Object.assign(new Error(`pi-analysis stats failed: HTTP ${response.status}`), {
        status: response.status,
      })
    }
    const data = await response.json()
    unavailable.value = false
    // ★ 2026-11-18 审计轮 P1：后端把「检测链是否接线」一起返回了。
    //	没接线时队列**恒为空**（唯一的生产者是 promptinjection.Detector，
    //	而整条检测链在 LLM_GATEWAY_USE_V2_PIPELINE 门控内，默认关）
    //	⇒ 必须如实显示，否则运维会把「没有生产者」读成「没有错误发生」。
    chainNotWired.value = data?.detection_chain_wired === false

    // 计算统计信息
    let pending = 0, analyzed = 0, falsePositives = 0, totalDuration = 0, count = 0
    
    if (data.by_type) {
      data.by_type.forEach((item: any) => {
        pending += item.pending || 0
        analyzed += item.analyzed || 0
      })
    }
    
    stats.value = {
      pending,
      analyzed,
      falsePositiveRate: analyzed > 0 ? ((falsePositives / analyzed) * 100).toFixed(1) + '%' : '0%',
      avgDuration: count > 0 ? Math.round(totalDuration / count) + 'ms' : '0ms'
    }
  } catch (error) {
    console.error('Failed to load stats:', error)
    markUnavailable(error)
  }
}

// ★ 2026-11-18 审计轮 P2：把「API 不可用」与「没有数据」区分开。
// 后端在无 DB / 无 admin 鉴权材料时**不注册**这批路由（fail-closed），
// 请求会得到 404 ⇒ 页面必须如实说明，而不是显示一张空表。
const unavailable = ref(false)
const unavailableReason = ref('')
// ★ 2026-11-18 审计轮 P1：后端告知「检测链未接线」时的标记。
const chainNotWired = ref(false)

function markUnavailable(error: unknown) {
  const status = (error as { status?: number })?.status
  if (status === 404 || status === 401 || status === 403) {
    unavailable.value = true
    unavailableReason.value = t('common.piAnalysis.unavailableReason')
    return
  }
  unavailable.value = true
  unavailableReason.value = t('common.piAnalysis.unavailableNetwork')
}

// 加载队列
async function loadQueue() {
  try {
    const params = new URLSearchParams({
      limit: queueLimit.value.toString(),
      offset: queueOffset.value.toString(),
    })
    if (queueStatus.value) {
      params.set('status', queueStatus.value)
    }
    
    const response = await fetch(`/api/admin/pi-analysis/queue?${params}`)
    const data = await response.json()
    queueData.value = data.errors || []
    queueTotal.value = data.total || 0
  } catch (error) {
    console.error('Failed to load queue:', error)
  }
}

// 加载结果
async function loadResults() {
  try {
    const params = new URLSearchParams({
      limit: resultsLimit.value.toString(),
      offset: resultsOffset.value.toString(),
    })
    
    const response = await fetch(`/api/admin/pi-analysis/results?${params}`)
    const data = await response.json()
    resultsData.value = data.results || []
    resultsTotal.value = data.total || 0
  } catch (error) {
    console.error('Failed to load results:', error)
  }
}

// 查看详情
async function viewDetail(queueId: number) {
  try {
    const response = await fetch(`/api/admin/pi-analysis/results/${queueId}`)
    detailData.value = await response.json()
    showDetailDialog.value = true
  } catch (error) {
    console.error('Failed to load detail:', error)
  }
}

function viewResult(queueId: number) {
  viewDetail(queueId)
}

function closeDetail() {
  showDetailDialog.value = false
  detailData.value = null
}

// 触发分析
async function triggerAnalysis() {
  isTriggering.value = true
  try {
    await fetch('/api/admin/pi-analysis/trigger', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ batch_size: 10 })
    })
    alert('分析已触发，请稍后刷新查看结果')
  } catch (error) {
    console.error('Failed to trigger analysis:', error)
    alert('触发失败: ' + error)
  } finally {
    isTriggering.value = false
  }
}

// 刷新数据
function refreshData() {
  loadStats()
  if (activeTab.value === 'queue') {
    loadQueue()
  } else {
    loadResults()
  }
}

// 分页
function prevQueuePage() {
  if (queueOffset.value >= queueLimit.value) {
    queueOffset.value -= queueLimit.value
    loadQueue()
  }
}

function nextQueuePage() {
  if (queueOffset.value + queueLimit.value < queueTotal.value) {
    queueOffset.value += queueLimit.value
    loadQueue()
  }
}

function prevResultsPage() {
  if (resultsOffset.value >= resultsLimit.value) {
    resultsOffset.value -= resultsLimit.value
    loadResults()
  }
}

function nextResultsPage() {
  if (resultsOffset.value + resultsLimit.value < resultsTotal.value) {
    resultsOffset.value += resultsLimit.value
    loadResults()
  }
}

// 格式化函数
function formatErrorType(type: string): string {
  const map: Record<string, string> = {
    'llm_detection_failed': 'LLM检测失败',
    'rule_false_positive': '规则误报',
    'canary_detection_failed': 'Canary检测失败',
    'vector_match_failed': '向量匹配失败',
    'heuristic_misfire': '启发式误触发',
    'policy_load_failed': '策略加载失败',
    'unexpected_behavior': '意外行为',
    'performance_issue': '性能问题'
  }
  return map[type] || type
}

function formatStatus(status: string): string {
  const map: Record<string, string> = {
    'pending': '待处理',
    'processing': '处理中',
    'analyzed': '已分析',
    'failed': '失败',
    'skipped': '已跳过'
  }
  return map[status] || status
}

function formatSeverity(severity: string): string {
  const map: Record<string, string> = {
    'low': '低',
    'medium': '中',
    'high': '高',
    'critical': '严重'
  }
  return map[severity] || severity
}

function formatImpact(impact: string): string {
  const map: Record<string, string> = {
    'session': '会话',
    'tenant': '租户',
    'global': '全局'
  }
  return map[impact] || impact
}

function formatTime(time: string): string {
  return new Date(time).toLocaleString('zh-CN')
}

function getErrorTypeClass(type: string): string {
  if (type.includes('failed')) return 'badge-error'
  if (type.includes('false_positive')) return 'badge-warning'
  return 'badge-info'
}

function getPriorityClass(priority: number): string {
  if (priority >= 8) return 'priority-high'
  if (priority >= 5) return 'priority-medium'
  return 'priority-low'
}

function getStatusClass(status: string): string {
  const map: Record<string, string> = {
    'pending': 'status-pending',
    'processing': 'status-processing',
    'analyzed': 'status-success',
    'failed': 'status-error',
    'skipped': 'status-skipped'
  }
  return map[status] || ''
}

function getSeverityClass(severity: string): string {
  const map: Record<string, string> = {
    'low': 'severity-low',
    'medium': 'severity-medium',
    'high': 'severity-high',
    'critical': 'severity-critical'
  }
  return map[severity] || ''
}

// 初始化
onMounted(() => {
  loadStats()
  loadQueue()
  loadResults()
})
</script>

<style scoped>
.pi-analysis-view {
  padding: 20px;
}

/* ★ 2026-11-18 audit round P2: banner shown when the PI analysis API is unavailable. */
.unavailable-banner {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 16px;
  padding: 12px 16px;
  border-radius: 4px;
  border: 1px solid #d97706;
  background: rgba(217, 119, 6, 0.08);
  color: inherit;
  font-size: 13px;
  line-height: 1.5;
}

/* ★ 2026-11-18 audit round P1: banner shown when the detection chain is not wired. */
.chain-note-banner {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 16px;
  padding: 12px 16px;
  border-radius: 4px;
  border: 1px solid #6b7280;
  background: rgba(107, 114, 128, 0.08);
  color: inherit;
  font-size: 13px;
  line-height: 1.5;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.header h1 {
  margin: 0;
  font-size: 24px;
}

.actions {
  display: flex;
  gap: 10px;
}

.btn-primary, .btn-secondary {
  padding: 8px 16px;
  border-radius: 4px;
  border: none;
  cursor: pointer;
  font-size: 14px;
}

.btn-primary {
  background: #1890ff;
  color: white;
}

.btn-primary:hover {
  background: #40a9ff;
}

.btn-primary:disabled {
  background: #d9d9d9;
  cursor: not-allowed;
}

.btn-secondary {
  background: #f0f0f0;
  color: #333;
}

.btn-secondary:hover {
  background: #e0e0e0;
}

.stats-cards {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 24px;
}

.stat-card {
  background: white;
  padding: 20px;
  border-radius: 8px;
  box-shadow: 0 2px 4px rgba(0,0,0,0.1);
}

.stat-label {
  font-size: 14px;
  color: #666;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 28px;
  font-weight: bold;
  color: #1890ff;
}

.tabs {
  display: flex;
  gap: 0;
  margin-bottom: 20px;
  border-bottom: 2px solid #e8e8e8;
}

.tab {
  padding: 12px 24px;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 14px;
  color: #666;
  position: relative;
}

.tab:hover {
  color: #1890ff;
}

.tab.active {
  color: #1890ff;
  font-weight: bold;
}

.tab.active::after {
  content: '';
  position: absolute;
  bottom: -2px;
  left: 0;
  right: 0;
  height: 2px;
  background: #1890ff;
}

.table-container {
  background: white;
  border-radius: 8px;
  padding: 20px;
  box-shadow: 0 2px 4px rgba(0,0,0,0.1);
}

.filters {
  margin-bottom: 16px;
}

.filters select {
  padding: 8px 12px;
  border: 1px solid #d9d9d9;
  border-radius: 4px;
  font-size: 14px;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
}

.data-table th {
  text-align: left;
  padding: 12px;
  background: #fafafa;
  font-weight: 600;
  border-bottom: 1px solid #e8e8e8;
}

.data-table td {
  padding: 12px;
  border-bottom: 1px solid #f0f0f0;
}

.data-table tr:hover {
  background: #fafafa;
}

.badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.badge-error {
  background: #fff1f0;
  color: #cf1322;
}

.badge-warning {
  background: #fffbe6;
  color: #d48806;
}

.badge-info {
  background: #e6f7ff;
  color: #0958d9;
}

.badge-success {
  background: #f6ffed;
  color: #52c41a;
}

.priority {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: bold;
}

.priority-high {
  background: #ff4d4f;
  color: white;
}

.priority-medium {
  background: #faad14;
  color: white;
}

.priority-low {
  background: #52c41a;
  color: white;
}

.status {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.status-pending {
  background: #f0f0f0;
  color: #666;
}

.status-processing {
  background: #e6f7ff;
  color: #1890ff;
}

.status-success {
  background: #f6ffed;
  color: #52c41a;
}

.status-error {
  background: #fff1f0;
  color: #ff4d4f;
}

.severity {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.severity-low {
  background: #f6ffed;
  color: #52c41a;
}

.severity-medium {
  background: #fffbe6;
  color: #faad14;
}

.severity-high {
  background: #fff1f0;
  color: #ff4d4f;
}

.severity-critical {
  background: #000;
  color: #fff;
}

.error-message, .root-cause, .suggested-fix {
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.btn-link {
  background: none;
  border: none;
  color: #1890ff;
  cursor: pointer;
  text-decoration: underline;
}

.btn-link:hover {
  color: #40a9ff;
}

.pagination {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 16px;
  margin-top: 16px;
}

.pagination button {
  padding: 6px 12px;
  border: 1px solid #d9d9d9;
  background: white;
  border-radius: 4px;
  cursor: pointer;
}

.pagination button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.dialog-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0,0,0,0.5);
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 1000;
}

.dialog {
  background: white;
  border-radius: 8px;
  max-width: 800px;
  max-height: 80vh;
  overflow: auto;
  box-shadow: 0 4px 12px rgba(0,0,0,0.15);
}

.dialog-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 24px;
  border-bottom: 1px solid #e8e8e8;
}

.dialog-header h2 {
  margin: 0;
  font-size: 18px;
}

.btn-close {
  background: none;
  border: none;
  font-size: 24px;
  cursor: pointer;
  color: #999;
}

.btn-close:hover {
  color: #666;
}

.dialog-content {
  padding: 24px;
}

.detail-section {
  margin-bottom: 24px;
}

.detail-section h3 {
  font-size: 16px;
  margin-bottom: 12px;
  color: #333;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.detail-item {
  display: flex;
  gap: 8px;
}

.detail-item label {
  font-weight: 600;
  color: #666;
}

.detail-section pre {
  background: #f5f5f5;
  padding: 12px;
  border-radius: 4px;
  overflow-x: auto;
  font-size: 12px;
  line-height: 1.5;
}

.detail-section p {
  line-height: 1.6;
  color: #333;
}
</style>
