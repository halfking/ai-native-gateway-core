<script setup lang="ts">
// RequestTile.vue — 请求色块组件（80x60）
// 2026-07-05: 泳道系统中的单个请求可视化

import { computed } from 'vue'
import type { RequestTile as RequestTileType, GroupByDimension } from '../types/swimlane'
import { 
  VENDOR_COLORS, 
  STATUS_BORDER_COLORS, 
  getStatusBorderKey,
  calculateFontSize,
  truncateText
} from '../types/swimlane'

const props = defineProps<{
  tile: RequestTileType
  groupBy: GroupByDimension
  isHighlighted: boolean
  isDimmed: boolean
}>()

const emit = defineEmits<{
  click: [requestId: string]
}>()

// 背景色（原厂色）
const bgColor = computed(() => {
  return VENDOR_COLORS[props.tile.vendor] || VENDOR_COLORS['__unknown__']
})

// 边框色（状态色）
const borderColor = computed(() => {
  const key = getStatusBorderKey(props.tile.status, props.tile.error_kind)
  return STATUS_BORDER_COLORS[key] || STATUS_BORDER_COLORS['__default__']
})

// 时间显示（HH:mm）
const timeLabel = computed(() => {
  const date = new Date(props.tile.timestamp)
  const hh = String(date.getHours()).padStart(2, '0')
  const mm = String(date.getMinutes()).padStart(2, '0')
  return `${hh}:${mm}`
})

// 延迟显示
const latencyLabel = computed(() => {
  if (!props.tile.latency_ms) return ''
  const ms = props.tile.latency_ms
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(1)}s`
  }
  return `${Math.round(ms)}ms`
})

// 模型名称（动态字体大小）
const modelFontSize = computed(() => {
  return calculateFontSize(props.tile.model, 80)
})

// 第二行内容（根据分组模式）
const line2Content = computed(() => {
  if (props.groupBy === 'vendor') {
    return truncateText(props.tile.model, 12)
  }
  if (props.groupBy === 'provider') {
    return truncateText(props.tile.model, 12)
  }
  // model模式：显示状态或错误
  if (props.tile.status === 'success') return '✓ 成功'
  if (props.tile.status === 'in_progress') return '⋯ 进行中'
  if (props.tile.error_kind) {
    return props.tile.error_kind.slice(0, 8)
  }
  return '✗ 失败'
})

// 第三行内容（根据分组模式）
const line3Content = computed(() => {
  if (props.groupBy === 'vendor') {
    return truncateText(props.tile.provider, 10)
  }
  if (props.groupBy === 'provider') {
    return truncateText(props.tile.vendor, 10)
  }
  // model模式：显示供应商
  return truncateText(props.tile.provider, 10)
})

function handleClick() {
  emit('click', props.tile.request_id)
}
</script>

<template>
  <div 
    class="request-tile"
    :class="{
      'request-tile--highlighted': isHighlighted,
      'request-tile--dimmed': isDimmed,
    }"
    :style="{
      '--bg-color': bgColor,
      '--border-color': borderColor,
      '--model-font-size': modelFontSize + 'px',
    }"
    @click="handleClick"
  >
    <div class="request-tile__time">{{ timeLabel }}</div>
    <div class="request-tile__model">{{ line2Content }}</div>
    <div class="request-tile__provider">{{ line3Content }}</div>
    <div class="request-tile__latency" v-if="latencyLabel">{{ latencyLabel }}</div>
  </div>
</template>

<style scoped>
.request-tile {
  width: 80px;
  height: 60px;
  border-radius: 4px;
  border: 2px solid var(--border-color);
  background: var(--bg-color);
  padding: 4px 6px;
  cursor: pointer;
  transition: all 0.2s ease;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 
               'PingFang SC', 'Microsoft YaHei', sans-serif;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  flex-shrink: 0;
  position: relative;
  overflow: hidden;
}

.request-tile:hover {
  transform: scale(1.08);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
  z-index: 10;
}

.request-tile--highlighted {
  box-shadow: 0 0 0 3px rgba(99, 102, 241, 0.5);
  z-index: 5;
}

.request-tile--dimmed {
  opacity: 0.4;
  filter: grayscale(0.5);
}

.request-tile__time {
  font-size: 10px;
  text-align: center;
  line-height: 1.2;
  color: rgba(255, 255, 255, 0.9);
  font-weight: 500;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
}

.request-tile__model {
  font-size: var(--model-font-size, 10px);
  text-align: center;
  line-height: 1.2;
  font-weight: 600;
  color: rgba(255, 255, 255, 0.95);
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.4);
  overflow: hidden;
  text-overflow: ellipsis;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  word-break: break-word;
}

.request-tile__provider {
  font-size: 9px;
  text-align: center;
  line-height: 1.2;
  color: rgba(255, 255, 255, 0.8);
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.request-tile__latency {
  font-size: 9px;
  text-align: center;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  color: rgba(255, 255, 255, 0.85);
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.3);
  font-weight: 500;
}

@media (prefers-reduced-motion: reduce) {
  .request-tile {
    transition: opacity 0.15s linear;
  }
  .request-tile:hover {
    transform: none;
  }
}
</style>
