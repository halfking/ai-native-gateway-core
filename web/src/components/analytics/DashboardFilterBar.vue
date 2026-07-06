<template>
  <div class="dashboard-filter-bar">
    <el-card shadow="never" :body-style="{ padding: '16px' }">
      <div class="filter-row">
        <!-- 日期范围 -->
        <div class="filter-group">
          <label class="filter-label">时间范围</label>
          <div class="date-presets">
            <el-button
              v-for="preset in datePresets"
              :key="preset.value"
              size="small"
              :type="isPresetActive(preset.value) ? 'primary' : 'default'"
              @click="applyDatePreset(preset.value)"
            >
              {{ preset.label }}
            </el-button>
          </div>
          <el-date-picker
            v-model="dateRange"
            type="daterange"
            range-separator="-"
            start-placeholder="开始日期"
            end-placeholder="结束日期"
            :disabled-date="disabledDate"
            size="small"
            style="width: 280px; margin-left: 12px"
            @change="onDateChange"
          />
        </div>

        <!-- 模型筛选 -->
        <div class="filter-group">
          <label class="filter-label">模型</label>
          <el-select
            v-model="localFilters.model"
            multiple
            collapse-tags
            placeholder="选择模型"
            size="small"
            style="width: 200px"
            @change="onFilterChange"
          >
            <el-option
              v-for="model in modelOptions"
              :key="model"
              :label="model"
              :value="model"
            />
          </el-select>
        </div>

        <!-- 提供商筛选 -->
        <div class="filter-group">
          <label class="filter-label">提供商</label>
          <el-select
            v-model="localFilters.provider"
            multiple
            collapse-tags
            placeholder="选择提供商"
            size="small"
            style="width: 180px"
            @change="onFilterChange"
          >
            <el-option
              v-for="provider in providerOptions"
              :key="provider"
              :label="provider"
              :value="provider"
            />
          </el-select>
        </div>

        <!-- 合规状态 -->
        <div class="filter-group">
          <label class="filter-label">合规状态</label>
          <el-select
            v-model="localFilters.complianceStatus"
            placeholder="选择状态"
            clearable
            size="small"
            style="width: 140px"
            @change="onFilterChange"
          >
            <el-option label="合规" value="compliant" />
            <el-option label="警告" value="warning" />
            <el-option label="违规" value="violation" />
          </el-select>
        </div>

        <!-- 用户意图 -->
        <div class="filter-group">
          <label class="filter-label">用户意图</label>
          <el-select
            v-model="localFilters.userIntent"
            placeholder="选择意图"
            clearable
            size="small"
            style="width: 140px"
            @change="onFilterChange"
          >
            <el-option label="通用对话" value="chat" />
            <el-option label="代码生成" value="code" />
            <el-option label="工具调用" value="tool_use" />
            <el-option label="数据分析" value="data_analysis" />
            <el-option label="创意写作" value="creative" />
          </el-select>
        </div>

        <!-- 健康等级 -->
        <div class="filter-group">
          <label class="filter-label">健康等级</label>
          <el-select
            v-model="localFilters.healthGrade"
            placeholder="选择等级"
            clearable
            size="small"
            style="width: 120px"
            @change="onFilterChange"
          >
            <el-option label="A (优秀)" value="A" />
            <el-option label="B (良好)" value="B" />
            <el-option label="C (一般)" value="C" />
            <el-option label="D (较差)" value="D" />
            <el-option label="F (异常)" value="F" />
          </el-select>
        </div>

        <!-- 重置按钮 -->
        <div class="filter-actions">
          <el-button size="small" @click="resetFilters">重置</el-button>
        </div>
      </div>

      <!-- 高级筛选（可展开） -->
      <el-collapse v-model="advancedOpen" style="margin-top: 12px">
        <el-collapse-item name="advanced">
          <template #title>
            <span style="font-size: 13px; color: #606266">高级筛选</span>
          </template>
          <div class="advanced-filters">
            <div class="filter-group">
              <label class="filter-label">成本范围 (USD)</label>
              <el-input
                v-model.number="localFilters.minCost"
                placeholder="最小"
                size="small"
                style="width: 100px"
                @change="onFilterChange"
              />
              <span style="margin: 0 8px">-</span>
              <el-input
                v-model.number="localFilters.maxCost"
                placeholder="最大"
                size="small"
                style="width: 100px"
                @change="onFilterChange"
              />
            </div>

            <div class="filter-group">
              <label class="filter-label">延迟范围 (ms)</label>
              <el-input
                v-model.number="localFilters.latencyMin"
                placeholder="最小"
                size="small"
                style="width: 100px"
                @change="onFilterChange"
              />
              <span style="margin: 0 8px">-</span>
              <el-input
                v-model.number="localFilters.latencyMax"
                placeholder="最大"
                size="small"
                style="width: 100px"
                @change="onFilterChange"
              />
            </div>

            <div class="filter-group">
              <label class="filter-label">最小 Token 数</label>
              <el-input
                v-model.number="localFilters.tokenMin"
                placeholder="最小 Token"
                size="small"
                style="width: 150px"
                @change="onFilterChange"
              />
            </div>

            <div class="filter-group">
              <label class="filter-label">最小请求数</label>
              <el-input
                v-model.number="localFilters.requestCountMin"
                placeholder="最小请求"
                size="small"
                style="width: 150px"
                @change="onFilterChange"
              />
            </div>
          </div>
        </el-collapse-item>
      </el-collapse>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { ref, watch, onMounted, computed } from 'vue'
