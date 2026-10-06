<script setup lang="ts">
// RoutingLogView — 路由 / 探测 / 状态变更的事件流水。
//
// 数据源：GET /api/credentials/routing-log
//   ★ 路径前缀是 /api/credentials/，**不是** /api/admin/ ——
//     它注册在 monitor handler 下（credential_monitor.go:171 → handler.go:1453），
//     写成 /api/admin/... 会 404，而 404 与「时间窗超限」的 400 长得像，
//     很容易被误读成「这个功能没有数据」。
//
// 鉴权：h.admin，tenant_admin 可用。
//
// 分页：limit + offset（**不是** cursor，也不是 page/page_size）。
//   后端 limit>500 是静默 clamp，offset<0 归 0。
//
// ⚠️ 时间窗 > 7d 是 **400 硬失败**（credential_routing_log.go:235-237），
//   不是降级也不是 clamp ⇒ 选项本身按 7 天封顶，界面上直接告诉用户原因。

import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { ContinuousListController } from '@/hyper'
import HyperList from '@/components/common/HyperList.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchRoutingLog,
  buildRoutingLogWindow,
  ROUTING_LOG_KINDS,
  ROUTING_LOG_RESULTS,
  ROUTING_LOG_MAX_WINDOW_HOURS,
  type RoutingLogEntry,
  type RoutingLogKind,
  type RoutingLogResult,
} from '@/api/routingLog'

useHyperPage({ title: () => t('routingLog.title') })
const router = useRouter()

const PAGE_SIZE = 50

const kind = ref<RoutingLogKind>('all')
const result = ref<RoutingLogResult>('all')
const rangeHours = ref(24)
const total = ref(0)

/** 后端硬上限 7 天，选项封顶在这里而不是发出后吃 400。 */
const RANGE_OPTIONS = [
  { key: 'routingLog.range1h', hours: 1 },
  { key: 'routingLog.range6h', hours: 6 },
  { key: 'routingLog.range24h', hours: 24 },
  { key: 'routingLog.range3d', hours: 72 },
  { key: 'routingLog.range7d', hours: 168 },
].filter((o) => o.hours <= ROUTING_LOG_MAX_WINDOW_HOURS)

const availableRanges = computed(() => RANGE_OPTIONS)

const controller = new ContinuousListController<RoutingLogEntry>({
  fetchPage: async (page) => {
    const window_ = buildRoutingLogWindow(rangeHours.value)
    const resp = await fetchRoutingLog({
      ...window_,
      kind: kind.value,
      result: result.value,
      limit: PAGE_SIZE,
      // ★ 这个端点是 offset 分页：page 1 ⇒ offset 0。
      //   照抄 cursor 端点会永远停在第 1 页（offset 恒 0）。
      offset: (page - 1) * PAGE_SIZE,
    })
    total.value = resp.total ?? 0
    return { items: resp.entries ?? [], total: resp.total ?? (resp.entries ?? []).length }
  },
  stableKey: (e) => `rl-${e.ts}-${e.kind}-${e.credential_id ?? 0}-${e.model}`,
  scopeKey: 'routing-log',
})

async function onKindChange(k: RoutingLogKind): Promise<void> {
  if (kind.value === k) return
  kind.value = k
  controller.loadFirst('requery')
}
async function onResultChange(r: RoutingLogResult): Promise<void> {
  if (result.value === r) return
  result.value = r
  controller.loadFirst('requery')
}
async function onRangeChange(h: number): Promise<void> {
  if (rangeHours.value === h) return
  rangeHours.value = h
  controller.loadFirst('requery')
}

function tone(e: RoutingLogEntry): 'success' | 'warning' | 'danger' | 'muted' {
  if (e.result === undefined) {
    if (e.change === 'broke' || e.change === 'offline') return 'danger'
    if (e.change === 'recovered' || e.change === 'online') return 'success'
    return 'muted'
  }
  return e.result === 'failed' ? 'danger' : 'success'
}

const CHANGE_KEY: Record<string, string> = {
  recovered: 'routingLog.changeRecovered',
  broke: 'routingLog.changeBroke',
  online: 'routingLog.changeOnline',
  offline: 'routingLog.changeOffline',
}

function badgeText(e: RoutingLogEntry): string {
  if (e.change) return t(CHANGE_KEY[e.change] ?? 'routingLog.kindState')
  return e.status
}

