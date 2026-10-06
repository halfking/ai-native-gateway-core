<script setup lang="ts">
// InjectionView — 提示词注入「现象面」（stats + detections + attack-vectors，**admin 档**）。
//
// 它答的是「**有没有注入被命中、命中了之后做了什么**」。
// 配置面（策略/规则/引擎/处置矩阵/蜜罐）在 /injection-config。
//
// ⚠️★★★ 十个后端语义（详见 api/promptInjection.ts 文件头）：
//
// (1) ★★★★ `stats` 读**预聚合表** `prompt_injection_stats_enhanced`，
//     且没有统计行时返回**全 0 对象**（不是 404）⇒ 「没统计行」与「全是 0」不可区分。
//     与 detections 的实时读法**存在延迟** ⇒ 两个面板数字天然可能对不上。
// (2) ★★★★ `enabled`/`blocked` 的真值判定是 `== "true"` ⇒ 只发 true/false 字面量。
// (3) ★★★★ detections 用 `page` + `page_size`，且 `page_size` 越界是**回落默认 20**
//     （不是 clamp）；`attack-vectors` 同样规则但**响应没有 total**。
// (4) ★★★★ rules/engines/canary-tokens **根本不接受分页**（本页不涉及）。
// (5) ★★★★★ `risk_level` 是库里的 `integer(1..10)`，Go 侧声明成 `string`
//     ⇒ 响应里是 `"7"` 这种**数字字符串**，**不是**等级名。
// (6) ★★★ detections 的 `category` 是 `$n = ANY(categories)`（数组包含）。
// (7) ★ `stats` 的 `avg_score`/`avg_llm_confidence` 是 COALESCE(…,0) ⇒ 无数据是 0。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchInjectionStats,
  fetchInjectionDetections,
  fetchAttackVectors,
  parseInjectionRiskLevel,
  vectorPageLooksFull,
  INJECTION_CATEGORIES,
  INJECTION_SEVERITY_LEVELS,
  INJECTION_PAGE_SIZE_DEFAULT,
  INJECTION_PAGE_SIZE_MAX,
  type InjectionStats,
  type InjectionDetection,
  type AttackVector,
} from '@/api/promptInjection'

useHyperPage({ title: () => t('injection.title') })

const PAGE_SIZE_CHOICES = [INJECTION_PAGE_SIZE_DEFAULT, 50, INJECTION_PAGE_SIZE_MAX]

const detPage = ref(1)
const detPageSize = ref(INJECTION_PAGE_SIZE_DEFAULT)
const detBlocked = ref<'' | 'true' | 'false'>('')
const detRisk = ref('')
const detCategory = ref('')

const vecPage = ref(1)
const vecPageSize = ref(INJECTION_PAGE_SIZE_DEFAULT)

const stats = ref<InjectionStats | null>(null)
const detections = ref<InjectionDetection[]>([])
const detTotal = ref(0)
const vectors = ref<AttackVector[]>([])

const statsError = ref<string | null>(null)
const detError = ref<string | null>(null)
const vecError = ref<string | null>(null)
const loadingDet = ref(false)
const loadingVec = ref(false)

async function loadStats(): Promise<void> {
  try {
    stats.value = await fetchInjectionStats()
    statsError.value = null
  } catch (e) {
    stats.value = null
    statsError.value = (e as Error)?.message || t('common.error')
  }
}

async function loadDetections(): Promise<void> {
  loadingDet.value = true
  detError.value = null
  try {
    const r = await fetchInjectionDetections({
      page: detPage.value,
      pageSize: detPageSize.value,
      // ★ 只发字面量 true/false（见坑 2）
      blocked: detBlocked.value === '' ? undefined : detBlocked.value === 'true',
      riskLevel: detRisk.value.trim() || undefined,
      category: detCategory.value.trim() || undefined,
    })
    detections.value = r.detections
    detTotal.value = r.total
  } catch (e) {
    detections.value = []
    detTotal.value = 0
    detError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingDet.value = false
  }
}

