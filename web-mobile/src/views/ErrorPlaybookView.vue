<script setup lang="ts">
// ErrorPlaybookView — 错误处理手册（移动端**只读**形态，2026-11-25 菜单 PC 对齐轮）。
//
// PC 端 2026-11-21 落地了 /admin/error-playbook（data-ops 组，super 档）；
// 移动端此前零覆盖 —— 手册条目「enabled/auto_execute 是否开着、最近执行结果如何」
// 这两个值班高频问题在手机上答不了。本席补齐**只读**两条腿：
//   GET /api/admin/error-playbook/entries    手册条目（enabled/source/统计计数）
//   GET /api/admin/error-playbook/executions 执行留痕（状态/耗时/触发来源）
//   GET /api/admin/error-playbook/stats      统计与链路状态（engine_wired/llm_wired）
//
// ★ 写端点本页一律不碰（见 api/errorPlaybook.ts 文件头）：
//   POST /entries、PUT/DELETE /entries/{id}、POST /entries/{id}/execute、
//   POST /executions/{id}/revert、POST /analyze。
//   （execute/revert/analyze 都有真实副作用：跑脚本/调 API/发 LLM 分析。）
//
// ══════════════════════════════════════════════════════════════════════════
// ★ 权限与租户口径（与 ops-overview / platform-settings 同第三类）
// ══════════════════════════════════════════════════════════════════════════
// 注册处 wrap = AdminMiddleware（admin/error_playbook_handler.go:72-74，
// 只认证不检 role）⇒ 后端 tenant_admin 可读；但 entries/executions 两表
// **没有 tenant_id 列**（995_error_playbook.sql 建表），载荷是平台级全局事实
// ⇒ 抽屉席过度限制到 super_admin（PC 端菜单同档 super+hideForTenant）。
// 视图内无需段落级门控 —— 档位本身就是那道门。
//
// ★ 404 的语义：RegisterRoutes fail-closed（store/secret 缺失 ⇒ 端点不注册）
//   ⇒ 404 =「手册未装配」，不是请求失败；按 formatApiError 的通用 404 文案披露。
//
// ★ 列表可能少于 total：store 的 rows 循环按 `continue` 丢扫描失败行
//   （domains/errorplaybook/store.go ListEntries），total 来自独立 COUNT
//   ⇒ 本页显示「本页 n 条 · 总数 total」并披露差额语义。

import { computed, onMounted, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import { formatApiError } from '@/utils/errors'
import {
  getStats,
  listEntries,
  listExecutions,
  PLAYBOOK_SOURCES,
  type EntrySource,
  type ExecutionStatus,
  type PlaybookEntry,
  type PlaybookExecution,
  type PlaybookStats,
} from '@/api/errorPlaybook'

useHyperPage({ title: () => t('ep.title') })

// ★★ 深链兜底（RequestAnomaliesGuard ③-1 全族普查的口径）：meta.requiresRole
//   由 hyper-wiring 的 routeAllowedFor 拦截，但视图内仍按族规兜一层 ——
//   非 super_admin 时**不发请求**，只渲染 nope 区（体验层兜底，与安全层无关）。
const auth = useAuthStore()
const allowed = computed(() => auth.role === 'super_admin')

const stats = ref<PlaybookStats | null>(null)
const entries = ref<PlaybookEntry[]>([])
const entriesTotal = ref(0)
const execs = ref<PlaybookExecution[]>([])
const execsTotal = ref(0)
const loading = ref(false)
const error = ref('')

const sourceFilter = ref<EntrySource | ''>('')
const enabledFilter = ref<'all' | 'enabled' | 'disabled'>('all')


const FILTERED_SOURCES = PLAYBOOK_SOURCES

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  const aborted = (): boolean => loading.value === false
  try {
    const [s, e, x] = await Promise.all([
      getStats(),
      listEntries({
        source: sourceFilter.value || undefined,
        enabled: enabledFilter.value === 'all' ? undefined : enabledFilter.value === 'enabled',
        limit: 50,
      }),
      listExecutions({ limit: 20 }),
    ])
    if (aborted()) return
    stats.value = s
    entries.value = e.entries ?? []
    entriesTotal.value = e.total
    execs.value = x.executions ?? []
    execsTotal.value = x.total
  } catch (cause) {
    if (aborted()) return
    error.value = formatApiError(cause)
  } finally {
    loading.value = false
  }
}

onMounted(() => { /* ★ 全族普查钉死「挂载即取数」只有 /request-anomalies ⇒ 本席挂载不发请求，点「刷新」才发 */ })


