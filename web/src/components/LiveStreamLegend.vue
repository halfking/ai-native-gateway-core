<script setup lang="ts">
// LiveStreamLegend.vue — 泳道系统图例组件
// 2026-07-05: 显示维度图例（Top5）和状态图例，支持点击反转选择
// 2026-07-05 v2: 将状态图例移到同一行右侧

import { computed, ref } from 'vue'
import type { LiveStreamLegendItem } from '../composables/liveStreamStore'
import { VENDOR_COLORS, STATUS_BORDER_COLORS } from '../types/swimlane'

function dimensionColor(key: string): string {
  return VENDOR_COLORS[key] || '#6b7280'
}

function statusColor(key: string): string {
  return STATUS_BORDER_COLORS[key] || STATUS_BORDER_COLORS['__default__'] || '#6b7280'
}

const props = defineProps<{
  dimensionItems: LiveStreamLegendItem[]
  statusItems: LiveStreamLegendItem[]
  selectedLegends: Set<string>
  dimensionLabel: string
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
const expanded = ref(false)

function handleClick(key: string) {
  emit('toggleLegend', key)
}
</script>

<template>
  <div class="legend-container">
    <button type="button" class="legend-toggle" :aria-expanded="expanded" @click="expanded = !expanded">
      <span>{{ $t('dashboard.liveStream.legendButton') }}</span>
      <span class="legend-toggle__meta">
        <span v-if="hasSelection" class="legend-toggle__count">{{ props.selectedLegends.size }}</span>
        <span aria-hidden="true">{{ expanded ? '▴' : '▾' }}</span>
      </span>
    </button>

    <div v-if="expanded" class="legend-row">
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
          <span class="legend-swatch" :style="{ backgroundColor: dimensionColor(item.key) }" />
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
          <span class="legend-swatch legend-swatch--border" :style="{ borderColor: statusColor(item.key) }" />
          <span class="legend-label">{{ item.name }}</span>
        </span>
        <!-- 2026-07-15: 探测图例与 RequestTile 雷达角标一致 -->
        <span class="legend-item legend-item--status" title="探测请求：出错后主动直连上游验证（雷达角标 + 青色卡片）">
          <span class="legend-probe-badge" aria-hidden="true">
            <svg class="legend-probe-icon" viewBox="0 0 16 16">
              <circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" stroke-width="1.4" />
              <circle cx="8" cy="8" r="1.6" fill="currentColor" />
              <path d="M8 2.2v2.2M8 11.6v2.2M2.2 8h2.2M11.6 8h2.2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
            </svg>
          </span>
          <span class="legend-label">探测</span>
        </span>
        <span class="legend-heading legend-heading--stage">阶段</span>
        <span class="legend-item legend-item--status" title="路由中：已收到请求，正在选路/排队">
          <span class="legend-stage legend-stage--routing" aria-hidden="true">⏳</span>
          <span class="legend-label">路由中</span>
        </span>
        <span class="legend-item legend-item--status" title="等大模型：已发上游，等待响应">
          <span class="legend-stage legend-stage--llm" aria-hidden="true">🔄</span>
          <span class="legend-label">等大模型</span>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.legend-container {
  margin-top: 12px;
  padding: 10px 12px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}

.legend-toggle {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.legend-toggle__meta {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--text-secondary);
}

.legend-toggle__count {
  min-width: 18px;
  padding: 2px 5px;
  border-radius: 9px;
  background: var(--accent);
  color: var(--bg);
  text-align: center;
  font-size: 11px;
}

.legend-row {
  margin-top: 10px;
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
  color: var(--text);
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
  color: var(--text-secondary);
  border: 1px solid transparent;
  background: transparent;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}

.legend-item:not(.legend-item--status):hover {
  background: var(--bg);
  border-color: var(--border);
  color: var(--text);
}

.legend-item--selected {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: var(--accent);
  color: var(--accent);
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

/* 2026-07-15: 探测图例角标（与 RequestTile 雷达 SVG 一致） */
.legend-probe-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border-radius: 3px;
  color: var(--probe-dark-bg);
  background: linear-gradient(180deg, var(--probe-cyan-light) 0%, var(--probe-cyan) 100%);
  border: 1.5px solid var(--probe-cyan-darker);
  box-shadow: 0 0 4px color-mix(in srgb, var(--probe-cyan) 30%, transparent);
  flex-shrink: 0;
}

.legend-probe-icon {
  width: 10px;
  height: 10px;
  display: block;
}

.legend-heading--stage {
  margin-left: 8px;
  padding-left: 8px;
  border-left: 1px solid var(--border);
}

.legend-stage {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  font-size: 10px;
  line-height: 1;
  flex-shrink: 0;
}

.legend-stage--routing {
  color: var(--accent);
}

.legend-stage--llm {
  color: var(--accent);
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
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
}

.legend-item--selected .legend-count {
  color: var(--accent);
}

@media (max-width: 1024px) {
  .legend-row {
    flex-direction: column;
    align-items: stretch;
  }

  .legend-section--right {
    border-top: 1px solid var(--border);
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
