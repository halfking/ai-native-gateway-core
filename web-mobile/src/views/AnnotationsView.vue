<script setup lang="ts">
// AnnotationsView — 人工标注工作台（/annotations，**admin 档**）。
//
// 数据源（三条，全部 admin 档，见 api/annotations.ts 文件头）：
//   GET /api/admin/annotations/stats              四个统计块
//   GET /api/admin/annotations/samples            请求级样本（含采样策略）
//   GET /api/admin/annotations/first-turn-samples 每会话首轮工作台
//
// ★ 本页**只有只读面**。写操作（三条）全部不碰：
//   POST /annotations              创建标注
//   POST /annotations/batch        批量标注
//   DELETE /annotations/{id}       删标注
// 后两者互不可逆 —— 删一条标注会改变该样本 accuracy 的统计口径，
// 而 stats 端的数字是历史累计的，删完就对不上了。
//
// ## ★★★★★ 四处「不能都渲染成同一个东西」
//
// (1) ★★★★★ **稀疏键**：`FirstTurnSample` 有 9 个指针字段，其中
//      **5 个带 `omitempty`**（human_task_type / human_model / human_provider /
//      is_correct / reason / annotator / annotated_at，handler.go:113-120）
//      ⇒ **未标注时这些键根本不存在**。
//      ⇒ 缺键必须渲染成「未标注」，**绝不能**渲染成 0 或空串
//      ——「没标过」与「标了但判错」是两件事。
//
// (2) ★★★★ **`stats` 在零标注时整条 500**（annotation/stats.go:31-59 查的是
//      无聚合子句的单行汇总表 `FROM annotation_stats`，空表返 `pgx.ErrNoRows`）
//      且四个块串联早退、任一失败整条挂。
//      ⇒ 500 **不能**被渲染成「统计为零」。
//      ⇒ 本页对这个端点**不写任何降级**：错误就显示错误。
//
// (3) ★★★ **`accuracy_percent` 的 0 与「无意义」在值上不可分**
//      ⇒ `total_annotations === 0` 时那个百分数不算数。
//
// (4) ★★★ **`strategy` 是条件键**：只在非 recent 时下发（handler.go:225-227）
//      ⇒ 键缺失 = recent，不是「策略未知」。
//      而 **first-turn 端点根本没有 strategy 键** —— 两个端点别串。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchAnnotationStats,
  fetchSamples,
  fetchFirstTurnSamples,
  annotationAccuracyMeaningless,
  annotationCountsContradict,
  annotationOverallUnavailable,
  annotationAllDistributionsEmpty,
  providerAccuracyContradicts,
  samplesSizeEffective,
  samplesStrategyEffective,
  samplesHasNextPage,
  firstTurnDateValid,
  SAMPLES_SIZE_DEFAULT,
  SAMPLES_PER_STRATA_MAX,
  type AnnotationStatsResponse,
  type AnnotationStats,
  type ProviderAccuracy,
  type SamplesResponse,
  type FirstTurnSamplesResponse,
} from '@/api/annotations'

useHyperPage({ title: () => t('annotations.title') })

/** 无数据占位。字形与 0 不同，且带独立 class（判据锚在原因上）。 */
const NO_DATA = '—'

type SectionKey = 'stats' | 'samples' | 'firstTurn'

const stats = ref<AnnotationStatsResponse | null>(null)
const samples = ref<SamplesResponse | null>(null)
const firstTurn = ref<FirstTurnSamplesResponse | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({ stats: false, samples: false, firstTurn: false })
const error = ref<Record<SectionKey, string | null>>({ stats: null, samples: null, firstTurn: null })

/* ── stats 段的参数（无筛选） ──────────────────────────────────────── */
/* ── samples 段的参数 ─────────────────────────────────────────────── */

const samplesPage = ref(1)
const samplesSize = ref(SAMPLES_SIZE_DEFAULT)
/** ★ 非法值后端会 400（handler.go:198-200）⇒ 输入框不允许产生非法值。 */
const samplesStrategy = ref<'recent' | 'disagreement' | 'stratified'>('recent')
const samplesPerStrata = ref(5)

/* ── first-turn 段的参数 ──────────────────────────────────────────── */