import { ElMessage } from 'element-plus'

export interface AnalyticsFilters {
  dateFrom: string
  dateTo: string
  model: string[]
  provider: string[]
  complianceStatus: string
  userIntent: string
  healthGrade: string
  minCost: number | null
  maxCost: number | null
  latencyMin: number | null
  latencyMax: number | null
  tokenMin: number | null
  requestCountMin: number | null
}

const props = defineProps<{
  modelValue: AnalyticsFilters
  modelOptions?: string[]
  providerOptions?: string[]
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: AnalyticsFilters): void
  (e: 'change', value: AnalyticsFilters): void
}>()

// 本地筛选状态
const localFilters = ref<AnalyticsFilters>({ ...props.modelValue })
const dateRange = ref<[Date, Date]>([new Date(), new Date()])
const advancedOpen = ref<string[]>([])

// 日期预设
const datePresets = [
  { label: '今天', value: 0 },
  { label: '7天', value: 7 },
  { label: '30天', value: 30 },
  { label: '90天', value: 90 }
]

// 模型和提供商选项（从 props 或默认）
const modelOptions = computed(() => props.modelOptions || [
  'gpt-4o',
  'gpt-4o-mini',
  'gpt-4-turbo',
  'claude-3-5-sonnet',
  'claude-3-opus',
  'gemini-1.5-pro'
])

const providerOptions = computed(() => props.providerOptions || [
  'openai',
  'anthropic',
  'google',
  'azure',
  'aws'
])

// 检查日期预设是否激活
const isPresetActive = (days: number) => {
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const from = new Date(dateRange.value[0])
  from.setHours(0, 0, 0, 0)
  const to = new Date(dateRange.value[1])
  to.setHours(23, 59, 59, 999)

  if (days === 0) {
    // 今天
    return from.getTime() === today.getTime() && to.getTime() >= today.getTime()
  } else {
    const expectedFrom = new Date(today)
    expectedFrom.setDate(expectedFrom.getDate() - days)
    const diff = Math.abs(from.getTime() - expectedFrom.getTime())
    return diff < 24 * 60 * 60 * 1000 // 1天容差
  }
}

// 应用日期预设
const applyDatePreset = (days: number) => {
  const today = new Date()
  today.setHours(23, 59, 59, 999)

  if (days === 0) {
    // 今天
    const start = new Date()
    start.setHours(0, 0, 0, 0)
    dateRange.value = [start, today]
  } else {
    const start = new Date()
    start.setDate(start.getDate() - days)
    start.setHours(0, 0, 0, 0)
    dateRange.value = [start, today]
  }

  onDateChange()
}

