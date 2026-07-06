<template>
  <div class="session-analytics-dashboard">
    <div class="dashboard-header">
      <h2>会话分析 Dashboard</h2>
      <el-button @click="refreshData" :loading="loading">
        <el-icon><Refresh /></el-icon>
        刷新
      </el-button>
    </div>

    <!-- 统计卡片 -->
    <el-row :gutter="20" class="stats-row">
      <el-col :span="6">
        <el-card shadow="hover">
          <el-statistic title="今日会话总数" :value="stats.total_sessions">
            <template #suffix>个</template>
          </el-statistic>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover">
          <el-statistic title="活跃会话" :value="stats.active_sessions">
            <template #suffix>个</template>
            <template #prefix>
              <el-icon color="#67c23a"><ChatDotRound /></el-icon>
            </template>
          </el-statistic>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover">
          <el-statistic title="总成本" :value="stats.total_cost" :precision="2">
            <template #prefix>$</template>
          </el-statistic>
        </el-card>
      </el-col>
      <el-col :span="6">
        <el-card shadow="hover">
          <el-statistic title="合规率" :value="stats.compliance_rate" :precision="1">
            <template #suffix>%</template>
          </el-statistic>
        </el-card>
      </el-col>
    </el-row>

    <!-- 筛选器 -->
    <el-card class="filter-card" shadow="never">
      <el-form :inline="true" :model="filters" class="filter-form">
        <el-form-item label="合规状态">
          <el-select v-model="filters.compliance_status" @change="loadSessions" clearable placeholder="全部">
            <el-option label="合规" value="compliant" />
            <el-option label="警告" value="warning" />
            <el-option label="违规" value="violation" />
          </el-select>
        </el-form-item>

        <el-form-item label="用户意图">
          <el-select v-model="filters.user_intent" @change="loadSessions" clearable placeholder="全部">
            <el-option label="聊天" value="chat" />
            <el-option label="代码" value="code" />
            <el-option label="工具使用" value="tool_use" />
            <el-option label="数据分析" value="data_analysis" />
            <el-option label="创意" value="creative" />
          </el-select>
        </el-form-item>

        <el-form-item label="成本范围">
          <el-input v-model="filters.min_cost" placeholder="最小" style="width: 100px" />
          <span style="margin: 0 8px">-</span>
          <el-input v-model="filters.max_cost" placeholder="最大" style="width: 100px" />
        </el-form-item>

        <el-form-item label="搜索">
          <el-input
            v-model="filters.search"
            placeholder="标题或主题"
            @keyup.enter="loadSessions"
            clearable
            style="width: 200px"
          >
            <template #prefix>
              <el-icon><Search /></el-icon>
            </template>
          </el-input>
        </el-form-item>

        <el-form-item>
          <el-button type="primary" @click="loadSessions">查询</el-button>
          <el-button @click="resetFilters">重置</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 会话列表 -->
    <el-card shadow="hover">
      <template #header>
        <div class="card-header">
          <span>会话列表</span>
          <el-radio-group v-model="sortBy" size="small" @change="loadSessions">
            <el-radio-button label="last_request_at">最近活跃</el-radio-button>
            <el-radio-button label="total_cost_usd">成本</el-radio-button>
            <el-radio-button label="request_count">请求数</el-radio-button>
            <el-radio-button label="duration_seconds">时长</el-radio-button>
          </el-radio-group>
        </div>
      </template>

      <el-table :data="sessions" style="width: 100%" @row-click="viewSessionDetail" stripe>
        <el-table-column prop="title" label="标题" min-width="200">
          <template #default="scope">
            <div class="session-title">
              <span>{{ scope.row.title || '未命名会话' }}</span>
              <el-tag v-if="scope.row.user_intent" size="small" type="info" style="margin-left: 8px">
                {{ getIntentLabel(scope.row.user_intent) }}
              </el-tag>
            </div>
          </template>
        </el-table-column>

        <el-table-column prop="first_request_at" label="开始时间" width="180">
          <template #default="scope">
            {{ formatDateTime(scope.row.first_request_at) }}
          </template>
        </el-table-column>

        <el-table-column prop="duration_seconds" label="时长" width="100">
          <template #default="scope">
            {{ formatDuration(scope.row.duration_seconds) }}
          </template>
        </el-table-column>

        <el-table-column prop="request_count" label="请求数" width="90" align="right" />

        <el-table-column prop="total_cost_usd" label="成本" width="100" align="right">
          <template #default="scope">
            ${{ scope.row.total_cost_usd.toFixed(4) }}
          </template>
        </el-table-column>

        <el-table-column prop="total_tokens" label="Tokens" width="110" align="right">
          <template #default="scope">
            {{ formatNumber(scope.row.total_tokens) }}
          </template>
        </el-table-column>

        <el-table-column prop="avg_latency_ms" label="平均延迟" width="100" align="right">
          <template #default="scope">
            {{ scope.row.avg_latency_ms }}ms
          </template>
        </el-table-column>

        <el-table-column prop="primary_model" label="主要模型" width="150" show-overflow-tooltip />

        <el-table-column prop="compliance_status" label="合规" width="100">
          <template #default="scope">
            <el-tag :type="getComplianceType(scope.row.compliance_status)" size="small">
              {{ getComplianceLabel(scope.row.compliance_status) }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column label="操作" width="120" fixed="right">
          <template #default="scope">
            <el-button size="small" text @click.stop="viewSessionDetail(scope.row)">
              详情
            </el-button>
            <el-button size="small" text @click.stop="exportSession(scope.row.session_key)">
              导出
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination
        v-model:current-page="pagination.page"
        v-model:page-size="pagination.page_size"
        :total="pagination.total"
        :page-sizes="[10, 20, 50, 100]"
        layout="total, sizes, prev, pager, next, jumper"
        @current-change="loadSessions"
        @size-change="loadSessions"
        style="margin-top: 20px; justify-content: flex-end"
      />
    </el-card>

    <!-- 会话详情对话框 -->
    <el-drawer
      v-model="detailDrawerVisible"
      :title="currentSession?.title || '会话详情'"
      size="70%"
      direction="rtl"
    >
      <div v-if="sessionDetail" class="session-detail">
        <!-- 摘要信息 -->
        <el-card shadow="never" class="detail-card">
          <template #header>
            <span>会话摘要</span>
          </template>

          <el-descriptions :column="2" border>
            <el-descriptions-item label="会话 Key">{{ sessionDetail.summary.session_key }}</el-descriptions-item>
            <el-descriptions-item label="时长">{{ formatDuration(sessionDetail.summary.duration_seconds) }}</el-descriptions-item>
            <el-descriptions-item label="开始时间">{{ formatDateTime(sessionDetail.summary.first_request_at) }}</el-descriptions-item>
            <el-descriptions-item label="结束时间">{{ formatDateTime(sessionDetail.summary.last_request_at) }}</el-descriptions-item>
            <el-descriptions-item label="请求数">{{ sessionDetail.summary.request_count }}</el-descriptions-item>
            <el-descriptions-item label="成功率">
              {{ (sessionDetail.summary.success_count / sessionDetail.summary.request_count * 100).toFixed(1) }}%
            </el-descriptions-item>
            <el-descriptions-item label="总成本">${{ sessionDetail.summary.total_cost_usd.toFixed(4) }}</el-descriptions-item>
            <el-descriptions-item label="总 Tokens">{{ formatNumber(sessionDetail.summary.total_tokens) }}</el-descriptions-item>
            <el-descriptions-item label="平均延迟">{{ sessionDetail.summary.avg_latency_ms }}ms</el-descriptions-item>
            <el-descriptions-item label="质量评分" v-if="sessionDetail.summary.quality_score">
              <el-rate v-model="sessionDetail.summary.quality_score" disabled :max="10" />
            </el-descriptions-item>
          </el-descriptions>

          <el-divider />

          <div v-if="sessionDetail.summary.summary">
            <h4>会话总结</h4>
            <p>{{ sessionDetail.summary.summary }}</p>
          </div>

          <div v-if="sessionDetail.summary.key_topics && sessionDetail.summary.key_topics.length > 0">
            <h4>关键主题</h4>
            <el-tag v-for="topic in sessionDetail.summary.key_topics" :key="topic" style="margin-right: 8px">
              {{ topic }}
            </el-tag>
          </div>
        </el-card>

        <!-- 成本分解 -->
        <el-card shadow="never" class="detail-card">
          <template #header>
            <span>成本分解</span>
          </template>

          <el-row :gutter="20">
            <el-col :span="12">
              <h4>按模型</h4>
              <div v-for="(cost, model) in sessionDetail.analysis.cost_breakdown.by_model" :key="model" class="cost-item">
                <span>{{ model }}</span>
                <span>${{ cost.toFixed(4) }}</span>
              </div>
            </el-col>
            <el-col :span="12">
              <h4>按提供商</h4>
              <div v-for="(cost, provider) in sessionDetail.analysis.cost_breakdown.by_provider" :key="provider" class="cost-item">
                <span>{{ provider }}</span>
                <span>${{ cost.toFixed(4) }}</span>
              </div>
            </el-col>
          </el-row>
        </el-card>

        <!-- 模型切换 -->
        <el-card v-if="sessionDetail.analysis.model_switches.length > 0" shadow="never" class="detail-card">
          <template #header>
            <span>模型切换 ({{ sessionDetail.analysis.model_switches.length }})</span>
          </template>

          <el-timeline>
            <el-timeline-item
              v-for="(sw, index) in sessionDetail.analysis.model_switches"
              :key="index"
              :timestamp="formatDateTime(sw.timestamp)"
              placement="top"
            >
              <el-tag type="info" size="small">{{ sw.from_model }}</el-tag>
              <el-icon style="margin: 0 8px"><Right /></el-icon>
              <el-tag type="success" size="small">{{ sw.to_model }}</el-tag>
              <span style="margin-left: 12px; color: var(--muted, #8b949e)">{{ sw.reason }}</span>
            </el-timeline-item>
          </el-timeline>
        </el-card>

        <!-- 合规问题 -->
        <el-card v-if="sessionDetail.analysis.compliance_issues.length > 0" shadow="never" class="detail-card">
          <template #header>
            <span>合规问题 ({{ sessionDetail.analysis.compliance_issues.length }})</span>
          </template>

          <el-table :data="sessionDetail.analysis.compliance_issues" stripe>
            <el-table-column prop="timestamp" label="时间" width="180">
              <template #default="scope">
                {{ formatDateTime(scope.row.timestamp) }}
              </template>
            </el-table-column>
            <el-table-column prop="issue_type" label="类型" width="120">
              <template #default="scope">
                <el-tag :type="getIssueTypeTag(scope.row.issue_type)" size="small">
                  {{ getIssueTypeLabel(scope.row.issue_type) }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="severity" label="严重程度" width="100">
              <template #default="scope">
                <el-tag :type="getSeverityTag(scope.row.severity)" size="small">
                  {{ scope.row.severity }}/10
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="description" label="描述" show-overflow-tooltip />
            <el-table-column prop="action_taken" label="动作" width="100" />
          </el-table>
        </el-card>

        <!-- 请求时间线 -->
        <el-card shadow="never" class="detail-card">
          <template #header>
            <span>请求时间线 ({{ sessionDetail.timeline.length }})</span>
          </template>

          <el-timeline>
            <el-timeline-item
              v-for="event in sessionDetail.timeline"
              :key="event.request_id"
              :timestamp="formatDateTime(event.created_at)"
              placement="top"
              :type="event.status === 'success' ? 'success' : 'danger'"
            >
              <el-card shadow="never" class="timeline-card">
                <div class="timeline-content">
                  <div class="timeline-row">
                    <span class="label">模型:</span>
                    <span>{{ event.upstream_model }}</span>
                  </div>
                  <div class="timeline-row">
                    <span class="label">Tokens:</span>
                    <span>{{ event.prompt_tokens }} + {{ event.completion_tokens }} = {{ event.prompt_tokens + event.completion_tokens }}</span>
                  </div>
                  <div class="timeline-row">
                    <span class="label">成本:</span>
                    <span>${{ event.total_cost.toFixed(6) }}</span>
                  </div>
                  <div class="timeline-row">
                    <span class="label">延迟:</span>
                    <span>{{ event.latency_ms }}ms</span>
                  </div>
                  <div v-if="event.error_message" class="timeline-row error">
                    <span class="label">错误:</span>
                    <span>{{ event.error_message }}</span>
                  </div>
                </div>
              </el-card>
            </el-timeline-item>
          </el-timeline>
        </el-card>
      </div>

      <div v-else class="loading-container">
        <el-icon class="is-loading"><Loading /></el-icon>
        <span>加载中...</span>
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { localeRef } from '../i18n'
import { ElMessage } from 'element-plus'
import {
  Refresh, ChatDotRound, Search, Right, Loading
} from '@element-plus/icons-vue'
import api from '@/api'

const stats = reactive({
  total_sessions: 0,
  active_sessions: 0,
  total_requests: 0,
  total_cost: 0,
  avg_cost_per_session: 0,
  avg_tokens_per_session: 0,
  avg_latency: 0,
  compliance_rate: 0,
  high_quality_rate: 0,
})

const filters = reactive({
  compliance_status: '',
  user_intent: '',
  min_cost: '',
  max_cost: '',
  search: '',
})

const sortBy = ref('last_request_at')

const sessions = ref<any[]>([])
const pagination = reactive({
  page: 1,
  page_size: 20,
  total: 0,
})

const detailDrawerVisible = ref(false)
const currentSession = ref<any>(null)
const sessionDetail = ref<any>(null)

const loading = ref(false)

const loadStats = async () => {
  try {
    const res = await api.get('/admin/sessions/stats')
    Object.assign(stats, res.data)
  } catch (error: any) {
    ElMessage.error('加载统计失败: ' + error.message)
  }
}

const loadSessions = async () => {
  loading.value = true
  try {
    const params = {
      page: pagination.page,
      page_size: pagination.page_size,
      order_by: sortBy.value,
      ...filters,
    }

    const res = await api.get('/admin/sessions', { params })
    sessions.value = res.data.sessions
    pagination.total = res.data.total
  } catch (error: any) {
    ElMessage.error('加载会话列表失败: ' + error.message)
  } finally {
    loading.value = false
  }
}

const viewSessionDetail = async (session: any) => {
  currentSession.value = session
  detailDrawerVisible.value = true
  sessionDetail.value = null

  try {
    const res = await api.get(`/admin/sessions/${session.session_key}`)
    sessionDetail.value = res.data
  } catch (error: any) {
    ElMessage.error('加载会话详情失败: ' + error.message)
  }
}

const exportSession = async (sessionKey: string) => {
  try {
    const res = await api.get(`/admin/sessions/${sessionKey}/export`, {
      responseType: 'blob',
    })

    const url = window.URL.createObjectURL(new Blob([res.data]))
    const link = document.createElement('a')
    link.href = url
    link.setAttribute('download', `session_${sessionKey}.json`)
    document.body.appendChild(link)
    link.click()
    link.remove()

    ElMessage.success('导出成功')
  } catch (error: any) {
    ElMessage.error('导出失败: ' + error.message)
  }
}

const refreshData = () => {
  loadStats()
  loadSessions()
}

const resetFilters = () => {
  Object.assign(filters, {
    compliance_status: '',
    user_intent: '',
    min_cost: '',
    max_cost: '',
    search: '',
  })
  loadSessions()
}

const formatDateTime = (dateStr: string) => {
  return new Date(dateStr).toLocaleString(localeRef.value)
}

const formatDuration = (seconds: number) => {
  if (seconds < 60) return `${seconds}秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}分${seconds % 60}秒`
  return `${Math.floor(seconds / 3600)}小时${Math.floor((seconds % 3600) / 60)}分`
}

const formatNumber = (num: number) => {
  return new Intl.NumberFormat('zh-CN').format(num)
}

const getIntentLabel = (intent: string) => {
  const labels: Record<string, string> = {
    chat: '聊天',
    code: '代码',
    tool_use: '工具',
    data_analysis: '分析',
    creative: '创意',
  }
  return labels[intent] || intent
}

const getComplianceType = (status: string) => {
  const types: Record<string, string> = {
    compliant: 'success',
    warning: 'warning',
    violation: 'danger',
  }
  return types[status] || ''
}

const getComplianceLabel = (status: string) => {
  const labels: Record<string, string> = {
    compliant: '合规',
    warning: '警告',
    violation: '违规',
  }
  return labels[status] || status
}

const getIssueTypeTag = (type: string) => {
  const tags: Record<string, string> = {
    pii: 'warning',
    toxic: 'danger',
    prompt_injection: 'danger',
    bias: 'warning',
    hallucination: 'info',
  }
  return tags[type] || ''
}

const getIssueTypeLabel = (type: string) => {
  const labels: Record<string, string> = {
    pii: 'PII',
    toxic: '毒性',
    prompt_injection: '提示词注入',
    bias: '偏见',
    hallucination: '幻觉',
  }
  return labels[type] || type
}

const getSeverityTag = (severity: number) => {
  if (severity >= 9) return 'danger'
  if (severity >= 7) return 'warning'
  return 'info'
}

onMounted(() => {
  loadStats()
  loadSessions()
})
</script>

<style scoped lang="scss">
.session-analytics-dashboard {
  padding: 20px;
}

.dashboard-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;

  h2 {
    margin: 0;
    font-size: 24px;
  }
}

.stats-row {
  margin-bottom: 20px;
}

.filter-card {
  margin-bottom: 20px;

  .filter-form {
    margin: 0;
  }
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.session-title {
  display: flex;
  align-items: center;
}

.session-detail {
  .detail-card {
    margin-bottom: 20px;

    h4 {
      margin: 0 0 12px 0;
      font-size: 14px;
      font-weight: bold;
    }

    p {
      margin: 0;
      line-height: 1.6;
      color: var(--muted, #8b949e);
    }
  }

  .cost-item {
    display: flex;
    justify-content: space-between;
    padding: 8px 0;
    border-bottom: 1px solid var(--border, #30363d);

    &:last-child {
      border-bottom: none;
    }
  }

  .timeline-card {
    background: #0f1117;
    border: 1px solid var(--border, #30363d);

    .timeline-content {
      .timeline-row {
        margin-bottom: 4px;

        .label {
          font-weight: bold;
          margin-right: 8px;
          color: var(--muted, #8b949e);
        }

        &.error {
          color: var(--danger, #f85149);
        }
      }
    }
  }
}

.loading-container {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 400px;
  color: var(--muted, #8b949e);

  .el-icon {
    font-size: 48px;
    margin-bottom: 16px;
  }
}
</style>