function openEntry(e: RoutingLogEntry): void {
  if (!e.request_id) return
  void router.push({ path: `/journey/${encodeURIComponent(e.request_id)}` })
}
</script>

<template>
  <div class="view-root rlog">
    <div class="rlog__row" role="tablist">
      <button
        v-for="k in ROUTING_LOG_KINDS"
        :key="k"
        type="button"
        class="rlog__chip"
        :class="{ 'rlog__chip--on': kind === k }"
        :aria-selected="kind === k"
        @click="onKindChange(k)"
      >
        {{ t(`routingLog.kind${k.charAt(0).toUpperCase()}${k.slice(1).replace('_', '')}`) }}
      </button>
    </div>

    <div class="rlog__row" role="tablist">
      <button
        v-for="r in ROUTING_LOG_RESULTS"
        :key="r"
        type="button"
        class="rlog__chip rlog__chip--sm"
        :class="{ 'rlog__chip--on': result === r }"
        :aria-selected="result === r"
        @click="onResultChange(r)"
      >
        {{ t(`routingLog.result${r.charAt(0).toUpperCase()}${r.slice(1)}`) }}
      </button>
    </div>

    <div class="rlog__row" role="tablist">
      <button
        v-for="o in availableRanges"
        :key="o.hours"
        type="button"
        class="rlog__chip rlog__chip--sm"
        :class="{ 'rlog__chip--on': rangeHours === o.hours }"
        :aria-selected="rangeHours === o.hours"
        @click="onRangeChange(o.hours)"
      >
        {{ t(o.key) }}
      </button>
    </div>

    <p class="rlog__summary">
      <span>{{ t('routingLog.total', { n: total }) }}</span>
    </p>
    <p class="rlog__hint">
      <AppIcon name="alert" :size="14" />
      <span>{{ t('routingLog.windowMaxed') }}</span>
    </p>

    <HyperList
      :controller="controller"
      :item-key="(e: RoutingLogEntry) => `rl-${e.ts}-${e.kind}-${e.credential_id ?? 0}-${e.model}`"
      :on-refresh="async () => { controller.loadFirst('requery') }"
      :empty-hint="t('routingLog.empty')"
    >
      <template #item="{ item: e }">
        <button
          type="button"
          class="data-card rlog-card"
          :class="{ 'rlog-card--link': !!e.request_id }"
          @click="openEntry(e)"
        >
          <div class="card-row">
            <span class="rlog-card__model">
              <StatusDot :tone="tone(e)" />
              {{ e.model || '—' }}
            </span>
            <span class="badge" :class="`badge--${tone(e) === 'muted' ? 'muted' : tone(e)}`">
              {{ badgeText(e) }}
            </span>
          </div>
          <div class="rlog-card__fields">
            <span class="rlog-card__field">{{ relativeTime(e.ts) }}</span>
            <span v-if="e.credential_label" class="rlog-card__field">{{ e.credential_label }}</span>
            <span v-if="e.provider_name" class="rlog-card__field">{{ e.provider_name }}</span>
            <span v-if="e.latency_ms != null" class="rlog-card__field">{{ t('routingLog.latency') }} {{ e.latency_ms }}ms</span>
            <span v-if="e.http_status != null" class="rlog-card__field">HTTP {{ e.http_status }}</span>
          </div>
          <p v-if="e.switch_reason" class="rlog-card__note">{{ e.switch_reason }}</p>
          <p v-if="e.error_code || e.error_message" class="rlog-card__err">
            <template v-if="e.error_code">{{ e.error_code }}</template>
            <template v-if="e.error_code && e.error_message"> · </template>
            <template v-if="e.error_message">{{ e.error_message }}</template>
          </p>
        </button>
      </template>
    </HyperList>
  </div>
</template>

<style scoped>
.rlog {
  padding: var(--app-space-3) var(--app-space-3) 0;
}
.rlog__row {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-bottom: var(--app-space-2);
}
.rlog__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.rlog__chip--sm {
  font-size: 13px;
  padding: 0 14px;
}
.rlog__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.rlog__summary {
  margin: var(--app-space-1) 0 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.rlog__hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.rlog-card {
  display: block;
  width: 100%;
  text-align: left;
}
.rlog-card--link {
  cursor: pointer;
}
.rlog-card__model {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rlog-card__fields {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 6px;
}
.rlog-card__field {
  font-size: 12px;
  color: var(--app-text-muted);
}
.rlog-card__note {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.rlog-card__err {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-danger);
  word-break: break-word;
}
</style>
