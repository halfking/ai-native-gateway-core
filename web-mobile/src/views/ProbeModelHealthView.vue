<script setup lang="ts">
// ProbeModelHealthView — 模型级健康总览（/api/admin/probe/dashboard，**admin 档**）。
//
// 它与已上移的几页构成「从粗到细」的完整粒度：
//   本页     = **模型**级：这个模型整体健康吗
//   /heatmap = 模型 × 凭据：具体哪个组合在坏
//   /probe    = **任务**级：谁在跑、供应商直连多快
//   /node-tasks = 节点探测队列：哪些凭据在被探测、排到第几次
//
// ⚠️★★★ 四个后端语义（详见 api/probeModelHealth.ts 文件头）：
//
// (1) ★★★ **后端把 SQL NULL 压成了 0，客户端拿不到区分依据。**
//     `nullFloat64` / `nullInt`（probe_dashboard.go:2336-2348）对 NULL 返回 0，
//     而 `ModelHealthSummary` 的 `healthy_percentage` / `avg_success_rate_7d` /
//     `total_real_success_24h` 都是**普通 `float64` / `int`**（无指针无 omitempty）。
//     ⇒ 「0% 健康」与「没有数据」在响应里同形。
//     ★ 唯一可推导的判据：`total_credentials === 0` ——
//       「0 个里的 0%」在数学上不成立，那个 0 必然是 NULL 被压平的产物。
//     ⇒ `total_credentials === 0` 时所有派生统计显示「无数据」；
//       `> 0` 时 0 仍可能是真值（也可能不是），文案留余地。
//
// (2) ★ `real_success_rate_24h` 是 `*float64` + omitempty
//     ⇒ 字段缺失 = 近 24h 没有真实请求，不是 0% 成功率。
//
// (3) ★ `model` 过滤是 **ILIKE 子串匹配**（:675-679），
//     传 `gpt-4` 会同时命中 `gpt-4o` / `gpt-4o-mini` / `gpt-4-turbo`。
//     ⇒ 搜索框旁必须说明「按子串匹配」，否则用户会以为筛的是精确名。
//
// (4) **`overall_health` 是后端给的权威判定**，配色词表外一律 muted
//     —— 给看不懂的状态打绿色等于把「不知道」显示成「健康」。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchProbeDashboard,
  derivedStatsAbsent,
  realSuccessRate24hOf,
  realRequests24hOf,
  healthTone,
  healthKeyOf,
  type ModelHealthSummary,
} from '@/api/probeModelHealth'

useHyperPage({ title: () => t('probeModel.title') })

