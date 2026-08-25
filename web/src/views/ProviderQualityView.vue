<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { qualityApi } from '../api/quality-api-client'
import { formatQualityScore, formatCalculatedAt, QUALITY_GRADE_COLORS, QUALITY_GRADE_LABELS } from '../types/quality-api'
import type { ProviderQualityData, RankingItem } from '../types/quality-api'

// 数据
const loading = ref(false)
const error = ref<string | null>(null)
const providers = ref<ProviderQualityData[]>([])
const ranking = ref<RankingItem[]>([])
const activeTab = ref<'providers' | 'ranking'>('ranking')

// 过滤参数
const filters = ref({
  model_name: '',
  min_score: 0,
  limit: 20,
  order_by: 'quality_score' as const,
})

// 加载排行榜
async function loadRanking() {
  loading.value = true
  error.value = null

  try {
    const response = await qualityApi.getRanking(filters.value)
    if (response.code === 0 && response.data) {
      ranking.value = response.data.ranking
    } else {
      error.value = response.message || '加载失败'
    }
  } catch (e: any) {
    error.value = e.message || '网络错误'
  } finally {
    loading.value = false
  }
}

// 加载供应商列表（所有供应商的质量画像）
async function loadProviders() {
  loading.value = true
  error.value = null

  try {
    // 从排行榜中提取唯一的供应商 ID
    const providerIds = [...new Set(ranking.value.map(item => item.provider_id))]

    // 并发加载所有供应商的详细数据
    const promises = providerIds.map(id =>
      qualityApi.getProviderQuality({ provider_id: id })
    )

    const responses = await Promise.all(promises)
    providers.value = responses
      .filter(r => r.code === 0 && r.data)
      .map(r => r.data!)
  } catch (e: any) {
    error.value = e.message || '网络错误'
  } finally {
    loading.value = false
  }
}

// 初始化
onMounted(async () => {
  await loadRanking()
})

// 切换标签时加载数据
async function handleTabChange(tab: 'providers' | 'ranking') {
  activeTab.value = tab
  if (tab === 'providers' && providers.value.length === 0) {
    await loadProviders()
  }
}
</script>

