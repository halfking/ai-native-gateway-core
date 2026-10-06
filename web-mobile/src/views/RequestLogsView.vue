<script setup lang="ts">
// RequestLogsView — 请求日志（admin 档，tenant_admin 可用）。
//
// 与 /integrity 同为**服务端分页**，但分页参数不同：这里是 `page`/`page_size`
// （logs.go:478-488），不是 model-integrity 的 `limit`/`offset`。别照抄。
//
// ★ 时间窗必须**按租户收窄**：后端 clampQueryWindowForTenant（logs.go:476/:1336-1341）
//   对非 default 租户把跨度 >72h 的查询静默收窄到 3 天。用户选「7 天」只拿到 3 天
//   且无任何提示 ⇒ 这里的时间选择器直接不提供超限选项，不做「选了再被打折」。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import {
  fetchRequestLogs,
  maxWindowHours,
  costNumber,
  type RequestLogRow,
  type RequestLogStatus,
} from '@/api/requestLogs'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { relativeTime } from '@/utils/format'

useHyperPage({ title: () => t('logs.title') })
const auth = useAuthStore()

const PAGE_SIZE = 20

/** 该租户允许的最大窗口（小时）——决定时间选择器有哪些选项。 */
const maxHours = computed(() => maxWindowHours(auth.userInfo?.tenant_id))

interface Range {
  key: string
  hours: number
}
/** 选项按租户上限过滤；超限的**不渲染**而不是选了再被后端打折。 */
const RANGES: Range[] = [
  { key: 'logs.range1h', hours: 1 },
  { key: 'logs.range6h', hours: 6 },
  { key: 'logs.range24h', hours: 24 },
  { key: 'logs.range3d', hours: 72 },
  { key: 'logs.range7d', hours: 168 },
  { key: 'logs.range30d', hours: 720 },
]
const availableRanges = computed(() => RANGES.filter((r) => r.hours <= maxHours.value))

const rangeKey = ref('logs.range24h')
const statusFilter = ref<RequestLogStatus | 'all'>('all')
const query = ref('')
const total = ref(0)
const aggregate = ref<{ total_requests?: number; failure_requests?: number; avg_latency_ms?: number } | null>(null)

function currentRange(): Range {
  return availableRanges.value.find((r) => r.key === rangeKey.value) ?? availableRanges.value[0] ?? RANGES[2]!
}
/** 拼 from 参数：后端接受 RFC3339 / "2006-01-02T15:04:05" / "2006-01-02 15:04:05"（logs.go:1350）。 */
function fromParam(): string {
  return new Date(Date.now() - currentRange().hours * 3600_000).toISOString()
}

const controller = new ContinuousListController<RequestLogRow>({
  fetchPage: async (page) => {
    const q = query.value.trim()
    const resp = await fetchRequestLogs({
      page,
      page_size: PAGE_SIZE,
      from: fromParam(),
      ...(q ? { q } : {}),
      ...(statusFilter.value === 'all' ? {} : { request_status: statusFilter.value }),
    })
    // count 是命中总数（logs.go:831-833）
    total.value = resp.count
    aggregate.value = resp.aggregate ?? null
    return { items: resp.items, total: resp.count }
  },
  stableKey: (r) => `req-${r.request_id}`,
  scopeKey: 'request-logs',
})

/** 窗口/状态是服务端参数 ⇒ 换它们必须重取；只有 q（搜索）是客户端。 */
async function onRangeChange(key: string): Promise<void> {
  if (rangeKey.value === key) return
  rangeKey.value = key
  controller.loadFirst('requery')
}
async function onStatusChange(s: RequestLogStatus | 'all'): Promise<void> {
  if (statusFilter.value === s) return
  statusFilter.value = s
  controller.loadFirst('requery')
}

// 搜索防抖 —— 但注意：q 是**服务端**参数（logs.go 里有 q 过滤），
// 所以它与 range/status 一样要重取，只是频率由防抖控制。
let debounceTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput(ev: Event): void {
  const value = (ev.target as HTMLInputElement).value
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    query.value = value
    controller.loadFirst('requery')
  }, 300)
}
// ★ 必须真卸载定时器：否则组件卸载后定时器仍会 fire → loadFirst 打到已卸载的
//   controller 上。这是移动端列表页的通用坑，NodesView 也踩过（onBeforeUnmount 清 timer）。
onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer)
})

