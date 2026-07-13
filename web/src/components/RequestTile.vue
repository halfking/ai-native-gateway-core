<script setup lang="ts">
// RequestTile.vue — 请求色块组件（80x60）
// 2026-07-05: 泳道系统中的单个请求可视化
// 2026-07-13 v4: 视觉重构
//   - 状态以「左上角小三角形」表示：failure=红、success=绿、in_progress=蓝、cancelled=灰
//   - 边框色 = 模型/原厂色（vendor/provider）
//   - 探测/测试请求在三角形中显示 "T"
//   - 文字统一亮色（暗色皮肤下可读性好）
//   - 新增空闲（idle）样式：虚框表示

import { computed } from 'vue'
import type { RequestTile as RequestTileType, GroupByDimension } from '../types/swimlane'
import {
  VENDOR_COLORS,
  getStatusBorderKey,
  calculateFontSize,
  truncateText,
} from '../types/swimlane'
import { errorKindLabel } from '../composables/liveStreamDisplay'

const props = defineProps<{
  tile: RequestTileType
  groupBy: GroupByDimension
  isHighlighted: boolean
  isDimmed: boolean
}>()

const emit = defineEmits<{
  click: [requestId: string]
}>()

// 边框色（模型/原厂色） - 2026-07-13 v4: 边框色表示模型来源
const borderColor = computed(() => {
  return VENDOR_COLORS[props.tile.vendor] || VENDOR_COLORS['__unknown__']
})

// 状态三角形颜色 - 2026-07-13 v4: 左上角小三角形
const statusColor = computed(() => {
  const s = props.tile.status
  // 优先级：failure > success > in_progress > cancelled
  if (s === 'failure') return '#ef4444'          // 红
  if (s === 'success') return '#22c55e'          // 绿
  if (s === 'in_progress') return '#3b82f6'      // 蓝
  if (s === 'cancelled' || s === 'canceled') return '#6b7280' // 用户取消（灰）
  if (s === 'idle') return 'transparent'        // 空闲不显示三角
  return '#a1a1aa'                                // 兜底灰
})

// 是否显示三角形（空闲不显示）
const showStatusTriangle = computed(() => {
  return props.tile.status !== 'idle'
})

// 是否是探测/测试请求
const isTestRequest = computed(() => {
  return props.tile.is_probe === true
})

// 是否空闲块
const isIdle = computed(() => {
  return props.tile.status === 'idle'
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
  if (isIdle.value) return '空闲'
  if (props.tile.is_probe) {
    const origin = props.tile.probe_origin === 'gateway' ? 'GW' :
                   props.tile.probe_origin === 'scheduled' ? 'SCHED' : 'DIRECT'
    const attempt = props.tile.probe_attempt ? `#${props.tile.probe_attempt}` : ''
    return `${origin}${attempt}`
  }

  if (props.groupBy === 'vendor') {
    return truncateText(props.tile.model, 12)
  }
  if (props.groupBy === 'provider') {
    return truncateText(props.tile.model, 12)
  }
  // model模式：显示状态或错误
  if (props.tile.status === 'success') return '✓ 成功'
  if (props.tile.status === 'in_progress') return '⋯ 处理中'
  if (props.tile.status === 'cancelled' || props.tile.status === 'canceled') return '取消'
  if (props.tile.error_kind) {
    const label = errorKindLabel(props.tile.error_kind)
    return truncateText(label, 12)
  }
  return '—'
})

