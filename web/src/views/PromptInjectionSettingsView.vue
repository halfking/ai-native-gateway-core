<template>
  <div class="prompt-injection-settings">
    <div class="settings-header">
      <h2>提示词注入检测设置</h2>
      <p class="description">配置提示词注入检测规则，保护您的 LLM 应用免受恶意注入攻击</p>
    </div>

    <!-- 策略配置 -->
    <el-card class="section-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>基础配置</span>
          <el-switch
            v-model="policy.enabled"
            active-text="启用"
            inactive-text="禁用"
            @change="handlePolicyChange"
          />
        </div>
      </template>

      <el-form :model="policy" label-width="160px" label-position="left">
        <el-form-item label="检测模式">
          <el-radio-group v-model="policy.detection_mode" @change="handlePolicyChange">
            <el-radio label="observe">
              <span>观察模式</span>
              <el-tooltip content="仅记录检测结果，不阻断请求（推荐用于测试）" placement="top">
                <el-icon class="info-icon"><QuestionFilled /></el-icon>
              </el-tooltip>
            </el-radio>
            <el-radio label="enforce">
              <span>强制模式</span>
              <el-tooltip content="根据策略阻断高风险请求" placement="top">
                <el-icon class="info-icon"><QuestionFilled /></el-icon>
              </el-tooltip>
            </el-radio>
          </el-radio-group>
        </el-form-item>

        <el-divider />

        <h4>检测层级</h4>
        <el-form-item label="基础规则检测">
          <el-switch v-model="policy.enable_basic_rules" @change="handlePolicyChange" />
          <span class="help-text">包含 10+ 常见注入模式（角色劫持、指令泄漏）</span>
        </el-form-item>

        <el-form-item label="高级规则检测">
          <el-switch v-model="policy.enable_advanced_rules" @change="handlePolicyChange" />
          <span class="help-text">包含 15+ 高级绕过技术（DAN、编码绕过）</span>
        </el-form-item>

        <el-form-item label="启发式检测">
          <el-switch v-model="policy.enable_heuristics" @change="handlePolicyChange" />
          <span class="help-text">基于行为特征的智能检测</span>
        </el-form-item>

        <el-form-item label="ML 模型检测">
          <el-switch v-model="policy.enable_ml_model" @change="handlePolicyChange" :disabled="true" />
          <el-tag type="info" size="small">实验性功能</el-tag>
        </el-form-item>

        <el-divider />

        <h4>分数阈值配置</h4>
        <div class="threshold-config">
          <el-form-item label="记录阈值（3-5分）">
            <el-slider
              v-model="policy.score_threshold_log"
              :min="0"
              :max="10"
              :marks="{ 0: '0', 3: '3', 5: '5', 10: '10' }"
              @change="handlePolicyChange"
            />
          </el-form-item>

          <el-form-item label="警告阈值（6-7分）">
            <el-slider
              v-model="policy.score_threshold_warn"
              :min="0"
              :max="10"
              :marks="{ 0: '0', 6: '6', 7: '7', 10: '10' }"
              @change="handlePolicyChange"
            />
          </el-form-item>

          <el-form-item label="清洗阈值（8-9分）">
            <el-slider
              v-model="policy.score_threshold_sanitize"
              :min="0"
              :max="10"
              :marks="{ 0: '0', 8: '8', 9: '9', 10: '10' }"
              @change="handlePolicyChange"
            />
          </el-form-item>

          <el-form-item label="阻断阈值（10分）">
            <el-slider
              v-model="policy.score_threshold_block"
              :min="0"
              :max="10"
              :marks="{ 0: '0', 10: '10' }"
              @change="handlePolicyChange"
            />
          </el-form-item>
        </div>

        <el-divider />

        <h4>响应动作配置</h4>
        <el-form-item label="低风险（0-5分）">
          <el-select v-model="policy.action_on_low_risk" @change="handlePolicyChange">
            <el-option label="仅记录" value="log" />
            <el-option label="警告" value="warn" />
          </el-select>
        </el-form-item>

        <el-form-item label="中风险（6-7分）">
          <el-select v-model="policy.action_on_medium_risk" @change="handlePolicyChange">
            <el-option label="警告" value="warn" />
            <el-option label="清洗" value="sanitize" />
          </el-select>
        </el-form-item>

        <el-form-item label="高风险（8+分）">
          <el-select v-model="policy.action_on_high_risk" @change="handlePolicyChange">
            <el-option label="清洗" value="sanitize" />
            <el-option label="阻断" value="block" />
            <el-option label="人工审批" value="approval" />
          </el-select>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 白名单配置 -->
    <el-card class="section-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>白名单配置</span>
        </div>
      </template>

      <el-form label-width="160px">
        <el-form-item label="白名单正则">
          <el-input
            v-model="whitelistPatternInput"
            placeholder="例如: ^测试.*$"
            @keyup.enter="addWhitelistPattern"
          >
            <template #append>
              <el-button @click="addWhitelistPattern">添加</el-button>
            </template>
          </el-input>
          <div class="whitelist-tags">
            <el-tag
              v-for="(pattern, index) in policy.whitelist_patterns"
              :key="index"
              closable
              @close="removeWhitelistPattern(index)"
              style="margin: 4px"
            >
              {{ pattern }}
            </el-tag>
          </div>
        </el-form-item>

        <el-form-item label="白名单用户">
          <el-input
            v-model="whitelistUserInput"
            placeholder="用户邮箱或 ID"
            @keyup.enter="addWhitelistUser"
          >
            <template #append>
              <el-button @click="addWhitelistUser">添加</el-button>
            </template>
          </el-input>
          <div class="whitelist-tags">
            <el-tag
              v-for="(user, index) in policy.whitelist_users"
              :key="index"
              closable
              @close="removeWhitelistUser(index)"
              style="margin: 4px"
            >
              {{ user }}
            </el-tag>
          </div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 通知配置 -->
    <el-card class="section-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>通知配置</span>
          <el-switch
            v-model="policy.notify_on_detection"
            active-text="启用"
            inactive-text="禁用"
            @change="handlePolicyChange"
          />
        </div>
      </template>

      <el-form label-width="160px" v-if="policy.notify_on_detection">
        <el-form-item label="Webhook URL">
          <el-input
            v-model="policy.notification_webhook"
            placeholder="https://your-webhook.com/endpoint"
            @change="handlePolicyChange"
          />
        </el-form-item>

        <el-form-item label="通知邮箱">
          <el-input
            v-model="policy.notification_email"
            placeholder="admin@example.com"
            @change="handlePolicyChange"
          />
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 统计信息 -->
    <el-card class="section-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>今日统计</span>
          <el-button size="small" @click="refreshStats">刷新</el-button>
        </div>
      </template>

      <el-row :gutter="20">
        <el-col :span="6">
          <el-statistic title="总检测次数" :value="stats.total_detections">
            <template #suffix>次</template>
          </el-statistic>
        </el-col>
        <el-col :span="6">
          <el-statistic title="阻断次数" :value="stats.blocked_count">
            <template #suffix>次</template>
          </el-statistic>
        </el-col>
        <el-col :span="6">
          <el-statistic title="平均评分" :value="stats.avg_score" :precision="1">
            <template #suffix>/ 10</template>
          </el-statistic>
        </el-col>
        <el-col :span="6">
          <el-statistic title="最高评分" :value="stats.max_score">
            <template #suffix>/ 10</template>
          </el-statistic>
        </el-col>
      </el-row>

      <el-divider />

      <div class="risk-distribution">
        <h4>风险等级分布</h4>
        <el-row :gutter="10">
          <el-col :span="6">
            <div class="risk-item risk-critical">
              <div class="risk-label">严重</div>
              <div class="risk-count">{{ stats.critical_count }}</div>
            </div>
          </el-col>
          <el-col :span="6">
            <div class="risk-item risk-high">
              <div class="risk-label">高</div>
              <div class="risk-count">{{ stats.high_count }}</div>
            </div>
          </el-col>
          <el-col :span="6">
            <div class="risk-item risk-medium">
              <div class="risk-label">中</div>
              <div class="risk-count">{{ stats.medium_count }}</div>
            </div>
          </el-col>
          <el-col :span="6">
            <div class="risk-item risk-low">
              <div class="risk-label">低</div>
              <div class="risk-count">{{ stats.low_count }}</div>
            </div>
          </el-col>
        </el-row>
      </div>

      <el-divider />

      <div class="policy-stats">
        <p><strong>策略信息：</strong></p>
        <p>总检测次数（累计）: {{ policy.total_detections }}</p>
        <p>总阻断次数（累计）: {{ policy.total_blocks }}</p>
        <p>最后检测时间: {{ policy.last_detection_at || '暂无' }}</p>
      </div>
    </el-card>

    <!-- 检测规则列表 -->
    <el-card class="section-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>检测规则列表</span>
          <el-button-group>
            <el-button
              size="small"
              :type="ruleFilter === 'all' ? 'primary' : ''"
              @click="ruleFilter = 'all'; loadRules()"
            >
              全部 ({{ rules.length }})
            </el-button>
            <el-button
              size="small"
              :type="ruleFilter === 'basic' ? 'primary' : ''"
              @click="ruleFilter = 'basic'; loadRules()"
            >
              基础规则
            </el-button>
            <el-button
              size="small"
              :type="ruleFilter === 'advanced' ? 'primary' : ''"
              @click="ruleFilter = 'advanced'; loadRules()"
            >
              高级规则
            </el-button>
          </el-button-group>
        </div>
      </template>

      <el-table :data="rules" style="width: 100%" stripe>
        <el-table-column prop="rule_name" label="规则名称" width="250" />
        <el-table-column prop="category" label="分类" width="150">
          <template #default="scope">
            <el-tag :type="getCategoryType(scope.row.category)" size="small">
              {{ getCategoryLabel(scope.row.category) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="severity" label="严重等级" width="100">
          <template #default="scope">
            <el-tag :type="getSeverityType(scope.row.severity)" size="small">
              {{ scope.row.severity }}/10
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="说明" show-overflow-tooltip />
        <el-table-column prop="enabled" label="状态" width="100">
          <template #default="scope">
            <el-switch
              v-model="scope.row.enabled"
              @change="toggleRule(scope.row)"
            />
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 检测日志 -->
    <el-card class="section-card" shadow="hover">
      <template #header>
        <div class="card-header">
          <span>检测日志</span>
          <el-button size="small" @click="loadDetections">刷新</el-button>
        </div>
      </template>

      <el-form :inline="true" class="filter-form">
        <el-form-item label="风险等级">
          <el-select v-model="detectionFilter.risk_level" @change="loadDetections" clearable>
            <el-option label="严重" value="critical" />
            <el-option label="高" value="high" />
            <el-option label="中" value="medium" />
            <el-option label="低" value="low" />
          </el-select>
        </el-form-item>

        <el-form-item label="是否阻断">
          <el-select v-model="detectionFilter.blocked" @change="loadDetections" clearable>
            <el-option label="已阻断" value="true" />
            <el-option label="未阻断" value="false" />
          </el-select>
        </el-form-item>

        <el-form-item label="会话">
          <el-input
            v-model="detectionFilter.session_key"
            placeholder="Session Key"
            @keyup.enter="loadDetections"
            clearable
          />
        </el-form-item>
      </el-form>

      <el-table :data="detections" style="width: 100%" stripe>
        <el-table-column prop="detected_at" label="时间" width="180" />
        <el-table-column prop="request_id" label="请求 ID" width="200" show-overflow-tooltip />
        <el-table-column prop="detection_score" label="评分" width="80">
          <template #default="scope">
            <el-tag :type="getSeverityType(scope.row.detection_score)" size="small">
              {{ scope.row.detection_score }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="risk_level" label="风险等级" width="100">
          <template #default="scope">
            <el-tag :type="getRiskLevelType(scope.row.risk_level)" size="small">
              {{ getRiskLevelLabel(scope.row.risk_level) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="matched_rules_count" label="匹配规则数" width="110" />
        <el-table-column prop="action_taken" label="动作" width="100">
          <template #default="scope">
            <el-tag :type="getActionType(scope.row.action_taken)" size="small">
              {{ getActionLabel(scope.row.action_taken) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="blocked" label="阻断" width="80">
          <template #default="scope">
            <el-icon v-if="scope.row.blocked" color="#f56c6c"><CircleCloseFilled /></el-icon>
            <el-icon v-else color="#67c23a"><CircleCheckFilled /></el-icon>
          </template>
        </el-table-column>
        <el-table-column prop="evidence_text" label="证据" show-overflow-tooltip />
      </el-table>

      <el-pagination
        v-model:current-page="detectionPagination.page"
        v-model:page-size="detectionPagination.page_size"
        :total="detectionPagination.total"
        :page-sizes="[10, 20, 50, 100]"
        layout="total, sizes, prev, pager, next, jumper"
        @current-change="loadDetections"
        @size-change="loadDetections"
        style="margin-top: 20px"
      />
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { QuestionFilled, CircleCloseFilled, CircleCheckFilled } from '@element-plus/icons-vue'
import api from '@/api'

// 策略配置
const policy = reactive({
  tenant_id: '',
  enabled: true,
  detection_mode: 'observe',
  enable_basic_rules: true,
  enable_advanced_rules: true,
  enable_heuristics: true,
  enable_ml_model: false,
  score_threshold_log: 3,
  score_threshold_warn: 6,
  score_threshold_sanitize: 8,
  score_threshold_block: 10,
  action_on_low_risk: 'log',
  action_on_medium_risk: 'warn',
  action_on_high_risk: 'block',
  whitelist_patterns: [] as string[],
  whitelist_users: [] as string[],
  notify_on_detection: false,
  notification_webhook: '',
  notification_email: '',
  total_detections: 0,
  total_blocks: 0,
  last_detection_at: null as string | null,
})

// 统计数据
const stats = reactive({
  total_detections: 0,
  blocked_count: 0,
  critical_count: 0,
  high_count: 0,
  medium_count: 0,
  low_count: 0,
  avg_score: 0,
  max_score: 0,
})

// 规则列表
const rules = ref([])
const ruleFilter = ref('all')

// 检测日志
const detections = ref([])
const detectionFilter = reactive({
  risk_level: '',
  blocked: '',
  session_key: '',
})
const detectionPagination = reactive({
  page: 1,
  page_size: 20,
  total: 0,
})

// 白名单输入
const whitelistPatternInput = ref('')
const whitelistUserInput = ref('')

// 加载策略
const loadPolicy = async () => {
  try {
    const res = await api.get('/admin/prompt-injection/policy')
    Object.assign(policy, res.data)
  } catch (error: any) {
    ElMessage.error('加载策略失败: ' + error.message)
  }
}

// 更新策略
const handlePolicyChange = async () => {
  try {
    await api.put('/admin/prompt-injection/policy', policy)
    ElMessage.success('策略已更新')
  } catch (error: any) {
    ElMessage.error('更新策略失败: ' + error.message)
  }
}

// 白名单操作
const addWhitelistPattern = () => {
  if (whitelistPatternInput.value.trim()) {
    policy.whitelist_patterns.push(whitelistPatternInput.value.trim())
    whitelistPatternInput.value = ''
    handlePolicyChange()
  }
}

const removeWhitelistPattern = (index: number) => {
  policy.whitelist_patterns.splice(index, 1)
  handlePolicyChange()
}

const addWhitelistUser = () => {
  if (whitelistUserInput.value.trim()) {
    policy.whitelist_users.push(whitelistUserInput.value.trim())
    whitelistUserInput.value = ''
    handlePolicyChange()
  }
}

const removeWhitelistUser = (index: number) => {
  policy.whitelist_users.splice(index, 1)
  handlePolicyChange()
}

// 加载统计
const refreshStats = async () => {
  try {
    const res = await api.get('/admin/prompt-injection/stats')
    Object.assign(stats, res.data)
  } catch (error: any) {
    ElMessage.error('加载统计失败: ' + error.message)
  }
}

// 加载规则
const loadRules = async () => {
  try {
    const params = ruleFilter.value !== 'all' ? { type: ruleFilter.value } : {}
    const res = await api.get('/admin/prompt-injection/rules', { params })
    rules.value = res.data.rules
  } catch (error: any) {
    ElMessage.error('加载规则失败: ' + error.message)
  }
}

// 切换规则
const toggleRule = async (rule: any) => {
  try {
    await api.patch(`/admin/prompt-injection/rules/${rule.id}/toggle`, {
      enabled: rule.enabled,
    })
    ElMessage.success(`规则 ${rule.rule_name} 已${rule.enabled ? '启用' : '禁用'}`)
  } catch (error: any) {
    ElMessage.error('切换规则失败: ' + error.message)
    rule.enabled = !rule.enabled // 回滚
  }
}

// 加载检测日志
const loadDetections = async () => {
  try {
    const params = {
      page: detectionPagination.page,
      page_size: detectionPagination.page_size,
      ...detectionFilter,
    }
    const res = await api.get('/admin/prompt-injection/detections', { params })
    detections.value = res.data.detections
    detectionPagination.total = res.data.total
  } catch (error: any) {
    ElMessage.error('加载检测日志失败: ' + error.message)
  }
}

// 辅助函数
const getCategoryType = (category: string) => {
  const types: Record<string, string> = {
    role_hijack: 'danger',
    instruction_leak: 'warning',
    dan: 'danger',
    bypass: 'info',
  }
  return types[category] || ''
}

const getCategoryLabel = (category: string) => {
  const labels: Record<string, string> = {
    role_hijack: '角色劫持',
    instruction_leak: '指令泄漏',
    dan: 'DAN越狱',
    bypass: '绕过技术',
  }
  return labels[category] || category
}

const getSeverityType = (severity: number) => {
  if (severity >= 9) return 'danger'
  if (severity >= 7) return 'warning'
  if (severity >= 5) return 'info'
  return 'success'
}

const getRiskLevelType = (level: string) => {
  const types: Record<string, string> = {
    critical: 'danger',
    high: 'danger',
    medium: 'warning',
    low: 'info',
  }
  return types[level] || ''
}

const getRiskLevelLabel = (level: string) => {
  const labels: Record<string, string> = {
    critical: '严重',
    high: '高',
    medium: '中',
    low: '低',
  }
  return labels[level] || level
}

const getActionType = (action: string) => {
  const types: Record<string, string> = {
    block: 'danger',
    sanitize: 'warning',
    warn: 'info',
    log: 'success',
  }
  return types[action] || ''
}

const getActionLabel = (action: string) => {
  const labels: Record<string, string> = {
    block: '阻断',
    sanitize: '清洗',
    warn: '警告',
    log: '记录',
  }
  return labels[action] || action
}

onMounted(() => {
  loadPolicy()
  refreshStats()
  loadRules()
  loadDetections()
})
</script>

<style scoped lang="scss">
.prompt-injection-settings {
  padding: 20px;
}

.settings-header {
  margin-bottom: 24px;

  h2 {
    margin: 0 0 8px 0;
    font-size: 24px;
  }

  .description {
    color: #909399;
    margin: 0;
  }
}

.section-card {
  margin-bottom: 20px;

  .card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
}

.help-text {
  margin-left: 12px;
  color: #909399;
  font-size: 12px;
}

.info-icon {
  margin-left: 4px;
  color: #909399;
  cursor: help;
}

.threshold-config {
  padding: 0 20px;
}

.whitelist-tags {
  margin-top: 8px;
}

.risk-distribution {
  h4 {
    margin: 0 0 16px 0;
  }

  .risk-item {
    padding: 16px;
    border-radius: 4px;
    text-align: center;

    .risk-label {
      font-size: 14px;
      margin-bottom: 8px;
    }

    .risk-count {
      font-size: 24px;
      font-weight: bold;
    }

    &.risk-critical {
      background: #fef0f0;
      color: #f56c6c;
    }

    &.risk-high {
      background: #fef0f0;
      color: #f56c6c;
    }

    &.risk-medium {
      background: #fdf6ec;
      color: #e6a23c;
    }

    &.risk-low {
      background: #f0f9ff;
      color: #409eff;
    }
  }
}

.policy-stats {
  p {
    margin: 4px 0;
    color: #606266;
  }
}

.filter-form {
  margin-bottom: 16px;
}
</style>
