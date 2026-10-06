<script setup lang="ts">
// ComplianceHitsView — 输出合规「命中与复核」面（stats + records + review-queue，**admin 档**）。
//
// 它答的是「**命中了什么、有没有等人复核**」。与策略/词库面
// （/compliance-policy）分开，因为那一族字段多且是配置而非现象。
//
// ⚠️★★★ 六个后端语义（详见 api/outputCompliance.ts 文件头）：
//
// (1) ★★★★★ `stats` 里有三个字段**不是真值**：
//     `jailbreak_hits` 恒 0（本仓无该检测器）、`avg_latency_ms` 恒 0（表无此列）、
//     `total_checks` **恒等于** `total_issues`。⇒ 页面分别标注，绝不说成「检查次数」。
// (2) ★★★★ `stats` 只给 pii/secret/toxic 三类计数，而 issue_type 有**五个**取值
//     ⇒ internal_ip 与 bias 的命中在 stats 里没有字段。
// (3) ★★★★★ `stats` 单路失败**静默回 0** ⇒ 页面上的 0 可能是查询失败。
// (4) ★★★★★ `records` 的 `content_preview` **只在 redacted=true 时有内容**
//     ⇒ 空预览 = 该行未脱敏（安全约束），不是「没记到内容」。
// (5) ★★★★ `review-queue` / `keywords` 可能**整个端点 500**（可空列被裸扫）
//     ⇒ 500 时渲染成「后端扫描失败」，**绝不**退化成「队列为空」。
// (6) ★★★ `review-queue` 响应**没有 total**；默认条数 queue=20 / records=50 / feedback=20。
// (7) ★★★★★★ **`feedback` 的响应键是 `feedback`，不是 `items`。**
//     后端 `listFeedback`（admin/output_compliance_handler.go:711）写的是
//     `"feedback": items`，而同 handler 的 `review-queue`（:602）写的是 `"items"`
//     ⇒ 同族两个列表端点**键不同名**。本页两个面板各取各的键。
//     ★ 本模块第一版把这里写成了 `items`，而夹具也照着 `items` 写 ⇒ 用例全绿、
//       对真后端 100% 抛错。见 api/outputCompliance.ts 坑 16。
// (8) ★★★★ `feedback.reporter` / `comment` 的 JSON tag 带 `omitempty`
//     ⇒ **键可能整个不存在**（不是「值为空」）。
// (9) ★★★ `feedback.created_at` 由 pgx 把 TIMESTAMPTZ **直接扫进字符串、没有 `.UTC()`**
//     ⇒ 与 queue/keywords 同族，格式未实测；解析不了就原样回显。
// (10) ★★★ `type` 查询参数**没有 allowlist**（DB CHECK 只约束写入）
//     ⇒ 只发 `COMPLIANCE_FEEDBACK_TYPES` 这三个字面值。
//
// ★ 写操作 approve/reject/feedback(POST) 本页一律不碰。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchComplianceStats,
  fetchComplianceRecords,
  fetchComplianceReviewQueue,
  fetchComplianceFeedback,
  COMPLIANCE_QUEUE_STATUSES,
  COMPLIANCE_FEEDBACK_TYPES,
  COMPLIANCE_ISSUE_TYPES,
  COMPLIANCE_RECORDS_DEFAULT_LIMIT,
  COMPLIANCE_QUEUE_DEFAULT_LIMIT,
  COMPLIANCE_FEEDBACK_DEFAULT_LIMIT,
  COMPLIANCE_LIMIT_MAX,
  type ComplianceStats,
  type ComplianceRecord,
  type ComplianceQueueItem,
  type ComplianceQueueStatus,
  type ComplianceFeedback,
  type ComplianceFeedbackType,
} from '@/api/outputCompliance'

useHyperPage({ title: () => t('compliance.title') })

const RECORD_LIMIT_CHOICES = [COMPLIANCE_RECORDS_DEFAULT_LIMIT, 100, COMPLIANCE_LIMIT_MAX]

const recLimit = ref(COMPLIANCE_RECORDS_DEFAULT_LIMIT)
const recOffset = ref(0)
const checkType = ref('')
const hitType = ref('')

