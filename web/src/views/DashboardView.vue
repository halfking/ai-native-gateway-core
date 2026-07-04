<script setup lang="ts">
// DashboardView.vue — 仪表盘入口（新旧版本切换）
// 2026-07-05: 支持V1（旧版）和V2（新版泳道系统）切换

import { ref, onMounted, computed } from 'vue'
import DashboardViewV2 from './DashboardViewV2.vue'
import DashboardViewLegacy from './DashboardViewLegacy.vue'
import TenantDashboardView from './TenantDashboardView.vue'
import { isDefaultTenant } from '../store'

const STORAGE_KEY = 'dashboard_version'

// 版本选择（默认V2）
const version = ref<'v1' | 'v2'>('v2')

// 从localStorage恢复版本选择
onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY)
  if (saved === 'v1' || saved === 'v2') {
    version.value = saved
  }
})

// 切换版本
function switchVersion(v: 'v1' | 'v2') {
  version.value = v
  localStorage.setItem(STORAGE_KEY, v)
}

const showTenantDashboard = computed(() => !isDefaultTenant())
</script>

<template>
  <TenantDashboardView v-if="showTenantDashboard" />
  <div v-else>
    <!-- 版本切换器 -->
    <div class="version-switcher">
      <button
        type="button"
        class="version-btn"
        :class="{ 'version-btn--active': version === 'v2' }"
        @click="switchVersion('v2')"
      >
        V2 新版（泳道）
      </button>
      <button
        type="button"
        class="version-btn"
        :class="{ 'version-btn--active': version === 'v1' }"
        @click="switchVersion('v1')"
      >
        V1 旧版
      </button>
    </div>

    <!-- 渲染对应版本 -->
    <DashboardViewV2 v-if="version === 'v2'" />
    <DashboardViewLegacy v-else />
  </div>
</template>

<style scoped>
.version-switcher {
  display: flex;
  gap: 6px;
  margin-bottom: 16px;
  padding: 6px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  width: fit-content;
}

.version-btn {
  padding: 8px 16px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.version-btn:hover {
  color: var(--text, #e6edf3);
  background: var(--bg, #0f1117);
}

.version-btn--active {
  background: var(--card, #1c2128);
  border-color: var(--accent, #6366f1);
  color: var(--accent, #6366f1);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.2);
}
</style>