async function loadVectors(): Promise<void> {
  loadingVec.value = true
  vecError.value = null
  try {
    const r = await fetchAttackVectors({ page: vecPage.value, pageSize: vecPageSize.value })
    vectors.value = r.vectors
  } catch (e) {
    vectors.value = []
    vecError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingVec.value = false
  }
}

function applyDetBlocked(v: '' | 'true' | 'false'): void {
  detBlocked.value = detBlocked.value === v ? '' : v
  detPage.value = 1
  void loadDetections()
}

function applyDetCategory(v: string): void {
  detCategory.value = detCategory.value === v ? '' : v
  detPage.value = 1
  void loadDetections()
}

function applyDetRisk(v: string): void {
  detRisk.value = detRisk.value === v ? '' : v
  detPage.value = 1
  void loadDetections()
}

function applyDetPageSize(n: number): void {
  detPageSize.value = n
  detPage.value = 1
  void loadDetections()
}

function applyVecPageSize(n: number): void {
  vecPageSize.value = n
  vecPage.value = 1
  void loadVectors()
}

function gotoDet(p: number): void {
  detPage.value = Math.max(1, p)
  void loadDetections()
}

function gotoVec(p: number): void {
  vecPage.value = Math.max(1, p)
  void loadVectors()
}

function clearDetFilters(): void {
  detBlocked.value = ''
  detRisk.value = ''
  detCategory.value = ''
  detPage.value = 1
  void loadDetections()
}

void loadStats()
void loadDetections()
void loadVectors()

/** ★ detections 有真 total ⇒ 精确分页。 */
const detHasMore = computed(() => detPage.value * detPageSize.value < detTotal.value)
const detTotalPages = computed(() => Math.max(1, Math.ceil(detTotal.value / detPageSize.value)))

/** ★ vectors 没有 total ⇒ 只能按「这页排满」近似。见坑 3。 */
const vecMaybeMore = computed(() => vectorPageLooksFull(vecPageSize.value, vectors.value.length))

/**
 * ★ `risk_level` 是 1..10 的数字字符串（**不是**等级名）。
 * 解析不出来就显示「—」，**绝不**硬套一个分档。
 */
function riskOf(d: InjectionDetection): number | null {
  return parseInjectionRiskLevel(d.risk_level)
}

function riskTone(n: number | null): 'danger' | 'warning' | 'muted' {
  if (n === null) return 'muted'
  if (n >= 8) return 'danger'
  if (n >= 5) return 'warning'
  return 'muted'
}

function actionTone(a: string): 'danger' | 'warning' | 'muted' {
  if (a === 'block' || a === 'terminate' || a === 'reject') return 'danger'
  if (a === 'replace' || a === 'redact' || a === 'remove' || a === 'quarantine') return 'warning'
  return 'muted'
}

onBeforeUnmount(() => {
  stats.value = null
  detections.value = []
  vectors.value = []
  statsError.value = null
  detError.value = null
  vecError.value = null
})
</script>

