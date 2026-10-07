<script setup lang="ts">
// RoutingPolicyView — 路由策略配置面（2026-10-08，第一百零一批）。
//
// GET /api/routing/policy            （superAdmin）
// GET /api/routing/featured          （superAdmin）
// GET /api/routing/scoring-weights   （superAdmin）
// GET /api/routing/featured-models   （admin）
//
// ⚠️★ 同族**三档一档**：`featured-models` 是 admin 档（tenant_admin 可用），
//   其余三个是 superAdmin 档（tenant_admin 403，`handler.go:1200 :1201 :1215`）。
//   ⇒ 抽屉席设 `requiresRole: 'super_admin'`，并同步 AppDrawer.spec.ts 的白名单。
//   ⇒ ★ 后果：**`featured-models` 在移动端被顺带收严成 superAdmin**（前端严于后端）。
//   这是刻意的取舍 —— 同一页混档会让「谁能看哪块」变得不可解释；记在此处备查。
//   ★ 绝不能按「同前缀都是一类」来定档，必须逐条读注册。
//
// ★★★ 本页最要紧的一件事：**三处「同形」绝不能渲染成确定结论。**
//   1. `policy` 回 `{}` 是**三合一**（没有这一行 / 查询失败 / 文本为空）⇒
//      `fetchRoutingPolicy` 给 `null` ⇒ 只能说「无法判定」，**不能说「未配置」**。
//   2. `scoring-weights` 的默认值在查询失败、解析失败、缺键三种情况下产出同一份，
//      响应里**没有任何标记** ⇒ 必须把 `note` 披露给用户，否则会被当成线上真值。
//   3. `featured_models === []` 时「没配精选模型」与「查不出来」同形。
//   ★ `standardized_name` 恒等于 `name`（`routing.go:4081-4082`）⇒ 只渲染一个。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchRoutingPolicy,
  fetchRoutingFeatured,
  fetchRoutingScoringWeights,
  fetchRoutingFeaturedModels,
  SCORING_WEIGHTS_DISPLAY_ONLY_NOTE,
  type RoutingPolicyRow,
  type RoutingScoringWeights,
  type FeaturedModelEntry,
} from '@/api/routingPolicy'

useHyperPage({ title: () => t('rp.title') })

const policy = ref<RoutingPolicyRow | null>(null)
const weights = ref<RoutingScoringWeights | null>(null)
const featured = ref<string[] | null>(null)
const featuredModels = ref<FeaturedModelEntry[] | null>(null)
const error = ref<string | null>(null)
const loading = ref(false)

/**
 * ★ `policy` 行里那十四个「键在但可为 null」的键（`row_to_json` 对可空列输出 null）。
 * 这里显式列出来，避免用 `Object.entries` 把它们和 `weights_json` 之类混在一起平铺。
 */
const POLICY_NULLABLE_FIELDS = [
  'notes',
  'algorithm_version',
  'retry_per_credential',
  'tier_fallback_max',
  'slot_soft_limit_ratio',
  'slot_hard_limit_ratio',
  'slot_wait_max_ms',
  'circuit_open_seconds',
  'circuit_failure_threshold',
  'circuit_max_open_seconds',
  'stats_window_minutes',
  'stats_update_interval_seconds',
] as const

/**
 * ★★ `null` 与 `0` / `""` 必须显示成**不同的东西**。
 * `row_to_json` 对可空列输出 `null`，而 NOT NULL 列可能是 `0` / `""`
 * ⇒ 把 null 渲染成 `0` 会凭空造出一个「配置为 0」的事实。
 */
function fieldText(v: number | string | null | undefined): string {
  if (v === null || v === undefined) return t('rp.nullValue')
  if (v === '') return t('rp.emptyString')
  return String(v)
}

/** ★ 五个保证存在的数字键（`SCORING_WEIGHTS_DEFAULT_KEYS` 的顺序即展示顺序）。 */
const WEIGHT_KEYS = [
  'price',
  'session_load',
  'failure_penalty',
  'default_price_cny',
  'default_price_usd',
] as const

const weightRows = computed<Array<{ key: string; value: number }>>(() => {
  const w = weights.value
  if (!w) return []
  return WEIGHT_KEYS.map((k) => ({ key: k, value: w[k] }))
})

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const [p, w, fe, fm] = await Promise.allSettled([
      fetchRoutingPolicy(),
      fetchRoutingScoringWeights(),
      fetchRoutingFeatured(),
      fetchRoutingFeaturedModels(),
    ])
    if (p.status === 'fulfilled') policy.value = p.value
    if (w.status === 'fulfilled') weights.value = w.value
    if (fe.status === 'fulfilled') featured.value = fe.value.featured_models
    if (fm.status === 'fulfilled') featuredModels.value = fm.value.models
    const rejected = [p, w, fe, fm].filter((r) => r.status === 'rejected') as PromiseRejectedResult[]
    // ★ 只报**实际发生**的那几条失败，不因为一条失败就说整页失败。
    if (rejected.length > 0) {
      error.value = rejected.map((r) => (r.reason as Error)?.message || t('common.error')).join(' / ')
    }
  } catch (e) {
    policy.value = null
    weights.value = null
    featured.value = null
    featuredModels.value = null
    error.value = (e as Error)?.message || t('common.error')
  } finally {
    loading.value = false
  }
}

