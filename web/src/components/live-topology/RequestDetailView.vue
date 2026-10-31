<script setup lang="ts">
// RequestDetailView.vue —— 请求详情内容（2026-10-18 从 RequestZoomPanel 抽取）
//
// 同一份详情内容有两个宿主：拓扑页的内联放大面板（RequestZoomPanel，
// 覆盖式）与全页详情路由（/live/request/:id，RequestDetailPage）。
// 抽成共享组件是必须的 —— 两处各写一份必漂移（同一请求在放大面板和
// 全页里字段不一致，是同一族「双真值」缺陷）。
//
// 展示口径纪律：缺省字段渲染成「—」，**禁止用 0 冒充**（本仓既有铁律）。
// 没有上报 `ttfb` 就显示「—」，不显示 0ms。
//
// 类名沿用 zoom__*（既有判据按这些类断言，抽取不应要求判据跟着改名）。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ActionEvent } from '../../composables/liveStreamStore'
import type { PanelEntry } from '../../composables/useLiveTopology'
import { clientDisplayName } from '../../lib/liveTopologyGraph'

const props = defineProps<{
  entry: PanelEntry
  decisions: ActionEvent[]
}>()

const { t } = useI18n()

const rows = computed(() => {
  const e = props.entry
  return [
    { key: 'requestId', label: t('liveTopology.zoom.requestId'), value: e.requestId || '—' },
    { key: 'time', label: t('liveTopology.zoom.time'), value: e.ts },
    { key: 'client', label: t('liveTopology.zoom.client'), value: clientLabel(e.client) },
    { key: 'protocol', label: t('liveTopology.zoom.protocol'), value: e.clientProtocol || '—' },
    { key: 'provider', label: t('liveTopology.zoom.provider'), value: e.provider },
    { key: 'node', label: t('liveTopology.zoom.node'), value: e.nodeId || '—' },
    { key: 'model', label: t('liveTopology.zoom.model'), value: modelValue(e) },
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

/** 模型行的值（2026-10-28 选路层轮）：
 *  - auto 请求：`auto → 最终模型`（点名的是 auto，网关按任务选出 —— 面板
 *    行内同款表达，这里补全浮层/详情侧）；
 *  - 别名请求（requestedModel ≠ 最终 canonical 名）：`别名 → 标准名`；
 *  - 其余：只显示最终模型名，不编映射。
 *  语义注意：auto 请求的 client_model 在后端已被改写为选中的模型
 *  （preAutoModel 只进 requestjourney），所以「要的是 auto」由 isAuto
 *  表达，不能靠 requestedModel === 'auto' 反推。 */
function modelValue(e: PanelEntry): string {
  const final = e.model || '—'
  if (e.isAuto) return `auto → ${final}`
  if (e.requestedModel && e.requestedModel !== e.model) return `${e.requestedModel} → ${final}`
  return final
}

/** 客户端易读名：identity 兜底键/unknown 不做 Title 化（哈希前缀被大写
 *  反而失真），known 品牌走 clientDisplayName。 */
function clientLabel(client: string): string {
  if (!client || client === 'unknown') return client || '—'
  return clientDisplayName(client)
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
  <div class="detail">
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
  </div>
</template>

<style scoped>
.detail {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.zoom__grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 4px 8px;
  margin: 0;
}

.zoom__row {
  display: flex;
  flex-direction: column;
}

.zoom__label {
  font-size: 11px;
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
  padding-top: 4px;
}

.zoom__section {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.zoom__timeline {
  margin: 4px 0 0;
  padding-left: 12px;
  font-size: 11px;
  color: var(--kx-text);
}

.zoom__step {
  display: flex;
  gap: 4px;
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
