<script setup lang="ts">
// ProbeHealthView — 探测系统健康 + 队列快照（**admin 档**，`wrapAdmin`）。
//
// 数据源：
//   GET /api/admin/probe/system-health
//   GET /api/admin/probe/queue-snapshot
//
// 它补的是已上移的 /probe 答不了的那一问：
//   /probe（本页之前已上移）= **具体哪个任务/供应商**在跑、跑得快不快
//   本页                       = **整个探测系统现在健康吗、有没有卡住**
//
// ⚠️★★ 这两个端点是本仓库里**最容易读错**的形状。四个后端语义决定了这个
// 页面的全部结构（详见 api/probeHealth.ts 文件头）：
//
// (1) ★★★ **顶层字段全是 legacy 的。**
//     handler 把 `ProbeSystemHealth` marshal 后摊平到顶层
//     （probe_dashboard.go:958-972），新数据只挂在 `unified` 下，
//     且字段名完全不同（顶层 `total_nodes` vs `unified.total_credentials`）。
//     ⇒ 本页**只读 `unified`**。API 层也没导出顶层具名字段，
//       「读错源」在类型层面就做不到。
//
// (2) ★★★ **`queue-snapshot` 顶层 `queues` / `total` 也是 legacy 的**，
//     而那个 legacy 视图里躺着 **572 行历史积压**（handler 自己的注释：
//     probe_dashboard.go:764-767「the 572-row historical backlog lives in the
//     legacy view and is unaffected by the active queue」）。
//     ⇒ 拿 `total` 当「当前积压」会显示 572，而活动队列可能只有 25。
//     本页的「活动积压」只由 `unified` 算（`activeBacklogOf`），
//     legacy 明细单独放在**明确标注**的折叠区块里。
//
// (3) ★★ **system-health 的 legacy 查询软失败**（只 slog.Warn，响应仍 200，
//     legacy 保持零值），而 **queue-snapshot 的是硬失败**（直接 500）——
//     两条线不对称。且 handler 注释声称会「surface the error in the response」，
//     **代码没这么做**（grep `legacyErr` 只有 slog.Warn 一处）。
//     ⇒ `legacy.total_nodes === 0` 无法区分「真 0」与「没加载」，
//       客户端也不能替它编。本页把它渲染成「可能未加载」。
//
// (4) **`legacy_mode_safe: false` 是后端的显式警告**，表示 legacy 已不权威。
//     ⇒ 必须透出并据此给 legacy 区块打「历史遗留」标注，
//       绝不把 legacy 数字和 unified 数字并排放进同一个统计条。
//
// (5) **`success_rate_last_1h` 是 omitempty 的 `*float64`** ⇒ 字段缺失 =
//     近 1h 没有运行记录，不是 0% 成功率。三态，见 `successRate1hOf`。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchProbeSystemHealth,
  fetchProbeQueueSnapshot,
  legacySectionMayBeUnloaded,
  activeBacklogOf,
  leaseAnomaliesOf,
  hasLeaseAnomaly,
  successRate1hOf,
  type UnifiedProbeSystemHealth,
  type UnifiedProbeQueueStats,
  type TotalLegacySystemHealth,
  type LegacyQueueRow,
} from '@/api/probeHealth'

useHyperPage({ title: () => t('probeHealth.title') })

const health = ref<UnifiedProbeSystemHealth | null>(null)
const healthLegacy = ref<TotalLegacySystemHealth | null>(null)
const legacyModeSafe = ref<boolean | null>(null)
const queue = ref<UnifiedProbeQueueStats | null>(null)
const legacyRows = ref<LegacyQueueRow[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
/** legacy 明细默认收起 —— 它不是当前状况，不该和活动数据抢注意力。 */
const legacyOpen = ref(false)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  // ★ 两个端点**分别记错误**：system-health 挂了不代表 queue-snapshot 挂了。
  //   合并成一个 error 会把「一段挂了」显示成「整页都挂了」（§11.30 同款纪律）。
  //
  // ★★ 2026-10-07 修（doc 10 §4.6.57）：**慢也必须和挂分开处理**。
  //   原写法是 `await Promise.allSettled([a(), b()])` 之后再**统一**赋第 83-87 行，
  //   而 `allSettled` 要**两个都 settle** 才返回 ⇒ 任一端点慢，
  //   **连已经 200 的那一段的数据也要一起等**。
  //   生产实测 `/api/admin/probe/system-health` 要 15~35 秒
  //   （同类管理端点只要 0.25~0.9 秒），那段时间里 queue-snapshot 早已返回，
  //   页面却只有一句「正在加载…」，queue 的活动积压一个数都不显示。
  //   ⇒ 改成**各自的段各自的赋值**：谁先回来谁先渲染。
  //   注释里原来那句「system-health 挂了不代表 queue-snapshot 挂了」只覆盖了
  //   **快速 reject**，没覆盖**慢而不 settle** —— 两种形态对「另一段要不要等」
  //   的影响是一样的，所以都归到这一处修。
  const healthTask = fetchProbeSystemHealth().then(
    (v) => {
      health.value = v.unified ?? null
      healthLegacy.value = v.legacy ?? null
      legacyModeSafe.value = v.legacy_mode_safe ?? null
      return null
    },
    (e: unknown) => sectionName('health') + '：' + describeError(e),
  )
  const queueTask = fetchProbeQueueSnapshot().then(
    (v) => {
      queue.value = v.unified ?? null
      legacyRows.value = v.legacy?.queues ?? []
      return null
    },
    (e: unknown) => sectionName('queue') + '：' + describeError(e),
  )
  const settled = await Promise.all([healthTask, queueTask])
  const errs = settled.filter((e): e is string => e !== null)
  error.value = errs.length ? errs.join('　') : null
  loading.value = false
  loaded.value = true
}
void load()

