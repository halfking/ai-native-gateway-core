<script setup lang="ts">
/**
 * ActionTimeline — 请求动作时间线（V3.3-OBS OBS-FE2，26号 §2/§6、24号 §7）
 *
 * 渲染一个请求的 request_lifecycle 动作序列（seq 升序）。数据只来自
 * liveStreamStore 的 SSE 推送索引（getRequestActions），不调后端查询、
 * 不造第二状态机；事件未推送时显示空态文案。
 *
 * BE2 已把动作 detail 字段摊平到事件顶层（queue_depth/ttfb_ms/
 * from_credential_id…），这里按已知键给出中文标签，未知键原样 k=v 展示；
 * 字段缺省 = 未上报，不渲染（禁止零值冒充）。
 *
 * 设计约束：颜色只用 var(--kx-*)（rule 12）；动画只用 transform/opacity；
 * 页面隐藏时 store 已停止写入，本组件无自身轮询。
 */
import { computed } from 'vue'
import { getRequestActions, type ActionEvent } from '../composables/liveStreamStore'
import { actionEventLabel } from '../composables/liveStreamDisplay'
import { credentialDisplayName, useCredentialLabels } from '../composables/useCredentialLabels'

const props = defineProps<{
  requestId: string
}>()

const actions = computed<ActionEvent[]>(() => getRequestActions(props.requestId))
// 2026-08-23 凭据显示：订阅标签缓存 revision，让异步加载完成后
// 时间线 credential 名称自动刷新。
const { labelRevision } = useCredentialLabels()
function credLabel(id: number): string {
  void labelRevision.value
  return credentialDisplayName(id)
}

// BE2 摊平后的已知 detail 键 → 中文标签；未知键退回原键名。
const DETAIL_LABELS: Record<string, string> = {
  client_protocol: '协议',
  auto_decision: 'auto 决策',
  queue_depth: '队列深度',
  weight: '权重',
  tier: '层级',
  sticky: '粘滞',
  attempt: '尝试',
  ttfb_ms: 'TTFB',
  status: '状态',
  latency_ms: '耗时',
  from_credential_id: '原节点',
  to_credential_id: '新节点',
  reason: '原因',
  from_model: '原模型',
  to_model: '新模型',
  blocked_reasons: '拦截原因',
}

function actionLabel(ev: ActionEvent): string {
  return actionEventLabel(ev.action)
}

// node_switch / model_switch 打特别标（26号：描边），retry 也有描边 + ⭐角标。
function isSwitchAction(ev: ActionEvent): boolean {
  return ev.action === 'node_switch' || ev.action === 'model_switch'
}

interface DetailChip {
  key: string
  label: string
  value: string
}

// 收集摊平的 detail 键：显式列出已知键 + 兜底扫描其余非核心字段，
// 避免把 request_id/seq/action/ts 等核心列重复进 chips。
const CORE_KEYS = new Set([
  'request_id', 'seq', 'action', 'ts', 'model', 'credential_id',
  'error_kind', 'retry_seq', 'retry',
])

function detailChips(ev: ActionEvent): DetailChip[] {
  const rec = ev as unknown as Record<string, unknown>
  const chips: DetailChip[] = []
  for (const key of Object.keys(DETAIL_LABELS)) {
    const v = rec[key]
    if (v === undefined || v === null || v === '') continue
    chips.push({ key, label: DETAIL_LABELS[key], value: String(v) })
  }
  for (const key of Object.keys(rec)) {
    if (CORE_KEYS.has(key) || key in DETAIL_LABELS) continue
    const v = rec[key]
    if (v === undefined || v === null || v === '' || typeof v === 'object') continue
    chips.push({ key, label: key, value: String(v) })
  }
  return chips
}

function timeLabel(ev: ActionEvent): string {
  if (!ev.ts) return ''
  const t = new Date(ev.ts)
  if (Number.isNaN(t.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`
}

const hasActions = computed(() => actions.value.length > 0)
</script>

<template>
  <div class="action-timeline" role="list" :aria-label="`动作时间线 ${requestId}`">
    <ul v-if="hasActions" class="at-list">
      <li
        v-for="ev in actions"
        :key="`${ev.seq ?? 0}-${ev.action ?? ''}`"
        class="at-row"
        :class="{
          'at-row--error': !!ev.error_kind,
          'at-row--retry': ev.retry === true,
          'at-row--switch': isSwitchAction(ev),
          'at-row--terminal': ev.action === 'reply' || ev.action === 'no_route',
        }"
        role="listitem"
      >
        <span class="at-time">{{ timeLabel(ev) }}</span>
        <span class="at-marker" aria-hidden="true" />
        <span class="at-name">{{ actionLabel(ev) }}</span>
        <span v-if="ev.model" class="at-model">{{ ev.model }}</span>
        <span v-if="ev.credential_id" class="at-cred">{{ credLabel(ev.credential_id) }}</span>
        <span v-if="ev.retry === true" class="at-retry" :title="`重试第 ${ev.retry_seq ?? '?'} 次`">⭐r{{ ev.retry_seq ?? '?' }}</span>
        <span v-if="ev.error_kind" class="at-error">{{ ev.error_kind }}</span>
        <span v-if="detailChips(ev).length" class="at-chips">
          <span
            v-for="chip in detailChips(ev)"
            :key="chip.key"
            class="at-chip"
            :title="`${chip.key} = ${chip.value}`"
          >{{ chip.label }} {{ chip.value }}</span>
        </span>
      </li>
    </ul>
    <p v-else class="at-empty">
      暂无动作事件推送（等待 request_lifecycle）
    </p>
  </div>
</template>

<style scoped>
.action-timeline {
  color: var(--kx-text);
}
.at-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.at-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  padding: 3px 0;
  font-size: 12px;
  border-bottom: 1px solid color-mix(in srgb, var(--kx-border) 50%, transparent);
}
.at-row:last-child {
  border-bottom: none;
}
.at-time {
  color: var(--kx-muted, var(--kx-text));
  font-variant-numeric: tabular-nums;
  min-width: 52px;
}
.at-marker {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--kx-primary);
  flex: none;
}
.at-row--error .at-marker {
  background: var(--kx-danger);
}
.at-row--retry .at-marker {
  background: var(--kx-warning);
}
.at-row--terminal .at-marker {
  background: var(--kx-success);
}
/* 26号：retry / switch 行打特别标 —— 描边 + 角标（⭐r{n} 在模板侧）。 */
.at-row--retry {
  border: 1px solid var(--kx-warning);
  border-radius: 4px;
  padding: 2px 4px;
}
.at-row--switch {
  border: 1px dashed var(--kx-primary);
  border-radius: 4px;
  padding: 2px 4px;
}
.at-name {
  font-weight: 600;
}
.at-model {
  color: var(--kx-muted, var(--kx-text));
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.at-cred {
  color: var(--kx-muted, var(--kx-text));
}
.at-retry {
  color: var(--kx-warning);
  font-weight: 600;
}
.at-error {
  color: var(--kx-danger);
}
.at-chips {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}
.at-chip {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 8px;
  background: color-mix(in srgb, var(--kx-primary) 10%, var(--kx-surface));
  color: var(--kx-text);
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.at-empty {
  margin: 0;
  padding: 8px 0;
  font-size: 12px;
  color: var(--kx-muted, var(--kx-text));
}
</style>
