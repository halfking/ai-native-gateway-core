<script setup lang="ts">
// LiveStreamLegend.vue — 泳道系统图例组件
// 2026-07-05: 显示维度图例（Top5）和状态图例，支持点击反转选择
// 2026-07-05 v2: 将状态图例移到同一行右侧

import { computed } from 'vue'

interface LegendItem {
  key: string
  name: string
  color: string
  count?: number
}

const props = defineProps<{
  dimensionItems: LegendItem[]      // 维度图例（原厂/供应商/模型 Top5）
  statusItems: LegendItem[]          // 状态图例
  selectedLegends: Set<string>       // 已选中的图例
  dimensionLabel: string             // 维度标签（原厂/供应商/模型）
}>()

const emit = defineEmits<{
  toggleLegend: [key: string]
}>()

// 检查是否选中
function isSelected(key: string): boolean {
  return props.selectedLegends.has(key)
}

// 检查是否有任何选中
const hasSelection = computed(() => props.selectedLegends.size > 0)

function handleClick(key: string) {
  emit('toggleLegend', key)
}
</script>

<template>
  <div class="legend-container">
    <div class="legend-row">
      <!-- 左侧：维度图例 -->
      <div class="legend-section legend-section--left">
        <span class="legend-heading">{{ dimensionLabel }}</span>
        <button
          v-for="item in dimensionItems"
          :key="item.key"
          type="button"
          class="legend-item"
          :class="{
            'legend-item--selected': isSelected(item.key),
            'legend-item--dimmed': hasSelection && !isSelected(item.key)
          }"
          @click="handleClick(item.key)"
          :title="`${item.name} (${item.count || 0}个请求) - 点击${isSelected(item.key) ? '取消' : ''}高亮`"
        >
          <span class="legend-swatch" :style="{ backgroundColor: item.color }" />
          <span class="legend-label">{{ item.name }}</span>
          <span v-if="item.count != null" class="legend-count">({{ item.count }})</span>
        </button>
      </div>

      <!-- 右侧：状态图例 -->
      <div class="legend-section legend-section--right">
        <span class="legend-heading">状态</span>
        <span
          v-for="item in statusItems"
          :key="item.key"
          class="legend-item legend-item--status"
          :title="item.name"
        >
          <span class="legend-swatch legend-swatch--border" :style="{ borderColor: item.color }" />
          <span class="legend-label">{{ item.name }}</span>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.legend-container {
  margin-top: 12px;
  padding: 10px 12px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}

.legend-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 24px;
  flex-wrap: wrap;
}

.legend-section {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.legend-section--left {
  flex: 1;
  min-width: 0;
}

.legend-section--right {
  flex-shrink: 0;
}

.legend-heading {
  font-size: 12px;
  font-weight: 600;
  color: var(--text, #e6edf3);
  margin-right: 4px;
  white-space: nowrap;
}

.legend-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 8px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  border: 1px solid transparent;
  background: transparent;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.legend-item:not(.legend-item--status):hover {
  background: var(--bg, #0f1117);
  border-color: var(--border, #30363d);
  color: var(--text, #e6edf3);
}

.legend-item--selected {
  background: rgba(99, 102, 241, 0.12);
  border-color: var(--accent, #6366f1);
  color: var(--accent, #6366f1);
}

.legend-item--dimmed {
  opacity: 0.5;
}

.legend-item--status {
  cursor: default;
  padding: 3px 6px;
}

.legend-swatch {
  display: inline-block;
  width: 12px;
  height: 12px;
  border-radius: 2px;
  flex-shrink: 0;
  box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.1);
}

.legend-swatch--border {
  background: transparent;
  border: 2px solid;
  box-shadow: none;
}

.legend-label {
  line-height: 1;
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.legend-count {
  font-size: 11px;
  color: var(--text-tertiary, #6e7681);
  font-variant-numeric: tabular-nums;
}

.legend-item--selected .legend-count {
  color: var(--accent, #6366f1);
}

@media (max-width: 1024px) {
  .legend-row {
    flex-direction: column;
    align-items: stretch;
  }
  
  .legend-section--right {
    border-top: 1px solid var(--border, #30363d);
    padding-top: 8px;
    margin-top: 4px;
  }
}

@media (max-width: 768px) {
  .legend-section {
    gap: 6px;
  }
  
  .legend-item {
    padding: 3px 6px;
    font-size: 11px;
  }
  
  .legend-swatch {
    width: 10px;
    height: 10px;
  }
}
</style>