function fmtTime(v: string | null | undefined): string {
  if (!v || v.startsWith('0001-01-01')) return t('ep.never')
  return v.replace('T', ' ').slice(0, 16)
}

function sourceLabel(s: string): string {
  return (FILTERED_SOURCES as readonly string[]).includes(s) ? t(`ep.source.${s}`) : s
}

function statusLabel(s: string): string {
  return (['running', 'success', 'failed', 'skipped'] as const).includes(s as ExecutionStatus)
    ? t(`ep.status.${s}`)
    : s
}
</script>

<template>
  <div class="ep">
    <!-- ★ 深链兜底：非 super_admin 只渲染这一区，不渲染任何数据面。 -->
    <section v-if="!allowed" class="ep__sec" data-testid="ep-nope">
      <p class="ep__msg" data-testid="ep-nope-msg">{{ t('ep.nope') }}</p>
    </section>
    <template v-else>
    <section class="ep__sec">
      <header class="ep__head">
        <h2 class="ep__h">{{ t('ep.title') }}</h2>
        <button type="button" class="ep__btn" data-testid="ep-load" :disabled="loading" @click="load">
          {{ loading ? t('common.loading') : t('ep.reload') }}
        </button>
      </header>
      <p class="ep__msg" data-testid="ep-readonly-note">{{ t('ep.readonlyNote') }}</p>
      <p v-if="error" class="ep__msg ep__msg--err" data-testid="ep-error">{{ error }}</p>
    </section>

    <!-- ══ 统计与链路 ══ -->
    <section v-if="stats" class="ep__sec">
      <header class="ep__head">
        <h2 class="ep__h">{{ t('ep.stats.title') }}</h2>
      </header>
      <ul class="ep__grid" data-testid="ep-stats">
        <li class="ep__kv"><span>{{ t('ep.stats.totalEntries') }}</span><b data-testid="ep-stats-total">{{ stats.total_entries ?? '—' }}</b></li>
        <li class="ep__kv"><span>{{ t('ep.stats.enabledEntries') }}</span><b>{{ stats.enabled_entries ?? '—' }}</b></li>
        <li class="ep__kv"><span>{{ t('ep.stats.autoExecuteEntries') }}</span><b>{{ stats.auto_execute_entries ?? '—' }}</b></li>
        <li class="ep__kv"><span>{{ t('ep.stats.executionsTotal') }}</span><b>{{ stats.executions?.total ?? '—' }}</b></li>
        <li class="ep__kv"><span>{{ t('ep.stats.pendingUnknown') }}</span><b>{{ stats.pending_unknown ?? '—' }}</b></li>
      </ul>
      <p class="ep__msg" data-testid="ep-engine-state">
        {{ t('ep.stats.engineState') }}：{{ stats.engine_wired ? t('ep.stats.engineOn') : t('ep.stats.engineOff') }}
        · {{ stats.llm_wired ? t('ep.stats.llmOn') : t('ep.stats.llmOff') }}
      </p>
    </section>

    <!-- ══ 手册条目 ══ -->
    <section class="ep__sec">
      <header class="ep__head">
        <h2 class="ep__h">{{ t('ep.entries.title') }}</h2>
        <select v-model="sourceFilter" class="ep__select" data-testid="ep-source-filter" :aria-label="t('ep.filter.source')">
          <option value="">{{ t('ep.filter.all') }}</option>
          <option v-for="s in FILTERED_SOURCES" :key="s" :value="s">{{ t(`ep.source.${s}`) }}</option>
        </select>
        <select v-model="enabledFilter" class="ep__select" data-testid="ep-enabled-filter" :aria-label="t('ep.filter.enabled')">
          <option value="all">{{ t('ep.filter.all') }}</option>
          <option value="enabled">{{ t('ep.filter.enabledOnly') }}</option>
          <option value="disabled">{{ t('ep.filter.disabledOnly') }}</option>
        </select>
      </header>
      <p v-if="entries.length" class="ep__msg" data-testid="ep-entries-count">
        {{ t('ep.entries.count', { n: entries.length, total: entriesTotal }) }}
      </p>
      <p v-if="!entries.length && !loading && !error" class="ep__msg" data-testid="ep-entries-empty">{{ t('ep.entries.empty') }}</p>
      <ul class="ep__list" data-testid="ep-entries">
        <li v-for="e in entries" :key="e.id" class="ep__card">
          <div class="ep__row">
            <b>{{ e.title || e.error_type }}</b>
            <span class="ep__badge" :class="e.enabled ? 'ep__badge--on' : 'ep__badge--off'">
              {{ e.enabled ? t('ep.enabled') : t('ep.disabled') }}
            </span>
          </div>
          <div class="ep__row ep__row--sub">
            <span class="ep__mono">{{ e.error_type }}</span>
            <span>{{ sourceLabel(e.source) }} · {{ t(`ep.plan.${e.plan_kind}`) }}</span>
          </div>
          <div class="ep__row ep__row--sub">
            <span>{{ t('ep.entries.hitCount') }} {{ e.hit_count }} · {{ t('ep.entries.execCount') }} {{ e.execution_count }}（{{ t('ep.entries.successCount') }} {{ e.success_count }} / {{ t('ep.entries.failureCount') }} {{ e.failure_count }}）</span>
          </div>
          <div class="ep__row ep__row--sub">
            <span>{{ t('ep.entries.lastExecuted') }} {{ fmtTime(e.last_executed_at) }}</span>
            <span v-if="e.auto_execute" class="ep__badge ep__badge--auto">{{ t('ep.entries.autoExecute') }}</span>
          </div>
        </li>
      </ul>
    </section>

    <!-- ══ 执行留痕 ══ -->
    <section class="ep__sec">
      <header class="ep__head">
        <h2 class="ep__h">{{ t('ep.execs.title') }}</h2>
      </header>
      <p v-if="execs.length" class="ep__msg" data-testid="ep-execs-count">
        {{ t('ep.execs.count', { n: execs.length, total: execsTotal }) }}
      </p>
      <p v-if="!execs.length && !loading && !error" class="ep__msg" data-testid="ep-execs-empty">{{ t('ep.execs.empty') }}</p>
      <ul class="ep__list" data-testid="ep-execs">
        <li v-for="x in execs" :key="x.id" class="ep__card">
          <div class="ep__row">
            <span class="ep__mono">{{ x.error_type }}</span>
            <span class="ep__badge" :class="`ep__badge--${x.status}`">{{ statusLabel(x.status) }}</span>
          </div>
          <div class="ep__row ep__row--sub">
            <span>{{ t(`ep.trigger.${x.trigger_source}`) }} · {{ x.duration_ms }}ms · {{ fmtTime(x.created_at) }}</span>
          </div>
          <div v-if="x.error_message" class="ep__row ep__row--sub">
            <span class="ep__msg ep__msg--err">{{ x.error_message }}</span>
          </div>
        </li>
      </ul>
    </section>
    </template>
  </div>