void load()

onBeforeUnmount(() => {
  policy.value = null
  weights.value = null
  featured.value = null
  featuredModels.value = null
  error.value = null
})
</script>

<template>
  <div class="rp">
    <p v-if="loading" class="rp__msg">{{ t('common.loading') }}</p>

    <section v-if="error" class="rp__panel">
      <p class="rp__msg rp__msg--err">{{ error }}</p>
    </section>

    <!-- ── 策略行 ── -->
    <section class="rp__panel">
      <h2 class="rp__h">{{ t('rp.policyTitle') }}</h2>
      <!-- ★★ 三合一语义：不能写成「未配置」 -->
      <p v-if="policy === null" class="rp__warn">{{ t('rp.policyUnknown') }}</p>
      <template v-else>
        <p class="rp__note">
          {{ t('rp.policyMeta', { tenant: policy.tenant_id, version: policy.updated_at }) }}
        </p>
        <dl class="rp__kv">
          <div class="rp__kvRow">
            <dt>{{ t('rp.stickyTtl') }}</dt>
            <dd class="num">{{ fieldText(policy.sticky_ttl_seconds) }}</dd>
          </div>
          <div class="rp__kvRow">
            <dt>{{ t('rp.localBonus') }}</dt>
            <dd class="num">{{ fieldText(policy.local_bonus) }}</dd>
          </div>
          <div v-for="f in POLICY_NULLABLE_FIELDS" :key="f" class="rp__kvRow">
            <dt>{{ f }}</dt>
            <dd class="num" :class="{ 'rp__kv--null': policy[f] === null }">
              {{ fieldText(policy[f]) }}
            </dd>
          </div>
        </dl>
      </template>
    </section>

    <!-- ── 评分权重 ── -->
    <section class="rp__panel">
      <h2 class="rp__h">{{ t('rp.weightsTitle') }}</h2>
      <!-- ★ 兜底值与真值同形 ⇒ 必须把披露 note 摆在数据旁边 -->
      <p class="rp__warn">{{ t('rp.weightsFallbackNote') }}</p>
      <p class="rp__note rp__note--quote">{{ SCORING_WEIGHTS_DISPLAY_ONLY_NOTE }}</p>
      <dl v-if="weights" class="rp__kv">
        <div v-for="r in weightRows" :key="r.key" class="rp__kvRow">
          <dt>{{ r.key }}</dt>
          <dd class="num">{{ r.value }}</dd>
        </div>
      </dl>
    </section>

    <!-- ── 精选模型 ── -->
    <section class="rp__panel">
      <h2 class="rp__h">{{ t('rp.featuredTitle') }}</h2>
      <!-- ★★ 两个「精选」不是同一个东西，nil 编码还相反：
             `featured.featured_models` 恒为数组（COALESCE 过，查询失败也回空数组），
             而 `policy.featured_models` 是**可为 null 的原始列**。
             把两者混成一个「精选模型」列表会把「原始列没配」说成「生效列表为空」。 -->
      <p class="rp__note">{{ t('rp.featuredTwoSources') }}</p>
      <p v-if="featured === null" class="rp__warn">{{ t('rp.featuredUnknown') }}</p>
      <p v-else-if="featured.length === 0" class="rp__warn">{{ t('rp.featuredEmpty') }}</p>
      <ul v-else class="rp__list">
        <li v-for="name in featured" :key="name" class="rp__item">
          <span class="rp__itemName">{{ name }}</span>
        </li>
      </ul>
      <ul v-if="featuredModels !== null && featuredModels.length > 0" class="rp__list">
        <li v-for="m in featuredModels" :key="m.name + m.source" class="rp__item">
          <!-- ★★ standardized_name 恒等于 name ⇒ 只渲染一个，否则会被读成两种东西 -->
          <span class="rp__itemName">{{ m.name }}</span>
          <span class="badge" :class="m.source === 'policy' ? 'badge--info' : 'badge--muted'">
            {{ m.source }}
          </span>
          <span class="num rp__itemCount">{{ m.count }}</span>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.rp {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.rp__panel {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.rp__h {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
}

.rp__msg {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.rp__msg--err {
  color: var(--app-danger);
}

.rp__note {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
  word-break: break-all;
}

.rp__note--quote {
  padding: var(--app-space-2);
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-sm);
  font-style: italic;
}

/* 三个「无法判定」态统一用警示色，与正常值区分开 */
.rp__warn {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-warning, var(--app-danger));
}

.rp__kv {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  margin: 0;
}

.rp__kvRow {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-3);
  font-size: 0.75rem;
}

.rp__kvRow dt {
  color: var(--app-text-muted);
}

.rp__kvRow dd {
  margin: 0;
  word-break: break-all;
}

.rp__kv--null {
  color: var(--app-text-muted);
  font-style: italic;
}

.rp__list {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  margin: 0;
  padding: 0;
  list-style: none;
}

.rp__item {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  font-size: 0.8125rem;
}

.rp__itemName {
  flex: 1;
  word-break: break-all;
}

.rp__itemCount {
  color: var(--app-text-muted);
}
</style>