<template>
  <div class="view-root iv">
    <!-- ══════ 统计 ══════ -->
    <section class="iv__panel">
      <span class="iv__panel-title">{{ t('injection.stats') }}</span>

      <!-- ★★★★ 预聚合表 + 无行时全 0 -->
      <p class="iv__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injection.statsAggregatedNote') }}</span>
      </p>

      <p v-if="statsError" class="iv__msg iv__msg--err">{{ statsError }}</p>
      <template v-else-if="stats">
        <div class="iv__grid">
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.totalDetections') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.total_detections) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.blocked') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.blocked_count) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.affectedSessions') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.affected_sessions) }}</span>
          </span>
        </div>

        <p class="iv__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('injection.avgZeroNote') }}</span>
        </p>

        <div class="iv__grid">
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.sev_critical') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.critical_count) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.sev_high') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.high_count) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.sev_medium') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.medium_count) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.sev_low') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.low_count) }}</span>
          </span>
        </div>

        <div class="iv__grid">
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.avgScore') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.avg_score) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.maxScore') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.max_score) }}</span>
          </span>
          <span class="iv__cell">
            <span class="iv__cell-l">{{ t('injection.canaryLeak') }}</span>
            <span class="iv__cell-v">{{ fmtInt(stats.canary_leak_count) }}</span>
          </span>
        </div>

        <div class="iv__tags">
          <span class="iv__kv">
            <span class="iv__kv-l">{{ t('injection.action_approval') }}</span>
            <span class="iv__kv-v">{{ fmtInt(stats.approval_count) }}</span>
          </span>
          <span class="iv__kv">
            <span class="iv__kv-l">{{ t('injection.action_replaced') }}</span>
            <span class="iv__kv-v">{{ fmtInt(stats.replaced_count) }}</span>
          </span>
          <span class="iv__kv">
            <span class="iv__kv-l">{{ t('injection.action_terminated') }}</span>
            <span class="iv__kv-v">{{ fmtInt(stats.terminated_count) }}</span>
          </span>
        </div>
      </template>
    </section>

    <!-- ══════ 检测记录 ══════ -->
    <section class="iv__panel">
      <span class="iv__panel-title">{{ t('injection.detections') }}</span>

      <!-- ★★★★★ risk_level 是 1..10 的数字，不是等级名 -->
      <p class="iv__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injection.riskLevelNote') }}</span>
      </p>

      <div class="iv__chips" role="group" :aria-label="t('injection.blockedLabel')">
        <button
          v-for="v in (['', 'true', 'false'] as const)"
          :key="v || 'all'"
          type="button"
          class="iv__chip"
          :class="{ 'iv__chip--on': detBlocked === v }"
          @click="applyDetBlocked(v)"
        >
          {{ v === '' ? t('injection.blockedAll') : v === 'true' ? t('injection.blockedYes') : t('injection.blockedNo') }}
        </button>
      </div>

      <div class="iv__chips" role="group" :aria-label="t('injection.riskLabel')">
        <button
          v-for="n in [7, 8, 9, 10]"
          :key="n"
          type="button"
          class="iv__chip"
          :class="{ 'iv__chip--on': detRisk === String(n) }"
          @click="applyDetRisk(String(n))"
        >
          {{ t('injection.riskAtLeast', { n }) }}
        </button>
      </div>

      <div class="iv__chips" role="group" :aria-label="t('injection.categoryLabel')">
        <button
          v-for="c in INJECTION_CATEGORIES"
          :key="c"
          type="button"
          class="iv__chip iv__chip--sm"
          :class="{ 'iv__chip--on': detCategory === c }"
          @click="applyDetCategory(c)"
        >
          {{ t('injection.cat_' + c) }}
        </button>
      </div>

      <div class="iv__chips" role="group" :aria-label="t('injection.limitLabel')">
        <button
          v-for="n in PAGE_SIZE_CHOICES"
          :key="n"
          type="button"
          class="iv__chip"
          :class="{ 'iv__chip--on': detPageSize === n }"
          @click="applyDetPageSize(n)"
        >
          {{ t('injection.limitN', { n }) }}
        </button>
      </div>

      <div class="iv__row">
        <button type="button" class="iv__btn" @click="clearDetFilters">{{ t('common.clearFilters') }}</button>
        <span class="iv__meta">{{ t('injection.detPageInfo', { page: detPage, pages: detTotalPages, total: detTotal }) }}</span>
      </div>

      <p v-if="loadingDet" class="iv__msg">{{ t('common.loading') }}</p>
      <p v-if="detError" class="iv__msg iv__msg--err">
        {{ detError }}
        <span class="iv__sub">{{ t('injection.scanFailHint') }}</span>
      </p>
      <p v-else-if="!detections.length" class="iv__msg">{{ t('injection.noDetections') }}</p>

      <ul v-if="detections.length" class="iv__list">
        <li v-for="d in detections" :key="d.id" class="iv__item">
          <div class="iv__item-head">
            <StatusDot :tone="riskTone(riskOf(d))" />
            <!-- ★★ 数字，不是等级名 -->
            <!-- ★ riskOf 可能返回 null（响应形状不对），所以不能直接喂给 i18n 的 {n} -->
            <span v-if="riskOf(d) !== null" class="iv__risk">{{ t('injection.riskLevel', { n: riskOf(d) as number }) }}</span>
            <span v-else class="iv__risk iv__risk--bad">{{ t('injection.riskUnparsed') }}</span>
            <span class="iv__act" :class="`iv__act--${actionTone(d.action_taken)}`">{{ d.action_taken || '—' }}</span>
            <span v-if="d.blocked" class="iv__blocked">{{ t('injection.blockedYes') }}</span>
          </div>

          <div class="iv__kv-row">
            <span class="iv__kv">
              <span class="iv__kv-l">{{ t('injection.detectionScore') }}</span>
              <span class="iv__kv-v">{{ d.detection_score }}</span>
            </span>
            <span class="iv__kv">
              <span class="iv__kv-l">{{ t('injection.matchedRules', { n: d.matched_rules_count }) }}</span>
              <span class="iv__kv-v">{{ d.matched_rules || '—' }}</span>
            </span>
            <span v-if="d.llm_confidence !== null" class="iv__kv">
              <span class="iv__kv-l">{{ t('injection.llmConfidence') }}</span>
              <span class="iv__kv-v">{{ d.llm_confidence }}</span>
            </span>
          </div>

          <div v-if="d.categories.length" class="iv__tags">
            <span v-for="c in d.categories" :key="c" class="iv__tag">{{ t('injection.cat_' + c) }}</span>
          </div>
          <p v-if="d.evidence_text" class="iv__sample">{{ d.evidence_text }}</p>
          <p v-if="d.llm_reason" class="iv__meta">{{ d.llm_reason }}</p>
          <p v-if="d.canary_token_leaked" class="iv__warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('injection.canaryLeaked', { v: d.canary_token_leaked }) }}</span>
          </p>
          <p class="iv__meta">
            {{ t('injection.detectedAt', { t: relativeTime(d.detected_at) }) }}
            <span v-if="d.session_key"> · {{ t('injection.sessionPrefix') }} {{ d.session_key }}</span>
          </p>
        </li>
      </ul>

      <div v-if="detections.length" class="iv__row">
        <button type="button" class="iv__btn" :disabled="detPage === 1" @click="gotoDet(detPage - 1)">
          {{ t('injection.prev') }}
        </button>
        <button type="button" class="iv__btn" :disabled="!detHasMore" @click="gotoDet(detPage + 1)">
          {{ t('injection.next') }}
        </button>
      </div>
    </section>

    <!-- ══════ 攻击向量 ══════ -->
    <section class="iv__panel">
      <span class="iv__panel-title">{{ t('injection.attackVectors') }}</span>

      <!-- ★★ 没有 total ⇒ 只能近似 -->
      <p class="iv__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('injection.vectorsNoTotalNote') }}</span>
      </p>

      <div class="iv__chips" role="group" :aria-label="t('injection.limitLabel')">
        <button
          v-for="n in PAGE_SIZE_CHOICES"
          :key="n"
          type="button"
          class="iv__chip"
          :class="{ 'iv__chip--on': vecPageSize === n }"
          @click="applyVecPageSize(n)"
        >
          {{ t('injection.limitN', { n }) }}
        </button>
      </div>

      <p v-if="loadingVec" class="iv__msg">{{ t('common.loading') }}</p>
      <p v-if="vecError" class="iv__msg iv__msg--err">
        {{ vecError }}
        <span class="iv__sub">{{ t('injection.scanFailHint') }}</span>
      </p>
      <p v-else-if="!vectors.length" class="iv__msg">{{ t('injection.noVectors') }}</p>

      <ul v-if="vectors.length" class="iv__list">
        <li v-for="v of vectors" :key="v.id" class="iv__item">
          <div class="iv__item-head">
            <StatusDot :tone="v.severity >= 8 ? 'danger' : v.severity >= 5 ? 'warning' : 'muted'" />
            <span class="iv__sev">{{ t('injection.severity', { n: v.severity }) }}</span>
            <span v-if="v.source" class="iv__src">{{ v.source }}</span>
          </div>
          <p v-if="v.attack_text" class="iv__sample">{{ v.attack_text }}</p>
          <div v-if="v.categories.length" class="iv__tags">
            <span v-for="c in v.categories" :key="c" class="iv__tag">{{ t('injection.cat_' + c) }}</span>
          </div>
          <p class="iv__meta">
            {{ t('injection.createdAt', { t: relativeTime(v.created_at) }) }}
            <!-- ★ 键存在但值可能 null -->
            <span v-if="v.detected_at"> · {{ t('injection.detectedAt', { t: relativeTime(v.detected_at) }) }}</span>
          </p>
        </li>
      </ul>

      <div v-if="vectors.length" class="iv__row">
        <span class="iv__meta">
          {{ t('injection.vecPageInfo', { page: vecPage, more: vecMaybeMore ? t('injection.maybeMore') : '' }) }}
        </span>
        <button type="button" class="iv__btn" :disabled="vecPage === 1" @click="gotoVec(vecPage - 1)">
          {{ t('injection.prev') }}
        </button>
        <button type="button" class="iv__btn" :disabled="!vecMaybeMore" @click="gotoVec(vecPage + 1)">
          {{ t('injection.next') }}
        </button>
      </div>
    </section>

    <p class="iv__foot">{{ t('injection.severityLevelsNote', { levels: INJECTION_SEVERITY_LEVELS.join(' / ') }) }}</p>
  </div>
