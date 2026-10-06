<script setup lang="ts">
// RequestJourneyView — 「现在有哪些请求在飞 / 卡在哪一站」。
//
// 数据源：GET /api/admin/request-journeys/queues（AdminMiddleware，tenant_admin 可用）
//
// ★ 与 requestLogs 的分工：requestLogs 是**已结束请求的结果面**，
//   本页是**在途请求的过程面**。两者都不提供「为什么失败」——
//   那个答案在 /journey/:id 的事件流里。
//
// ⚠️ 本端点**无分页**（有界 FIFO ring，靠 producer 的 cap 截断）。
//   所以 fetchPage 恒返回第 1 页并把 total 设成 items.length，
//   让 controller 直接进 exhausted（continuousList.ts:224 的判据）。
//   若照搬带分页的端点写法、total 留空，controller 会认为永远还有下一页，
//   滚到底反复请求同一个全量响应。

import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { ContinuousListController } from '@/hyper'
import HyperList from '@/components/common/HyperList.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchJourneyQueues,
  isDegraded,
  isIngress,
  JOURNEY_VIEWS,
  JOURNEY_LIFECYCLE_STATES,
  type JourneyView,
  type JourneyLifecycleState,
  type RequestSnapshot,
  type IngressSnapshot,
} from '@/api/requestJourney'

useHyperPage({ title: () => t('journey.title') })
const router = useRouter()

type Row = RequestSnapshot | IngressSnapshot

const view = ref<JourneyView>('total')
const lifecycle = ref<JourneyLifecycleState | ''>('')
const observationStatus = ref<string>('healthy')
const observationScope = ref<string>('')
const counts = ref<{ pending?: number; in_flight?: number; completed?: number }>({})

const controller = new ContinuousListController<Row>({
  fetchPage: async (_page) => {
    const resp = await fetchJourneyQueues({
      view: view.value,
      ...(lifecycle.value ? { lifecycle_state: lifecycle.value } : {}),
    })
    // ★ 降级信号必须在**这里**就存下来：它是这个端点唯一的「我可能没看全」提示。
    //   只在模板里判 resp 的话，列表一滚动就看不到降级横幅了。
    observationStatus.value = resp.observation_status ?? 'healthy'
    observationScope.value = resp.observation_scope ?? ''
    counts.value = resp.total_snapshot ?? {}
    const items = resp.total_snapshot?.requests ?? []
    // total = items.length ⇒ controller 判定已到底（见文件头注释）
    return { items, total: items.length }
  },
  stableKey: (r) => `jr-${r.request_id}`,
  scopeKey: 'request-journey-queues',
})

const degraded = computed(() => isDegraded({ observation_status: observationStatus.value }))
const scopeIsLocal = computed(() => observationScope.value === 'instance_local')

async function onViewChange(v: JourneyView): Promise<void> {
  if (view.value === v) return
  view.value = v
  controller.loadFirst('requery')
}
async function onLifecycleChange(s: JourneyLifecycleState | ''): Promise<void> {
  if (lifecycle.value === s) return
  lifecycle.value = s
  controller.loadFirst('requery')
}

function tone(r: Row): 'success' | 'warning' | 'danger' | 'muted' {
  if (r.error_kind || (isIngress(r) ? r.status === 'failed' : r.outcome === 'failure')) return 'danger'
  if (isIngress(r) ? r.status === 'in_flight' : r.lifecycle_state === 'in_flight') return 'warning'
  return 'muted'
}

function stageText(r: Row): string {
  if (isIngress(r)) return r.status
  return r.current_stage
}

function modelText(r: Row): string {
  if (isIngress(r)) return '—'
  return r.resolved_model || r.requested_model || '—'
}

function providerText(r: Row): string {
  return isIngress(r) ? r.protocol : ''
}

function httpText(r: Row): string | null {
  const s = r.http_status
  return typeof s === 'number' ? String(s) : null
}

function errorText(r: Row): string | null {
  return r.error_kind ?? null
}

function open(r: Row): void {
  void router.push({ path: `/journey/${encodeURIComponent(r.request_id)}` })
}
</script>

