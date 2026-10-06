<script setup lang="ts">
// RouteFunnelView — 单个模型的请求漏斗（/auto-route/analytics/funnel，**superAdmin 档**）。
//
// 数据源：GET /api/admin/auto-route/analytics/funnel?model=&window=
//
// 它补的是已上移的 /overrides 与 /routing-audit 都答不了的那一问：
//   /overrides   = 现在生效的**规则**是什么
//   /routing-audit = 这些规则**谁在什么时候**改的
//   本页         = 请求进来到执行成功，**中间被筛掉了多少**、这数**可不可信**
//
// ⚠️★ 三个后端语义决定了 UI 的做法：
//
// (1) ★★ **`meta.approximate` 是权威位，必须透出。**
//     analytics.go 的三档口径：
//       exact       ← routing_decision_log 的 decision_trace 聚合（真数）
//       approximate ← RDL 一行都没有，退到 request_logs，且 planned≈请求数×3、
//                     routable≈routed×2 —— **是乘出来的估算，不是数出来的**
//       mixed      ← RDL 有行但 trace 全空，用 request_logs 补总数
//     把估算显示成精确统计，就是本专题 §11.20 修的「降级被显示成真的一分钱没花」
//     的同族错误。⇒ 页首永远挂一条置信度条，`approximate` 时用警示色。
//
// (2) ★★ **近似模式下 `meta.blocked` 恒为 0，而 0 有两种完全不同的含义。**
//     只有 exact 分支才 `SUM(blocked_candidates)`（analytics.go:1019 附近）；
//     approximate/mixed 的补数 SQL 里**根本没有 blocked 列** ⇒ 字段必然零值。
//     ⇒ 「近似 + blocked=0」= **没算过**，不是「一个都没被拦」。
//     见 `blockedIsInconclusive`。显示成 0 就是在编造一个我们并不知道的结论。
//
// (3) ★ **funnel 有 2 分钟服务端缓存**（funnel_cache.go:21-24，key=scope|model|window）。
//     用户刚在 /overrides 改完规则就过来刷新，会看到**没变化的旧数**。
//     ⇒ 必须自曝「最多滞后 2 分钟」，否则用户会认定「我刚写的规则没生效」
//     并去重复提交 —— 而重复提交必然撞 409，越急越错。
//
// (4) **model 必填**，空串 ⇒ 400 `model parameter required`。
//     **window 只有 24h / 7d**，`12h`/`30d` 一律 400 `window must be 24h or 7d`
//     ⇒ 本页只给这两个 chip，不提供自由输入。
//
// 整条 auto-route 线是 superAdmin（handler.go:1381 / :1430），导航已按 requiresRole 挡住。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt } from '@/utils/format'
import {
  fetchRouteFunnel,
  isApproximate,
  blockedIsInconclusive,
  stageRate,
  ANALYTICS_WINDOWS,
  FUNNEL_CACHE_TTL_MS,
  type AnalyticsWindow,
  type FunnelMeta,
  type FunnelResponse,
} from '@/api/autoRouteInsights'

useHyperPage({ title: () => t('funnel.title') })

const model = ref('')
const window = ref<AnalyticsWindow>('7d')
const data = ref<FunnelResponse | null>(null)
const meta = ref<FunnelMeta | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const APPROX_LABEL: Record<string, string> = {
  exact: 'funnel.sourceExact',
  approximate: 'funnel.sourceApproximate',
  mixed: 'funnel.sourceMixed',
}

async function load(): Promise<void> {
  const m = model.value.trim()
  // ★ 空 model 后端必 400。与其发一次注定失败的请求，不如本地拦下并说清原因。
  if (m === '') {
    error.value = t('funnel.modelRequired')
    data.value = null
    meta.value = null
    return
  }
  loading.value = true
  error.value = null
  try {
    const resp = await fetchRouteFunnel({ model: m, window: window.value })
    data.value = resp
    meta.value = resp.meta ?? null
  } catch (err) {
    data.value = null
    meta.value = null
    error.value = describeError(err)
  } finally {
    loading.value = false
  }
}

function onSearch(): void {
  void load()
}

function onWindowChange(w: AnalyticsWindow): void {
  if (window.value === w) return
  window.value = w
  if (model.value.trim() !== '') void load()
}