const model = ref('')
const rows = ref<ModelHealthSummary[]>([])
const loading = ref(false)
const searched = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const resp = await fetchProbeDashboard(model.value.trim() ? { model: model.value.trim() } : {})
    rows.value = resp.models ?? []
  } catch (err) {
    rows.value = []
    error.value = describeError(err)
  } finally {
    loading.value = false
    searched.value = true
  }
}
void load()

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('probeModel.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

function onSearch(): void {
  void load()
}

function onClear(): void {
  model.value = ''
  void load()
}

/** ★ 派生统计是否「没数据」。见文件头 (1)。 */
function statsAbsent(m: ModelHealthSummary): boolean {
  return derivedStatsAbsent(m)
}

/** ★ 24h 真实请求三态。 */
const realRate = (m: ModelHealthSummary) => realSuccessRate24hOf(m)
const realCount = (m: ModelHealthSummary) => realRequests24hOf(m)

/** 状态分布之和，用来发现「分母与明细对不上」的可疑行。 */
function distributionSum(m: ModelHealthSummary): number {
  return m.healthy_count + m.suspicious_count + m.failing_count + m.probing_count
}

/**
 * ★ 分母为 0 或明细对不上时给警告。
 * 「分母 0 但明细有值」说明后端给的数自相矛盾 —— 这种行不该被当成正常展示。
 */
function distributionMismatch(m: ModelHealthSummary): boolean {
  if (m.total_credentials === 0) return distributionSum(m) > 0
  return distributionSum(m) > m.total_credentials
}

const isEmpty = computed(() => searched.value && !loading.value && rows.value.length === 0 && !error.value)

onBeforeUnmount(() => {
  rows.value = []
})
</script>

<template>
  <div class="view-root pm">
    <form class="pm__form" @submit.prevent="onSearch">
      <label class="pm__field">
        <span>{{ t('probeModel.searchLabel') }}</span>
        <input
          v-model="model"
          class="pm__input"
          :placeholder="t('probeModel.searchPlaceholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
        />
      </label>
      <div class="pm__actions">
        <button type="submit" class="pm__btn pm__btn--go">{{ t('probeModel.search') }}</button>
        <button v-if="model" type="button" class="pm__btn" @click="onClear">{{ t('common.clearFilters') }}</button>
      </div>
    </form>

    <!-- ★ 后端是 ILIKE 子串匹配：必须说出来，否则用户以为筛的是精确名。 -->
    <p v-if="model" class="pm__note">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('probeModel.substringNote', { q: model }) }}</span>
    </p>

    <p v-if="error" class="pm__msg pm__msg--err">{{ error }}</p>
    <p v-if="loading" class="pm__msg">{{ t('common.loading') }}</p>
    <p v-if="isEmpty" class="pm__msg">{{ t('probeModel.empty') }}</p>
    <p v-else-if="!loading && rows.length" class="pm__count">{{ t('probeModel.count', { n: rows.length }) }}</p>

    <ul v-if="rows.length" class="pm__list">
      <li v-for="m in rows" :key="m.provider_model_id" class="pm__item">
        <div class="pm__item-head">
          <StatusDot :tone="healthTone(m.overall_health)" />
          <span class="pm__item-name">{{ m.raw_model_name }}</span>
          <span class="badge" :class="'badge--' + (healthTone(m.overall_health) === 'muted' ? 'muted' : healthTone(m.overall_health))">
            {{ t(healthKeyOf(m.overall_health)) }}
          </span>
        </div>
        <p class="pm__item-sub">
          {{ m.provider_name }}
          <span v-if="m.outbound_model_name && m.outbound_model_name !== m.raw_model_name" class="pm__item-alias">
            → {{ m.outbound_model_name }}
          </span>
          <span v-if="m.protocol" class="pm__item-proto">{{ m.protocol }}</span>
        </p>

        <!-- ★ 明细与分母对不上 ⇒ 后端数据自相矛盾，必须标出来 -->
        <p v-if="distributionMismatch(m)" class="pm__note pm__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('probeModel.distMismatch', { sum: distributionSum(m), total: m.total_credentials }) }}</span>
        </p>

        <div class="pm__dist">
          <div class="pm__dist-item">
            <span class="pm__dot pm__dot--ok" />
            <span class="pm__dist-n">{{ fmtInt(m.healthy_count) }}</span>
            <span class="pm__dist-l">{{ t('probeModel.healthy') }}</span>
          </div>
          <div class="pm__dist-item">
            <span class="pm__dot pm__dot--warn" />
            <span class="pm__dist-n">{{ fmtInt(m.suspicious_count) }}</span>
            <span class="pm__dist-l">{{ t('probeModel.suspicious') }}</span>
          </div>
          <div class="pm__dist-item">
            <span class="pm__dot pm__dot--bad" />
            <span class="pm__dist-n">{{ fmtInt(m.failing_count) }}</span>
            <span class="pm__dist-l">{{ t('probeModel.failing') }}</span>
          </div>
          <div class="pm__dist-item">
            <span class="pm__dot pm__dot--info" />
            <span class="pm__dist-n">{{ fmtInt(m.probing_count) }}</span>
            <span class="pm__dist-l">{{ t('probeModel.probing') }}</span>
          </div>
        </div>

        <dl class="pm__kv">
          <div class="pm__kv-row">
            <dt>{{ t('probeModel.healthyPct') }}</dt>
            <!-- ★ 分母 0 ⇒ 「无数据」而不是 0% -->
            <dd :class="{ 'pm__unknown': statsAbsent(m) }">
              {{ statsAbsent(m) ? t('probeModel.noData') : m.healthy_percentage.toFixed(1) + '%' }}
            </dd>
          </div>
          <div class="pm__kv-row">
            <dt>{{ t('probeModel.avgSuccess7d') }}</dt>
            <dd :class="{ 'pm__unknown': statsAbsent(m) }">
              {{ statsAbsent(m) ? t('probeModel.noData') : (m.avg_success_rate_7d * 100).toFixed(1) + '%' }}
            </dd>
          </div>
          <div class="pm__kv-row">
            <dt>{{ t('probeModel.realRate24h') }}</dt>
            <!-- ★ 三态：字段缺失 = 没有真实请求，不是 0% -->
            <dd v-if="realRate(m).kind === 'no_requests'" class="pm__unknown">{{ t('probeModel.noRequests24h') }}</dd>
            <!-- ★ real_success_rate_24h 是 0..1 的**比例**，必须 ×100。
                 漏乘会把 98.4% 渲染成「1.0%」—— 本条判据当场抓出来的。 -->
            <dd v-else>{{ ((realRate(m) as { value: number }).value * 100).toFixed(1) }}%</dd>
          </div>
          <div class="pm__kv-row">
            <dt>{{ t('probeModel.realCount24h') }}</dt>
            <!-- ★ 总量 0 有歧义（NULL→0）⇒ null 显示「—」 -->
            <dd :class="{ 'pm__unknown': realCount(m) === null }">
              {{ realCount(m) === null ? t('probeModel.noData') : fmtInt(realCount(m) as number) }}
            </dd>
          </div>
        </dl>

        <p v-if="m.critical_nodes > 0" class="pm__note pm__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('probeModel.criticalNodes', { n: m.critical_nodes }) }}</span>
        </p>
        <p v-if="m.last_verified_at" class="pm__meta">
          {{ t('probeModel.lastVerified', { t: relativeTime(m.last_verified_at) }) }}
        </p>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.pm {
  padding: var(--app-space-3);
}
.pm__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.pm__field {
  display: block;
}
.pm__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.pm__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.pm__actions {
  display: flex;
  gap: var(--app-space-2);
}
.pm__btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.pm__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.pm__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.pm__note--warn {
  color: var(--app-warning);
}
.pm__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.pm__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.pm__count {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 12px;
}
.pm__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.pm__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.pm__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.pm__item-name {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-word;
  min-width: 0;
}
.pm__item-sub {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  word-break: break-word;
}
.pm__item-alias {
  margin-left: 4px;
  color: var(--app-text-muted);
}
.pm__item-proto {
  margin-left: 4px;
  padding: 0 5px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 10px;
}
.pm__dist {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin: var(--app-space-3) 0 0;
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
  border-bottom: 1px solid var(--app-border);
}
.pm__dist-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.pm__dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  align-self: center;
}
.pm__dot--ok {
  background: var(--app-success);
}
.pm__dot--warn {
  background: var(--app-warning);
}
.pm__dot--bad {
  background: var(--app-danger);
}
.pm__dot--info {
  background: var(--app-primary);
}
.pm__dist-n {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.pm__dist-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.pm__kv {
  margin: var(--app-space-2) 0 0;
  /* ★ 两列网格：让每个格子有 48px 的高度预算（R1），而不把卡片撑到 4×48px。
     触控门只认「声明的 min-height」，所以这里不用 R1-legacy 豁免 ——
     那是给**存量**用的，拿它盖新代码就是撒谎。 */
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 var(--app-space-3);
}
.pm__kv-row {
  display: flex;
  gap: 6px;
  min-height: 48px;
  align-items: center;
  min-width: 0;
}
.pm__kv-row dt {
  font-size: 11px;
  color: var(--app-text-muted);
  flex-shrink: 0;
}
.pm__kv-row dd {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
  min-width: 0;
}
/* ★ 「无数据」必须与真实数字视觉上分得开。 */
.pm__unknown {
  color: var(--app-text-muted) !important;
  font-weight: 500 !important;
}
.pm__meta {
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
}
</style>