const firstTurnPage = ref(1)
const firstTurnSize = ref(50)
const firstTurnStart = ref('')
const firstTurnEnd = ref('')
/** ★ 缺省是「今天(UTC)」而不是「不限」（resolveFirstTurnDateRange :628-633）。 */
const firstTurnDefaultsToday = computed(() => firstTurnStart.value === '' && firstTurnEnd.value === '')
/** ★ 格式错后端 400 ⇒ 前端只允许 YYYY-MM-DD。 */
const firstTurnDateValidAll = computed(
  () => firstTurnDateValid(firstTurnStart.value || undefined) && firstTurnDateValid(firstTurnEnd.value || undefined),
)

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('annotations.errForbidden')
  // ★★ 零标注时后端整条 500 —— 这**不是**「统计为零」。
  if (statusCode === 500 && stats.value === null) return t('annotations.stats.serverError')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    if (section === 'stats') {
      stats.value = await fetchAnnotationStats()
    } else if (section === 'samples') {
      samples.value = await fetchSamples({
        page: samplesPage.value,
        size: samplesSize.value,
        strategy: samplesStrategy.value,
        perStrata: samplesPerStrata.value,
      })
    } else {
      firstTurn.value = await fetchFirstTurnSamples({
        page: firstTurnPage.value,
        size: firstTurnSize.value,
        ...(firstTurnStart.value ? { startDate: firstTurnStart.value } : {}),
        ...(firstTurnEnd.value ? { endDate: firstTurnEnd.value } : {}),
      })
    }
  } catch (err) {
    // ★ 失败 ⇒ 清空，绝不保留上一次结果冒充本次。
    if (section === 'stats') stats.value = null
    if (section === 'samples') samples.value = null
    if (section === 'firstTurn') firstTurn.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

/* ── stats (2)(3) ─────────────────────────────────────────────────── */

const overall = computed<AnnotationStats | null>(() => stats.value?.overall ?? null)
const overallMissing = computed(() => stats.value ? annotationOverallUnavailable(stats.value) : false)
const accuracyMeaningless = computed(() => overall.value ? annotationAccuracyMeaningless(overall.value) : false)
const countsContradict = computed(() => overall.value ? annotationCountsContradict(overall.value) : false)
const allEmpty = computed(() => stats.value ? annotationAllDistributionsEmpty(stats.value) : false)

const providerRows = computed<ProviderAccuracy[]>(() => stats.value?.by_provider ?? [])
const annotatorRows = computed(() => stats.value?.by_annotator ?? [])
const reasonRows = computed(() => stats.value?.by_reason ?? [])

function providerContradicts(p: ProviderAccuracy): boolean {
  return providerAccuracyContradicts(p)
}

/* ── samples (4) ──────────────────────────────────────────────────── */

/** ★ 键缺失 = recent，不是「策略未知」。 */
const samplesStrategyEcho = computed(() => (samples.value ? samplesStrategyEffective(samples.value) : 'recent'))
const samplesRows = computed(() => samples.value?.samples ?? [])
const samplesHasNext = computed(() =>
  samples.value ? samplesHasNextPage(samples.value, samplesSize.value) : false,
)
const samplesSizeEcho = computed(() => samplesSizeEffective(samplesSize.value))
const samplesPerStrataEcho = computed(() =>
  Math.min(Math.max(samplesPerStrata.value, 1), SAMPLES_PER_STRATA_MAX),
)

function gotoSamplesPage(delta: number): void {
  samplesPage.value = Math.max(1, samplesPage.value + delta)
  void load('samples')
}

/** ★ strategy 选 stratified 时 per_strata 才有意义，否则提示它是空转的。 */
const perStrataActive = computed(() => samplesStrategy.value === 'stratified')

/* ── first-turn (1) ───────────────────────────────────────────────── */

const firstTurnRows = computed(() => firstTurn.value?.samples ?? [])

/**
 * ★★★ 稀疏键单元格：`undefined` 意味着**后端没写这个键**。
 * 在 first-turn 里 5 个标注字段都带 omitempty ⇒ 未标注时键不存在。
 * 缺键 → 「未标注」，不是 0、不是空串。
 */
function sparse(row: Record<string, unknown>, key: string): { text: string; missing: boolean } {
  const v = row[key]
  if (v === undefined || v === null) return { text: t('annotations.notAnnotated'), missing: true }
  if (typeof v === 'boolean') return { text: v ? t('annotations.correct') : t('annotations.incorrect'), missing: false }
  if (typeof v === 'number') return { text: fmtNum(v, 2), missing: false }
  return { text: String(v), missing: false }
}

/** ★ 未标注的整行不再显示它的「是否标注」标志。 */
function isAnnotated(row: Record<string, unknown>): boolean {
  return 'is_correct' in row || 'annotated_at' in row
}

function gotoFirstTurnPage(delta: number): void {
  firstTurnPage.value = Math.max(1, firstTurnPage.value + delta)
  void load('firstTurn')
}

onBeforeUnmount(() => {
  stats.value = null
  samples.value = null
  firstTurn.value = null
})
</script>

<template>
  <div class="view-root an">
    <!-- ══ 1. 标注统计 ═══════════════════════════════════════════════ -->
    <section class="an__section">
      <header class="an__head">
        <h2 class="an__title">{{ t('annotations.stats.title') }}</h2>
        <button
          type="button"
          class="an__load"
          :disabled="loading === 'stats'"
          @click="load('stats')"
        >
          {{ loaded.stats && !error.stats ? t('annotations.reload') : t('annotations.load') }}
        </button>
      </header>

      <p v-if="error.stats" class="an__msg an__msg--err">{{ error.stats }}</p>
      <p v-if="loading === 'stats'" class="an__msg">{{ t('common.loading') }}</p>

      <template v-if="stats">
        <!-- ★★ overall 为 null：本块不可用，不是「零标注」 -->
        <p v-if="overallMissing" class="an__msg an__msg--warn">
          {{ t('annotations.stats.overallMissing') }}
        </p>

        <template v-else-if="overall">
          <div class="an__kpis">
            <div class="an__kpi">
              <span class="an__kpi-label">{{ t('annotations.stats.total') }}</span>
              <span class="an__kpi-value">{{ fmtInt(overall.total_annotations) }}</span>
            </div>
            <div class="an__kpi">
              <span class="an__kpi-label">{{ t('annotations.stats.accuracy') }}</span>
              <!-- ★ 分母为 0 时百分数无意义，不是 0% -->
              <span class="an__kpi-value" :class="{ 'an__nodata': accuracyMeaningless }">
                {{ accuracyMeaningless
                  ? t('annotations.stats.noAnnotations')
                  : fmtNum(overall.accuracy_percent, 2) + '%' }}
              </span>
            </div>
          </div>

          <!-- ★ 计数器自相矛盾必须单独报警，不能混在数字里 -->
          <p v-if="countsContradict" class="an__msg an__msg--err">
            {{ t('annotations.stats.contradict') }}
          </p>

          <dl class="an__kv">
            <div class="an__kv-row">
              <dt>{{ t('annotations.stats.correct') }}</dt>
              <dd>{{ fmtInt(overall.correct_count) }}</dd>
            </div>
            <div class="an__kv-row">
              <dt>{{ t('annotations.stats.incorrect') }}</dt>
              <dd>{{ fmtInt(overall.incorrect_count) }}</dd>
            </div>
            <div class="an__kv-row">
              <dt>{{ t('annotations.stats.annotators') }}</dt>
              <dd>{{ fmtInt(overall.num_annotators) }}</dd>
            </div>
            <div class="an__kv-row">
              <dt>{{ t('annotations.stats.firstAt') }}</dt>
              <dd :class="{ 'an__nodata': overall.first_annotation_at === null }">
                {{ overall.first_annotation_at ?? NO_DATA }}
              </dd>
            </div>
            <div class="an__kv-row">
              <dt>{{ t('annotations.stats.lastAt') }}</dt>
              <dd :class="{ 'an__nodata': overall.last_annotation_at === null }">
                {{ overall.last_annotation_at ?? NO_DATA }}
              </dd>
            </div>
          </dl>
        </template>

        <!-- ★ by_provider 自相矛盾逐行报警 -->
        <ul v-if="providerRows.length" class="an__list">
          <li v-for="p in providerRows" :key="p.provider" class="an__item">
            <div class="an__item-head">
              <span class="an__item-title">{{ p.provider }}</span>
              <span class="an__kpi-value-small">{{ fmtNum(p.accuracy_percent, 2) }}%</span>
            </div>
            <p v-if="providerContradicts(p)" class="an__msg an__msg--err">
              {{ t('annotations.stats.providerContradict') }}
            </p>
            <dl class="an__kv">
              <div class="an__kv-row">
                <dt>{{ t('annotations.stats.totalPredictions') }}</dt>
                <dd>{{ fmtInt(p.total_predictions) }}</dd>
              </div>
            </dl>
          </li>
        </ul>

        <ul v-if="reasonRows.length" class="an__chips">
          <li v-for="r in reasonRows" :key="r.reason" class="an__chip">
            {{ r.reason }} · {{ fmtInt(r.count) }} · {{ fmtNum(r.percentage, 2) }}%
          </li>
        </ul>

        <!-- ★★ by_annotator 是三个分布块之一，此前漏渲染 —— 
             只显示两块却不说，会让人以为「没有人标注」 -->
        <template v-if="annotatorRows.length">
          <h3 class="an__subtitle">{{ t('annotations.stats.byAnnotator') }}</h3>
          <ul class="an__list">
            <li v-for="a in annotatorRows" :key="a.annotator" class="an__item">
              <div class="an__item-head">
                <span class="an__item-title">{{ a.annotator }}</span>
                <span class="an__kpi-value-small">{{ fmtNum(a.accuracy_percent, 2) }}%</span>
              </div>
              <dl class="an__kv">
                <div class="an__kv-row">
                  <dt>{{ t('annotations.stats.total') }}</dt>
                  <dd>{{ fmtInt(a.total_annotations) }}</dd>
                </div>
                <!-- ★ AnnotatorStats 的两个时间是值类型（time.Time 非指针）
                     ⇒ 恒有值，与 overall 的指针字段相反 -->
                <div class="an__kv-row">
                  <dt>{{ t('annotations.stats.lastAt') }}</dt>
                  <dd>{{ a.last_annotation_at }}</dd>
                </div>
              </dl>
            </li>
          </ul>
        </template>

        <!-- ★ 三个分布块全空：说「还没有任何可聚合的标注」，不是「空页面」 -->
        <p v-if="allEmpty && !overallMissing" class="an__msg">
          {{ t('annotations.stats.allEmpty') }}
        </p>
      </template>
    </section>

    <!-- ══ 2. 请求级样本 ═════════════════════════════════════════════ -->
    <section class="an__section">
      <header class="an__head">
        <h2 class="an__title">{{ t('annotations.samples.title') }}</h2>
      </header>

      <div class="an__filters">
        <label class="an__field">
          <span class="an__field-label">{{ t('annotations.samples.strategy') }}</span>
          <select v-model="samplesStrategy" class="an__input">
            <option value="recent">{{ t('annotations.samples.strategyOption_recent') }}</option>
            <option value="disagreement">{{ t('annotations.samples.strategyOption_disagreement') }}</option>
            <option value="stratified">{{ t('annotations.samples.strategyOption_stratified') }}</option>
          </select>
        </label>
        <label class="an__field">
          <span class="an__field-label">{{ t('annotations.samples.size') }}</span>
          <input v-model.number="samplesSize" type="number" class="an__input" min="1" max="200" />
        </label>
        <label class="an__field">
          <span class="an__field-label">{{ t('annotations.samples.perStrata') }}</span>
          <input v-model.number="samplesPerStrata" type="number" class="an__input" min="1" :max="SAMPLES_PER_STRATA_MAX" />
        </label>
        <!-- ★★ 回显后端**实际生效**的值：size 双向钳位、per_strata 钳位 -->
        <p class="an__echo">
          {{ t('annotations.samples.effective', {
            size: samplesSizeEcho,
            perStrata: samplesPerStrataEcho,
            strategy: samplesStrategyEcho,
          }) }}
        </p>
        <!-- ★ per_strata 只在 stratified 下生效 -->
        <p v-if="!perStrataActive" class="an__msg">{{ t('annotations.samples.perStrataIdle') }}</p>
        <button type="button" class="an__load" :disabled="loading === 'samples'" @click="load('samples')">
          {{ loaded.samples && !error.samples ? t('annotations.reload') : t('annotations.load') }}
        </button>
      </div>

      <p v-if="error.samples" class="an__msg an__msg--err">{{ error.samples }}</p>
      <p v-if="loading === 'samples'" class="an__msg">{{ t('common.loading') }}</p>

      <template v-if="samples">
        <p class="an__echo">{{ t('annotations.samples.count', { total: fmtInt(samples.total) }) }}</p>

        <ul v-if="samplesRows.length" class="an__list">
          <li v-for="(r, idx) in samplesRows" :key="idx" class="an__item">
            <div class="an__item-head">
              <span class="an__item-title">{{ sparse(r as Record<string, unknown>, 'request_id').text }}</span>
            </div>
            <dl class="an__kv">
              <div class="an__kv-row">
                <dt>{{ t('annotations.samples.model') }}</dt>
                <dd :class="{ 'an__nodata': sparse(r as Record<string, unknown>, 'chosen_model').missing }">
                  {{ sparse(r as Record<string, unknown>, 'chosen_model').text }}
                </dd>
              </div>
              <div class="an__kv-row">
                <dt>{{ t('annotations.samples.taskType') }}</dt>
                <dd :class="{ 'an__nodata': sparse(r as Record<string, unknown>, 'task_type').missing }">
                  {{ sparse(r as Record<string, unknown>, 'task_type').text }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
        <p v-else-if="loaded.samples && !error.samples" class="an__msg">{{ t('annotations.empty') }}</p>

        <div class="an__pager">
          <button
            type="button"
            class="an__pager-btn"
            :disabled="samplesPage <= 1"
            @click="gotoSamplesPage(-1)"
          >{{ t('annotations.prev') }}</button>
          <span class="an__pager-label">{{ t('annotations.page', { page: samplesPage }) }}</span>
          <button
            type="button"
            class="an__pager-btn"
            :disabled="!samplesHasNext"
            @click="gotoSamplesPage(1)"
          >{{ t('annotations.next') }}</button>
        </div>
      </template>
    </section>

    <!-- ══ 3. 首轮工作台 ═════════════════════════════════════════════ -->
    <section class="an__section">
      <header class="an__head">
        <h2 class="an__title">{{ t('annotations.firstTurn.title') }}</h2>
      </header>

      <div class="an__filters">
        <label class="an__field">
          <span class="an__field-label">{{ t('annotations.firstTurn.start') }}</span>
          <input v-model="firstTurnStart" type="date" class="an__input" />
        </label>
        <label class="an__field">
          <span class="an__field-label">{{ t('annotations.firstTurn.end') }}</span>
          <input v-model="firstTurnEnd" type="date" class="an__input" />
        </label>
        <!-- ★ 缺省是「今天(UTC)」而不是「不限」—— 与 samples 段语义相反 -->
        <p v-if="firstTurnDefaultsToday" class="an__msg">
          {{ t('annotations.firstTurn.defaultsToday') }}
        </p>
        <p v-if="!firstTurnDateValidAll" class="an__msg an__msg--err">
          {{ t('annotations.firstTurn.badDate') }}
        </p>
        <button
          type="button"
          class="an__load"
          :disabled="loading === 'firstTurn' || !firstTurnDateValidAll"
          @click="load('firstTurn')"
        >
          {{ loaded.firstTurn && !error.firstTurn ? t('annotations.reload') : t('annotations.load') }}
        </button>
      </div>

      <p v-if="error.firstTurn" class="an__msg an__msg--err">{{ error.firstTurn }}</p>
      <p v-if="loading === 'firstTurn'" class="an__msg">{{ t('common.loading') }}</p>

      <template v-if="firstTurn">
        <p class="an__echo">{{ t('annotations.samples.count', { total: fmtInt(firstTurn.total) }) }}</p>

        <ul v-if="firstTurnRows.length" class="an__list">
          <li v-for="(r, idx) in firstTurnRows" :key="idx" class="an__item">
            <div class="an__item-head">
              <span class="an__item-title">{{ sparse(r as Record<string, unknown>, 'session_id').text }}</span>
              <span v-if="!isAnnotated(r as Record<string, unknown>)" class="an__badge">
                {{ t('annotations.notAnnotated') }}
              </span>
            </div>
            <dl class="an__kv">
              <div class="an__kv-row">
                <dt>{{ t('annotations.firstTurn.model') }}</dt>
                <dd>{{ sparse(r as Record<string, unknown>, 'chosen_model').text }}</dd>
              </div>
              <!-- ★★★ 这两行是稀疏键的展示面：未标注时后端根本不写这些键 -->
              <div class="an__kv-row">
                <dt>{{ t('annotations.firstTurn.humanModel') }}</dt>
                <dd :class="{ 'an__nodata': sparse(r as Record<string, unknown>, 'human_model').missing }">
                  {{ sparse(r as Record<string, unknown>, 'human_model').text }}
                </dd>
              </div>
              <div class="an__kv-row">
                <dt>{{ t('annotations.firstTurn.verdict') }}</dt>
                <dd :class="{ 'an__nodata': sparse(r as Record<string, unknown>, 'is_correct').missing }">
                  {{ sparse(r as Record<string, unknown>, 'is_correct').text }}
                </dd>
              </div>
              <div class="an__kv-row">
                <dt>{{ t('annotations.firstTurn.reason') }}</dt>
                <dd :class="{ 'an__nodata': sparse(r as Record<string, unknown>, 'reason').missing }">
                  {{ sparse(r as Record<string, unknown>, 'reason').text }}
                </dd>
              </div>
            </dl>
          </li>
        </ul>
        <p v-else-if="loaded.firstTurn && !error.firstTurn" class="an__msg">{{ t('annotations.empty') }}</p>

        <div class="an__pager">
          <button
            type="button"
            class="an__pager-btn"
            :disabled="firstTurnPage <= 1"
            @click="gotoFirstTurnPage(-1)"
          >{{ t('annotations.prev') }}</button>
          <span class="an__pager-label">{{ t('annotations.page', { page: firstTurnPage }) }}</span>
          <button
            type="button"
            class="an__pager-btn"
            :disabled="!firstTurnRows.length"
            @click="gotoFirstTurnPage(1)"
          >{{ t('annotations.next') }}</button>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* AnnotationsView — 触控热区一律 ≥48 CSS px（UI规范 17 §4-R1）。 */
.an__section {
  margin-bottom: 16px;
}
.an__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.an__title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
}
.an__load {
  min-height: 48px;
  min-width: 88px;
  padding: 0 16px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.an__load:disabled {
  opacity: 0.6;
  cursor: default;
}
.an__filters {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-end;
  margin-bottom: 12px;
}
.an__field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.an__field-label {
  font-size: 12px;
  opacity: 0.75;
}
.an__input {
  min-height: 48px;
  padding: 0 10px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  font-size: 14px;
}
.an__msg {
  font-size: 13px;
  margin: 4px 0;
  opacity: 0.8;
}
.an__msg--err {
  color: var(--danger, #cf222e);
  opacity: 1;
}
.an__msg--warn {
  color: var(--warning, #9a6700);
  opacity: 1;
}
.an__echo {
  font-size: 12px;
  opacity: 0.7;
  margin: 4px 0;
}
.an__subtitle {
  font-size: 13px;
  font-weight: 600;
  margin: 12px 0 0;
  opacity: 0.85;
}
.an__kpis {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}
.an__kpi {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
}
.an__kpi-label {
  font-size: 12px;
  opacity: 0.75;
}
.an__kpi-value {
  font-size: 18px;
  font-weight: 600;
}
.an__kpi-value-small {
  font-size: 15px;
  font-weight: 600;
}
/* ★ 「未标注」占位：字形与 class 双锚，避免「—」被当成 0 或「标注正确」 */
.an__nodata {
  opacity: 0.55;
  font-style: italic;
}
.an__badge {
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 999px;
  background: rgba(154, 103, 0, 0.12);
  color: var(--warning, #9a6700);
}
.an__kv {
  margin: 8px 0 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.an__kv-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  font-size: 13px;
}
.an__kv dt {
  opacity: 0.75;
}
.an__kv dd {
  margin: 0;
  text-align: right;
  word-break: break-all;
}
.an__list {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.an__item {
  border: 1px solid var(--app-border);
  border-radius: 8px;
  padding: 10px;
}
.an__item-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
}
.an__item-title {
  font-size: 14px;
  font-weight: 600;
  word-break: break-all;
}
.an__chips {
  list-style: none;
  margin: 8px 0 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.an__chip {
  font-size: 12px;
  padding: 8px 10px;
  border: 1px solid var(--app-border);
  border-radius: 999px;
  min-height: 48px;
  display: inline-flex;
  align-items: center;
}
.an__pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
}
.an__pager-btn {
  min-height: 48px;
  min-width: 72px;
  padding: 0 14px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface);
  color: inherit;
  font-size: 14px;
  cursor: pointer;
}
.an__pager-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.an__pager-label {
  font-size: 13px;
  opacity: 0.75;
}
</style>