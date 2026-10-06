<script setup lang="ts">
// TuningProposalsView — 自动路由的调优建议（/auto-route/tuning/proposals，**superAdmin 档**）。
//
// 数据源：GET /api/admin/auto-route/tuning/proposals?status=&category=&limit=
//
// 它和 /overrides 是同一个闭环的两端：
//   /overrides = 人已经决定下来的**规则**
//   本页       = 系统观察到的**建议**（待审 / 已批 / 已拒 / 已生效）
// 只看规则不知道规则合不合理；只看建议不知道哪些已经落地了。
//
// ⚠️★ 四个后端语义决定了 UI 的做法：
//
// (1) ★ **status / category 是 allowlist，非法值 400**
//     （auto_route_tuning.go:188 / :193）。空串 = 不过滤，是合法值。
//     ⇒ 筛选器只能是**固定 chip**，绝不能是自由输入框 —— 用户手输一个
//     `pending `（带空格）或 `Pending` 就会吃 400，而后端**不做** TrimSpace
//     和 ToLower（对比 analytics 的 window 用 strings.ToLower，两处不同）。
//
// (2) ★ **limit 越界 400**（`limit must be 1-500`，:200），后端默认 50。
//     ⇒ 前端自己夹，且默认值必须写死成后端那个 50，不能拍脑袋写 100。
//
// (3) ★★ **`proposal` 与 `evidence` 是 json.RawMessage，没有任何 schema。**
//     后端会随建议类型变化（keyword_add / weight_adjust / threshold_change
//     各有自己的结构）。写死 `p.model` 访问在某个类型上就是 undefined，
//     渲染出去是「该建议没有模型」—— 一个我们并不知道的结论。
//     ⇒ 只在键存在时取，取不到就不显示这一行（`pickFields`）。
//
// (4) ★ **状态词的配色必须封闭。** 词表外一律 muted —— 给一个看不懂的状态
//     打 success，等于把「不知道」显示成「已生效」。同 probeTasks 的纪律。
//
// (5) 本页**只读**。审批动作（POST proposals/:id/{approve,reject}）后端写
//     `tuning_proposals` 状态并落审核人，属于**有副作用的人工决策**，
//     本轮不上移（移动端上审批会绕过桌面端的复核环节）。
//     这不是「做不了」，是明确划在范围外。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchTuningProposals,
  proposalStatusTone,
  pickFields,
  PROPOSAL_FIELDS,
  EVIDENCE_FIELDS,
  TUNING_STATUSES,
  TUNING_CATEGORIES,
  TUNING_PROPOSALS_DEFAULT_LIMIT,
  type TuningProposal,
  type TuningStatus,
  type TuningCategory,
} from '@/api/autoRouteInsights'

useHyperPage({ title: () => t('proposals.title') })

