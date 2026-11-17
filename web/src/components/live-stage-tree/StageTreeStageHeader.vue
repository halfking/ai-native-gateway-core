<script setup lang="ts">
// StageTreeStageHeader.vue —— 请求阶段树的**列头**（2026-11-17 设计对齐轮）
//
// ★ 为什么必须有列头（设计第 8 轮）：
//   一条带横跨 9 列（客户端 + 7 个发送轨阶段），没有列头时
//   「这个格子是路由还是压缩」全靠记忆 ⇒ 读错列 ⇒ 把数读成别的阶段的。
//   ⇒ 列头与带**共用同一套列宽**（stageTreeLayout.ts 是唯一来源），
//   并且列头带**实时计数**（进行中 ◐n / 失败 ✕n）—— 一眼看出哪一段在堵。
//
// ★ 列头**粘顶**：树会持续往上翻，列头必须一直在 ⇒ position: sticky。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { StageKey } from '../../types/liveStageTree'
import {
  COMPACT_GRID_COLUMNS,
  FULL_GRID_COLUMNS,
  SEND_COLUMN,
} from './stageTreeLayout'

const props = defineProps<{
  mode: 'full' | 'compact'
  /** 每个阶段的计数：进行中 / 失败。 */
  counts: Record<string, { active: number; failed: number }>
}>()

const { t } = useI18n()

const gridColumns = computed(() =>
  props.mode === 'compact' ? COMPACT_GRID_COLUMNS : FULL_GRID_COLUMNS,
)

/** 发送轨阶段的列位顺序（与 SEND_COLUMN 同源）。 */
const SEND_ORDER: StageKey[] = [
  'auth',
  'sanitize',
  'approval',
  'injection',
  'route',
  'compress',
  'provider',
]

/** 列名（★ 逐字面量 t()：动态键无法被引用扫描器解析）。 */
function stageName(key: StageKey): string {
  switch (key) {
    case 'auth':
      return t('liveStageTree.stage.auth')
    case 'sanitize':
      return t('liveStageTree.stage.sanitize')
    case 'approval':
      return t('liveStageTree.stage.approval')
    case 'injection':
      return t('liveStageTree.stage.injection')
    case 'route':
      return t('liveStageTree.stage.route')
    case 'compress':
      return t('liveStageTree.stage.compress')
    case 'provider':
      return t('liveStageTree.stage.provider')
    default:
      return ''
  }
}
</script>

<template>
  <div class="stagehead" :style="{ gridTemplateColumns: gridColumns }" data-testid="stage-tree-header">
    <div class="stagehead__cell is-time" :style="{ gridColumn: '1' }">
      <b>{{ t('liveStageTree.header.time') }}</b>
    </div>
    <div class="stagehead__cell" :style="{ gridColumn: '2' }">
      <b>{{ t('liveStageTree.header.client') }}</b>
    </div>

    <template v-if="props.mode === 'full'">
      <div
        v-for="key in SEND_ORDER"
        :key="key"
        class="stagehead__cell"
        :style="{ gridColumn: String(SEND_COLUMN[key]) }"
      >
        <b>{{ stageName(key) }}</b>
        <span class="stagehead__count">
          <span v-if="props.counts[key]?.active" class="is-active">◐{{ props.counts[key].active }}</span>
          <span v-if="props.counts[key]?.failed" class="is-failed">✕{{ props.counts[key].failed }}</span>
        </span>
      </div>
    </template>

    <template v-else>
      <div class="stagehead__cell is-flow" :style="{ gridColumn: '3 / span 11' }">
        <b>{{ t('liveStageTree.header.flow') }}</b>
      </div>
      <div class="stagehead__cell is-error" :style="{ gridColumn: '14' }">
        <b>{{ t('liveStageTree.error.column') }}</b>
      </div>
    </template>
  </div>
</template>

<style scoped>
.stagehead {
  display: grid;
  gap: 0 8px;
  align-items: center;
  height: 26px;
  padding: 0 12px;
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
  position: sticky;
  top: 0;
  z-index: 3;
}

.stagehead__cell {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  min-width: 0;
  color: var(--kx-text-secondary);
  font-size: var(--kx-text-2xs);
}

.stagehead__cell b {
  color: var(--kx-text);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.stagehead__cell.is-time {
  justify-content: flex-start;
}

.stagehead__count {
  display: inline-flex;
  gap: 4px;
  font-variant-numeric: tabular-nums;
}

.stagehead__count .is-active {
  color: var(--kx-primary);
}

.stagehead__count .is-failed {
  color: var(--kx-danger);
}
</style>
