<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ApprovalConfigPanel from '../components/ApprovalConfigPanel.vue'
import CompressionConfigPanel from '../components/CompressionConfigPanel.vue'
import HealthScoreConfigPanel from '../components/HealthScoreConfigPanel.vue'

const { t } = useI18n()

const activeTab = ref('approval')

const tabs = [
  { key: 'approval', label: t('sessions.config.approvalTab'), icon: '✓' },
  { key: 'compression', label: t('sessions.config.compressionTab'), icon: '🗜️' },
  { key: 'health', label: t('sessions.config.healthTab'), icon: '💚' },
]
</script>

<template>
  <div class="session-config-view">
    <div class="page-header">
      <div>
        <h1>{{ t('sessions.config.title') }}</h1>
        <p class="page-description">{{ t('sessions.config.subtitle') }}</p>
      </div>
    </div>

    <!-- Tab Navigation -->
    <div class="tab-nav">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        class="tab-button"
        :class="{ active: activeTab === tab.key }"
        @click="activeTab = tab.key"
      >
        <span class="tab-icon">{{ tab.icon }}</span>
        <span class="tab-label">{{ tab.label }}</span>
      </button>
    </div>

    <!-- Tab Content -->
    <div class="tab-content">
      <ApprovalConfigPanel v-if="activeTab === 'approval'" />
      <CompressionConfigPanel v-else-if="activeTab === 'compression'" />
      <HealthScoreConfigPanel v-else-if="activeTab === 'health'" />
    </div>
  </div>
</template>

<style scoped>
.session-config-view {
  padding: 20px;
  max-width: 1400px;
  margin: 0 auto;
}

.page-header {
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0;
  font-size: 28px;
  font-weight: 600;
  color: #303133;
}

.page-description {
  margin: 8px 0 0;
  font-size: 14px;
  color: #909399;
}

.tab-nav {
  display: flex;
  gap: 8px;
  border-bottom: 2px solid #e4e7ed;
  margin-bottom: 24px;
}

.tab-button {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 24px;
  background: transparent;
  border: none;
  border-bottom: 3px solid transparent;
  cursor: pointer;
  font-size: 15px;
  font-weight: 500;
  color: #606266;
  transition: all 0.2s;
  margin-bottom: -2px;
}

.tab-button:hover {
  color: #409eff;
  background: #f5f7fa;
}

.tab-button.active {
  color: #409eff;
  border-bottom-color: #409eff;
  background: transparent;
}

.tab-icon {
  font-size: 18px;
}

.tab-label {
  white-space: nowrap;
}

.tab-content {
  animation: fadeIn 0.3s ease;
}

@keyframes fadeIn {
  from {
    opacity: 0;
    transform: translateY(10px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