</template>

<style scoped>
.ep {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px;
}
.ep__sec {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border-radius: 12px;
  background: var(--app-surface, #fff);
  border: 1px solid var(--app-border, #e5e7eb);
}
.ep__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.ep__h {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}
.ep__btn {
  border: 1px solid var(--app-border, #e5e7eb);
  border-radius: 8px;
  background: var(--app-surface, #fff);
  padding: 4px 10px;
  font-size: 13px;
}
.ep__select {
  border: 1px solid var(--app-border, #e5e7eb);
  border-radius: 8px;
  background: var(--app-surface, #fff);
  padding: 3px 6px;
  font-size: 12px;
  max-inline-size: 40%;
}
.ep__msg {
  font-size: 13px;
  line-height: 1.5;
  margin: 0;
  color: var(--app-text-secondary);
}
.ep__msg--err {
  color: var(--app-danger);
}
.ep__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px 12px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.ep__kv {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
}
.ep__list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.ep__card {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px;
  border: 1px solid var(--app-border, #e5e7eb);
  border-radius: 10px;
  font-size: 13px;
}
.ep__row {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 8px;
}
.ep__row--sub {
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ep__mono {
  font-family: ui-monospace, monospace;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.ep__badge {
  font-size: 11px;
  border-radius: 999px;
  padding: 1px 8px;
  border: 1px solid var(--app-border, #e5e7eb);
  white-space: nowrap;
}
.ep__badge--on {
  color: var(--app-success, #16a34a);
  border-color: var(--app-success, #16a34a);
}
.ep__badge--off {
  color: var(--app-text-secondary);
}
.ep__badge--auto {
  color: var(--app-warning, #d97706);
  border-color: var(--app-warning, #d97706);
}
.ep__badge--success {
  color: var(--app-success, #16a34a);
  border-color: var(--app-success, #16a34a);
}
.ep__badge--failed {
  color: var(--app-danger, #dc2626);
  border-color: var(--app-danger, #dc2626);
}
.ep__badge--running,
.ep__badge--skipped {
  color: var(--app-text-secondary);
}
</style>