const status = ref<TuningStatus>('')
const category = ref<TuningCategory>('')
const limit = ref(TUNING_PROPOSALS_DEFAULT_LIMIT)
const rows = ref<TuningProposal[]>([])
const echo = ref<{ status: string; category: string } | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const resp = await fetchTuningProposals({
      status: status.value,
      category: category.value,
      limit: limit.value,
    })
    // ★ 后端显式初始化为 []（:238 的注释专门解释了 nil 会让页面 .length 崩），
    //   但仍然兜一层 —— 形状不符要看得见，而不是崩在别处。
    rows.value = resp.proposals ?? []
    echo.value = resp.filter ?? null
  } catch (err) {
    rows.value = []
    echo.value = null
    error.value = describeError(err)
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('proposals.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

function pickStatus(next: TuningStatus): void {
  if (status.value === next) return
  status.value = next
  void load()
}

function pickCategory(next: TuningCategory): void {
  if (category.value === next) return
  category.value = next
  void load()
}

function onLimitChange(): void {
  void load()
}

const STATUS_CHIPS = computed(() => TUNING_STATUSES.filter((s) => s !== ''))
const CATEGORY_CHIPS = computed(() => TUNING_CATEGORIES.filter((c) => c !== ''))

/** 状态词 → i18n key。词表外不猜，落到 statusUnknown。 */
function statusKeyOf(s: string | null | undefined): string {
  const v = (s ?? '').toLowerCase()
  return TUNING_STATUSES.includes(v as TuningStatus) ? 'proposals.status.' + v : 'proposals.statusUnknown'
}

function categoryKeyOf(c: string | null | undefined): string {
  const v = (c ?? '').toLowerCase()
  return TUNING_CATEGORIES.includes(v as TuningCategory) ? 'proposals.category.' + v : 'proposals.categoryUnknown'
}

/** ★ 取不到键就不显示这一行，而不是显示「没有模型」。 */
const proposalFieldsOf = (p: TuningProposal) => pickFields(p.proposal, PROPOSAL_FIELDS)
const evidenceFieldsOf = (p: TuningProposal) => pickFields(p.evidence, EVIDENCE_FIELDS)

const reviewTrailOf = (p: TuningProposal) => {
  const out: Array<[string, string]> = []
  if (p.reviewed_by) out.push([t('proposals.reviewedBy'), p.reviewed_by])
  if (p.reviewed_at) out.push([t('proposals.reviewedAt'), relativeTime(p.reviewed_at)])
  if (p.applied_at) out.push([t('proposals.appliedAt'), relativeTime(p.applied_at)])
  if (p.review_note) out.push([t('proposals.reviewNote'), p.review_note])
  return out
}

onBeforeUnmount(() => {
  rows.value = []
  echo.value = null
})
</script>

<template>
  <div class="view-root tp">
    <section class="tp__filters">
      <div class="tp__filter-row" role="group" :aria-label="t('proposals.statusFilter')">
        <button
          type="button"
          class="tp__chip"
          :class="{ 'tp__chip--on': status === '' }"
          :aria-pressed="status === ''"
          @click="pickStatus('')"
        >
          {{ t('proposals.filterAll') }}
        </button>
        <button
          v-for="s in STATUS_CHIPS"
          :key="s"
          type="button"
          class="tp__chip"
          :class="{ 'tp__chip--on': status === s }"
          :aria-pressed="status === s"
          @click="pickStatus(s)"
        >
          {{ t('proposals.status.' + s) }}
        </button>
      </div>

      <div class="tp__filter-row" role="group" :aria-label="t('proposals.categoryFilter')">
        <button
          type="button"
          class="tp__chip tp__chip--sub"
          :class="{ 'tp__chip--on': category === '' }"
          :aria-pressed="category === ''"
          @click="pickCategory('')"
        >
          {{ t('proposals.filterAll') }}
        </button>
        <button
          v-for="c in CATEGORY_CHIPS"
          :key="c"
          type="button"
          class="tp__chip tp__chip--sub"
          :class="{ 'tp__chip--on': category === c }"
          :aria-pressed="category === c"
          @click="pickCategory(c)"
        >
          {{ t('proposals.category.' + c) }}
        </button>
      </div>

      <label class="tp__limit">
        <span>{{ t('proposals.limit', { n: limit }) }}</span>
        <input v-model.number="limit" type="number" min="1" max="500" step="1" class="tp__limit-input" @change="onLimitChange" />
      </label>
    </section>

    <p v-if="error" class="tp__msg tp__msg--err">{{ error }}</p>
    <!-- 回显后端实际用的过滤条件：★ 两个值都是 string（:257）。
         拿去和用户点的 chip 对不上，就是「筛的不是你以为的那个」。 -->
    <p v-if="echo" class="tp__echo">
      {{ t('proposals.echo', { status: echo.status || t('proposals.filterAll'), category: echo.category || t('proposals.filterAll') }) }}
    </p>

    <p v-if="loading" class="tp__msg">{{ t('common.loading') }}</p>
    <p v-else-if="loaded && rows.length === 0" class="tp__msg">{{ t('proposals.empty') }}</p>

    <ul v-else class="tp__list">
      <li v-for="p in rows" :key="p.id" class="tp__item">
        <div class="tp__item-head">
          <StatusDot :tone="proposalStatusTone(p.status)" />
          <span class="tp__item-status">{{ t(statusKeyOf(p.status)) }}</span>
          <span class="badge badge--muted">{{ t(categoryKeyOf(p.category)) }}</span>
          <span class="tp__item-time">{{ relativeTime(p.ts) }}</span>
        </div>

        <!-- ★ proposal/evidence 无 schema：只渲染真取到的键，取不到就整段不出现。 -->
        <dl v-if="proposalFieldsOf(p).length" class="tp__kv">
          <div v-for="[k, v] in proposalFieldsOf(p)" :key="'p' + k" class="tp__kv-row">
            <dt>{{ k }}</dt>
            <dd>{{ v }}</dd>
          </div>
        </dl>
        <dl v-if="evidenceFieldsOf(p).length" class="tp__kv tp__kv--evidence">
          <div v-for="[k, v] in evidenceFieldsOf(p)" :key="'e' + k" class="tp__kv-row">
            <dt>{{ k }}</dt>
            <dd>{{ v }}</dd>
          </div>
        </dl>

        <dl v-if="reviewTrailOf(p).length" class="tp__kv tp__kv--review">
          <div v-for="[k, v] in reviewTrailOf(p)" :key="k" class="tp__kv-row">
            <dt>{{ k }}</dt>
            <dd>{{ v }}</dd>
          </div>
        </dl>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.tp {
  padding: var(--app-space-3);
}
.tp__filters {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}
.tp__filter-row {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.tp__chip {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.tp__chip--sub {
  font-size: 12px;
}
.tp__chip--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}
.tp__limit {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.tp__limit-input {
  width: 80px;
  min-height: 48px;
  padding: 0 8px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.tp__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.tp__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.tp__echo {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 11px;
}
.tp__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.tp__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.tp__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.tp__item-status {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}
.tp__item-time {
  margin-left: auto;
  font-size: 11px;
  color: var(--app-text-muted);
}
.tp__kv {
  margin: var(--app-space-2) 0 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 2px;
}
.tp__kv--evidence {
  padding-top: var(--app-space-2);
  border-top: 1px dashed var(--app-border);
}
.tp__kv--review {
  padding-top: var(--app-space-2);
  border-top: 1px dashed var(--app-border);
}
.tp__kv-row {
  display: flex;
  gap: var(--app-space-2);
  min-width: 0;
}
.tp__kv-row dt {
  font-size: 11px;
  color: var(--app-text-muted);
  min-width: 88px;
  flex-shrink: 0;
}
.tp__kv-row dd {
  margin: 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  min-width: 0;
  word-break: break-word;
}
</style>