const queueStatus = ref<ComplianceQueueStatus>('pending')
const queueOffset = ref(0)

/** ★ 空串 = 后端不过滤（`listFeedback` 只在 type != "" 时加条件）。 */
const fbType = ref<ComplianceFeedbackType | ''>('')
const fbOffset = ref(0)

const stats = ref<ComplianceStats | null>(null)
const records = ref<ComplianceRecord[]>([])
const recTotal = ref(0)
const queue = ref<ComplianceQueueItem[]>([])
const feedback = ref<ComplianceFeedback[]>([])

const statsError = ref<string | null>(null)
const recError = ref<string | null>(null)
const queueError = ref<string | null>(null)
const fbError = ref<string | null>(null)
const loadingRec = ref(false)
const loadingQueue = ref(false)
const loadingFb = ref(false)

async function loadStats(): Promise<void> {
  try {
    stats.value = await fetchComplianceStats()
    statsError.value = null
  } catch (e) {
    stats.value = null
    statsError.value = (e as Error)?.message || t('common.error')
  }
}

async function loadRecords(): Promise<void> {
  loadingRec.value = true
  recError.value = null
  try {
    const r = await fetchComplianceRecords({
      limit: recLimit.value,
      offset: recOffset.value,
      checkType: checkType.value.trim() || undefined,
      hitType: hitType.value.trim() || undefined,
    })
    records.value = r.records
    recTotal.value = r.total
  } catch (e) {
    // ★★ 抛错**不许**退化成空列表：那会把「后端扫描失败」显示成「没有命中」。
    records.value = []
    recTotal.value = 0
    recError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingRec.value = false
  }
}

async function loadQueue(): Promise<void> {
  loadingQueue.value = true
  queueError.value = null
  try {
    const r = await fetchComplianceReviewQueue({
      status: queueStatus.value,
      limit: COMPLIANCE_QUEUE_DEFAULT_LIMIT,
      offset: queueOffset.value,
    })
    queue.value = r.items
  } catch (e) {
    queue.value = []
    queueError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingQueue.value = false
  }
}

function applyRecordLimit(n: number): void {
  recLimit.value = n
  recOffset.value = 0
  void loadRecords()
}

function applyCheckType(v: string): void {
  checkType.value = checkType.value === v ? '' : v
  recOffset.value = 0
  void loadRecords()
}

function applyQueueStatus(s: ComplianceQueueStatus): void {
  queueStatus.value = s
  queueOffset.value = 0
  void loadQueue()
}

async function loadFeedback(): Promise<void> {
  loadingFb.value = true
  fbError.value = null
  try {
    const r = await fetchComplianceFeedback({
      type: fbType.value === '' ? undefined : fbType.value,
      limit: COMPLIANCE_FEEDBACK_DEFAULT_LIMIT,
      offset: fbOffset.value,
    })
    // ★★★ 键是 `feedback`，**不是** `items`（后端 :711）。见坑 16。
    feedback.value = r.feedback
  } catch (e) {
    // ★★ 抛错**不许**退化成空列表：那会把「后端扫描失败」显示成「还没有人复核过」。
    feedback.value = []
    fbError.value = (e as Error)?.message || t('common.error')
  } finally {
    loadingFb.value = false
  }
}

function applyFeedbackType(v: ComplianceFeedbackType | ''): void {
  fbType.value = fbType.value === v ? '' : v
  fbOffset.value = 0
  void loadFeedback()
}

function gotoFeedback(next: number): void {
  fbOffset.value = Math.max(0, next)
  void loadFeedback()
}

function gotoRecords(next: number): void {
  recOffset.value = Math.max(0, next)
  void loadRecords()
}

function gotoQueue(next: number): void {
  queueOffset.value = Math.max(0, next)
  void loadQueue()
}

function submitRecords(): void {
  recOffset.value = 0
  void loadRecords()
}

function clearFilters(): void {
  checkType.value = ''
  hitType.value = ''
  recOffset.value = 0
  void loadRecords()
}

void loadStats()
void loadRecords()
void loadQueue()
void loadFeedback()

