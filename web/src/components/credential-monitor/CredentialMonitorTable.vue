<script setup lang="ts">
/**
 * CredentialMonitorTable — 凭据监控主列表（2026-09-13 P3-5 第二批，
 * 自 CredentialMonitorView.vue 拆出，方案 §4.7-4 大文件拆分）。
 * 只承载表格结构与勾选交互；数据加载与详情抽屉在宿主视图。
 */
import CredentialStatusBar from '../CredentialStatusBar.vue'
import { effectiveCredentialReason, healthBadge, rateClass, rateText } from './helpers'
import type { CredentialMonitorSummary } from '../../api'

export interface CredentialRow extends CredentialMonitorSummary {
  modelTotal: number
  modelAvailable: number
}

defineProps<{
  rows: CredentialRow[]
  selectedIds: Set<number>
}>()

const emit = defineEmits<{
  'toggle-all': []
  toggle: [id: number]
  open: [cred: CredentialMonitorSummary]
}>()
</script>

<template>
  <div class="card" style="overflow-x:auto;padding:0">
    <table class="data-table dense">
      <thead>
        <tr>
          <th style="width:40px">
            <input
              type="checkbox"
              :checked="rows.length > 0 && rows.every(c => selectedIds.has(c.id))"
              @change="emit('toggle-all')"
            />
          </th>
          <th>凭据</th>
          <th>供应商</th>
          <th>可用性</th>
          <th>健康</th>
          <th>模型 (可用/总数)</th>
          <th>最近成功率</th>
          <th>broken 模型</th>
          <th>并发</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="c in rows" :key="c.id" class="clickable-row" @click="emit('open', c)">
          <td @click.stop>
            <input type="checkbox" :checked="selectedIds.has(c.id)" @change="emit('toggle', c.id)" />
          </td>
          <td>
            <div>{{ c.label || `#${c.id}` }}</div>
            <div class="cell-sub">ID: {{ c.id }}</div>
          </td>
          <td>{{ c.provider_name }}</td>
          <td>
            <CredentialStatusBar
              :credential="c"
              :labels="{ active: 'ready', cooling: 'cooling', degraded: 'degraded', rate_limited: 'rate_limited', unreachable: 'unreachable', auth_failed: 'auth_failed', suspended: 'suspended', quota_exhausted: 'quota_exhausted', disabled: 'disabled', deleted: 'deleted', unknown: 'unknown' }"
              :reason="effectiveCredentialReason(c)"
            />
            <div v-if="c.state_reason_code" class="cell-sub">{{ c.state_reason_code }}</div>
          </td>
          <td>
            <span class="badge" :class="healthBadge(c.health_status)">{{ c.health_status }}</span>
          </td>
          <td>
            <span :class="c.modelAvailable < c.modelTotal ? 'rate-warn' : ''">
              {{ c.modelAvailable }}/{{ c.modelTotal }}
            </span>
          </td>
          <td>
            <span class="rate-cell" :class="rateClass(c.aggregated_success_rate)">
              {{ rateText(c.aggregated_success_rate) }}
            </span>
          </td>
          <td>
            <span v-if="(c.broken_model_count ?? 0) === 0" class="cell-muted">—</span>
            <span v-else class="badge badge-red model-badge">{{ c.broken_model_count }}</span>
          </td>
          <td>
            <div>手动: {{ c.concurrency_limit || '—' }}</div>
            <div class="cell-sub">生效: {{ c.effective_concurrency }}</div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
/* Main credentials data table — denser than the global style.css default
   (which is 13px / 10px 12px). Mirrors the .dense-table pattern from
   /routing-v2's overview tab so the credentials list can show more rows
   without the right edge pushing past the sidebar. */
.data-table.dense thead th {
  padding: 5px 8px;
  font-size: 10px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  background: var(--bg-subtle);
}
.data-table.dense tbody td {
  padding: 5px 8px;
  font-size: 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.data-table.dense tbody tr:last-child td { border-bottom: none; }

.model-badge {
  font-size: 10px;
  padding: 1px 6px;
}

/* Clickable table rows (click opens the detail drawer) */
.clickable-row {
  cursor: pointer;
}
.clickable-row:hover {
  background: color-mix(in srgb, var(--kx-text) 4%, transparent) !important;
}

/* Rate coloring */
.rate-cell { font-weight: 600; }
.rate-good { color: var(--success); }
.rate-warn { color: var(--warning); }
.rate-bad { color: var(--danger); }
.rate-none { color: var(--muted); }

.cell-sub { font-size: 11px; color: var(--muted); }
.cell-muted { color: var(--muted); }
</style>
