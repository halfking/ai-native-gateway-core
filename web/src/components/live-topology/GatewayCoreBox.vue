<script setup lang="ts">
// GatewayCoreBox.vue —— 网关核心「大方块」（2026-10-17）
//
// 维护者要求：把内部的缓存、脱敏、路由、节点状态管理、会话数据文件存储、
// 数据会话写入、错误记录、探测任务、任务识别**按模块流程合理布局**，
// 放在**一个大方块**里表示是一个整体。
//
// 本组件就是那个大方块：外框 + 3×3 内部模块格 + 层间流程箭头。
//  · 主链路模块（缓存→脱敏→路由→节点状态→会话文件→会话写入）实线边框；
//  · 旁路模块（错误记录/探测任务/任务识别）虚线边框 + 淡色 —— 它们不是每个请求
//    都要串行走一遍，画成实线会让人误读成「九步流水线」。
//  · 每格显示窗口内命中数，失败时转红；**没有数据就是 0**，不编数字。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { GATEWAY_BRANCH_MODULES, GATEWAY_MODULES, type GatewayModuleKey } from '../../types/liveTopology'
import type { GatewayCoreBox } from '../../types/liveTopology'

const props = defineProps<{
  box: GatewayCoreBox
  stats: Record<GatewayModuleKey, { count: number; errors: number }>
  /** 聚焦中的模块（父层点选后传入，纯视觉）。 */
  selected?: GatewayModuleKey | null
}>()

const emit = defineEmits<{
  (e: 'select-module', key: GatewayModuleKey): void
}>()

const { t } = useI18n()

interface CellView {
  key: GatewayModuleKey
  x: number
  y: number
  w: number
  h: number
  label: string
  count: number
  branch: boolean
  tone: 'idle' | 'active' | 'error'
}

const cells = computed<CellView[]>(() =>
  props.box.cells.map((cell) => {
    const stat = props.stats?.[cell.key]
    const count = stat?.count ?? 0
    const errors = stat?.errors ?? 0
    return {
      key: cell.key,
      x: cell.x,
      y: cell.y,
      w: cell.w,
      h: cell.h,
      label: t(`liveTopology.gateway.${cell.key}`),
      count,
      branch: GATEWAY_BRANCH_MODULES.has(cell.key),
      // 有错误且错误占比显著 → error；只是「有量」→ active；没量 → idle
      tone: errors > 0 && count > 0 ? 'error' : count > 0 ? 'active' : 'idle',
    }
  }),
)

const title = computed(() => t('liveTopology.gateway.title'))
const subtitle = computed(() => t('liveTopology.gateway.subtitle'))
</script>

<template>
  <section
    class="gw"
    :style="{ left: `${box.x}px`, top: `${box.y}px`, width: `${box.w}px`, height: `${box.h}px` }"
    :aria-label="title"
  >
    <header class="gw__head">
      <span class="gw__title">{{ title }}</span>
      <span class="gw__subtitle">{{ subtitle }}</span>
    </header>
    <ul class="gw__grid">
      <li
        v-for="cell in cells"
        :key="cell.key"
        class="gw__cell"
        :class="[`is-${cell.tone}`, { 'is-branch': cell.branch, 'is-selected': cell.key === selected }]"
        :style="{ left: `${cell.x - box.x}px`, top: `${cell.y - box.y}px`, width: `${cell.w}px`, height: `${cell.h}px` }"
        tabindex="0"
        role="button"
        :aria-label="`${cell.label} ${cell.count}`"
        @click="emit('select-module', cell.key)"
        @keydown.enter.prevent="emit('select-module', cell.key)"
      >
        <span class="gw__cell-label">{{ cell.label }}</span>
        <span class="gw__cell-count">{{ cell.count }}</span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.gw {
  position: absolute;
  display: block;
  border: 1px solid var(--kx-primary);
  border-radius: 14px;
  background:
    linear-gradient(180deg, color-mix(in srgb, var(--kx-primary) 10%, transparent), transparent 40%),
    color-mix(in srgb, var(--kx-surface) 92%, transparent);
  box-shadow: var(--kx-shadow-md);
  pointer-events: none;
}

.gw__head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 6px 12px 0;
}

.gw__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 700;
  color: var(--kx-text);
  letter-spacing: 0.04em;
}

.gw__subtitle {
  font-size: 11px;
  color: var(--kx-muted);
}

.gw__grid {
  margin: 0;
  padding: 0;
  list-style: none;
}

.gw__cell {
  position: absolute;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 1px;
  padding: 4px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 9px;
  background: color-mix(in srgb, var(--kx-surface) 86%, transparent);
  pointer-events: auto;
  cursor: pointer;
  transition: transform 140ms ease, border-color 140ms ease;
}

.gw__cell:hover,
.gw__cell:focus-visible {
  transform: translateY(-1px);
  border-color: var(--kx-primary);
  outline: none;
}

/* 旁路模块：虚线 + 淡色，避免被读成串行流水线的一步 */
.gw__cell.is-branch {
  border-style: dashed;
  opacity: 0.82;
}

.gw__cell.is-active {
  border-color: color-mix(in srgb, var(--kx-primary) 55%, var(--kx-border));
}

.gw__cell.is-error {
  border-color: var(--kx-danger);
}

.gw__cell.is-idle {
  opacity: 0.7;
}

/* 聚焦中的模块：环形描边（不是整格变色 —— 变红会被读成故障） */
.gw__cell.is-selected {
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--kx-primary) 60%, transparent);
}

.gw__cell-label {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.gw__cell-count {
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-primary);
}

.gw__cell.is-error .gw__cell-count {
  color: var(--kx-danger);
}

.gw__cell.is-idle .gw__cell-count {
  color: var(--kx-muted);
}

@media (prefers-reduced-motion: reduce) {
  .gw__cell {
    transition: none;
  }
}
</style>