const recHasMore = computed(() => recOffset.value + records.value.length < recTotal.value)
/**
 * ★★ 队列响应**没有 total** ⇒ 只能按「这页刚好满」推测还有下一页。
 * 这本身就是近似，所以页面上说「可能还有更多」而不是「共 N 条」。
 */
const queueMaybeMore = computed(() => queue.value.length >= COMPLIANCE_QUEUE_DEFAULT_LIMIT)

/**
 * ★★ `feedback` 响应**同样没有 total** ⇒ 与队列一样只能按「这页刚好满」近似。
 * 它和队列是同一复核闭环的两半（待复核 / 复核结论），所以放同一页相邻两块。
 */
const fbMaybeMore = computed(() => feedback.value.length >= COMPLIANCE_FEEDBACK_DEFAULT_LIMIT)

/** ★ 五类取值域里，stats 只统计三类；差额就是另两类的命中（见坑 2）。 */
const uncountedHits = computed(() => {
  if (!stats.value) return 0
  const { total_issues, pii_hits, secret_hits, toxicity_hits } = stats.value
  return total_issues - (pii_hits + secret_hits + toxicity_hits)
})

function severityTone(s: number): 'danger' | 'warning' | 'muted' {
  if (s >= 8) return 'danger'
  if (s >= 5) return 'warning'
  return 'muted'
}

function severityLabel(s: number): string {
  if (s >= 8) return t('compliance.sev_high')
  if (s >= 5) return t('compliance.sev_mid')
  return t('compliance.sev_low')
}

/** ★ 只有 redacted=true 才有 preview（安全约束）。见坑 4。 */
function hasPreview(r: ComplianceRecord): boolean {
  return r.redacted && r.content_preview !== ''
}

/**
 * ★ `reporter` / `comment` 的 JSON tag 带 `omitempty` ⇒ **键可能整个不存在**。
 * 这里统一走「有值才显示」，缺键与空串都不会渲染出空白行。
 */
function fbMeta(f: ComplianceFeedback): string {
  const who = f.reporter || t('compliance.none')
  const what = f.comment || t('compliance.noComment')
  return `${who} · ${what}`
}

/**
 * ★ 三种结论的语义方向不同，所以色也不同：
 * `false_positive`（误报）= 引擎报错了 ⇒ **danger**；
 * `false_negative`（漏报）= 引擎漏了   ⇒ **warning**；
 * `correct`（正确）        = 判对了   ⇒ **muted**（正常态，不该抢眼）。
 */
function fbTypeTone(tp: string): 'danger' | 'warning' | 'muted' {
  if (tp === 'false_positive') return 'danger'
  if (tp === 'false_negative') return 'warning'
  return 'muted'
}

onBeforeUnmount(() => {
  stats.value = null
  records.value = []
  queue.value = []
  feedback.value = []
  statsError.value = null
  recError.value = null
  queueError.value = null
  fbError.value = null
})
</script>

