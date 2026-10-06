<script setup lang="ts">
// PendingResponsesView — 待处理响应排障（/api/admin/pending-responses{,/stats,/{id}}，**admin 档**）。
//
// 它答的是一个很具体的运维问题：**「网关现在有没有卡住的请求？」**
// 与已上移的 /sessions-online（在线会话）、/routing-opt（优化器效果）、
// /data-lifecycle（存储）互不重叠。
//
// ⚠️★★★ 九个后端语义（详见 api/pendingResponses.ts 文件头）：
//
// (1) ★★★★ `status` 过滤是**假的**：底层只扫 `in_progress`，发 `completed`/`failed`
//     只会**静默拿到空数组**。⇒ 本页**不提供**那些选项。
// (2) ★★★★ 列表里的 `provider_id` / `is_stream` / `bytes_buffered` **恒为 0/false**
//     （`StaleEntry` 根本没有这三个字段），而 JSON 没有 omitempty
//     ⇒ **看起来像真值**。⇒ 本页**不显示**这三列，改为一句说明 + 详情页看真值。
// (3) ★★★ 时间是 Unix **秒**，不是 ISO。
// (4) ★★★ `stats.by_status` 只有一个键且恒等于 `total` ⇒ 没有信息量，不展示。
// (5) ★★★ `oldest_created_at` 为 0 = 「没有条目」，不是 1970 年。
// (6) ★★ limit 越界静默回落/clamp、offset 越界静默返空，都不报错。
// (7) ★★ 列表按租户过滤（tenant_admin 看不到别家），但响应**不返回租户**。
// (8) ★★ 详情 404 与「跨租户不可见」共用一个码 ⇒ 那是「查不到」，不是出错。
// (9) ★ 错误信封是**嵌套 JSON**（与 routing-opt 的 text/plain 不同族）。
//
// ★ 写操作 `DELETE /api/admin/pending-responses/{id}` 不在本页。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchPendingList,
  fetchPendingStats,
  fetchPendingDetail,
  pendingUnixToIso,
  pendingAgeBand,
  PENDING_LIMIT_DEFAULT,
  PENDING_LIMIT_MAX,
  type PendingListEntry,
  type PendingDetail,
  type PendingStats,
  type PendingAgeBand,
} from '@/api/pendingResponses'

useHyperPage({ title: () => t('pending.title') })

const LIMIT_CHOICES = [PENDING_LIMIT_DEFAULT, 100, PENDING_LIMIT_MAX]

const limit = ref(PENDING_LIMIT_DEFAULT)
const offset = ref(0)
const sessionFilter = ref('')

const entries = ref<PendingListEntry[]>([])
const total = ref(0)
const stats = ref<PendingStats | null>(null)

const loading = ref(false)
const error = ref<string | null>(null)
const statsError = ref<string | null>(null)

/** ★ 详情是**独立**的一路请求：它有自己的错误位，不许清空列表。 */
const detail = ref<PendingDetail | null>(null)
const detailLoading = ref(false)
/** ★ 404 = 「这个会话没有待处理响应」/「不在你的租户里」，**不是**服务出错。 */
const detailMissing = ref(false)
const detailError = ref<string | null>(null)

async function loadStats(): Promise<void> {
  try {
    stats.value = await fetchPendingStats()
    statsError.value = null
  } catch (e) {
    stats.value = null
    statsError.value = (e as Error)?.message || t('common.error')
  }
}

async function loadList(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const r = await fetchPendingList({
      limit: limit.value,
      offset: offset.value,
      sessionId: sessionFilter.value.trim() || undefined,
    })
    entries.value = r.entries
    total.value = r.count
    // ★ 后端回显的 offset 可能被修正（越界时），跟着它走。
    offset.value = r.offset
  } catch (e) {
    entries.value = []
    total.value = 0
    error.value = (e as Error)?.message || t('common.error')
  } finally {
    loading.value = false
  }
}

async function loadDetail(sessionId: string): Promise<void> {
  detailLoading.value = true
  detailError.value = null
  detailMissing.value = false
  detail.value = null
  try {
    detail.value = await fetchPendingDetail(sessionId)
  } catch (e) {
    const status = (e as { status?: number })?.status
    if (status === 404) {
      // ★ 404 同时表示「没有」与「不在你的租户里」—— 两者都渲染成「查不到」。
      detailMissing.value = true
    } else {
      detailError.value = (e as Error)?.message || t('common.error')
    }
  } finally {
    detailLoading.value = false
  }
}

function reload(): void {
  void loadList()
  void loadStats()
}

function applyLimit(n: number): void {
  limit.value = n
  offset.value = 0
  reload()
}