const detail = ref<RequestLogRow | null>(null)
const detailOpenProxy = computed({
  get: () => detail.value != null,
  set: (v: boolean) => {
    if (!v) detail.value = null
  },
})

function rowTone(r: RequestLogRow): 'success' | 'warning' | 'danger' | 'muted' {
  if (r.request_status === 'rate_limited') return 'warning'
  if (r.request_status === 'in_progress') return 'muted'
  if (r.success === false) return 'danger'
  if (r.error_kind) return 'danger'
  return 'success'
}

function statusLabel(r: RequestLogRow): string {
  switch (r.request_status) {
    case 'success':
      return t('logs.statusSuccess')
    case 'failure':
      return t('logs.statusFailure')
    case 'rate_limited':
      return t('logs.statusRateLimited')
    case 'in_progress':
      return t('logs.statusInProgress')
    default:
      return r.success === false ? t('logs.statusFailure') : t('logs.statusSuccess')
  }
}

function costText(r: RequestLogRow): string | null {
  const n = costNumber(r.cost_display ?? r.cost_usd)
  if (n == null) return null
  const cur = r.cost_currency || 'USD'
  return cur === 'USD' ? `$${n.toFixed(n < 0.01 ? 6 : 4)}` : `${n.toFixed(6)} ${cur}`
}

// 租户上限变化（例如 hydrate 完成后拿到真实 tenant_id）要重取，
// 否则首屏可能用了超限窗口、拿到被后端打折的结果。
watch(maxHours, () => {
  if (!availableRanges.value.some((r) => r.key === rangeKey.value)) {
    rangeKey.value = availableRanges.value[0]?.key ?? 'logs.range24h'
  }
  controller.loadFirst('requery')
})
</script>

<template>
  <div class="view-root logs">
    <div class="logs__search">
      <AppIcon name="search" :size="18" />
      <input
        type="search"
        :placeholder="t('logs.searchPlaceholder')"
        :aria-label="t('common.search')"
        @input="onSearchInput"
      />
    </div>

    <div class="logs__ranges" role="tablist">
      <button
        v-for="r in availableRanges"
        :key="r.key"
        type="button"
        class="logs__chip"
        :class="{ 'logs__chip--on': rangeKey === r.key }"
        :aria-selected="rangeKey === r.key"
        @click="onRangeChange(r.key)"
      >
        {{ t(r.key) }}
      </button>
    </div>

    <div class="logs__statuses" role="tablist">
      <button
        v-for="s in (['all', 'success', 'failure', 'rate_limited'] as const)"
        :key="s"
        type="button"
        class="logs__chip logs__chip--sm"
        :class="{ 'logs__chip--on': statusFilter === s }"
        :aria-selected="statusFilter === s"
        @click="onStatusChange(s)"
      >
        {{ s === 'all' ? t('logs.statusAll') : statusLabel({ request_status: s } as RequestLogRow) }}
      </button>
    </div>

    <p class="logs__summary">
      <span>{{ t('logs.total', { n: total }) }}</span>
      <span v-if="aggregate?.failure_requests != null">{{ t('logs.failures', { n: aggregate.failure_requests }) }}</span>
      <span v-if="aggregate?.avg_latency_ms != null">
        {{ t('logs.avgLatency', { ms: Math.round(aggregate.avg_latency_ms) }) }}
      </span>
    </p>
    <p v-if="maxHours < 168" class="logs__window-hint">
      {{ t('logs.windowCapped', { h: maxHours }) }}
    </p>

    <HyperList
      :controller="controller"
      :item-key="(r: RequestLogRow) => `req-${r.request_id}`"
      :on-refresh="async () => { controller.loadFirst('requery') }"
      :empty-hint="t('logs.empty')"
    >
      <template #item="{ item: r }">
        <button type="button" class="data-card log-card" @click="detail = r">
          <div class="card-row">
            <span class="log-card__model">
              <StatusDot :tone="rowTone(r)" />
              {{ r.outbound_model || r.client_model || '—' }}
            </span>
            <span class="badge" :class="rowTone(r) === 'danger' ? 'badge--danger' : rowTone(r) === 'warning' ? 'badge--warning' : rowTone(r) === 'muted' ? 'badge--muted' : 'badge--success'">
              {{ statusLabel(r) }}
            </span>
          </div>
          <div class="log-card__fields">
            <span class="log-card__field">{{ relativeTime(r.ts) }}</span>
            <span v-if="r.provider_name" class="log-card__field">{{ r.provider_name }}</span>
            <span v-if="r.latency_ms != null" class="log-card__field">{{ r.latency_ms }}ms</span>
            <span v-if="r.total_tokens != null" class="log-card__field">{{ t('logs.tokens', { n: r.total_tokens }) }}</span>
            <span v-if="costText(r)" class="log-card__field">{{ costText(r) }}</span>
          </div>
          <div v-if="r.error_kind" class="log-card__err">{{ r.error_kind }}</div>
        </button>
      </template>
    </HyperList>

    <AppSheet v-model="detailOpenProxy" presentation="sheet" :title="detail ? (detail.request_id || t('logs.detail')) : ''">
      <template v-if="detail">
        <div class="data-card">
          <div class="card-field">
            <span>{{ t('logs.model') }}</span>
            <span class="card-field__value">{{ detail.client_model || '—' }}</span>
          </div>
          <div v-if="detail.outbound_model" class="card-field">
            <span>{{ t('logs.outboundModel') }}</span>
            <span class="card-field__value">{{ detail.outbound_model }}</span>
          </div>
          <div v-if="detail.provider_name" class="card-field">
            <span>{{ t('logs.provider') }}</span>
            <span class="card-field__value">
              {{ detail.provider_name }}<template v-if="detail.credential_label"> · {{ detail.credential_label }}</template>
            </span>
          </div>
          <div v-if="detail.latency_ms != null" class="card-field">
            <span>{{ t('logs.latency') }}</span>
            <span class="card-field__value num">{{ detail.latency_ms }}ms</span>
          </div>
          <div class="card-field">
            <span>{{ t('logs.tokens') }}</span>
            <span class="card-field__value num">
              {{ t('logs.tokenBreakdown', { p: detail.prompt_tokens ?? '—', c: detail.completion_tokens ?? '—' }) }}
            </span>
          </div>
          <div v-if="costText(detail)" class="card-field">
            <span>{{ t('logs.cost') }}</span>
            <span class="card-field__value num">{{ costText(detail) }}</span>
          </div>
          <div v-if="detail.error_kind" class="card-field">
            <span>{{ t('logs.errorKind') }}</span>
            <span class="card-field__value">{{ detail.error_kind }}</span>
          </div>
          <div class="card-field">
            <span>{{ t('logs.requestId') }}</span>
            <span class="card-field__value mono">{{ detail.request_id }}</span>
          </div>
        </div>
      </template>
    </AppSheet>
  </div>