// 禁用未来日期
const disabledDate = (date: Date) => {
  return date.getTime() > Date.now()
}

// 日期变更
const onDateChange = () => {
  if (!dateRange.value || dateRange.value.length !== 2) return

  const [from, to] = dateRange.value

  // 校验日期范围（最大90天）
  const diffDays = Math.ceil((to.getTime() - from.getTime()) / (1000 * 60 * 60 * 24))
  if (diffDays > 90) {
    ElMessage.warning('日期范围不能超过 90 天，请缩小范围')
    applyDatePreset(90)
    return
  }

  localFilters.value.dateFrom = from.toISOString().split('T')[0]
  localFilters.value.dateTo = to.toISOString().split('T')[0]

  onFilterChange()
}

// 筛选变更（防抖处理在父组件）
let changeTimer: ReturnType<typeof setTimeout> | null = null
const onFilterChange = () => {
  if (changeTimer) {
    clearTimeout(changeTimer)
  }

  changeTimer = setTimeout(() => {
    emit('update:modelValue', { ...localFilters.value })
    emit('change', { ...localFilters.value })
    // 持久化到 sessionStorage
    saveFilters()
  }, 500)
}

// 重置筛选
const resetFilters = () => {
  applyDatePreset(7) // 默认最近7天
  localFilters.value = {
    dateFrom: localFilters.value.dateFrom,
    dateTo: localFilters.value.dateTo,
    model: [],
    provider: [],
    complianceStatus: '',
    userIntent: '',
    healthGrade: '',
    minCost: null,
    maxCost: null,
    latencyMin: null,
    latencyMax: null,
    tokenMin: null,
    requestCountMin: null
  }
  advancedOpen.value = []
  onFilterChange()
}

// 持久化筛选器到 sessionStorage
const saveFilters = () => {
  try {
    sessionStorage.setItem('analyticsFilters', JSON.stringify(localFilters.value))
  } catch (e) {
    console.warn('Failed to save filters to sessionStorage:', e)
  }
}

// 从 sessionStorage 恢复筛选器
const loadFilters = () => {
  try {
    const saved = sessionStorage.getItem('analyticsFilters')
    if (saved) {
      const parsed = JSON.parse(saved)
      localFilters.value = { ...localFilters.value, ...parsed }

      // 恢复日期范围
      if (parsed.dateFrom && parsed.dateTo) {
        dateRange.value = [new Date(parsed.dateFrom), new Date(parsed.dateTo)]
      }
    }
  } catch (e) {
    console.warn('Failed to load filters from sessionStorage:', e)
  }
}

// 监听外部变更
watch(
  () => props.modelValue,
  (newVal) => {
    localFilters.value = { ...newVal }
  },
  { deep: true }
)

onMounted(() => {
  // 首次加载时尝试恢复筛选器，否则使用默认（最近7天）
  loadFilters()

  // 如果没有保存的筛选器，使用默认
  if (!sessionStorage.getItem('analyticsFilters')) {
    applyDatePreset(7)
  } else {
    // 触发初始加载
    emit('change', { ...localFilters.value })
  }
})
</script>

<style scoped>
.dashboard-filter-bar {
  margin-bottom: 20px;
}

.filter-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
}

.filter-group {
  display: flex;
  align-items: center;
  gap: 8px;
}

.filter-label {
  font-size: 13px;
  color: #606266;
  white-space: nowrap;
  font-weight: 500;
}

.date-presets {
  display: flex;
  gap: 4px;
}

.filter-actions {
  margin-left: auto;
}

.advanced-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  padding: 8px 0;
}

:deep(.el-card__body) {
  padding: 16px;
}

:deep(.el-collapse-item__header) {
  height: 36px;
  line-height: 36px;
  font-size: 13px;
}

:deep(.el-collapse-item__content) {
  padding-bottom: 12px;
}
</style>