</template>

<style scoped>
.iv {
  padding: var(--app-space-3);
}
.iv__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.iv__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.iv__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
}
.iv__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.iv__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.iv__cell-v {
  font-size: 19px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.iv__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
  color: var(--app-text-secondary);
}
.iv__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.iv__sub {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  opacity: 0.85;
}
.iv__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.iv__warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 11px;
  line-height: 1.5;
  word-break: break-all;
}
.iv__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.iv__sample {
  margin: 4px 0 0;
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 11px;
  line-height: 1.5;
  word-break: break-word;
  white-space: pre-wrap;
}
.iv__chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.iv__chip {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.iv__chip--sm {
  font-size: 11px;
  padding: 0 10px;
}
.iv__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.iv__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.iv__btn {
  min-height: 48px;
  min-width: 72px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.iv__btn[disabled] {
  opacity: 0.45;
}
.iv__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.iv__item {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.iv__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.iv__risk {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.iv__risk--bad {
  color: var(--app-text-muted);
  font-weight: 400;
  font-size: 11px;
}
.iv__act {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  font-size: 11px;
  font-weight: 700;
}
.iv__act--danger {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.iv__act--warning {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.iv__act--muted {
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
}
.iv__blocked {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 10px;
  line-height: 1.6;
}
.iv__sev {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.iv__src {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 11px;
}
.iv__kv-row,
.iv__tags {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: 4px;
}
.iv__kv {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.iv__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.iv__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}
.iv__tag {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-primary-soft);
  color: var(--app-primary);
  font-size: 11px;
  font-weight: 600;
  word-break: break-all;
}
.iv__foot {
  margin: 0;
  font-size: 10px;
  color: var(--app-text-muted);
  line-height: 1.5;
}
</style>