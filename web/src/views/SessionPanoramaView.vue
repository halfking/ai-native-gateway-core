<template>
  <div class="session-panorama">
    <!-- 顶部：标题 + 摘要 + 标签 -->
    <el-page-header @back="$router.push('/admin/session-analytics')" style="margin-bottom: 16px">
      <template #content>
        <span class="pano-title">{{ panorama?.summary?.title || '会话全景图' }}</span>
      </template>
    </el-page-header>

    <div v-if="loading" class="loading-box">
      <el-icon class="is-loading"><Loading /></el-icon>
      <span>加载全景数据...</span>
    </div>

    <template v-else-if="panorama">
      <!-- 关键指标卡片 -->
      <el-row :gutter="16" class="metric-row">
        <el-col :span="4">
          <el-card shadow="hover"><el-statistic title="请求数" :value="panorama.summary.request_count" /></el-card>
        </el-col>
        <el-col :span="4">
          <el-card shadow="hover"><el-statistic title="成功率" :value="successRate" :precision="1" suffix="%" /></el-card>
        </el-col>
        <el-col :span="4">
          <el-card shadow="hover"><el-statistic title="总成本" :value="panorama.summary.total_cost_usd" :precision="4" prefix="$" /></el-card>
        </el-col>
        <el-col :span="4">
          <el-card shadow="hover"><el-statistic title="总 Tokens" :value="panorama.summary.total_tokens" /></el-card>
        </el-col>
        <el-col :span="4">
          <el-card shadow="hover"><el-statistic title="花费时间" :value="panorama.summary.duration_seconds" suffix="s" /></el-card>
        </el-col>
        <el-col :span="4">
          <el-card shadow="hover"><el-statistic title="平均延迟" :value="panorama.summary.avg_latency_ms" suffix="ms" /></el-card>
        </el-col>
      </el-row>

      <!-- 成本与节省 -->
      <el-row :gutter="16" style="margin-top: 16px">
        <el-col :span="12">
          <el-card shadow="never">
            <template #header>成本分解</template>
            <el-descriptions :column="2" border size="small">
              <el-descriptions-item label="输入成本">${{ panorama.summary.input_cost_usd.toFixed(4) }}</el-descriptions-item>
              <el-descriptions-item label="输出成本">${{ panorama.summary.output_cost_usd.toFixed(4) }}</el-descriptions-item>
              <el-descriptions-item label="总成本">${{ panorama.summary.total_cost_usd.toFixed(4) }}</el-descriptions-item>
              <el-descriptions-item label="主要模型">{{ panorama.summary.primary_model || '—' }}</el-descriptions-item>
            </el-descriptions>
            <div v-if="cacheSavings > 0 || compressionSavings > 0" class="savings-box">
              <h4>💰 已节省</h4>
              <p v-if="cacheSavings > 0">缓存命中节省 ~{{ cacheSavings.toLocaleString() }} tokens</p>
              <p v-if="compressionSavings > 0">压缩节省 {{ panorama.analysis.compression_savings?.compressed_requests || 0 }} 次请求</p>
            </div>
          </el-card>
        </el-col>
        <el-col :span="12">
          <el-card shadow="never">
            <template #header>
              <span>标签</span>
              <el-button size="small" text style="float: right" @click="tagDialogVisible = true">+ 添加</el-button>
            </template>
            <div class="tag-cloud">
              <el-tag
                v-for="tag in panorama.tags" :key="tag.id"
                :type="tagSourceColor(tag.tag_source)" size="small"
                style="margin: 2px"
                :aria-label="`${tag.tag_key}: ${tag.tag_value}`"
              >
                {{ tag.tag_key }}: {{ tag.tag_value }}
              </el-tag>
              <span v-if="panorama.tags.length === 0" class="muted">暂无标签</span>
            </div>
            <div v-if="panorama.cluster" class="cluster-box">
              <h4>🔗 所属聚类</h4>
              <p>{{ panorama.cluster.label || panorama.cluster.cluster_id }}（相似度 {{ (panorama.cluster.score * 100).toFixed(0) }}%）</p>
            </div>
          </el-card>
        </el-col>
      </el-row>

      <!-- 健康面板 -->
      <HealthPanel 
        :gw-session-id="gwSessionId" 
        @jump-to="handleJumpTo"
        ref="healthPanelRef"
      />

      <!-- 会话总结 -->
      <el-card v-if="panorama.summary.summary" shadow="never" style="margin-top: 16px">
        <template #header>会话总结</template>
        <p>{{ panorama.summary.summary }}</p>
        <div v-if="panorama.summary.key_topics?.length">
          <el-tag v-for="t in panorama.summary.key_topics" :key="t" type="info" size="small" style="margin-right: 6px">{{ t }}</el-tag>
        </div>
      </el-card>

      <!-- 逐步摘要时间线 -->
      <el-card id="timeline" shadow="never" style="margin-top: 16px">
        <template #header>逐步摘要（{{ panorama.step_summaries.length }} 步）</template>
        <el-timeline>
          <el-timeline-item
            v-for="step in panorama.step_summaries" :key="step.request_id"
            :timestamp="`第 ${step.step_index} 步`" placement="top"
          >
            <div class="step-card">
              <el-tag v-if="step.is_llm_generated" type="success" size="small">LLM</el-tag>
              <el-tag v-else size="small" type="info">规则</el-tag>
              <p v-if="step.request_summary" class="step-req">📥 {{ step.request_summary }}</p>
              <p v-if="step.response_summary" class="step-res">📤 {{ step.response_summary }}</p>
              <p v-if="step.tool_calls_summary" class="step-tools">🔧 {{ step.tool_calls_summary }}</p>
            </div>
          </el-timeline-item>
        </el-timeline>
        <el-empty v-if="panorama.step_summaries.length === 0" description="暂无逐步摘要" />
      </el-card>

      <!-- 模型切换可视化 -->
      <el-card id="model-switches" shadow="never" style="margin-top: 16px" v-if="panorama.analysis?.model_switches?.length">
        <template #header>模型切换历史（{{ panorama.analysis.model_switches.length }} 次）</template>
        <el-timeline>
          <el-timeline-item
            v-for="(sw, idx) in panorama.analysis.model_switches"
            :key="idx"
            :timestamp="sw.reason || '切换'"
            placement="top"
          >
            <div>
              <el-tag type="info" size="small">{{ sw.from_model }}</el-tag>
              <el-icon style="margin: 0 8px"><Right /></el-icon>
              <el-tag type="primary" size="small">{{ sw.to_model }}</el-tag>
            </div>
          </el-timeline-item>
        </el-timeline>
      </el-card>

      <!-- 优化建议 -->
      <el-card id="suggestions" shadow="never" style="margin-top: 16px">
        <template #header>
          <span>优化建议（{{ panorama.suggestions.length }}）</span>
        </template>
        <el-table :data="panorama.suggestions" stripe size="small">
          <el-table-column prop="severity" label="级别" width="110">
            <template #default="scope">
              <el-tag :type="severityColor(scope?.row?.severity)" size="small">{{ severityLabel(scope?.row?.severity) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="title" label="建议" min-width="200" />
          <el-table-column label="潜在节省" width="160">
            <template #default="scope">
              <span v-if="scope?.row?.potential_savings_cost > 0">${{ scope?.row?.potential_savings_cost.toFixed(4) }}</span>
              <span v-else-if="scope?.row?.potential_savings_tokens > 0">{{ scope?.row?.potential_savings_tokens.toLocaleString() }} tokens</span>
              <span v-else>—</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="scope">
              <el-button v-if="!scope?.row?.applied && !scope?.row?.dismissed" size="small" type="primary" text @click="applySugg(scope?.row)">采纳</el-button>
              <el-tag v-else-if="scope?.row?.applied" type="success" size="small">已采纳</el-tag>
            </template>
          </el-table-column>
        </el-table>
        <el-empty v-if="panorama.suggestions.length === 0" description="暂无优化建议（当前会话已较优）" />
      </el-card>
    </template>

    <div v-else class="error-box">
      <el-result icon="error" title="加载失败" sub-title="无法加载会话全景数据，请稍后重试">
        <template #extra>
          <el-button type="primary" @click="loadPanorama">重新加载</el-button>
          <el-button @click="$router.push('/admin/session-analytics')">返回列表</el-button>
        </template>
      </el-result>
    </div>

    <!-- 添加标签对话框 -->
    <el-dialog v-model="tagDialogVisible" title="添加标签" width="400px">
      <el-form label-width="80px">
        <el-form-item label="键">
          <el-input v-model="newTag.key" placeholder="如 topic / custom" />
        </el-form-item>
        <el-form-item label="值">
          <el-input v-model="newTag.value" placeholder="标签值" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="tagDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="addTag">添加</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Loading, Right } from '@element-plus/icons-vue'
import {
  getSessionPanorama, addSessionTag, applySuggestion,
} from '../api/sessionAnalytics'
import type { SessionPanorama } from '../api/sessionAnalytics'
import HealthPanel from '../components/session/HealthPanel.vue'

const route = useRoute()
const gwSessionId = computed(() => route.params.id as string)

const panorama = ref<SessionPanorama | null>(null)
const loading = ref(true)
const tagDialogVisible = ref(false)
const newTag = ref({ key: '', value: '' })
const healthPanelRef = ref<InstanceType<typeof HealthPanel> | null>(null)

const successRate = computed(() => {
  if (!panorama.value || panorama.value.summary.request_count === 0) return 0
  return (panorama.value.summary.success_count / panorama.value.summary.request_count) * 100
})

const cacheSavings = computed(() => {
  const a = panorama.value?.analysis
  return a?.cache_savings?.cache_read_tokens || 0
})

const compressionSavings = computed(() => {
  const a = panorama.value?.analysis
  return a?.compression_savings?.compressed_requests || 0
})

const loadPanorama = async () => {
  loading.value = true
  try {
    panorama.value = await getSessionPanorama(gwSessionId.value)
  } catch (e: any) {
    ElMessage.error('加载全景数据失败: ' + e.message)
  } finally {
    loading.value = false
  }
}

const addTag = async () => {
  if (!newTag.value.key || !newTag.value.value) return
  try {
    await addSessionTag(gwSessionId.value, newTag.value.key, newTag.value.value)
    ElMessage.success('标签已添加')
    tagDialogVisible.value = false
    newTag.value = { key: '', value: '' }
    loadPanorama()
  } catch (e: any) {
    ElMessage.error('添加失败: ' + e.message)
  }
}

const applySugg = async (row: any) => {
  try {
    await applySuggestion(gwSessionId.value, row.id)
    ElMessage.success('建议已采纳')
    loadPanorama()
  } catch (e: any) {
    ElMessage.error('操作失败: ' + e.message)
  }
}

const tagSourceColor = (src: string) => {
  if (src === 'manual') return 'success'
  if (src === 'llm') return 'warning'
  return 'info'
}

const severityColor = (s: string) => {
  if (s === 'action_required') return 'danger'
  if (s === 'warn') return 'warning'
  return 'info'
}

const severityLabel = (s: string) => {
  const m: Record<string, string> = { action_required: '需处理', warn: '警告', info: '提示' }
  return m[s] || s
}

const handleJumpTo = (target: string) => {
  // 处理健康面板的诊断导航跳转
  const element = document.querySelector(target)
  if (element) {
    element.scrollIntoView({ behavior: 'smooth', block: 'start' })
    // 高亮目标元素
    element.classList.add('highlight-pulse')
    setTimeout(() => {
      element.classList.remove('highlight-pulse')
    }, 2000)
  }
}

onMounted(loadPanorama)
</script>

<style scoped>
.session-panorama { padding: 16px; }
.pano-title { font-weight: 600; font-size: 16px; }
.loading-box { text-align: center; padding: 60px; color: var(--el-text-color-secondary); }
.error-box { padding: 40px 0; }
.metric-row .el-card { text-align: center; }
.savings-box { margin-top: 12px; padding: 8px; background: var(--el-color-success-light-9); border-radius: 4px; }
.savings-box h4 { margin: 0 0 4px; color: var(--el-color-success); }
.cluster-box { margin-top: 12px; padding: 8px; background: var(--el-color-primary-light-9); border-radius: 4px; }
.cluster-box h4 { margin: 0 0 4px; }
.tag-cloud { min-height: 32px; }
.muted { color: var(--el-text-color-secondary); }
.step-card { padding: 8px; background: var(--el-fill-color-light); border-radius: 4px; }
.step-req { color: var(--el-color-primary); margin: 4px 0; }
.step-res { color: var(--el-color-success); margin: 4px 0; }
.step-tools { color: var(--el-color-warning); margin: 4px 0; font-size: 13px; }

/* 高亮动画 */
@keyframes highlight-pulse {
  0%, 100% { box-shadow: 0 0 0 0 rgba(var(--el-color-primary-rgb), 0); }
  50% { box-shadow: 0 0 0 8px rgba(var(--el-color-primary-rgb), 0.3); }
}

:deep(.highlight-pulse) {
  animation: highlight-pulse 1s ease-in-out 2;
}
</style>