<template>
  <div class="view-root ch">
    <!-- ══════ 概览 ══════ -->
    <section class="ch__panel">
      <span class="ch__panel-title">{{ t('compliance.overview') }}</span>

      <p v-if="statsError" class="ch__msg ch__msg--err">{{ statsError }}</p>
      <template v-else-if="stats">
        <div class="ch__grid">
          <span class="ch__cell">
            <span class="ch__cell-l">{{ t('compliance.totalIssues') }}</span>
            <span class="ch__cell-v">{{ fmtInt(stats.total_issues) }}</span>
          </span>
          <span class="ch__cell">
            <span class="ch__cell-l">{{ t('compliance.blocked') }}</span>
            <span class="ch__cell-v">{{ fmtInt(stats.blocked) }}</span>
          </span>
          <span class="ch__cell">
            <span class="ch__cell-l">{{ t('compliance.pendingReviews') }}</span>
            <span class="ch__cell-v">{{ fmtInt(stats.pending_reviews) }}</span>
          </span>
        </div>

        <!-- ★★ total_checks 恒等于 total_issues，不是「检查次数」 -->
        <p class="ch__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliance.totalChecksNote') }}</span>
        </p>
        <!-- ★★ 两个恒为 0 的字段 -->
        <p class="ch__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliance.alwaysZeroNote') }}</span>
        </p>
        <!-- ★★ 单路失败静默回 0 -->
        <p class="ch__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliance.silentZeroNote') }}</span>
        </p>

        <div class="ch__grid">
          <span class="ch__cell">
            <span class="ch__cell-l">{{ t('compliance.piiHits') }}</span>
            <span class="ch__cell-v">{{ fmtInt(stats.pii_hits) }}</span>
          </span>
          <span class="ch__cell">
            <span class="ch__cell-l">{{ t('compliance.secretHits') }}</span>
            <span class="ch__cell-v">{{ fmtInt(stats.secret_hits) }}</span>
          </span>
          <span class="ch__cell">
            <span class="ch__cell-l">{{ t('compliance.toxicityHits') }}</span>
            <span class="ch__cell-v">{{ fmtInt(stats.toxicity_hits) }}</span>
          </span>
        </div>
        <!-- ★★ stats 只统计三类，差额是另两类 -->
        <p v-if="uncountedHits > 0" class="ch__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('compliance.uncountedNote', { n: uncountedHits, types: COMPLIANCE_ISSUE_TYPES.slice(3).join(' / ') }) }}</span>
        </p>
        <p class="ch__meta">
          {{ stats.last_updated ? t('compliance.lastDetected', { t: relativeTime(stats.last_updated) }) : t('compliance.neverDetected') }}
        </p>
      </template>
    </section>

    <!-- ══════ 命中记录 ══════ -->
    <section class="ch__panel">
      <span class="ch__panel-title">{{ t('compliance.records') }}</span>

      <!-- ★★★★ 空预览是安全约束，不是「没记到」 -->
      <p class="ch__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('compliance.previewRuleNote') }}</span>
      </p>

      <div class="ch__chips" role="group" :aria-label="t('compliance.checkTypeLabel')">
        <button
          v-for="ct in COMPLIANCE_ISSUE_TYPES"
          :key="ct"
          type="button"
          class="ch__chip"
          :class="{ 'ch__chip--on': checkType === ct }"
          @click="applyCheckType(ct)"
        >
          {{ t('compliance.issue_' + ct) }}
        </button>
      </div>

      <form class="ch__form" @submit.prevent="submitRecords">
        <label class="ch__field">
          <span>{{ t('compliance.hitType') }}</span>
          <input v-model="hitType" class="ch__input" :placeholder="t('compliance.exactHint')" autocomplete="off" spellcheck="false" />
        </label>
        <div class="ch__chips" role="group" :aria-label="t('compliance.limitLabel')">
          <button
            v-for="n in RECORD_LIMIT_CHOICES"
            :key="n"
            type="button"
            class="ch__chip"
            :class="{ 'ch__chip--on': recLimit === n }"
            @click="applyRecordLimit(n)"
          >
            {{ t('compliance.limitN', { n }) }}
          </button>
        </div>
        <div class="ch__row">
          <button type="submit" class="ch__btn ch__btn--go">{{ t('compliance.query') }}</button>
          <button type="button" class="ch__btn" @click="clearFilters">{{ t('common.clearFilters') }}</button>
        </div>
      </form>

      <p v-if="loadingRec" class="ch__msg">{{ t('common.loading') }}</p>
      <p v-if="recError" class="ch__msg ch__msg--err">
        {{ recError }}
        <span class="ch__sub">{{ t('compliance.scanFailHint') }}</span>
      </p>
      <p v-else-if="!records.length" class="ch__msg">{{ t('compliance.noRecords') }}</p>

      <ul v-if="records.length" class="ch__list">
        <li v-for="r in records" :key="r.id" class="ch__item">
          <div class="ch__item-head">
            <StatusDot :tone="severityTone(r.severity)" />
            <span class="ch__type">{{ t('compliance.issue_' + r.check_type) }}</span>
            <span v-if="r.hit_type" class="ch__hit">{{ r.hit_type }}</span>
            <span class="ch__sev">{{ severityLabel(r.severity) }} {{ r.severity }}</span>
          </div>
          <!-- ★★ 只有 redacted=true 才有内容 -->
          <p v-if="hasPreview(r)" class="ch__preview">{{ r.content_preview }}</p>
          <p v-else class="ch__note">
            <AppIcon name="alert" :size="13" />
            <span>{{ r.redacted ? t('compliance.previewEmpty') : t('compliance.notRedacted') }}</span>
          </p>
          <p class="ch__meta">
            {{ t('compliance.createdAt', { t: relativeTime(r.created_at) }) }}
            <span v-if="r.session_id"> · {{ t('compliance.sessionPrefix') }} {{ r.session_id }}</span>
          </p>
        </li>
      </ul>

      <div v-if="records.length || recOffset > 0" class="ch__row">
        <button type="button" class="ch__btn" :disabled="recOffset === 0" @click="gotoRecords(recOffset - recLimit)">
          {{ t('compliance.prev') }}
        </button>
        <span class="ch__meta">{{ t('compliance.pageInfo', { from: recOffset + 1, to: recOffset + records.length, total: recTotal }) }}</span>
        <button type="button" class="ch__btn" :disabled="!recHasMore" @click="gotoRecords(recOffset + recLimit)">
          {{ t('compliance.next') }}
        </button>
      </div>
    </section>

    <!-- ══════ 复核队列 ══════ -->
    <section class="ch__panel">
      <span class="ch__panel-title">{{ t('compliance.reviewQueue') }}</span>

      <!-- ★★ 响应没有 total，只能近似说「可能还有更多」 -->
      <p class="ch__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('compliance.queueNoTotalNote') }}</span>
      </p>

      <div class="ch__chips" role="group" :aria-label="t('compliance.queueStatusLabel')">
        <button
          v-for="s in COMPLIANCE_QUEUE_STATUSES"
          :key="s"
          type="button"
          class="ch__chip"
          :class="{ 'ch__chip--on': queueStatus === s }"
          @click="applyQueueStatus(s)"
        >
          {{ t('compliance.qstatus_' + s) }}
        </button>
      </div>

      <p v-if="loadingQueue" class="ch__msg">{{ t('common.loading') }}</p>
      <p v-if="queueError" class="ch__msg ch__msg--err">
        {{ queueError }}
        <span class="ch__sub">{{ t('compliance.scanFailHint') }}</span>
      </p>
      <p v-else-if="!queue.length" class="ch__msg">{{ t('compliance.queueEmpty', { s: t('compliance.qstatus_' + queueStatus) }) }}</p>

      <ul v-if="queue.length" class="ch__list">
        <li v-for="q in queue" :key="q.id" class="ch__item">
          <div class="ch__item-head">
            <StatusDot :tone="severityTone(q.severity)" />
            <span class="ch__type">{{ t('compliance.issue_' + q.issue_type) }}</span>
            <span v-if="q.issue_subtype" class="ch__hit">{{ q.issue_subtype }}</span>
            <span class="ch__sev">{{ severityLabel(q.severity) }} {{ q.severity }}</span>
          </div>
          <p class="ch__meta">{{ t('compliance.requestId', { id: q.request_id }) }}</p>
          <!-- ★★ 这几列是**可空列**：缺失要显示「无」，不能显示空 -->
          <p class="ch__meta">{{ t('compliance.sessionPrefix') }} {{ q.session_key || t('compliance.none') }}</p>
          <p v-if="q.reviewer || q.review_comment" class="ch__meta">
            {{ t('compliance.reviewedBy', { who: q.reviewer || t('compliance.none') }) }}
            <span v-if="q.review_comment"> · {{ q.review_comment }}</span>
          </p>
          <p v-if="q.reviewed_at" class="ch__meta">{{ t('compliance.reviewedAt', { t: relativeTime(q.reviewed_at) }) }}</p>
        </li>
      </ul>

      <div v-if="queue.length || queueOffset > 0" class="ch__row">
        <button type="button" class="ch__btn" :disabled="queueOffset === 0" @click="gotoQueue(queueOffset - COMPLIANCE_QUEUE_DEFAULT_LIMIT)">
          {{ t('compliance.prev') }}
        </button>
        <span class="ch__meta">
          {{ t('compliance.queuePage', { from: queueOffset + 1, to: queueOffset + queue.length, more: queueMaybeMore ? t('compliance.maybeMore') : '' }) }}
        </span>
        <button type="button" class="ch__btn" :disabled="!queueMaybeMore" @click="gotoQueue(queueOffset + COMPLIANCE_QUEUE_DEFAULT_LIMIT)">
          {{ t('compliance.next') }}
        </button>
      </div>
    </section>

    <!-- ══════ 复核结论（feedback） ══════ -->
    <section class="ch__panel">
      <span class="ch__panel-title">{{ t('compliance.feedback') }}</span>

      <!-- ★★ 与队列同样的两条限制：没有 total、默认 20 条 -->
      <p class="ch__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('compliance.feedbackNoTotalNote') }}</span>
      </p>
      <!-- ★★★ 响应键是 feedback 而不是 items；同族两个列表键不同名 -->
      <p class="ch__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('compliance.feedbackKeyNote') }}</span>
      </p>

      <div class="ch__chips" role="group" :aria-label="t('compliance.feedbackTypeLabel')">
        <button
          type="button"
          class="ch__chip"
          :class="{ 'ch__chip--on': fbType === '' }"
          @click="applyFeedbackType('')"
        >
          {{ t('compliance.fb_all') }}
        </button>
        <button
          v-for="tp in COMPLIANCE_FEEDBACK_TYPES"
          :key="tp"
          type="button"
          class="ch__chip"
          :class="{ 'ch__chip--on': fbType === tp }"
          @click="applyFeedbackType(tp)"
        >
          {{ t('compliance.ftype_' + tp) }}
        </button>
      </div>

      <p v-if="loadingFb" class="ch__msg">{{ t('common.loading') }}</p>
      <p v-if="fbError" class="ch__msg ch__msg--err">
        {{ fbError }}
        <span class="ch__sub">{{ t('compliance.scanFailHint') }}</span>
      </p>
      <p v-else-if="!feedback.length" class="ch__msg">{{ t('compliance.feedbackEmpty') }}</p>

      <ul v-if="feedback.length" class="ch__list">
        <li v-for="f in feedback" :key="f.id" class="ch__item">
          <div class="ch__item-head">
            <StatusDot :tone="fbTypeTone(f.feedback_type)" />
            <span class="ch__type">{{ t('compliance.ftype_' + f.feedback_type) }}</span>
            <span class="ch__hit">{{ f.feedback_type }}</span>
          </div>
          <p class="ch__meta">{{ t('compliance.auditRef', { id: f.audit_id }) }}</p>
          <!-- ★ reporter/comment 带 omitempty ⇒ 键可能整个不存在，不渲染空白 -->
          <p class="ch__meta">{{ fbMeta(f) }}</p>
          <p class="ch__meta">{{ t('compliance.feedbackAt', { t: relativeTime(f.created_at) }) }}</p>
        </li>
      </ul>

      <div v-if="feedback.length || fbOffset > 0" class="ch__row">
        <button type="button" class="ch__btn" :disabled="fbOffset === 0" @click="gotoFeedback(fbOffset - COMPLIANCE_FEEDBACK_DEFAULT_LIMIT)">
          {{ t('compliance.prev') }}
        </button>
        <span class="ch__meta">
          {{ t('compliance.queuePage', { from: fbOffset + 1, to: fbOffset + feedback.length, more: fbMaybeMore ? t('compliance.maybeMore') : '' }) }}
        </span>
        <button type="button" class="ch__btn" :disabled="!fbMaybeMore" @click="gotoFeedback(fbOffset + COMPLIANCE_FEEDBACK_DEFAULT_LIMIT)">
          {{ t('compliance.next') }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ch {
  padding: var(--app-space-3);
}
.ch__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ch__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.ch__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
}
.ch__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.ch__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ch__cell-v {
  font-size: 20px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ch__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ch__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ch__sub {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  opacity: 0.85;
}
.ch__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ch__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.ch__preview {
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
.ch__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin: var(--app-space-2) 0;
}
.ch__field {
  display: block;
}
.ch__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ch__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ch__chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.ch__chip {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ch__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.ch__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.ch__btn {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ch__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.ch__btn[disabled] {
  opacity: 0.45;
}
.ch__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.ch__item {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.ch__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ch__type {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.ch__hit {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 11px;
  word-break: break-all;
}
.ch__sev {
  font-size: 11px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
</style>