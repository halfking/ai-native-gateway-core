<script setup lang="ts">
// CredentialHeatmapView — 凭据 × 模型 × 时间的热力图（/heatmap）。
//
// 数据源：GET /api/credentials/heatmap（h.admin，tenant_admin 可用）
//
// 它补的是 NodesView / ProvidersView 都答不了的问题：
//   NodesView     「哪条路挂了」
//   ProvidersView 「哪个供应商挂了」
//   本页          「**哪个模型 × 哪个凭据的组合**在坏」
// 三者经常指向不同结论 —— 例如某模型整体 40% 失败，但分散在 3 条凭据上
// 而不是集中在 1 条，那问题就不是某条路坏了，而是这个模型的路由面太窄。
//
// ⚠️ 本端点的 4 个 400 约束全部在**发出前**处理（见 api/credentialHeatmap.ts 头）：
//   time_start/time_end 必填 · 窗口 ≤7d · 粒度 ∈ {1m,5m,15m,1h,1d} · 桶数 ≤5000
//   其中「桶数 ≤5000」最容易被忽略：7d 窗口下 1m 会得到 10080 桶 ⇒ 必然 400。
//   ⇒ 粒度选项按**当前窗口**过滤，非法组合根本不渲染。

import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import {
  fetchCredentialHeatmap,
  buildHeatmapWindow,
  allowedGranularities,
  finestAllowedGranularity,
  nodeStateTone,
  bucketRate,
  HEATMAP_MAX_WINDOW_HOURS,
  type HeatmapResponse,
  type HeatmapCredential,
  type HeatmapModel,
  type HeatmapBucket,
  type HeatmapGranularity,
} from '@/api/credentialHeatmap'

useHyperPage({ title: () => t('heatmap.title') })
const router = useRouter()

interface Range {
  key: string
  hours: number
}
const RANGES: Range[] = [
  { key: 'heatmap.range1h', hours: 1 },
  { key: 'heatmap.range6h', hours: 6 },
  { key: 'heatmap.range24h', hours: 24 },
  { key: 'heatmap.range3d', hours: 72 },
  { key: 'heatmap.range7d', hours: HEATMAP_MAX_WINDOW_HOURS },
].filter((r) => r.hours <= HEATMAP_MAX_WINDOW_HOURS)

const rangeKey = ref('heatmap.range24h')
const granularity = ref<HeatmapGranularity>('15m')
const creds = ref<HeatmapCredential[]>([])
const meta = ref<HeatmapResponse['meta'] | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const hours = computed(() => RANGES.find((r) => r.key === rangeKey.value)?.hours ?? 24)

/**
 * ★ 粒度选项随窗口收窄。
 *
 * 后端按「窗口 ÷ 粒度」算桶数，> 5000 直接 400。
 * 24h 窗口下 1m = 1440 桶 ✓；7d 窗口下 1m = 10080 桶 ✗。
 * 若只按窗口长短给固定粒度，用户在 7d 上选 1m 就会吃一个 400。
 */
const granularityOptions = computed<HeatmapGranularity[]>(() => allowedGranularities(hours.value))

// 当前粒度在新窗口下非法时（比如从 1h 的 1m 切到 7d），落到最细的合法值
watch(granularityOptions, (opts) => {
  if (opts.length === 0) return
  if (!opts.includes(granularity.value)) granularity.value = finestAllowedGranularity(hours.value)
})

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const w = buildHeatmapWindow(hours.value)
    const resp = await fetchCredentialHeatmap({
      time_start: w.time_start,
      time_end: w.time_end,
      granularity: granularity.value,
    })
    creds.value = resp.credentials ?? []
    meta.value = resp.meta ?? null
  } catch (err) {
    creds.value = []
    meta.value = null
    error.value = (err as Error)?.message ?? null
  } finally {
    loading.value = false
  }
}