<template>
  <div class="quality-profile-view">
    <div class="header">
      <h1>供应商质量画像</h1>
      <p class="subtitle">实时质量评分与排行榜</p>
    </div>

    <!-- 标签切换 -->
    <div class="tabs">
      <button
        :class="{ active: activeTab === 'ranking' }"
        @click="handleTabChange('ranking')"
      >
        质量排行榜
      </button>
      <button
        :class="{ active: activeTab === 'providers' }"
        @click="handleTabChange('providers')"
      >
        供应商详情
      </button>
    </div>

    <!-- 过滤器（仅排行榜） -->
    <div v-if="activeTab === 'ranking'" class="filters">
      <div class="filter-group">
        <label>模型名称</label>
        <input
          v-model="filters.model_name"
          placeholder="输入模型名称过滤"
          @input="loadRanking"
        />
      </div>

      <div class="filter-group">
        <label>最低分数</label>
        <input
          v-model.number="filters.min_score"
          type="number"
          min="0"
          max="100"
          @change="loadRanking"
        />
      </div>

      <div class="filter-group">
        <label>排序</label>
        <select v-model="filters.order_by" @change="loadRanking">
          <option value="quality_score">综合质量</option>
          <option value="availability_score">可用性</option>
          <option value="performance_score">性能</option>
        </select>
      </div>

      <div class="filter-group">
        <label>显示条数</label>
        <select v-model.number="filters.limit" @change="loadRanking">
          <option :value="10">10</option>
          <option :value="20">20</option>
          <option :value="50">50</option>
        </select>
      </div>

      <button class="refresh-btn" @click="loadRanking">刷新</button>
    </div>

    <!-- 加载状态 -->
    <div v-if="loading" class="loading">
      <div class="spinner"></div>
      <p>加载中...</p>
    </div>

    <!-- 错误提示 -->
    <div v-else-if="error" class="error-box">
      <p>{{ error }}</p>
      <button @click="activeTab === 'ranking' ? loadRanking() : loadProviders()">
        重试
      </button>
    </div>

    <!-- 排行榜视图 -->
    <div v-else-if="activeTab === 'ranking'" class="ranking-view">
      <div v-if="ranking.length === 0" class="empty">
        <p>暂无数据</p>
      </div>

      <div v-else class="ranking-table">
        <table>
          <thead>
            <tr>
              <th>排名</th>
              <th>供应商</th>
              <th>模型</th>
              <th>综合质量</th>
              <th>等级</th>
              <th>可用性</th>
              <th>性能</th>
              <th>更新时间</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in ranking" :key="`${item.provider_id}-${item.model_name}`">
              <td class="rank">
                <span :class="`rank-badge rank-${item.rank <= 3 ? item.rank : 'other'}`">
                  {{ item.rank }}
                </span>
              </td>
              <td class="provider-name">{{ item.provider_name }}</td>
              <td class="model-name">{{ item.model_name }}</td>
              <td class="score">
                <span class="score-value">{{ formatQualityScore(item.quality_score) }}</span>
              </td>
              <td class="grade">
                <span
                  :class="`grade-badge grade-${item.quality_grade}`"
                  :style="{ backgroundColor: QUALITY_GRADE_COLORS[item.quality_grade] }"
                >
                  {{ item.quality_grade }}
                </span>
                <span class="grade-label">{{ QUALITY_GRADE_LABELS[item.quality_grade] }}</span>
              </td>
              <td class="sub-score">{{ item.availability_score }}</td>
              <td class="sub-score">{{ item.performance_score }}</td>
              <td class="timestamp">{{ formatCalculatedAt(item.calculated_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 供应商详情视图 -->
    <div v-else-if="activeTab === 'providers'" class="providers-view">
      <div v-if="providers.length === 0" class="empty">
        <p>暂无数据</p>
      </div>

      <div v-else class="provider-cards">
        <div
          v-for="provider in providers"
          :key="provider.provider_id"
          class="provider-card"
        >
          <div class="card-header">
            <h3>{{ provider.provider_name }}</h3>
            <span class="model-count">{{ provider.models.length }} 个模型</span>
          </div>

          <div class="models-list">
            <div
              v-for="model in provider.models"
              :key="model.model_name"
              class="model-item"
            >
              <div class="model-header">
                <span class="model-name">{{ model.model_name }}</span>
                <div class="model-quality">
                  <span class="quality-score">{{ formatQualityScore(model.quality_score) }}</span>
                  <span
                    :class="`grade-badge grade-${model.quality_grade}`"
                    :style="{ backgroundColor: QUALITY_GRADE_COLORS[model.quality_grade] }"
                  >
                    {{ model.quality_grade }}
                  </span>
                </div>
              </div>

              <div class="scores-grid">
                <div class="score-item">
                  <label>可用性</label>
                  <div class="score-bar">
                    <div class="bar-fill" :style="{ width: model.scores.availability + '%' }"></div>
                    <span>{{ model.scores.availability }}</span>
                  </div>
                </div>

                <div class="score-item">
                  <label>性能</label>
                  <div class="score-bar">
                    <div class="bar-fill" :style="{ width: model.scores.performance + '%' }"></div>
                    <span>{{ model.scores.performance }}</span>
                  </div>
                </div>

                <div class="score-item">
                  <label>稳定性</label>
                  <div class="score-bar">
                    <div class="bar-fill" :style="{ width: model.scores.stability + '%' }"></div>
                    <span>{{ model.scores.stability }}</span>
                  </div>
                </div>

                <div class="score-item">
                  <label>成本效益</label>
                  <div class="score-bar">
                    <div class="bar-fill" :style="{ width: model.scores.cost_efficiency + '%' }"></div>
                    <span>{{ model.scores.cost_efficiency }}</span>
                  </div>
                </div>
              </div>

              <div class="model-footer">
                <span class="timestamp">更新: {{ formatCalculatedAt(model.calculated_at) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.quality-profile-view {
  padding: 24px;
  max-width: 1400px;
  margin: 0 auto;
}

.header {
  margin-bottom: 32px;
}

.header h1 {
  font-size: 28px;
  font-weight: 600;
  margin-bottom: 8px;
  color: var(--kx-text);
}

.subtitle {
  color: var(--muted);
  font-size: 14px;
}

/* 标签 */
.tabs {
  display: flex;
  gap: 8px;
  margin-bottom: 24px;
  border-bottom: 1px solid var(--border);
}

.tabs button {
  padding: 12px 24px;
  border: none;
  background: none;
  cursor: pointer;
  font-size: 14px;
  color: var(--muted);
  border-bottom: 2px solid transparent;
  transition: all 0.2s;
}

.tabs button.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
  font-weight: 500;
}

/* 过滤器 */
.filters {
  display: flex;
  gap: 16px;
  align-items: flex-end;
  padding: 20px;
  background: var(--surface-secondary);
  border-radius: 8px;
  margin-bottom: 24px;
}

.filter-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.filter-group label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 500;
}

.filter-group input,
.filter-group select {
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 14px;
  min-width: 150px;
}

.refresh-btn {
  padding: 8px 20px;
  background: var(--accent);
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  height: 36px;
}

.refresh-btn:hover {
  background: var(--accent);
}

/* 加载和错误 */
.loading {
  text-align: center;
  padding: 60px 20px;
}

.spinner {
  width: 40px;
  height: 40px;
  border: 4px solid var(--surface-secondary);
  border-top: 4px solid var(--accent);
  border-radius: 50%;
  animation: spin 1s linear infinite;
  margin: 0 auto 16px;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.error-box {
  text-align: center;
  padding: 40px 20px;
  background: var(--danger-bg);
  border: 1px solid var(--danger-bd);
  border-radius: 8px;
  color: var(--danger);
}

.error-box button {
  margin-top: 16px;
  padding: 8px 16px;
  background: var(--danger);
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
}

.empty {
  text-align: center;
  padding: 60px 20px;
  color: var(--muted);
}

/* 排行榜表格 */
.ranking-table {
  background: white;
  border-radius: 8px;
  overflow: hidden;
  box-shadow: 0 2px 8px var(--overlay-light);
}

.ranking-table table {
  width: 100%;
  border-collapse: collapse;
}

.ranking-table th {
  background: var(--surface-secondary);
  padding: 16px;
  text-align: left;
  font-weight: 600;
  font-size: 14px;
  color: var(--kx-text);
  border-bottom: 2px solid var(--surface-secondary);
}

.ranking-table td {
  padding: 16px;
  border-bottom: 1px solid var(--surface-secondary);
  font-size: 14px;
}

.rank {
  width: 80px;
}

.rank-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border-radius: 50%;
  font-weight: 600;
  font-size: 14px;
}

.rank-1 {
  background: #ffd700;
  color: var(--on-primary);
}

.rank-2 {
  background: var(--border);
  color: var(--on-primary);
}

.rank-3 {
  background: #cd7f32;
  color: var(--on-primary);
}

.rank-other {
  background: var(--surface-secondary);
  color: var(--muted);
}

.provider-name {
  font-weight: 500;
  color: var(--kx-text);
}

.model-name {
  color: var(--muted);
  font-family: 'Monaco', 'Menlo', monospace;
  font-size: 13px;
}

.score {
  font-size: 18px;
  font-weight: 600;
  color: var(--accent);
}

.grade {
  display: flex;
  align-items: center;
  gap: 8px;
}

.grade-badge {
  display: inline-block;
  padding: 4px 10px;
  border-radius: 12px;
  color: white;
  font-weight: 600;
  font-size: 12px;
}

.grade-label {
  font-size: 12px;
  color: var(--muted);
}

.sub-score {
  color: var(--muted);
}

.timestamp {
  font-size: 12px;
  color: var(--muted);
}

/* 供应商卡片 */
.provider-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(500px, 1fr));
  gap: 24px;
}

.provider-card {
  background: white;
  border-radius: 8px;
  padding: 24px;
  box-shadow: 0 2px 8px var(--overlay-light);
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 2px solid var(--surface-secondary);
}

.card-header h3 {
  font-size: 18px;
  font-weight: 600;
  color: var(--kx-text);
  margin: 0;
}

.model-count {
  font-size: 12px;
  color: var(--muted);
  background: var(--surface-secondary);
  padding: 4px 12px;
  border-radius: 12px;
}

.models-list {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.model-item {
  border: 1px solid var(--surface-secondary);
  border-radius: 6px;
  padding: 16px;
  background: var(--surface-secondary);
}

.model-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.model-header .model-name {
  font-weight: 500;
  font-size: 14px;
  color: var(--kx-text);
}

.model-quality {
  display: flex;
  align-items: center;
  gap: 8px;
}

.quality-score {
  font-size: 20px;
  font-weight: 600;
  color: var(--accent);
}

.scores-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
  margin-bottom: 12px;
}

.score-item label {
  display: block;
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 6px;
}

.score-bar {
  position: relative;
  height: 24px;
  background: var(--border);
  border-radius: 12px;
  overflow: hidden;
}

.bar-fill {
  height: 100%;
  background: linear-gradient(90deg, var(--accent), var(--accent));
  transition: width 0.3s;
}

.score-bar span {
  position: absolute;
  right: 8px;
  top: 50%;
  transform: translateY(-50%);
  font-size: 11px;
  font-weight: 600;
  color: var(--kx-text);
}

.model-footer {
  padding-top: 12px;
  border-top: 1px solid var(--border);
}

/* 响应式 */
@media (max-width: 768px) {
  .filters {
    flex-direction: column;
    align-items: stretch;
  }

  .provider-cards {
    grid-template-columns: 1fr;
  }

  .ranking-table {
    overflow-x: auto;
  }
}
</style>