// 完整的 tooltip 信息
const tooltipText = computed(() => {
  const lines: string[] = []
  if (props.tile.is_probe) {
    lines.push('🛡️ 探测请求')
    const originLabel = props.tile.probe_origin === 'gateway' ? '网关路径' :
                        props.tile.probe_origin === 'scheduled' ? '定时探测' : '直连上游'
    lines.push(`来源: ${originLabel}`)
    if (props.tile.probe_attempt) {
      lines.push(`轮次: 第 ${props.tile.probe_attempt} 轮`)
    }
  }
  if (props.tile.status === 'failure') {
    if (props.tile.error_kind) {
      lines.push(`错误类型: ${errorKindLabel(props.tile.error_kind)}`)
      lines.push(`原始代码: ${props.tile.error_kind}`)
    } else {
      lines.push('状态: 请求失败')
    }
  } else if (props.tile.status === 'success') {
    lines.push('状态: 成功')
  } else if (props.tile.status === 'in_progress') {
    lines.push('状态: 处理中')
  } else if (props.tile.status === 'cancelled' || props.tile.status === 'canceled') {
    lines.push('状态: 用户取消')
  } else if (props.tile.status === 'idle') {
    lines.push('状态: 空闲（心跳占位）')
  }
  if (props.tile.model) lines.push(`模型: ${props.tile.model}`)
  if (props.tile.vendor) lines.push(`原厂: ${props.tile.vendor}`)
  if (props.tile.provider) lines.push(`供应商: ${props.tile.provider}`)
  if (props.tile.latency_ms != null) {
    const ms = props.tile.latency_ms
    lines.push(`延迟: ${ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : Math.round(ms) + 'ms'}`)
  }
  if (props.tile.prompt_tokens != null || props.tile.completion_tokens != null) {
    const p = props.tile.prompt_tokens ?? 0
    const c = props.tile.completion_tokens ?? 0
    lines.push(`Token: ${p} + ${c}`)
  }
  if (props.tile.cost_usd != null) {
    lines.push(`费用: $${props.tile.cost_usd.toFixed(4)}`)
  }
  if (props.tile.request_id) {
    lines.push(`ID: ${props.tile.request_id.slice(0, 12)}`)
  }
  if (props.tile.timestamp) {
    try {
      lines.push(`时间: ${new Date(props.tile.timestamp).toLocaleString()}`)
    } catch { /* ignore */ }
  }
  return lines.join('\n')
})

// 第三行内容（根据分组模式）
const line3Content = computed(() => {
  if (isIdle.value) return '心跳'
  if (props.groupBy === 'vendor') {
    return truncateText(props.tile.provider, 10)
  }
  if (props.groupBy === 'provider') {
    return truncateText(props.tile.vendor, 10)
  }
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
      'request-tile--probe': tile.is_probe,
      'request-tile--idle': isIdle,
    }"
    :style="{
      '--border-color': borderColor,
      '--status-color': statusColor,
      '--model-font-size': modelFontSize + 'px',
    }"
    :title="tooltipText"
    @click="handleClick"
  >
    <!-- 2026-07-13 v4: 左上角状态三角形（探测请求在三角形中显示 "T"） -->
    <div v-if="showStatusTriangle" class="request-tile__status-corner">
      <span v-if="isTestRequest" class="request-tile__status-label">T</span>
    </div>

    <div class="request-tile__time" v-if="!isIdle">{{ timeLabel }}</div>
    <div class="request-tile__model">{{ line2Content }}</div>
    <div class="request-tile__provider">{{ line3Content }}</div>
    <div class="request-tile__latency" v-if="latencyLabel && !isIdle">{{ latencyLabel }}</div>
  </div>
</template>

<style scoped>
.request-tile {
  width: 80px;
  height: 60px;
  border-radius: 4px;
  border: 1.5px solid var(--border-color, #6b7280);
  background: transparent;
  padding: 4px 6px;
  cursor: pointer;
  transition: transform 0.15s ease, box-shadow 0.15s ease;
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
  box-shadow: 0 0 0 2.5px rgba(99, 102, 241, 0.6);
  z-index: 5;
}

.request-tile--dimmed {
  opacity: 0.4;
  filter: grayscale(0.5);
}

/* 空闲色块：虚框表示 */
.request-tile--idle {
  border: 1.5px dashed var(--border-color, #6b7280);
  opacity: 0.55;
}

/* 2026-07-13 v4: 左上角小三角形（CSS border 实现） */
.request-tile__status-corner {
  position: absolute;
  top: 0;
  left: 0;
  width: 0;
  height: 0;
  border-style: solid;
  border-width: 9px 9px 0 0;
  border-color: var(--status-color, #6b7280) transparent transparent transparent;
  z-index: 2;
  pointer-events: none;
  line-height: 1;
}

.request-tile__status-label {
  position: absolute;
  top: -8px;
  left: 0px;
  font-size: 7px;
  font-weight: 800;
  color: #ffffff;
  text-shadow: 0 0 1px rgba(0,0,0,0.6);
  line-height: 1;
  width: 8px;
  text-align: center;
  letter-spacing: -0.2px;
}

/* 2026-07-13 v4: 亮色文字（暗主题下可读） */
.request-tile__time {
  font-size: 10px;
  text-align: center;
  line-height: 1.2;
  color: #f3f4f6;
  font-weight: 500;
}

.request-tile__model {
  font-size: var(--model-font-size, 10px);
  text-align: center;
  line-height: 1.2;
  font-weight: 600;
  color: #f9fafb;
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
  color: #d1d5db;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.request-tile__latency {
  font-size: 9px;
  text-align: center;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  color: #e5e7eb;
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