function submitFilter(): void {
  offset.value = 0
  reload()
}

function clearFilter(): void {
  sessionFilter.value = ''
  offset.value = 0
  reload()
}

function goto(next: number): void {
  offset.value = Math.max(0, next)
  reload()
}

function openDetail(entry: PendingListEntry): void {
  void loadDetail(entry.session_id)
}

void loadList()
void loadStats()

const hasMore = computed(() => offset.value + entries.value.length < total.value)
const oldestIso = computed(() => (stats.value ? pendingUnixToIso(stats.value.oldest_created_at) : undefined))

/** ★ 0 = 没有条目，不是 1970 年。见坑 5。 */
const hasAny = computed(() => (stats.value?.total ?? 0) > 0)

/**
 * Unix 秒 → 相对时间文案。
 *
 * ★ 不能直接把秒丢给 `relativeTime()`（它只认 ISO，会原样回显那个数字），
 * 也不能在换算失败时回落成「从未」—— 那会读成「这个请求从来没发生过」。
 */
function relOf(sec: number | null | undefined): string {
  const iso = pendingUnixToIso(sec)
  return iso ? relativeTime(iso) : t('pending.unknownTime')
}

function bandTone(b: PendingAgeBand): 'success' | 'warning' | 'danger' | 'muted' {
  if (b === 'over_10m') return 'danger'
  if (b === 'under_10m') return 'warning'
  if (b === 'under_1m') return 'success'
  return 'muted'
}

onBeforeUnmount(() => {
  entries.value = []
  stats.value = null
  detail.value = null
  error.value = null
  statsError.value = null
  detailError.value = null
  detailMissing.value = false
})
</script>

