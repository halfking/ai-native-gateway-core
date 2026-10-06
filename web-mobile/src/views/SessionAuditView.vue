<script setup lang="ts">
// SessionAuditView — 最近会话审计清单（/api/admin/sessions/list，**admin 档**）。
//
// 它答的是「最近有哪些会话、各自花了多少、其中一轮是怎么走的」：
//   /online 答「现在谁还活着」（另一个页面）
//   本页    答「历史上跑过的会话 + 逐轮明细」
//
// ⚠️★★★ 五个后端语义（详见 api/sessions.ts 文件头）：
//
// (1) ★★ 四个字段是**恒定值**，本页面**故意不渲染**：
//       `has_title` / `has_summary` 恒 false —— SQL 的 SELECT 列表里根本没有
//       session_titles / session_summaries 两张表；
//       `missing_session_ids` 恒 0、`has_session_id` 恒 true —— 空/NULL 的行
//       在构造结构体**之前**就被 `continue` 跳过了。
//     ⇒ 渲染成「无标题」「会话 ID 缺失」等于编造一个后端根本没查的结论。
//     ⇒ 页脚常驻一条说明，解释「为什么这里没有标题」。
//
// (2) ★★ `limit` 被后端**回显**在响应里 ⇒ `sessions.length >= limit`
//     是「确实被填满」的**精确**信号，不只是「可能截断」。
//
// (3) ★★ `tenant` 也被回显 ⇒ 页面显示**后端认下来的那个租户**，
//     而不是客户端猜的（`?tenant=` 只有 super/admin-key 生效，其余角色钉 auth 租户）。
//
// (4) ★★ `total_cost_usd` 的 **0 是 COALESCE 兜底值**（没有成本数据），不是免费。
//
// (5) ★★★ 展开某一行拉 `/sessions/{id}/timeline`：
//     响应会回告 `session_id_source`。若为 `numeric_fallback_resolved`，
//     说明后端是**把纯数字 id 当 sessions.id 猜着解析**的 ⇒ 必须标出来。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchSessionList,
  fetchSessionTimeline,
  sessionListTruncated,
  modelsUsedOf,
  costMayBeNoData,
  sessionIdSourceIsGuess,
  sessionIdSourceKnown,
  flattenTurns,
  SESSION_LIST_LIMIT_DEFAULT,
  SESSION_LIST_LIMIT_MAX,
  type SessionListResponse,
  type SessionTimelineResponse,
} from '@/api/sessions'

useHyperPage({ title: () => t('sessions.title') })

const LIMIT_CHOICES = [SESSION_LIST_LIMIT_DEFAULT, 100, SESSION_LIST_LIMIT_MAX]

const limit = ref<number>(SESSION_LIST_LIMIT_DEFAULT)
const data = ref<SessionListResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