</template>

<style scoped>
.logs__search {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-muted);
}

.logs__search input {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  min-width: 0;
  height: 100%;
  align-self: stretch;
}

.logs__search input:focus {
  outline: none;
}

.logs__ranges,
.logs__statuses {
  display: flex;
  gap: var(--app-space-2);
  overflow-x: auto;
  padding-bottom: var(--app-space-2);
}

.logs__statuses {
  padding-bottom: var(--app-space-1);
}

.logs__chip {
  flex: 0 0 auto;
  min-height: 48px;
  padding: 0 var(--app-space-3);
  /* R1（17 §4-R1 / 06 §7）：新增触控控件一律 ≥48 CSS px。
     44px 是存量 .btn 的下限、不是新标准；这些 chip 是 2026-10-06 新增的，
     走的是自定义选择器而非 .btn，故此前落在 32-40px —— 违反 R1。 */
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font: inherit;
  font-size: 0.8125rem;
  cursor: pointer;
}

.logs__chip--sm {
  min-height: 48px;
  font-size: 0.75rem;
  /* R1（17 §4-R1 / 06 §7）：新增触控控件一律 ≥48 CSS px。
     44px 是存量 .btn 的下限、不是新标准；这些 chip 是 2026-10-06 新增的，
     走的是自定义选择器而非 .btn，故此前落在 32-40px —— 违反 R1。 */
}

.logs__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: #fff;
}

.logs__summary {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-3);
  margin: 0 0 var(--app-space-1);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.logs__window-hint {
  margin: 0 0 var(--app-space-2);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.log-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  font: inherit;
  color: inherit;
  cursor: pointer;
}

.log-card:active {
  background: var(--app-primary-softer);
}

.log-card__model {
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-2);
  font-weight: 600;
  font-size: 0.9375rem;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.log-card__fields {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
}

.log-card__field {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.log-card__err {
  font-size: 0.75rem;
  color: var(--app-danger);
}
</style>