<template>
  <div class="view-root pd">
    <p v-if="loading" class="pd__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 概览 ══════ -->
    <section class="pd__panel">
      <span class="pd__panel-title">{{ t('pending.overview') }}</span>

      <p v-if="statsError" class="pd__msg pd__msg--err">{{ statsError }}</p>
      <template v-else-if="stats">
        <div class="pd__headline">
          <StatusDot :tone="hasAny ? 'warning' : 'muted'" />
          <span class="pd__big">{{ fmtInt(stats.total) }}</span>
        </div>

        <!-- ★★ by_status 恒等于 total 且只有一个键（见坑 4）⇒ 不展示它 -->
        <p class="pd__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('pending.onlyInProgress') }}</span>
        </p>
        <p class="pd__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('pending.noStatusFilterNote') }}</span>
        </p>

        <!-- ★ 0 = 没有条目，不是 1970 年（见坑 5） -->
        <div class="pd__grid">
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.oldest') }}</span>
            <span class="pd__cell-v">
              {{ oldestIso ? relativeTime(oldestIso) : t('pending.noEntries') }}
            </span>
          </span>
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.scanned') }}</span>
            <span class="pd__cell-v">{{ fmtInt(total) }}</span>
          </span>
        </div>
        <p class="pd__meta">{{ t('pending.tenantScopeNote') }}</p>
      </template>
    </section>

    <!-- ══════ 筛选 ══════ -->
    <section class="pd__panel">
      <span class="pd__panel-title">{{ t('pending.filter') }}</span>
      <form class="pd__form" @submit.prevent="submitFilter">
        <label class="pd__field">
          <span>{{ t('pending.sessionId') }}</span>
          <input
            v-model="sessionFilter"
            class="pd__input"
            :placeholder="t('pending.sessionIdHint')"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
          />
        </label>
        <div class="pd__chips" role="group" :aria-label="t('pending.limitLabel')">
          <button
            v-for="n in LIMIT_CHOICES"
            :key="n"
            type="button"
            class="pd__chip"
            :class="{ 'pd__chip--on': limit === n }"
            @click="applyLimit(n)"
          >
            {{ t('pending.limitN', { n }) }}
          </button>
        </div>
        <div class="pd__row">
          <button type="submit" class="pd__btn pd__btn--go">{{ t('pending.query') }}</button>
          <button type="button" class="pd__btn" @click="clearFilter">{{ t('common.clearFilters') }}</button>
        </div>
      </form>
    </section>

    <!-- ══════ 列表 ══════ -->
    <section class="pd__panel">
      <span class="pd__panel-title">{{ t('pending.list') }}</span>

      <!-- ★★★ 这三个字段在列表端点恒为 0/false，不能当真值显示 -->
      <p class="pd__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('pending.listFieldsFake') }}</span>
      </p>

      <p v-if="error" class="pd__msg pd__msg--err">{{ error }}</p>
      <p v-else-if="!entries.length && !loading" class="pd__msg">{{ t('pending.noPending') }}</p>

      <ul v-if="entries.length" class="pd__list">
        <li v-for="e in entries" :key="e.session_id + '|' + e.request_id" class="pd__item">
          <button type="button" class="pd__rowbtn" @click="openDetail(e)">
            <div class="pd__item-head">
              <StatusDot :tone="bandTone(pendingAgeBand(e.age_seconds))" />
              <span class="pd__sid">{{ e.session_id }}</span>
            </div>
            <p class="pd__meta">
              {{ t('pending.ageLine', { t: relOf(e.created_at) }) }}
              <span class="pd__badge">{{ t('pending.band_' + pendingAgeBand(e.age_seconds)) }}</span>
            </p>
            <p class="pd__meta">{{ t('pending.requestId', { id: e.request_id }) }}</p>
          </button>
        </li>
      </ul>

      <!-- ★★ 分页：offset 越界后端返空且不报错，所以按钮只能靠本地总数推 -->
      <div v-if="entries.length || offset > 0" class="pd__row">
        <button type="button" class="pd__btn" :disabled="offset === 0" @click="goto(offset - limit)">
          {{ t('pending.prev') }}
        </button>
        <span class="pd__meta">{{ t('pending.pageInfo', { from: offset + 1, to: offset + entries.length, total }) }}</span>
        <button type="button" class="pd__btn" :disabled="!hasMore" @click="goto(offset + limit)">
          {{ t('pending.next') }}
        </button>
      </div>
    </section>

    <!-- ══════ 详情 ══════ -->
    <section class="pd__panel">
      <span class="pd__panel-title">{{ t('pending.detail') }}</span>

      <p v-if="detailLoading" class="pd__msg">{{ t('common.loading') }}</p>
      <!-- ★ 404 = 查不到（含「不在你的租户里」），**不是**错误 -->
      <p v-else-if="detailMissing" class="pd__msg">{{ t('pending.detailMissing') }}</p>
      <p v-else-if="detailError" class="pd__msg pd__msg--err">{{ detailError }}</p>
      <p v-else-if="!detail" class="pd__msg">{{ t('pending.pickOne') }}</p>

      <template v-else>
        <p class="pd__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('pending.detailRealFields') }}</span>
        </p>
        <div class="pd__grid">
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.sessionId') }}</span>
            <span class="pd__cell-v pd__cell-v--wrap">{{ detail.session_id }}</span>
          </span>
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.status') }}</span>
            <span class="pd__cell-v">{{ detail.status }}</span>
          </span>
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.providerId') }}</span>
            <span class="pd__cell-v">{{ detail.provider_id }}</span>
          </span>
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.credentialId') }}</span>
            <span class="pd__cell-v">{{ detail.credential_id }}</span>
          </span>
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.isStream') }}</span>
            <span class="pd__cell-v">{{ detail.is_stream ? t('common.yes') : t('common.no') }}</span>
          </span>
          <span class="pd__cell">
            <span class="pd__cell-l">{{ t('pending.bytes') }}</span>
            <span class="pd__cell-v">{{ fmtInt(detail.bytes_buffered) }}</span>
          </span>
        </div>
        <p class="pd__meta">
          {{ t('pending.createdLine', { t: relOf(detail.created_at) }) }}
        </p>
        <p v-if="detail.completed_at > 0" class="pd__meta">
          {{ t('pending.completedLine', { t: relOf(detail.completed_at) }) }}
        </p>
        <p v-if="detail.error_message" class="pd__warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ detail.error_message }}</span>
        </p>
      </template>
    </section>
  </div>
</template>

<style scoped>
.pd {
  padding: var(--app-space-3);
}
.pd__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.pd__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.pd__headline {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pd__big {
  font-size: 22px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.pd__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.pd__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.pd__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.pd__cell-v {
  font-size: 15px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.pd__cell-v--wrap {
  word-break: break-all;
  font-size: 13px;
}
.pd__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
  color: var(--app-text-secondary);
}
.pd__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.pd__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.pd__warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-size: 11px;
  line-height: 1.5;
}
.pd__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.pd__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.pd__field {
  display: block;
}
.pd__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.pd__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.pd__chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.pd__chip {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.pd__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.pd__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.pd__btn {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.pd__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.pd__btn[disabled] {
  opacity: 0.45;
}
.pd__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.pd__item {
  border-top: 1px solid var(--app-border);
}
.pd__rowbtn {
  display: block;
  width: 100%;
  min-height: 48px;
  padding: var(--app-space-2) 0;
  border: 0;
  background: transparent;
  text-align: left;
  color: inherit;
}
.pd__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.pd__sid {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}
.pd__badge {
  display: inline-block;
  margin-left: 4px;
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 10px;
  line-height: 1.6;
}
</style>