/** 已展开的会话 id → 其轮次树。 */
const timelines = ref<Record<string, SessionTimelineResponse | null>>({})
const timelineLoading = ref<string | null>(null)
const timelineError = ref<Record<string, string>>({})

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchSessionList({ limit: limit.value })
  } catch (err) {
    data.value = null
    const st = (err as { status?: number })?.status
    // ★ list 的「没登录」是 **404**（handler 内钉死），不是 401 ——
    //   同族 online/timeline 才是 401。别照抄文案。
    if (st === 401 || st === 403 || st === 404) error.value = t('sessions.errForbidden')
    else error.value = (err as Error)?.message || t('common.error')
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

async function toggleTimeline(sessionId: string): Promise<void> {
  // 再次点击收起（不清缓存，回来时数据仍是新的那次）。
  if (timelines.value[sessionId] !== undefined) {
    const next = { ...timelines.value }
    delete next[sessionId]
    timelines.value = next
    return
  }
  timelineLoading.value = sessionId
  // ★ 重试前必须清掉上一次的错误：不清的话，即使这次拉成功，
  //   模板里的 `v-else-if="timelineError[id]"` 仍会命中错误分支，
  //   轮次永远渲染不出来（判据「失败后能重试成功」第一次就抓到了它）。
  const cleared = { ...timelineError.value }
  delete cleared[sessionId]
  timelineError.value = cleared
  try {
    const r = await fetchSessionTimeline(sessionId)
    timelines.value = { ...timelines.value, [sessionId]: r }
  } catch (err) {
    timelineError.value = { ...timelineError.value, [sessionId]: (err as Error)?.message || t('common.error') }
    // 失败也要占位，否则下一次点击会被当成「收起」
    timelines.value = { ...timelines.value, [sessionId]: null }
  } finally {
    timelineLoading.value = null
  }
}

const sessions = computed(() => data.value?.sessions ?? [])
const atCap = computed(() => sessionListTruncated(data.value))
const isEmpty = computed(() => loaded.value && !loading.value && !error.value && sessions.value.length === 0)

function openOf(id: string): boolean {
  return timelines.value[id] !== undefined
}
function timelineOf(id: string): SessionTimelineResponse | null {
  return timelines.value[id] ?? null
}
function flatTurnsOf(id: string) {
  const tl = timelineOf(id)
  return tl ? flattenTurns(tl.turns) : []
}
function turnTone(outcome: string | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  switch (outcome) {
    case 'final_success':
      return 'success'
    case 'failure':
      return 'danger'
    case 'rate_limited':
    case 'superseded_success':
      return 'warning'
    case 'in_progress':
      return 'muted'
    default:
      return 'muted'
  }
}

onBeforeUnmount(() => {
  data.value = null
  timelines.value = {}
  timelineError.value = {}
})
</script>

<template>
  <div class="view-root sa">
    <div class="sa__top">
      <div class="sa__limits" role="group" :aria-label="t('sessions.limitLabel')">
        <button
          v-for="n in LIMIT_CHOICES"
          :key="n"
          type="button"
          class="sa__chip"
          :class="{ 'sa__chip--on': limit === n }"
          @click="((limit = n), load())"
        >
          {{ t('sessions.limitN', { n }) }}
        </button>
      </div>
    </div>

    <!-- ★ 回显后端认下来的租户：superAdmin 可跨租户，其余角色钉 auth 租户 -->
    <p v-if="data?.tenant" class="sa__meta-line">
      {{ t('sessions.tenantEcho', { tenant: data.tenant }) }}
    </p>

    <p v-if="error" class="sa__msg sa__msg--err">{{ error }}</p>
    <p v-if="loading" class="sa__msg">{{ t('common.loading') }}</p>
    <p v-else-if="isEmpty" class="sa__msg">{{ t('sessions.empty') }}</p>

    <!-- ★★ limit 回显 ⇒ 撞上限是「确实被填满」，不是「可能」 -->
    <p v-if="atCap" class="sa__note sa__note--warn">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('sessions.truncated', { n: sessions.length, limit: data?.limit ?? limit }) }}</span>
    </p>
    <p v-else-if="sessions.length" class="sa__count">{{ t('sessions.count', { n: sessions.length }) }}</p>

    <ul v-if="sessions.length" class="sa__list">
      <li v-for="it in sessions" :key="it.primary_key" class="sa__item">
        <button type="button" class="sa__item-head" @click="toggleTimeline(it.primary_key)">
          <AppIcon name="chevron" :size="14" :class="{ 'sa__chev--open': openOf(it.primary_key) }" />
          <span class="sa__sid">{{ it.primary_key }}</span>
        </button>

        <div class="sa__kv">
          <span class="sa__kv-item">
            <span class="sa__kv-l">{{ t('sessions.turns') }}</span>
            <span class="sa__kv-v">{{ fmtInt(it.audit.total_turns) }}</span>
          </span>
          <span class="sa__kv-item">
            <span class="sa__kv-l">{{ t('sessions.promptTokens') }}</span>
            <span class="sa__kv-v">{{ fmtInt(it.audit.total_prompt_tokens) }}</span>
          </span>
          <span class="sa__kv-item">
            <span class="sa__kv-l">{{ t('sessions.respTokens') }}</span>
            <span class="sa__kv-v">{{ fmtInt(it.audit.total_resp_tokens) }}</span>
          </span>
        </div>

        <!-- ★★ cost=0 是 COALESCE 兜底值，不是免费 -->
        <div class="sa__kv">
          <span class="sa__kv-item" :class="{ 'sa__kv-item--maybe': costMayBeNoData(it.audit.total_cost_usd) }">
            <span class="sa__kv-l">{{ t('sessions.cost') }}</span>
            <span class="sa__kv-v">{{ t('sessions.costValue', { v: it.audit.total_cost_usd.toFixed(4) }) }}</span>
          </span>
          <span v-if="it.audit.has_compression" class="sa__kv-item">
            <span class="sa__kv-l">{{ t('sessions.compression') }}</span>
            <span class="sa__kv-v">{{ fmtInt(it.audit.compression_hits) }}</span>
          </span>
        </div>

        <p class="sa__models">
          {{ t('sessions.models', { list: modelsUsedOf(it.audit).join(' / ') || t('sessions.noModels') }) }}
        </p>
        <p v-if="it.audit.latest_at" class="sa__meta">{{ t('sessions.latestAt', { t: relativeTime(it.audit.latest_at) }) }}</p>

        <!-- ★★★ 轮次树 -->
        <section v-if="openOf(it.primary_key)" class="sa__tl">
          <p v-if="timelineLoading === it.primary_key" class="sa__msg">{{ t('common.loading') }}</p>
          <p v-else-if="timelineError[it.primary_key]" class="sa__msg sa__msg--err">{{ timelineError[it.primary_key] }}</p>

          <template v-else-if="timelineOf(it.primary_key)">
            <!-- ★★ 后端「猜着解析」的 id 必须标出来 -->
            <p v-if="sessionIdSourceIsGuess(timelineOf(it.primary_key)!.session_id_source)" class="sa__warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('sessions.idGuessWarn') }}</span>
            </p>
            <p v-else-if="!sessionIdSourceKnown(timelineOf(it.primary_key)!.session_id_source)" class="sa__warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('sessions.idSourceUnknown', { source: timelineOf(it.primary_key)!.session_id_source }) }}</span>
            </p>

            <p v-if="timelineOf(it.primary_key)!.truncated" class="sa__note sa__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('sessions.timelineTruncated', { n: timelineOf(it.primary_key)!.count }) }}</span>
            </p>

            <p v-if="!flatTurnsOf(it.primary_key).length" class="sa__msg">{{ t('sessions.noTurns') }}</p>

            <ul v-else class="sa__turns">
              <li
                v-for="{ turn, depth } in flatTurnsOf(it.primary_key)"
                :key="turn.request_id"
                class="sa__turn"
                :style="{ paddingLeft: depth * 12 + 8 + 'px' }"
              >
                <div class="sa__turn-head">
                  <StatusDot :tone="turnTone(turn.outcome)" />
                  <span class="sa__turn-type">{{ turn.request_type }}</span>
                  <span class="sa__turn-status">{{ turn.status }}</span>
                </div>
                <p class="sa__turn-meta">
                  {{ turn.request_id }}
                  <span v-if="turn.model"> · {{ turn.model }}</span>
                  <span v-if="turn.latency_ms !== undefined"> · {{ turn.latency_ms }}ms</span>
                </p>
                <p v-if="turn.is_final_success" class="sa__final">{{ t('sessions.finalSuccess') }}</p>
                <p v-if="turn.outcome_reason" class="sa__reason">
                  <span v-if="turn.error_kind">· {{ turn.error_kind }}</span>
                  <span v-if="turn.failure_stage">· {{ turn.failure_stage }}</span>
                  {{ turn.outcome_reason }}
                </p>
              </li>
            </ul>
          </template>
        </section>
      </li>
    </ul>

    <!-- ★★ 明确交代「为什么没有标题 / 会话 ID」这一栏 -->
    <section v-if="sessions.length" class="sa__legend">
      <p class="sa__legend-title">{{ t('sessions.legendTitle') }}</p>
      <p class="sa__legend-line">{{ t('sessions.legendNoTitle') }}</p>
      <p class="sa__legend-line">{{ t('sessions.legendNoCost') }}</p>
      <p class="sa__legend-line">{{ t('sessions.legendIdKind', { kind: data?.id_kind ?? 'gw_session_id' }) }}</p>
    </section>
  </div>
