<script setup lang="ts">
// DashboardView.vue — 仪表盘入口（新旧版本切换）
// 2026-07-05: 支持V1（旧版）和V2（新版泳道系统）切换
// 2026-07-05 v2: 优化版本切换器，紧凑图标设计

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

// 是否为默认租户
const isDefault = computed(() => isDefaultTenant())
</script>

<template>
  <div>
    <!-- 版本切换器 - 紧凑图标风格 -->
    <div class="version-switcher" v-if="isDefault">
      <button
        type="button"
        class="version-btn"
        :class="{ 'version-btn--active': version === 'v2' }"
        @click="switchVersion('v2')"
        title="新版仪表盘（推荐）- 泳道可视化"
      >
        V2
      </button>
      <button
        type="button"
        class="version-btn"
        :class="{ 'version-btn--active': version === 'v1' }"
        @click="switchVersion('v1')"
        title="旧版仪表盘"
      >
        V1
      </button>
    </div>

    <!-- 租户专用仪表盘 -->
    <TenantDashboardView v-if="!isDefault" />
    
    <!-- 默认租户仪表盘 -->
    <DashboardViewV2 v-else-if="version === 'v2'" />
    <DashboardViewLegacy v-else />
  </div>
</template>

<style scoped>
.version-switcher {
  display: inline-flex;
  gap: 4px;
  margin-bottom: 12px;
  padding: 3px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}

.version-btn {
  padding: 4px 12px;
  border: 1px solid transparent;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  min-width: 40px;
}

.version-btn:hover {
  color: var(--text, #e6edf3);
  background: var(--bg, #0f1117);
}

.version-btn--active {
  background: var(--accent, #6366f1);
  color: white;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.3);
}
</style>
