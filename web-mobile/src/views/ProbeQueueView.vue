<script setup lang="ts">
// ProbeQueueView — 凭据探测三态队列（2026-10-08，第一百零五批）。
//
// GET /api/admin/probe/tasks?status=pending|in_flight|completed&limit=
//
// 权限：**admin 档**（`admin/probe_dashboard.go:1907` 的 `adminWrap(h.handleProbeTaskRoute)`），
//   tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//   ★ 该路由是**方法多路复用**（GET/POST/DELETE 同一条），本页**只碰 GET**。
//
// ★★★ 本页最要紧的六件事（详见 api/probeTriStateTasks.ts 文件头）：
//   1. `status` 不是 DB 的 status —— 六个 DB 值被压成三个
//      （ready→pending / running→in_flight / 其余四值→completed + outcome）。
//      ★★ 原始值**只在 completed 行**以 `outcome` 保留，
//      pending / in_flight 行的原值（ready / running）**被丢掉且不可恢复**。
//      ⇒ 页面上**不能**出现「ready」「running」这种说法。
//   2. ★★★ `http_status` / `latency_ms` 是 `*int` + omitempty
//      ⇒ omitempty 只在 nil 时省略 ⇒ **数据库里的 0 会原样出现**。
//      ⇒ 用真值判断会把 `0` 说成「没测出状态码」—— **凭空造出一个事实**。
//   3. `outcome` 与 `next_retry_at_ms` 各自**只出现在一条腿**上，互斥且可验。
//   4. 供应商三键**全缺** = 未知（不是空串、不是 null）。
//   5. `count` 是**本页长度**不是总数（`probe_dashboard.go:1879` 的 `len(tasks)`）。
//   6. 三条腿的 ORDER BY **都有 id tiebreak** ⇒ 排序完全确定，可自验。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchProbeTriStateTasks,
  probeTaskOriginFromSource,
  probeTaskOriginMismatch,
  probeTaskOutcomeIsConsistent,
  probeTaskNextRetryIsConsistent,
  probeTaskRetriesExhausted,
  probeTaskProviderNameOrNull,
  probeTaskMayHaveMore,
  probeTaskNextRetryAtMsOrNull,
  probeTaskHasHttpStatus,
  probeTaskLatencyMsOrNull,
  PROBE_TASK_STATUSES,
  PROBE_TASK_DEFAULT_LIMIT,
  PROBE_TASK_MAX_LIMIT,
  type ProbeTaskStatus,
  type ProbeTaskOutcome,
  type ProbeTriStateResponse,
  type ProbeTriStateTask,
} from '@/api/probeTriStateTasks'

useHyperPage({ title: () => t('pq.title') })

const LIMIT_CHOICES = [50, 100, PROBE_TASK_MAX_LIMIT] as const

const leg = ref<ProbeTaskStatus>('pending')
const limit = ref<number>(PROBE_TASK_DEFAULT_LIMIT)
const resp = ref<ProbeTriStateResponse | null>(null)
const errorMsg = ref<string | null>(null)
const loading = ref(false)

/** ★★ 响应回显的 status 与请求的腿不一致 ⇒ 数据不是这一腿的，不能当作这一腿渲染。 */
const echoMismatch = computed(() => resp.value !== null && resp.value.status !== leg.value)

/** ★ 回显对了但行级 status 不对 ⇒ 说明过滤没按压缩值做，这页数据不可信。 */
const rowMismatch = computed(() =>
  (resp.value?.tasks ?? []).some((t) => t.status !== leg.value),
)

const tasks = computed<ProbeTriStateTask[]>(() => {
  if (!resp.value) return []
  // ★ 回显不对 ⇒ 不拿它当这一腿渲染，宁可空着也不显示错腿的数据。
  if (echoMismatch.value) return []
  return resp.value.tasks
})

/** ★★ 页满 ⇒ 「可能还有下一页」。count 提供不了额外信息（它是本页长度）。 */
const pageFull = computed(() => (resp.value ? probeTaskMayHaveMore(resp.value, limit.value) : false))

/** ★★★ 0 是真值，不是「没有」。两个渲染函数都必须用 `!== undefined` 判存在性。 */
function httpStatusText(tk: ProbeTriStateTask): string {
  if (!probeTaskHasHttpStatus(tk)) return t('pq.noHttpStatus')
  return String(tk.http_status)
}

function latencyText(tk: ProbeTriStateTask): string {
  const v = probeTaskLatencyMsOrNull(tk)
  if (v === null) return t('pq.noLatency')
  return `${v} ms`
}