</template>

<style scoped>
.sa {
  padding: var(--app-space-3);
}
.sa__top {
  margin-bottom: var(--app-space-2);
}
.sa__limits {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.sa__chip {
  min-height: 48px;
  min-width: 60px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.sa__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.sa__meta-line {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 11px;
}
.sa__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.sa__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.sa__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.sa__note--warn {
  color: var(--app-warning);
}
.sa__count {
  margin: var(--app-space-2) 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.sa__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.sa__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.sa__item-head {
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
.sa__sid {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-all;
  min-width: 0;
}
/* 只有一个合法图标名 `chevron`，展开态用旋转表示方向 */
.sa__chev--open {
  transform: rotate(90deg);
}
.sa__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.sa__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.sa__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.sa__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.sa__kv-item--maybe .sa__kv-v {
  color: var(--app-text-muted);
  font-style: italic;
}
.sa__models,
.sa__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.sa__tl {
  margin-top: var(--app-space-2);
  padding-top: var(--app-space-2);
  border-top: 1px solid var(--app-border);
}
.sa__warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0 0 var(--app-space-2);
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-size: 11px;
  line-height: 1.5;
}
.sa__turns {
  list-style: none;
  margin: 0;
  padding: 0;
}
.sa__turn {
  padding: 6px 8px;
  border-left: 2px solid var(--app-border);
  margin-bottom: 4px;
}
.sa__turn-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.sa__turn-type {
  font-size: 12px;
  font-weight: 600;
  color: var(--app-text);
}
.sa__turn-status {
  font-size: 11px;
  color: var(--app-text-secondary);
}
.sa__turn-meta {
  margin: 2px 0 0;
  font-size: 10px;
  color: var(--app-text-muted);
  word-break: break-all;
}
.sa__final {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--app-success);
}
.sa__reason {
  margin: 2px 0 0;
  font-size: 10px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.sa__legend {
  margin-top: var(--app-space-3);
  padding: var(--app-space-3);
  border: 1px dashed var(--app-border);
  border-radius: var(--app-radius);
}
.sa__legend-title {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 700;
  color: var(--app-text-secondary);
}
.sa__legend-line {
  margin: 0 0 4px;
  font-size: 11px;
  color: var(--app-text-muted);
  line-height: 1.6;
}
</style>