function sectionName(k: 'health' | 'queue'): string {
  return t(k === 'health' ? 'probeHealth.sectionHealth' : 'probeHealth.sectionQueue')
}

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('probeHealth.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

// ── unified 派生 ──────────────────────────────────────────────────────────

/** ★ 活动积压只由 unified 算。见文件头 (2)。 */
const activeBacklog = computed(() => activeBacklogOf(queue.value))

const anomalies = computed(() => leaseAnomaliesOf({ queue: queue.value, health: health.value }))
const hasAnomaly = computed(() => hasLeaseAnomaly(anomalies.value))

/** ★ 三态。缺失 ≠ 0%。见文件头 (5)。 */
const successRate = computed(() => successRate1hOf(health.value))

/** URSM 覆盖率。分母 0 ⇒ null ⇒ 「—」，不是 0%。 */
const ursmCoverage = computed(() => {
  const h = health.value
  if (!h || !h.total_credentials) return null
  return (h.credentials_with_ursm ?? 0) / h.total_credentials
})

/** ★ legacy 区块「可能未加载」。见文件头 (3)。 */
const legacyMayBeUnloaded = computed(() => legacySectionMayBeUnloaded(healthLegacy.value))

function legacyTone(): 'success' | 'warning' | 'danger' | 'muted' {
  // ★ legacy_mode_safe 是后端给的权威位。false = 已不权威 ⇒ 警示色。
  if (legacyModeSafe.value === true) return 'success'
  if (legacyModeSafe.value === false) return 'warning'
  return 'muted'
}

const isEmpty = computed(() => loaded.value && !loading.value && !health.value && !queue.value)

onBeforeUnmount(() => {
  health.value = null
  queue.value = null
  legacyRows.value = []
})
</script>

<template>
  <div class="view-root ph">
    <p v-if="loading" class="ph__msg">{{ t('common.loading') }}</p>
    <p v-if="error" class="ph__msg ph__msg--err">{{ error }}</p>
    <p v-if="!loading && isEmpty" class="ph__msg">{{ t('probeHealth.empty') }}</p>

    <!-- ── 租约异常：三个数分开，因为处置动作完全不同 ────────────────── -->
    <section v-if="queue" class="ph__anom" :class="{ 'ph__anom--bad': hasAnomaly }">
      <div class="ph__anom-head">
        <StatusDot :tone="hasAnomaly ? 'danger' : 'success'" />
        <span class="ph__anom-title">{{ t('probeHealth.anomalyTitle') }}</span>
      </div>
      <dl class="ph__anom-grid">
        <div class="ph__anom-item">
          <dt>{{ t('probeHealth.nodeUnclaimable') }}</dt>
          <dd :class="{ 'ph__bad': anomalies.nodeUnclaimable > 0 }">{{ fmtInt(anomalies.nodeUnclaimable) }}</dd>
        </div>
        <div class="ph__anom-item">
          <dt>{{ t('probeHealth.staleLeases') }}</dt>
          <dd :class="{ 'ph__bad': anomalies.staleLeases > 0 }">{{ fmtInt(anomalies.staleLeases) }}</dd>
        </div>
        <div class="ph__anom-item">
          <dt>{{ t('probeHealth.queueExpired') }}</dt>
          <dd :class="{ 'ph__bad': anomalies.queueExpired > 0 }">{{ fmtInt(anomalies.queueExpired) }}</dd>
        </div>
      </dl>
    </section>

    <!-- ── 活动队列（unified）────────────────────────────────────────── -->
    <section v-if="queue" class="ph__card">
      <h2 class="ph__card-title">{{ t('probeHealth.sectionQueue') }}</h2>

      <div class="ph__hero">
        <span class="ph__hero-label">{{ t('probeHealth.activeBacklog') }}</span>
        <!-- ★ 只有 unified 存在时才有这个数；unified 缺失显示「—」而不是 0。 -->
        <span class="ph__hero-value">{{ activeBacklog === null ? t('common.unknown') : fmtInt(activeBacklog) }}</span>
      </div>
      <p class="ph__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('probeHealth.activeOnlyNote') }}</span>
      </p>

      <dl class="ph__grid">
        <div class="ph__cell">
          <dt>{{ t('probeHealth.queueReady') }}</dt>
          <dd>{{ fmtInt(queue.queue_ready) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.queueRunning') }}</dt>
          <dd>{{ fmtInt(queue.queue_running) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.queueClaims') }}</dt>
          <dd>{{ fmtInt(queue.queue_claims) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.nodePending') }}</dt>
          <dd>{{ fmtInt(queue.node_pending) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.nodeDue') }}</dt>
          <dd>{{ fmtInt(queue.node_due) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.nodePaused') }}</dt>
          <dd>{{ fmtInt(queue.node_paused) }}</dd>
        </div>
      </dl>
      <p v-if="queue.last_run_at" class="ph__meta">{{ t('probeHealth.lastRunAt', { t: relativeTime(queue.last_run_at) }) }}</p>
    </section>

    <!-- ── 系统健康（unified）───────────────────────────────────────── -->
    <section v-if="health" class="ph__card">
      <h2 class="ph__card-title">{{ t('probeHealth.sectionHealth') }}</h2>

      <div class="ph__hero">
        <span class="ph__hero-label">{{ t('probeHealth.ursmCoverage') }}</span>
        <!-- ★ 分母 0 ⇒ 「—」，不是 0% -->
        <span class="ph__hero-value">{{ ursmCoverage === null ? t('common.unknown') : (ursmCoverage * 100).toFixed(1) + '%' }}</span>
      </div>

      <dl class="ph__grid">
        <div class="ph__cell">
          <dt>{{ t('probeHealth.totalCredentials') }}</dt>
          <dd>{{ fmtInt(health.total_credentials) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.noUrsm') }}</dt>
          <dd :class="{ 'ph__bad': health.credentials_no_ursm > 0 }">{{ fmtInt(health.credentials_no_ursm) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.nodeTotal') }}</dt>
          <dd>{{ fmtInt(health.node_total) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.nodeFailing') }}</dt>
          <dd :class="{ 'ph__bad': health.node_failing > 0 }">{{ fmtInt(health.node_failing) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.runsLast1h') }}</dt>
          <dd>{{ fmtInt(health.runs_last_1h) }}</dd>
        </div>
        <div class="ph__cell">
          <dt>{{ t('probeHealth.runsFailed1h') }}</dt>
          <dd :class="{ 'ph__bad': health.runs_failed_1h > 0 }">{{ fmtInt(health.runs_failed_1h) }}</dd>
        </div>
      </dl>

      <!-- ★ 成功率三态：字段缺失 = 这一小时没有运行记录，不是 0% -->
      <p class="ph__rate">
        <span class="ph__rate-label">{{ t('probeHealth.successRate1h') }}</span>
        <span v-if="successRate.kind === 'no_runs'" class="ph__unknown">{{ t('probeHealth.noRuns1h') }}</span>
        <span v-else class="ph__rate-value">{{ (successRate.value * 100).toFixed(1) }}%</span>
      </p>

      <p v-if="health.pseudo_success_count > 0" class="ph__note ph__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('probeHealth.pseudoSuccess', { n: health.pseudo_success_count }) }}</span>
      </p>
      <p class="ph__meta">{{ t('probeHealth.window2hNote') }}</p>
    </section>

    <!-- ── legacy 区块：明确标注、默认收起、可能未加载 ─────────────────── -->
    <section v-if="healthLegacy || legacyRows.length" class="ph__card ph__card--legacy">
      <button type="button" class="ph__legacy-toggle" :aria-expanded="legacyOpen" @click="legacyOpen = !legacyOpen">
        <StatusDot :tone="legacyTone()" />
        <span class="ph__legacy-title">{{ t('probeHealth.legacyTitle') }}</span>
        <span class="badge" :class="legacyModeSafe === false ? 'badge--warning' : 'badge--muted'">
          {{ legacyModeSafe === false ? t('probeHealth.legacyNotAuthoritative') : t('probeHealth.legacyFlagUnknown') }}
        </span>
        <AppIcon name="chevron" :size="14" />
      </button>

      <!-- ★ legacy_mode_safe=false 时必须说清：这些数字不是当前状况。 -->
      <p v-if="legacyModeSafe === false" class="ph__note ph__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('probeHealth.legacyWarn') }}</span>
      </p>

      <template v-if="legacyOpen">
        <!-- ★ 全 0 无法区分「真 0」与「没加载」（见文件头 (3)）。 -->
        <p v-if="legacyMayBeUnloaded" class="ph__note ph__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('probeHealth.legacyMayBeUnloaded') }}</span>
        </p>
        <dl v-else-if="healthLegacy" class="ph__grid">
          <div class="ph__cell">
            <dt>{{ t('probeHealth.legacyTotalNodes') }}</dt>
            <dd>{{ fmtInt(healthLegacy.total_nodes) }}</dd>
          </div>
          <div class="ph__cell">
            <dt>{{ t('probeHealth.legacyHealthy') }}</dt>
            <dd>{{ fmtInt(healthLegacy.healthy_nodes) }}</dd>
          </div>
          <div class="ph__cell">
            <dt>{{ t('probeHealth.legacyFailing') }}</dt>
            <dd>{{ fmtInt(healthLegacy.failing_nodes) }}</dd>
          </div>
          <div class="ph__cell">
            <dt>{{ t('probeHealth.legacyUrgentQueue') }}</dt>
            <dd>{{ fmtInt(healthLegacy.urgent_queue_size) }}</dd>
          </div>
        </dl>

        <p v-if="healthLegacy" class="ph__meta">{{ t('probeHealth.legacySource', { s: healthLegacy.legacy_source }) }}</p>

        <template v-if="legacyRows.length">
          <p class="ph__meta">{{ t('probeHealth.legacyBacklogNote') }}</p>
          <ul class="ph__lrows">
            <li v-for="r in legacyRows" :key="r.probe_priority + '|' + r.state" class="ph__lrow">
              <span class="ph__lrow-p">{{ r.probe_priority }}</span>
              <span class="ph__lrow-s">{{ r.state }}</span>
              <span class="ph__lrow-n">{{ fmtInt(r.queue_size) }}</span>
            </li>
          </ul>
        </template>
      </template>
    </section>
  </div>
</template>

<style scoped>
.ph {
  padding: var(--app-space-3);
}
.ph__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.ph__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ph__card {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
/* ★ legacy 区块的视觉必须和「当前状况」分得开。 */
.ph__card--legacy {
  background: var(--app-surface-muted);
  border-style: dashed;
}
.ph__card-title {
  margin: 0 0 var(--app-space-2);
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.ph__anom {
  border: 1px solid var(--app-border);
  border-left: 3px solid var(--app-success);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ph__anom--bad {
  border-left-color: var(--app-danger);
}
.ph__anom-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ph__anom-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.ph__anom-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--app-space-2);
  margin: var(--app-space-2) 0 0;
}
.ph__anom-item {
  min-width: 0;
}
.ph__anom-item dt {
  font-size: 11px;
  color: var(--app-text-muted);
  line-height: 1.3;
}
.ph__anom-item dd {
  margin: 2px 0 0;
  font-size: 17px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ph__bad {
  color: var(--app-danger) !important;
}
.ph__hero {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: var(--app-space-2) 0;
  border-bottom: 1px solid var(--app-border);
  margin-bottom: var(--app-space-2);
}
.ph__hero-label {
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ph__hero-value {
  font-size: 24px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ph__grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--app-space-2);
  margin: 0;
}
.ph__cell {
  min-width: 0;
}
.ph__cell dt {
  font-size: 11px;
  color: var(--app-text-muted);
  line-height: 1.3;
}
.ph__cell dd {
  margin: 2px 0 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ph__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ph__note--warn {
  color: var(--app-warning);
}
.ph__meta {
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ph__rate {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
  margin: var(--app-space-2) 0 0;
  padding-top: var(--app-space-2);
  border-top: 1px dashed var(--app-border);
}
.ph__rate-label {
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ph__rate-value {
  font-size: 15px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
/* ★ 「没有运行记录」必须和「0% 成功率」视觉上分得开。 */
.ph__unknown {
  font-size: 13px;
  color: var(--app-text-muted);
}
.ph__legacy-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-height: 48px;
  padding: 0;
  border: 0;
  background: transparent;
  text-align: left;
}
.ph__legacy-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  min-width: 0;
}
</style>