/** ★ `next_retry_at_ms` 只在 pending 腿出现（文件头第 (3) 条）。 */
function nextRetryText(tk: ProbeTriStateTask): string {
  const v = probeTaskNextRetryAtMsOrNull(tk)
  if (v === null) return t('pq.noNextRetry')
  return new Date(v).toISOString()
}

function providerText(tk: ProbeTriStateTask): string {
  // ★ 三键全缺才是「未知」（文件头第 (5) 条：SQL 给 `''` 再被 omitempty 吃掉）。
  // ★ 键缺失与空串在本端点**不可能同现**，所以 name/code 之间可以直接回退 ——
  //   不需要再判一次 `probeTaskProviderIsUnknown`，那在下面两条之后恒为 false（死分支）。
  const name = probeTaskProviderNameOrNull(tk)
  if (name !== null) return name
  if (tk.provider_code !== undefined) return tk.provider_code
  return t('pq.providerUnknown')
}

function outcomeTone(o: ProbeTaskOutcome | undefined): 'ok' | 'warn' | 'muted' {
  if (o === 'success') return 'ok'
  if (o === undefined) return 'muted'
  return 'warn'
}

/**
 * ★★ `outcome` ⇔ `status === 'completed'`（文件头第 (2) 条）——
 *   不一致时**绝不能**静默渲染成「无结果」：
 *   completed 行按契约必有 outcome，缺了就是契约漂移，必须说出来。
 */
function outcomeText(tk: ProbeTriStateTask): string {
  if (tk.outcome === undefined) return t('pq.outcomeNone')
  return t(`pq.outcomeDetail.${tk.outcome}`)
}

async function load(next?: ProbeTaskStatus): Promise<void> {
  if (next) leg.value = next
  loading.value = true
  errorMsg.value = null
  try {
    resp.value = await fetchProbeTriStateTasks(leg.value, limit.value)
  } catch (err) {
    // ★ 失败 ⇒ 清空，绝不保留上一次的成功结果冒充本次。
    resp.value = null
    errorMsg.value = (err as Error)?.message || String(err)
  } finally {
    loading.value = false
  }
}

// ★ 只有当前腿会被自动加载；另两条腿不预取 —— 它们各自是一次独立请求。
void load()

onBeforeUnmount(() => {
  resp.value = null
  errorMsg.value = null
})
</script>

