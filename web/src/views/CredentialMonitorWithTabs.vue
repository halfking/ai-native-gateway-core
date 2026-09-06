<script setup lang="ts">
import { ref } from 'vue'
import CredentialMonitorView from './CredentialMonitorView.vue'
import CredentialHeatmapView from './CredentialHeatmapView.vue'
import SegTabs, { type SegTab } from '../components/SegTabs.vue'

type ViewTab = 'list' | 'heatmap' | 'routing-log'
const activeTab = ref<ViewTab>('list')

const tabs: SegTab[] = [
  { value: 'list', label: '列表视图' },
  { value: 'heatmap', label: '热力图' },
  { value: 'routing-log', label: '路由记录' },
]
</script>

<template>
  <div class="credential-monitor-wrapper">
    <!-- Tab switcher -->
    <div class="view-tabs-container">
      <SegTabs v-model="activeTab" :tabs="tabs" />
    </div>

    <!-- Tab content -->
    <CredentialMonitorView v-if="activeTab === 'list'" />
    <CredentialHeatmapView v-else-if="activeTab === 'heatmap'" />
    <div v-else-if="activeTab === 'routing-log'" class="routing-log-placeholder">
      <div class="placeholder-content">
        <h2>路由记录</h2>
        <p>此功能正在开发中，即将上线。</p>
        <p class="hint">将显示所有模型的路由选择记录和自检测试记录，包括状态变化历史。</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.credential-monitor-wrapper {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
  min-height: 100vh;
}

.view-tabs-container {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 8px 12px;
}

.routing-log-placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 400px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

.placeholder-content {
  text-align: center;
  max-width: 500px;
  padding: 40px;
}

.placeholder-content h2 {
  margin: 0 0 16px 0;
  font-size: 24px;
  color: var(--text);
}

.placeholder-content p {
  margin: 8px 0;
  font-size: 14px;
  color: var(--muted);
}

.placeholder-content .hint {
  margin-top: 16px;
  padding: 12px;
  background: var(--bg-subtle);
  border-radius: var(--radius);
  font-size: 13px;
}
</style>