function onRangeChange(key: string): void {
  if (rangeKey.value === key) return
  rangeKey.value = key
  void load()
}
function onGranularityChange(g: HeatmapGranularity): void {
  if (granularity.value === g) return
  granularity.value = g
  void load()
}
void load()

function modelsOf(c: HeatmapCredential): HeatmapModel[] {
  return c.models ?? []
}

function bucketsOf(m: HeatmapModel): HeatmapBucket[] {
  return m.buckets ?? []
}

function stateLabel(m: HeatmapModel): string | null {
  const s = m.node_status?.state
  if (!s) return null
  return t(`heatmap.state_${s}`)
}

/** 桶的配色。无样本与「全失败」必须可区分。 */
function bucketTone(b: HeatmapBucket): 'success' | 'warning' | 'danger' | 'muted' {
  const r = bucketRate(b)
  if (r == null) return 'muted'
  if (r >= 0.99) return 'success'
  if (r >= 0.9) return 'warning'
  return 'danger'
}

function rateText(b: HeatmapBucket): string {
  const r = bucketRate(b)
  if (r == null) return t('heatmap.rateUnknown')
  return `${Math.round(r * 100)}%`
}

function totalRequests(m: HeatmapModel): number {
  return bucketsOf(m).reduce((acc, b) => acc + (b.total_requests ?? 0), 0)
}

/** ★ 无样本的模型不显示成「0%」——它只是没有流量，不是全部失败。 */
function modelHasSamples(m: HeatmapModel): boolean {
  return totalRequests(m) > 0
}

function openTimeline(credentialId: number): void {
  void router.push({ path: `/node-health/${credentialId}` })
}
</script>