<template>
  <div class="pq">
    <!-- ── 分段控件：三条腿 ── -->
    <nav class="pq__legs" :aria-label="t('pq.legLabel')">
      <button
        v-for="s in PROBE_TASK_STATUSES"
        :key="s"
        type="button"
        class="pq__leg"
        :class="{ 'pq__leg--on': s === leg }"
        :aria-pressed="s === leg"
        :disabled="loading"
        @click="load(s)"
      >
        {{ t(`pq.leg.${s}`) }}
      </button>
    </nav>

    <nav class="pq__legs" :aria-label="t('pq.limit')">
      <button
        v-for="n in LIMIT_CHOICES"
        :key="n"
        type="button"
        class="pq__leg"
        :class="{ 'pq__leg--on': n === limit }"
        :aria-pressed="n === limit"
        :disabled="loading"
        @click="limit = n; load()"
      >
        {{ n }}
      </button>
    </nav>

    <p v-if="loading" class="pq__msg">{{ t('common.loading') }}</p>
    <p v-if="errorMsg" class="pq__msg pq__msg--err">{{ errorMsg }}</p>

    <!-- ★★ 回显与请求的腿不一致 ⇒ 这是别的腿的数据，不能当这一腿显示 -->
    <p v-if="echoMismatch" class="pq__msg pq__msg--err">{{ t('pq.echoMismatch') }}</p>
    <!-- ★ 回显对了、行级 status 不对 ⇒ 过滤没按压缩值做 -->
    <p v-else-if="rowMismatch" class="pq__msg pq__msg--err">{{ t('pq.rowMismatch') }}</p>

    <p v-if="resp" class="pq__msg">
      {{ t('pq.pageCount', { n: resp.count }) }}
    </p>
    <!-- ★★ count 是本页长度，不是总数 ⇒ 页满时必须说「可能还有下一页」 -->
    <p v-if="pageFull" class="pq__msg pq__msg--warn">{{ t('pq.pageFull') }}</p>
    <p v-else-if="resp" class="pq__msg pq__msg--muted">{{ t('pq.pageComplete') }}</p>

    <p v-if="!loading && !errorMsg && tasks.length === 0 && !echoMismatch" class="pq__msg">
      {{ t('pq.empty', { leg: t(`pq.leg.${leg}`) }) }}
    </p>

    <article v-for="tk in tasks" :key="tk.id" class="pq__card">
      <div class="pq__row">
        <span class="pq__badge" :class="`pq__badge--${tk.status}`">{{ tk.status }}</span>
        <span class="pq__cred">{{ t('pq.credential') }} {{ tk.credential_id }}</span>
        <span class="pq__model">{{ tk.raw_model }}</span>
      </div>

      <div class="pq__meta">
        <span>{{ t('pq.command') }} {{ tk.command }}</span>
        <span>{{ t('pq.attempt') }} {{ tk.attempt }}/{{ tk.max_attempts }}</span>
        <span>{{ t('pq.priority') }} {{ tk.priority }}</span>
        <span>{{ t('pq.source') }} {{ tk.source }}</span>
      </div>

      <p class="pq__provider">
        {{ t('pq.provider') }} {{ providerText(tk) }}
        <!-- ★ origin 是从 source 推出来的；对不上就是数据异常，必须说出来 -->
        <span v-if="probeTaskOriginMismatch(tk)" class="pq__warn-inline">
          ★ {{ t('pq.originMismatch', { got: tk.origin, want: probeTaskOriginFromSource(tk.source) }) }}
        </span>
      </p>

      <!-- pending 腿专属：退避时间（键只在这条腿出现） -->
      <p v-if="tk.status === 'pending'" class="pq__note">
        {{ t('pq.nextRetry') }} {{ nextRetryText(tk) }}
      </p>
      <!-- ★★ next_retry_at_ms 只在 pending 腿出现；跑到别的腿上就是契约漂移 -->
      <p v-if="!probeTaskNextRetryIsConsistent(tk)" class="pq__note pq__note--danger">
        ★ {{ t('pq.nextRetryMismatch', { status: tk.status }) }}
      </p>

      <!-- ★★ 重试用尽且未进终态 ⇒ 这是个不会再自己前进的任务 -->
      <p v-if="probeTaskRetriesExhausted(tk)" class="pq__note pq__note--danger">
        ★ {{ t('pq.retriesExhausted') }}
      </p>

      <!-- completed 腿专属：outcome + http_status + latency -->
      <template v-if="tk.status === 'completed'">
        <p class="pq__note">
          {{ t('pq.outcome') }}
          <b :class="`pq__out--${outcomeTone(tk.outcome)}`">{{ outcomeText(tk) }}</b>
        </p>
        <!-- ★★★ http_status 的 0 必须显示成 0，绝不能变成「未测出」 -->
        <p class="pq__note">{{ t('pq.httpStatus') }} {{ httpStatusText(tk) }}</p>
        <p class="pq__note">{{ t('pq.latency') }} {{ latencyText(tk) }}</p>
        <p v-if="tk.finished_at" class="pq__note">{{ t('pq.finishedAt') }} {{ tk.finished_at }}</p>
        <p v-if="tk.reason_code" class="pq__note">{{ t('pq.reason') }} {{ tk.reason_code }}</p>
      </template>

      <!--
        ★★★ outcome 漂移告警**必须放在 completed 块之外**。
        第一版把它写进了 `v-if="status==='completed'"` 里 ⇒ 后果是
        「completed 行缺 outcome」看得见、而「**非 completed 行带 outcome**」——
        也就是更严重的那一格——反而被自己的守卫藏了起来。
        ⇒ 判别动作：**不一致告警的可见性不能由它所告警的那个条件决定。**
      -->
      <p v-if="!probeTaskOutcomeIsConsistent(tk)" class="pq__note pq__note--danger">
        ★ {{ t('pq.outcomeMismatch', { status: tk.status }) }}
      </p>
    </article>
  </div>
</template>

<style scoped>
.pq {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.pq__legs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1);
}

/* R1：新增触控控件一律 ≥48 CSS px */
.pq__leg {
  flex: 1 1 auto;
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.pq__leg--on {
  color: var(--app-text);
  background: var(--app-surface-muted);
  border-color: var(--app-text-muted);
  font-weight: 600;
}

.pq__msg {
  margin: 0;
  font-size: 0.8125rem;
  color: var(--app-text-muted);
}

.pq__msg--err {
  color: var(--app-danger);
}

.pq__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.pq__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.pq__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: baseline;
}

.pq__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
}

.pq__badge--pending {
  color: var(--app-text-secondary);
}

.pq__badge--in_flight {
  color: var(--app-info, var(--app-text));
}

.pq__badge--completed {
  color: var(--app-text-muted);
}

.pq__cred {
  font-size: 0.875rem;
  font-weight: 600;
}

.pq__model,
.pq__provider {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  word-break: break-all;
}

.pq__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.pq__note {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
  word-break: break-all;
}

.pq__note--danger,
.pq__warn-inline {
  color: var(--app-danger);
}

.pq__out--ok {
  color: var(--app-success, var(--app-text));
}

.pq__out--warn {
  color: var(--app-warning, var(--app-danger));
}
</style>