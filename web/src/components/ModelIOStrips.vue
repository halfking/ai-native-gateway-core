<script setup lang="ts">
/**
 * ModelIOStrips — 模型标题行右侧「输入 / 输出」双队列条。
 *
 * 输入：该模型当前在途请求（in_progress tile）= 当前请求队列。
 * 输出：该模型最近 N 条请求的客户端最终结果（success / failure /
 * rate_limited）= 输出稳定性。上游有错误但网关重试/切换节点后成功的
 * 请求，这里只保留最终 success → 绿色（可带「经重试」角标）。
 * 上游供应商每次尝试的质量仍由节点卡片滑窗（✓/✗）呈现。
 *
 * 颜色必须 var(--kx-*) / 既有语义 var，禁止硬编码 hex（rule 12）；
 * 成功率无样本时显示「—」，禁止零值冒充（13号门禁）。
 */
import { computed } from 'vue'
import type { LiveStreamTile } from '../composables/liveStreamStore'
import { statusBarColor, statusSemanticLabel } from '../composables/liveStreamDisplay'

const props = defineProps<{
  /** 输入：当前在途请求（in_progress tile）。 */
  inflight: LiveStreamTile[]
  /** 输出：客户端最终结果（success / failure / rate_limited tile）。 */
  terminal: LiveStreamTile[]
  /** 输出成功率（0-1）；null = 暂无已完成请求（显示 —，不冒充）。 */
  successRate: number | null
  /** 输出侧失败数（failure + rate_limited）。 */
  failed: number
  /** 经重试/节点切换后最终成功的 request_id 集合（best-effort 增强，
   *  动作回放窗口外缺省为不标记）。 */
  rescuedIds?: Set<string>
}>()

interface StripCell {
  key: string
  color: string
  title: string
  rescued: boolean
}

function requestLabel(tile: LiveStreamTile): string {
  return tile.request_id ? tile.request_id.slice(0, 12) : ''
}

/** rate_limited 对客户端是实际收到的 429，输出侧按警示色呈现；
 *  泳道小竖条（statusBarColor）把它归为 muted，是泳道自己的口径。 */
function outputCellColor(tile: LiveStreamTile): string {
  if (tile.status === 'rate_limited') return 'var(--warning)'
  return statusBarColor(tile.status, tile.error_kind)
}

const inflightCells = computed<StripCell[]>(() =>
  props.inflight.map(tile => ({
    key: tile.request_id || `${tile.timestamp}|inflight`,
    color: statusBarColor('in_progress', null),
    title: [
      statusSemanticLabel(tile.status, tile.error_kind),
      tile.stage ? tile.stage : '',
      requestLabel(tile),
    ].filter(Boolean).join(' · '),
    rescued: false,
  })),
)

const terminalCells = computed<StripCell[]>(() =>
  props.terminal.map(tile => {
    const rescued = tile.status === 'success' && (props.rescuedIds?.has(tile.request_id) ?? false)
    const semantic = statusSemanticLabel(tile.status, tile.error_kind)
    return {
      key: tile.request_id || `${tile.timestamp}|${tile.status}`,
      color: outputCellColor(tile),
      title: [
        rescued ? `${semantic}（经重试/切换节点后成功）` : semantic,
        requestLabel(tile),
      ].filter(Boolean).join(' · '),
      rescued,
    }
  }),
)

const successPctLabel = computed(() => {
  if (props.successRate == null) return '—'
  return `✓${Math.round(props.successRate * 100)}%`
})

const outputRowTitle = computed(() => {
  if (props.successRate == null) return '输出 · 客户端最终结果：暂无已完成的客户端请求'
  const total = props.terminal.length
  return `输出 · 客户端最终结果：成功 ${props.terminal.length - props.failed} / 共 ${total}（最近 ${total} 条，仅终态）`
})

const inputRowTitle = computed(() =>
  `输入 · 当前请求队列：${props.inflight.length} 个在途请求`,
)
</script>

<template>
  <div
    class="model-io-strips"
    role="img"
    :aria-label="`输入 ${inflight.length} 个在途请求；输出 ${successPctLabel}`"
  >
    <div class="model-io-strips__row" :title="inputRowTitle">
      <span class="model-io-strips__label model-io-strips__label--in">输入</span>
      <span class="model-io-strips__meta" :class="{ 'model-io-strips__meta--muted': inflight.length === 0 }">{{ inflight.length }}</span>
      <div class="model-io-strips__cells">
        <span
          v-for="cell in inflightCells"
          :key="`in-${cell.key}`"
          class="model-io-strips__cell"
          :style="{ background: cell.color }"
          :title="cell.title"
        />
      </div>
    </div>
    <div class="model-io-strips__row" :title="outputRowTitle">
      <span class="model-io-strips__label model-io-strips__label--out">输出</span>
      <span
        class="model-io-strips__meta"
        :class="successRate != null && failed > 0 ? 'model-io-strips__meta--bad' : 'model-io-strips__meta--muted'"
      >{{ successPctLabel }}<template v-if="failed > 0"> ✗{{ failed }}</template></span>
      <div class="model-io-strips__cells">
        <span
          v-for="cell in terminalCells"
          :key="`out-${cell.key}`"
          class="model-io-strips__cell"
          :class="{ 'model-io-strips__cell--rescued': cell.rescued }"
          :style="{ background: cell.color }"
          :title="cell.title"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-io-strips {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-left: auto;
  flex: 1 1 auto;
  min-width: 0;
  max-width: 240px;
}
.model-io-strips__row {
  display: flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.model-io-strips__label {
  flex: 0 0 auto;
  font-size: 10px;
  line-height: 1;
  padding: 2px 4px;
  border-radius: 4px;
  color: var(--kx-text-secondary);
  background: var(--kx-bg, var(--kx-surface));
  border: 1px solid var(--kx-border);
}
.model-io-strips__label--in {
  color: var(--kx-primary);
  border-color: color-mix(in srgb, var(--kx-primary) 35%, var(--kx-border));
}
.model-io-strips__label--out {
  color: var(--kx-success);
  border-color: color-mix(in srgb, var(--kx-success) 35%, var(--kx-border));
}
.model-io-strips__meta {
  flex: 0 0 auto;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-text);
  white-space: nowrap;
}
.model-io-strips__meta--muted {
  color: var(--kx-text-secondary);
}
.model-io-strips__meta--bad {
  color: var(--kx-danger);
}
.model-io-strips__cells {
  display: flex;
  align-items: stretch;
  gap: 1px;
  flex: 1 1 auto;
  min-width: 0;
  height: 9px;
  overflow: hidden;
  justify-content: flex-end;
}
.model-io-strips__cell {
  flex: 0 0 3px;
  width: 3px;
  min-width: 2px;
  border-radius: 1px;
  background: var(--muted);
}
/* 经重试/切换节点后最终成功：绿色格底部加警示色条，一眼区分
   “上游顺利”与“网关救回”。 */
.model-io-strips__cell--rescued {
  box-shadow: inset 0 -3px 0 color-mix(in srgb, var(--kx-warning) 85%, transparent);
}
</style>