<template>
  <div class="view-root hm">
    <div class="hm__row" role="tablist">
      <button
        v-for="r in RANGES"
        :key="r.key"
        type="button"
        class="hm__chip"
        :class="{ 'hm__chip--on': rangeKey === r.key }"
        :aria-selected="rangeKey === r.key"
        @click="onRangeChange(r.key)"
      >
        {{ t(r.key) }}
      </button>
    </div>

    <!-- ★ 粒度按窗口过滤后可能只剩一两个；此时说明原因而不是只留一个孤零零的 chip -->
    <div v-if="granularityOptions.length > 0" class="hm__row" role="tablist">
      <button
        v-for="g in granularityOptions"
        :key="g"
        type="button"
        class="hm__chip hm__chip--sm"
        :class="{ 'hm__chip--on': granularity === g }"
        :aria-selected="granularity === g"
        @click="onGranularityChange(g)"
      >
        {{ g }}
      </button>
    </div>

    <p class="hm__hint">
      <AppIcon name="alert" :size="14" />
      <span>{{ t('heatmap.windowMaxed') }}</span>
    </p>

    <p v-if="meta" class="hm__meta">
      <span>{{ t('heatmap.buckets', { n: meta.bucket_count }) }}</span>
    </p>

    <p v-if="loading" class="hm__msg">{{ t('common.loading') }}</p>
    <p v-else-if="error" class="hm__msg hm__msg--err">{{ error }}</p>
    <p v-else-if="creds.length === 0" class="hm__msg">{{ t('heatmap.empty') }}</p>

    <div v-for="c in creds" :key="c.credential_id" class="hm__cred">
      <div class="data-card hm__cred-head">
        <div class="card-row">
          <span class="hm__cred-label">{{ c.label || c.credential_id }}</span>
          <span class="badge badge--muted">{{ t('heatmap.modelCount', { n: modelsOf(c).length }) }}</span>
        </div>
        <p v-if="c.provider_name" class="hm__cred-provider">{{ c.provider_name }}</p>
        <button type="button" class="hm__timeline-link" @click="openTimeline(c.credential_id)">
          {{ t('heatmap.openTimeline') }}
        </button>
      </div>

      <div v-for="m in modelsOf(c)" :key="m.raw_model_name" class="hm__model">
        <div class="hm__model-head">
          <span class="hm__model-name">{{ m.raw_model_name }}</span>
          <template v-if="m.node_status">
            <StatusDot :tone="nodeStateTone(m.node_status.state)" />
            <span class="hm__model-state">{{ stateLabel(m) }}</span>
            <span v-if="m.node_status.paused" class="badge badge--warning">{{ t('heatmap.paused') }}</span>
            <span v-if="!m.node_status.routable" class="badge badge--danger">{{ t('heatmap.notRoutable') }}</span>
          </template>
        </div>

        <!-- ★ 无样本时明说「无样本」，不画一排灰条冒充「这段时间没流量」 -->
        <p v-if="!modelHasSamples(m)" class="hm__model-empty">{{ t('heatmap.noBuckets') }}</p>
        <template v-else>
          <div class="hm__strip" role="img" :aria-label="t('heatmap.requestCount', { n: totalRequests(m) })">
            <span
              v-for="(b, bi) in bucketsOf(m)"
              :key="bi"
              class="hm__cell"
              :class="`hm__cell--${bucketTone(b)}`"
              :title="`${b.time_bucket} ${rateText(b)}`"
            />
          </div>
          <p class="hm__model-meta">
            <span>{{ t('heatmap.requestCount', { n: totalRequests(m) }) }}</span>
            <span v-if="m.node_status && m.node_status.consecutive_failures > 0" class="hm__model-fails">
              {{ t('heatmap.consecFails', { n: m.node_status.consecutive_failures }) }}
            </span>
            <span v-if="m.node_status?.last_err_code" class="hm__model-err">
              {{ t('heatmap.lastErr', { c: m.node_status.last_err_code }) }}
            </span>
          </p>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.hm {
  padding: var(--app-space-3) var(--app-space-3) 0;
}
.hm__row {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-bottom: var(--app-space-2);
}
.hm__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.hm__chip--sm {
  font-size: 13px;
  padding: 0 14px;
  font-variant-numeric: tabular-nums;
}
.hm__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.hm__hint {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
  line-height: 1.5;
}
.hm__meta {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
}
.hm__msg {
  padding: var(--app-space-4) 0;
  color: var(--app-text-muted);
  font-size: 14px;
  text-align: center;
}
.hm__msg--err {
  color: var(--app-danger);
}
.hm__cred {
  margin-bottom: var(--app-space-3);
}
.hm__cred-head {
  padding: var(--app-space-3);
}
.hm__cred-label {
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.hm__cred-provider {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-muted);
}
.hm__timeline-link {
  min-height: 48px;
  margin-top: var(--app-space-2);
  padding: 0 12px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-primary);
  font-size: 13px;
}
.hm__model {
  margin-top: var(--app-space-2);
  padding-left: var(--app-space-3);
}
.hm__model-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  min-height: 48px;
}
.hm__model-name {
  font-size: 14px;
  color: var(--app-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}
.hm__model-state {
  font-size: 12px;
  color: var(--app-text-secondary);
}
.hm__model-empty {
  margin: 0 0 var(--app-space-2);
  font-size: 12px;
  color: var(--app-text-muted);
  font-style: italic;
}
.hm__strip {
  display: flex;
  gap: 1px;
  height: 22px;
  margin: var(--app-space-1) 0;
  overflow: hidden;
  border-radius: 3px;
}
.hm__cell {
  flex: 1 1 0;
  min-width: 1px;
}
.hm__cell--success {
  background: var(--app-success);
}
.hm__cell--warning {
  background: var(--app-warning);
}
.hm__cell--danger {
  background: var(--app-danger);
}
.hm__cell--muted {
  background: var(--app-border);
}
.hm__model-meta {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin: 0 0 var(--app-space-2);
  font-size: 12px;
  color: var(--app-text-muted);
}
.hm__model-fails {
  color: var(--app-danger);
}
.hm__model-err {
  color: var(--app-text-secondary);
}
</style>