<template>
  <div class="view-root journey">
    <div class="journey__row" role="tablist">
      <button
        v-for="v in JOURNEY_VIEWS"
        :key="v"
        type="button"
        class="journey__chip"
        :class="{ 'journey__chip--on': view === v }"
        :aria-selected="view === v"
        @click="onViewChange(v)"
      >
        {{ t(`journey.view${v.charAt(0).toUpperCase()}${v.slice(1)}`) }}
      </button>
    </div>

    <div class="journey__row" role="tablist">
      <button
        type="button"
        class="journey__chip journey__chip--sm"
        :class="{ 'journey__chip--on': lifecycle === '' }"
        :aria-selected="lifecycle === ''"
        @click="onLifecycleChange('')"
      >
        {{ t('logs.statusAll') }}
      </button>
      <button
        v-for="s in JOURNEY_LIFECYCLE_STATES"
        :key="s"
        type="button"
        class="journey__chip journey__chip--sm"
        :class="{ 'journey__chip--on': lifecycle === s }"
        :aria-selected="lifecycle === s"
        @click="onLifecycleChange(s)"
      >
        {{ t(`journey.${s === 'in_flight' ? 'inFlight' : s}`) }}
      </button>
    </div>

    <!-- ★ 降级横幅：与「空列表」是两件事，必须显式说明 -->
    <p v-if="degraded" class="journey__banner journey__banner--warn">
      <AppIcon name="alert" :size="16" />
      <span>{{ t('journey.degraded') }}</span>
    </p>
    <p v-else-if="scopeIsLocal" class="journey__banner">
      <AppIcon name="globe" :size="16" />
      <span>{{ t('journey.scopeLocal') }}</span>
    </p>
    <p v-else-if="observationScope === 'shared_redis'" class="journey__banner journey__banner--ok">
      <AppIcon name="check" :size="16" />
      <span>{{ t('journey.scopeShared') }}</span>
    </p>

    <p class="journey__counts">
      {{ t('journey.counts', {
        pending: counts.pending ?? 0,
        inFlight: counts.in_flight ?? 0,
        completed: counts.completed ?? 0,
      }) }}
    </p>

    <HyperList
      :controller="controller"
      :item-key="(r: Row) => `jr-${r.request_id}`"
      :on-refresh="async () => { controller.loadFirst('requery') }"
      :empty-hint="t('journey.empty')"
    >
      <template #item="{ item: r }">
        <button type="button" class="data-card journey-card" @click="open(r)">
          <div class="card-row">
            <span class="journey-card__model">
              <StatusDot :tone="tone(r)" />
              {{ modelText(r) }}
            </span>
            <span class="badge" :class="`badge--${tone(r) === 'muted' ? 'muted' : tone(r)}`">
              {{ stageText(r) }}
            </span>
          </div>
          <div class="journey-card__fields">
            <span class="journey-card__field">{{ relativeTime(r.updated_at) }}</span>
            <span v-if="providerText(r)" class="journey-card__field">{{ providerText(r) }}</span>
            <span v-if="httpText(r)" class="journey-card__field">HTTP {{ httpText(r) }}</span>
          </div>
          <div v-if="errorText(r)" class="journey-card__err">{{ errorText(r) }}</div>
        </button>
      </template>
    </HyperList>
  </div>
</template>

<style scoped>
.journey {
  padding: var(--app-space-3) var(--app-space-3) 0;
}
.journey__row {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-bottom: var(--app-space-2);
}
.journey__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.journey__chip--sm {
  font-size: 13px;
  padding: 0 14px;
}
.journey__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.journey__banner {
  display: flex;
  align-items: flex-start;
  gap: var(--app-space-2);
  margin: var(--app-space-2) 0;
  padding: var(--app-space-2) var(--app-space-3);
  border-radius: var(--app-radius);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 13px;
  line-height: 1.5;
}
.journey__banner--warn {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.journey__banner--ok {
  background: var(--app-success-soft);
  color: var(--app-success);
}
.journey__counts {
  margin: var(--app-space-1) 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
}
.journey-card {
  display: block;
  width: 100%;
  text-align: left;
}
.journey-card__model {
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
.journey-card__fields {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 6px;
}
.journey-card__field {
  font-size: 12px;
  color: var(--app-text-muted);
}
.journey-card__err {
  margin-top: 6px;
  font-size: 12px;
  color: var(--app-danger);
  word-break: break-all;
}
</style>
