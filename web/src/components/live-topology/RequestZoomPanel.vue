<script setup lang="ts">
// RequestZoomPanel.vue —— 「点击放大」的请求详情（2026-10-17）
//
// 维护者要求右面板的请求「可以点击放大」。这里做的是**内联覆盖式放大面板**：
// 不是弹窗、不是新页面 —— 拓扑图仍在视野里，放大的是这条请求的细节。
//
// 展示口径纪律：缺省字段渲染成「—」，**禁止用 0 冒充**（本仓既有铁律）。
// 没有上报 `ttfb` 就显示「—」，不显示 0ms。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ActionEvent } from '../../composables/liveStreamStore'
import type { PanelEntry } from '../../composables/useLiveTopology'

const props = defineProps<{
  entry: PanelEntry
  decisions: ActionEvent[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()

const rows = computed(() => {
  const e = props.entry
  return [
    { key: 'requestId', label: t('liveTopology.zoom.requestId'), value: e.requestId || '—' },
    { key: 'time', label: t('liveTopology.zoom.time'), value: e.ts },
    { key: 'client', label: t('liveTopology.zoom.client'), value: e.client || '—' },
    { key: 'protocol', label: t('liveTopology.zoom.protocol'), value: e.clientProtocol || '—' },
    { key: 'provider', label: t('liveTopology.zoom.provider'), value: e.provider },
    { key: 'node', label: t('liveTopology.zoom.node'), value: e.nodeId || '—' },
    { key: 'model', label: t('liveTopology.zoom.model'), value: e.model || '—' },
    { key: 'status', label: t('liveTopology.zoom.status'), value: statusLabel(e.status || 'in_progress') },
    { key: 'latency', label: t('liveTopology.zoom.latency'), value: typeof e.latencyMs === 'number' ? `${e.latencyMs} ms` : '—' },
    { key: 'tokens', label: t('liveTopology.zoom.tokens'), value: `${fmt(e.promptTokens)} / ${fmt(e.completionTokens)} / ${fmt(e.tokens)}` },
    { key: 'cost', label: t('liveTopology.zoom.cost'), value: typeof e.costUsd === 'number' ? `$${e.costUsd.toFixed(4)}` : '—' },
    { key: 'session', label: t('liveTopology.zoom.session'), value: e.sessionId || '—' },
    { key: 'taskType', label: t('liveTopology.zoom.taskType'), value: e.requestType || '—' },
    { key: 'probe', label: t('liveTopology.zoom.probe'), value: e.isProbe ? t('liveTopology.zoom.yes') : t('liveTopology.zoom.no') },
    { key: 'error', label: t('liveTopology.zoom.error'), value: e.errorKind ? `${e.errorKind}${e.failureStage ? ` (${e.failureStage})` : ''}` : '—' },
  ]
})

function fmt(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return String(value)
}

/** 阶段标签。**缺失就是缺失** —— 早先这里回落成 `routing`，
 *  等于给一条没上报阶段的事件凭空盖上「路由」章。 */
function stageLabel(stage?: string): string {
  return t(`liveTopology.decision.stage.${stage || 'unreported'}`)
}

/** 状态显示名。线上 status 枚举是 snake_case（in_progress/rate_limited），
 *  而 filter 文案键是 camelCase（inProgress/rateLimited）—— 早先直接拼键，
 *  in_progress/rate_limited 两条渲染成原始键名（2026-10-18 浏览器验收抓到）。
 *  未知枚举值显示原始字符串（比回落到「全部状态」诚实）。 */
function statusLabel(status: string): string {
  const key: Record<string, string> = {
    success: 'success',
    in_progress: 'inProgress',
    failure: 'failure',
    rate_limited: 'rateLimited',
  }
  const mapped = key[status]
  return mapped ? t(`liveTopology.filter.${mapped}`) : status
}

function actionLabel(action?: string): string {
  return action ? t(`liveTopology.decision.action.${action}`, action) : t('liveTopology.decision.action.unknown')
}
</script>

<template>
  <section class="zoom" role="region" :aria-label="t('liveTopology.zoom.title')">
    <header class="zoom__head">
      <span class="zoom__title">{{ t('liveTopology.zoom.title') }}</span>
      <button type="button" class="zoom__close" :aria-label="t('liveTopology.zoom.close')" @click="emit('close')">✕</button>
    </header>

    <dl class="zoom__grid">
      <div v-for="row in rows" :key="row.key" class="zoom__row">
        <dt class="zoom__label">{{ row.label }}</dt>
        <dd class="zoom__value">{{ row.value }}</dd>
      </div>
    </dl>

    <div class="zoom__decisions">
      <span class="zoom__section">{{ t('liveTopology.decision.title') }}</span>
      <ol v-if="decisions.length" class="zoom__timeline">
        <li v-for="(d, i) in decisions" :key="`${d.seq ?? i}-${d.action ?? i}`" class="zoom__step">
          <span class="zoom__stage">{{ stageLabel(d.stage_category) }}</span>
          <span class="zoom__action">{{ actionLabel(d.action) }}</span>
          <span v-if="typeof d.ttfb_ms === 'number'" class="zoom__meta">ttfb {{ d.ttfb_ms }}ms</span>
          <span v-if="d.queue_depth" class="zoom__meta">q{{ d.queue_depth }}</span>
          <span v-if="d.reason || d.auto_decision" class="zoom__meta">{{ d.reason || d.auto_decision }}</span>
        </li>
      </ol>
      <p v-else class="zoom__empty">{{ t('liveTopology.decision.empty') }}</p>
    </div>
  </section>
</template>

<style scoped>
.zoom {
  position: absolute;
  right: 12px;
  bottom: 12px;
  z-index: 5;
  width: min(460px, calc(100% - 24px));
  max-height: calc(100% - 24px);
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px;
  border: 1px solid var(--kx-primary);
  border-radius: 12px;
  background: color-mix(in srgb, var(--kx-surface) 97%, transparent);
  box-shadow: var(--kx-shadow-md);
  overflow-y: auto;
}

.zoom__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.zoom__title {
  font-size: var(--kx-font-md, 14px);
  font-weight: 700;
  color: var(--kx-text);
}

.zoom__close {
  border: 0;
  background: transparent;
  color: var(--kx-muted);
  cursor: pointer;
  font-size: 13px;
}

.zoom__close:hover {
  color: var(--kx-danger);
}

.zoom__grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px 10px;
  margin: 0;
}

.zoom__row {
  display: flex;
  flex-direction: column;
}

.zoom__label {
  font-size: 10px;
  color: var(--kx-muted);
}

.zoom__value {
  margin: 0;
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  font-variant-numeric: tabular-nums;
  word-break: break-all;
}

.zoom__decisions {
  border-top: 1px dashed var(--kx-border);
  padding-top: 6px;
}

.zoom__section {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.zoom__timeline {
  margin: 4px 0 0;
  padding-left: 14px;
  font-size: 11px;
  color: var(--kx-text);
}

.zoom__step {
  display: flex;
  gap: 6px;
  align-items: baseline;
  padding: 2px 0;
}

.zoom__stage {
  min-width: 44px;
  color: var(--kx-primary);
}

.zoom__action {
  flex: 1;
}

.zoom__meta {
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.zoom__empty {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--kx-muted);
}
</style>
