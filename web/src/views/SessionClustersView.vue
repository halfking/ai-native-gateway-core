<template>
  <div class="session-clusters">
    <div class="header">
      <h2>会话分组（聚类）</h2>
      <div>
        <el-button type="primary" :loading="running" @click="runCluster">
          <el-icon><Refresh /></el-icon>
          手动聚类
        </el-button>
        <el-button @click="loadClusters">刷新</el-button>
      </div>
    </div>

    <el-alert
      v-if="clusters.length === 0 && !loading"
      title="暂无聚类数据"
      type="info"
      :closable="false"
      show-icon
      style="margin-bottom: 16px"
    >
      点击「手动聚类」触发一次相似会话分组。聚类模式可在模块配置中调整（rule/vector/hybrid）。
    </el-alert>

    <!-- 聚类卡片网格 -->
    <el-row :gutter="16">
      <el-col v-for="cluster in clusters" :key="cluster.cluster_id" :span="8" style="margin-bottom: 16px">
        <el-card shadow="hover" class="cluster-card" @click="showDetail(cluster)">
          <div class="cluster-header">
            <span class="cluster-label">{{ cluster.label || cluster.coarse_key || '未命名聚类' }}</span>
            <el-tag size="small" type="info">{{ cluster.member_count }} 会话</el-tag>
          </div>
          <div class="cluster-topics">
            <el-tag v-for="t in cluster.topic_path?.slice(0, 3)" :key="t" size="small" style="margin: 2px">{{ t }}</el-tag>
          </div>
          <div class="cluster-stats">
            <span>平均成本 ${{ cluster.avg_cost_usd.toFixed(4) }}</span>
            <span v-if="cluster.avg_quality_score">质量 {{ cluster.avg_quality_score.toFixed(1) }}</span>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <!-- 聚类详情抽屉 -->
    <el-drawer v-model="detailVisible" :title="currentCluster?.label || '聚类详情'" size="60%">
      <div v-if="currentDetail">
        <el-descriptions :column="2" border size="small" style="margin-bottom: 16px">
          <el-descriptions-item label="聚类 ID">{{ currentDetail.cluster_id }}</el-descriptions-item>
          <el-descriptions-item label="粗聚类键">{{ currentDetail.coarse_key || '—' }}</el-descriptions-item>
          <el-descriptions-item label="成员数">{{ currentDetail.member_count }}</el-descriptions-item>
          <el-descriptions-item label="平均成本">${{ currentDetail.avg_cost_usd?.toFixed(4) }}</el-descriptions-item>
        </el-descriptions>

        <h4>成员会话</h4>
        <el-table :data="currentDetail.members" stripe size="small">
          <el-table-column prop="gw_session_id" label="会话 ID" min-width="200" />
          <el-table-column prop="title" label="标题" min-width="160">
            <template #default="{ row }">{{ row.title || '—' }}</template>
          </el-table-column>
          <el-table-column prop="total_cost_usd" label="成本" width="100">
            <template #default="{ row }">${{ row.total_cost_usd?.toFixed(4) }}</template>
          </el-table-column>
          <el-table-column prop="score" label="相似度" width="100">
            <template #default="{ row }">{{ (row.score * 100).toFixed(0) }}%</template>
          </el-table-column>
        </el-table>
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { listClusters, getClusterDetail, runClustering } from '../api/sessionAnalytics'
import type { SessionClusterItem } from '../api/sessionAnalytics'

const clusters = ref<SessionClusterItem[]>([])
const loading = ref(false)
const running = ref(false)
const detailVisible = ref(false)
const currentCluster = ref<SessionClusterItem | null>(null)
const currentDetail = ref<any>(null)

const loadClusters = async () => {
  loading.value = true
  try {
    const data = await listClusters({ page: 1, page_size: 50 })
    clusters.value = data.clusters
  } catch (e: any) {
    ElMessage.error('加载聚类失败: ' + e.message)
  } finally {
    loading.value = false
  }
}

const runCluster = async () => {
  running.value = true
  try {
    const res = await runClustering(168)
    ElMessage.success(`聚类完成，生成 ${res.clusters_built} 个分组`)
    loadClusters()
  } catch (e: any) {
    ElMessage.error('聚类失败: ' + e.message)
  } finally {
    running.value = false
  }
}

const showDetail = async (cluster: SessionClusterItem) => {
  currentCluster.value = cluster
  detailVisible.value = true
  currentDetail.value = null
  try {
    currentDetail.value = await getClusterDetail(cluster.cluster_id)
  } catch (e: any) {
    ElMessage.error('加载详情失败: ' + e.message)
  }
}

onMounted(loadClusters)
</script>

<style scoped>
.session-clusters { padding: 16px; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.cluster-card { cursor: pointer; }
.cluster-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.cluster-label { font-weight: 600; }
.cluster-topics { min-height: 28px; margin-bottom: 8px; }
.cluster-stats { display: flex; gap: 16px; color: var(--el-text-color-secondary); font-size: 13px; }
</style>
