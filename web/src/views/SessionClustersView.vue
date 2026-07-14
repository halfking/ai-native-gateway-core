<template>
  <div class="session-clusters">
    <div class="header">
      <h2>{{ t('sessions.clusters.title') }}</h2>
      <div>
        <el-button type="primary" :loading="running" @click="runCluster">
          <el-icon><Refresh /></el-icon>
          {{ t('sessions.clusters.runCluster') }}
        </el-button>
        <el-button @click="loadClusters">{{ t('sessions.clusters.refresh') }}</el-button>
      </div>
    </div>

    <el-alert
      v-if="clusters.length === 0 && !loading"
      :title="t('sessions.clusters.emptyTitle')"
      type="info"
      :closable="false"
      show-icon
      style="margin-bottom: 16px"
    >
      {{ t('sessions.clusters.emptyHint') }}
    </el-alert>

    <div v-if="loading && clusters.length === 0" class="loading-state" role="status" aria-live="polite">
      <span class="spinner" aria-hidden="true"></span>
      <span>{{ t('sessions.clusters.loading') }}</span>
    </div>

    <el-row :gutter="16">
      <el-col
        v-for="cluster in clusters"
        :key="cluster.cluster_id"
        :xs="24"
        :sm="12"
        :md="12"
        :lg="8"
        :xl="8"
        style="margin-bottom: 16px"
      >
        <el-card
          shadow="hover"
          class="cluster-card"
          role="button"
          tabindex="0"
          :aria-label="t('sessions.clusters.ariaLabel', { label: cluster.label || cluster.coarse_key || t('sessions.clusters.unnamed'), count: cluster.member_count })"
          @click="showDetail(cluster)"
          @keydown.enter="showDetail(cluster)"
          @keydown.space.prevent="showDetail(cluster)"
        >
          <div class="cluster-header">
            <span class="cluster-label">{{ cluster.label || cluster.coarse_key || t('sessions.clusters.unnamed') }}</span>
            <el-tag size="small" type="info">{{ t('sessions.clusters.sessionsCount', { n: cluster.member_count }) }}</el-tag>
          </div>
          <div class="cluster-topics">
            <el-tag v-for="topic in cluster.topic_path?.slice(0, 3)" :key="topic" size="small" style="margin: 2px">{{ topic }}</el-tag>
          </div>
          <div class="cluster-stats">
            <span>{{ t('sessions.clusters.avgCost') }} ${{ cluster.avg_cost_usd.toFixed(4) }}</span>
            <span v-if="cluster.avg_quality_score">{{ t('sessions.clusters.quality') }} {{ cluster.avg_quality_score.toFixed(1) }}</span>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-drawer v-model="detailVisible" :title="currentCluster?.label || t('sessions.clusters.detailTitle')" size="60%">
      <div v-if="currentDetail">
        <el-descriptions :column="2" border size="small" style="margin-bottom: 16px">
          <el-descriptions-item :label="t('sessions.clusters.fields.clusterId')">{{ currentDetail.cluster_id }}</el-descriptions-item>
          <el-descriptions-item :label="t('sessions.clusters.fields.coarseKey')">{{ currentDetail.coarse_key || '—' }}</el-descriptions-item>
          <el-descriptions-item :label="t('sessions.clusters.fields.memberCount')">{{ currentDetail.member_count }}</el-descriptions-item>
          <el-descriptions-item :label="t('sessions.clusters.fields.avgCost')">${{ currentDetail.avg_cost_usd?.toFixed(4) }}</el-descriptions-item>
        </el-descriptions>

        <h4>{{ t('sessions.clusters.membersTitle') }}</h4>
        <el-table :data="currentDetail.members" stripe size="small">
          <el-table-column prop="gw_session_id" :label="t('sessions.clusters.table.sessionId')" min-width="200" />
          <el-table-column prop="title" :label="t('sessions.clusters.table.title')" min-width="160">
            <template #default="scope">{{ scope?.row?.title || '—' }}</template>
          </el-table-column>
          <el-table-column prop="total_cost_usd" :label="t('sessions.clusters.table.cost')" width="100">
            <template #default="scope">
              <span v-if="scope?.row?.total_cost_usd != null">${{ scope.row.total_cost_usd.toFixed(4) }}</span>
              <span v-else>—</span>
            </template>
          </el-table-column>
          <el-table-column prop="score" :label="t('sessions.clusters.table.similarity')" width="100">
            <template #default="scope">
              <span v-if="scope?.row?.score != null">{{ (scope.row.score * 100).toFixed(0) }}%</span>
              <span v-else>—</span>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </el-drawer>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Refresh } from '@element-plus/icons-vue'
import { listClusters, getClusterDetail, runClustering } from '../api/sessionAnalytics'
import type { SessionClusterItem } from '../api/sessionAnalytics'

const { t } = useI18n()
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
    ElMessage.error(t('sessions.clusters.errors.loadFailed') + ': ' + e.message)
  } finally {
    loading.value = false
  }
}

const runCluster = async () => {
  running.value = true
  try {
    const res = await runClustering(168)
    ElMessage.success(t('sessions.clusters.success', { n: res.clusters_built }))
    loadClusters()
  } catch (e: any) {
    ElMessage.error(t('sessions.clusters.errors.clusterFailed') + ': ' + e.message)
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
    ElMessage.error(t('sessions.clusters.errors.detailFailed') + ': ' + e.message)
  }
}

onMounted(loadClusters)
</script>

<style scoped>
.session-clusters { padding: 16px; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.cluster-card { cursor: pointer; transition: transform 0.15s ease; }
.cluster-card:hover { transform: translateY(-2px); }
.cluster-card:focus-visible { outline: 2px solid var(--accent, #6366f1); outline-offset: 2px; }
.cluster-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.cluster-label { font-weight: 600; }
.cluster-topics { min-height: 28px; margin-bottom: 8px; }
.cluster-stats { display: flex; gap: 16px; color: var(--el-text-color-secondary); font-size: 13px; }
.loading-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 40px 20px;
  color: var(--muted);
  font-size: 13px;
}
.spinner {
  display: inline-block;
  width: 16px;
  height: 16px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: clusters-spin 0.8s linear infinite;
}
@keyframes clusters-spin { to { transform: rotate(360deg); } }
</style>