function describeError(err: unknown): string {
  const status = (err as { status?: number })?.status
  if (status === 403) return t('funnel.errForbidden')
  // ★ model 拼错时后端不 404 —— 查不到就是查不到，返回 200 + requests=0。
  //   所以「查不到」和「没数据」在响应里同形，见下方 requests===0 的空态。
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

const stages = computed(() => data.value?.stages ?? [])

/** 逐阶段转化率。分母为 0 ⇒ null ⇒ 渲染「—」而不是 0%。 */
const rates = computed(() => {
  const s = stages.value
  return s.map((cur, i) => (i === 0 ? null : stageRate(s[i - 1]?.value, cur.value)))
})

const sourceLabelKey = computed(() => {
  const ds = meta.value?.data_source ?? ''
  return APPROX_LABEL[ds] ?? 'funnel.sourceUnknown'
})

/** ★ blocked 的「不可信」判定 —— 见文件头 (2)。 */
const blockedUnknown = computed(() => blockedIsInconclusive(meta.value))

/** 词表外的 confidence 不给乐观色 —— 与 proposalStatusTone 同纪律。 */
const confidenceTone = computed<'success' | 'warning' | 'danger' | 'muted'>(() => {
  const c = (meta.value?.confidence ?? '').toLowerCase()
  if (c === 'high') return 'success'
  if (c === 'medium') return 'warning'
  return 'muted'
})

/** 阶段显示名：后端 label 是中文权威名，缺失才退回 key。两者都没有才空。 */
function stageName(s: { key: string; label: string } | undefined): string {
  if (!s) return ''
  return s.label || s.key || ''
}

const isApprox = computed(() => isApproximate(meta.value))

/**
 * ★ 空态挂在 `requests` 上，**不是**挂在 `data === null` 上。
 *
 * 模型名拼错时后端**不 404**，返回 200 + requests=0 —— 「查不到这个模型」和
 * 「这个模型本来就没流量」在响应里**完全同形**。挂在 `data === null` 上时，
 * 拼错模型名会得到一张「请求数 0 / 已选凭据 0 / trace 覆盖率 0%」的置信度
 * 面板：把「没查到」显示成「统计结果就是零」。
 * （本条是被 RouteFunnelView.spec 的 requests=0 用例当场抓出来的。）
 */
const noRequests = computed(() => data.value !== null && data.value.requests === 0)

/**
 * ★ 有请求但一个阶段都没有 = **形状不符**，不是「没数据」。
 * 这两种绝不能共用一条空态文案：前者要让人去查后端，后者是正常空结果。
 */
const shapeMismatch = computed(
  () => data.value !== null && data.value.requests > 0 && stages.value.length === 0,
)

/**
 * ★ 空结果下**不渲染阶段表**。
 *
 * requests=0 时后端仍会吐 3 个 value=0 的阶段。渲染出来是三行 0，
 * 看着像「量过了，结果是零」—— 而真相是「压根没量」。
 * ⇒ 空态与阶段表互斥，两者不许同时出现。
 */
const showStages = computed(() => stages.value.length > 0 && !noRequests.value && !shapeMismatch.value)

onBeforeUnmount(() => {
  data.value = null
  meta.value = null
  error.value = null
})
</script>

<template>
  <div class="view-root fn">
    <form class="fn__form" @submit.prevent="onSearch">
      <label class="fn__field">
        <span>{{ t('funnel.model') }} *</span>
        <input
          v-model="model"
          class="fn__input"
          :placeholder="t('funnel.modelPlaceholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
        />
      </label>

      <div class="fn__chips" role="group" :aria-label="t('funnel.window')">
        <button
          v-for="w in ANALYTICS_WINDOWS"
          :key="w"
          type="button"
          class="fn__chip"
          :class="{ 'fn__chip--on': window === w }"
          :aria-pressed="window === w"
          @click="onWindowChange(w)"
        >
          {{ t('funnel.window' + w) }}
        </button>
        <button type="button" class="fn__chip fn__chip--go" @click="onSearch">{{ t('funnel.search') }}</button>
      </div>
    </form>

    <p v-if="error" class="fn__msg fn__msg--err">{{ error }}</p>
    <p v-if="loading" class="fn__msg">{{ t('common.loading') }}</p>

    <!-- ★ 置信度条：approximate 时是警示色，且必须带 data_source 字面值，
         否则用户无法判断「估算」到底有多粗。 -->
    <section v-if="meta" class="fn__conf" :class="{ 'fn__conf--approx': isApprox }">
      <div class="fn__conf-head">
        <StatusDot :tone="confidenceTone" />
        <span class="fn__conf-title">{{ t('funnel.confidence') }}</span>
        <span class="badge" :class="isApprox ? 'badge--warning' : 'badge--muted'">
          {{ t(sourceLabelKey) }}
        </span>
      </div>
      <p class="fn__conf-hint">{{ meta.confidence_hint }}</p>

      <dl class="fn__facts">
        <div class="fn__fact">
          <dt>{{ t('funnel.requests') }}</dt>
          <dd>{{ fmtInt(data?.requests ?? 0) }}</dd>
        </div>
        <div class="fn__fact">
          <dt>{{ t('funnel.chosen') }}</dt>
          <dd>{{ fmtInt(meta.chosen) }}</dd>
        </div>
        <div class="fn__fact">
          <dt>{{ t('funnel.traceRatio') }}</dt>
          <dd>{{ Math.round((meta.trace_ratio ?? 0) * 100) }}%</dd>
        </div>
        <!-- ★ blocked 的 0 有歧义（见文件头 (2)）：近似模式下显示「未知」，
             绝不显示 0。 -->
        <div class="fn__fact">
          <dt>{{ t('funnel.blocked') }}</dt>
          <dd :class="{ 'fn__unknown': blockedUnknown }">
            {{ blockedUnknown ? t('funnel.unknown') : fmtInt(meta.blocked) }}
          </dd>
        </div>
      </dl>

      <!-- ★ 2 分钟缓存：不自曝的话，用户改完规则过来刷新会认定「规则没生效」
           并重复提交，重复提交必然撞 409。 -->
      <p class="fn__cache">
        <AppIcon name="clock" :size="13" />
        <span>{{ t('funnel.cacheHint', { min: FUNNEL_CACHE_TTL_MS / 60000 }) }}</span>
      </p>
    </section>

    <p v-if="!loading && !error && shapeMismatch" class="fn__msg fn__msg--err">{{ t('funnel.shapeMismatch') }}</p>
    <p v-else-if="!loading && !error && noRequests" class="fn__msg">{{ t('funnel.empty') }}</p>
    <p v-else-if="!loading && !error && !data" class="fn__msg">{{ t('funnel.empty') }}</p>

    <ol v-if="showStages" class="fn__stages">
      <li v-for="(s, i) in stages" :key="s.key" class="fn__stage">
        <div class="fn__stage-head">
          <span class="fn__stage-label">{{ s.label || s.key }}</span>
          <span class="fn__stage-value">{{ fmtInt(s.value) }}</span>
        </div>
        <p v-if="s.hint" class="fn__stage-hint">{{ s.hint }}</p>
        <!-- 分母为 0 ⇒ 「—」。0% 意味着「全被筛掉」，两件事不能混。 -->
        <p v-if="i > 0" class="fn__stage-rate">
          <span class="fn__rate-arrow">↓</span>
          <span :class="{ 'fn__unknown': rates[i] === null }">
            {{ rates[i] === null ? t('funnel.unknown') : Math.round(rates[i]! * 100) + '%' }}
          </span>
          <span class="fn__rate-from">{{ t('funnel.rateFrom', { prev: stageName(stages[i - 1]) }) }}</span>
        </p>
      </li>
    </ol>
  </div>
</template>

<style scoped>
.fn {
  padding: var(--app-space-3);
}
.fn__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}
.fn__field {
  display: block;
}
.fn__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.fn__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.fn__chips {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
}
.fn__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: var(--app-font-input);
}
.fn__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.fn__chip--go {
  margin-left: auto;
  color: var(--app-primary);
  border-color: var(--app-primary);
}
.fn__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.fn__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.fn__conf {
  border: 1px solid var(--app-border);
  border-left: 3px solid var(--app-success);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
/* ★ 近似数据不能和精确数据长得一样 —— 左边框换色是最便宜也最难忽略的信号。 */
.fn__conf--approx {
  border-left-color: var(--app-warning);
}
.fn__conf-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.fn__conf-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}
.fn__conf-hint {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  line-height: 1.5;
}
.fn__facts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--app-space-2);
  margin: var(--app-space-3) 0 0;
}
.fn__fact {
  min-width: 0;
}
.fn__fact dt {
  font-size: 11px;
  color: var(--app-text-muted);
}
.fn__fact dd {
  margin: 2px 0 0;
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
/* ★ 「未知」与真实数字视觉上必须分得开，否则用户会把「没算」读成「没有」。 */
.fn__unknown {
  color: var(--app-text-muted) !important;
  font-weight: 500 !important;
}
.fn__cache {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-3) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.fn__stages {
  list-style: none;
  margin: 0;
  padding: 0;
  counter-reset: fn-stage;
}
.fn__stage {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.fn__stage-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--app-space-2);
}
.fn__stage-label {
  font-size: 13px;
  color: var(--app-text-secondary);
  min-width: 0;
  word-break: break-word;
}
.fn__stage-value {
  font-size: 18px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.fn__stage-hint {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.fn__stage-rate {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  flex-wrap: wrap;
}
.fn__rate-arrow {
  color: var(--app-text-muted);
}
.fn__rate-from {
  color: var(--app-text-muted);
  font-size: 11px;
}
</style>