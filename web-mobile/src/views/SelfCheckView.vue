<script setup lang="ts">
// SelfCheckView — 系统自检面（2026-10-08，第一百零六批）。
//
// 五个 GET 端点，全部注册在 `admin/self_check_handlers.go:60-68` 的 `admin(...)`：
//   GET /api/self-check/runs                  （:61）
//   GET /api/self-check/settings              （:63）
//   GET /api/self-check/trigger/availability  （:65）
//   GET /api/self-check/stats                 （:67）
//   GET /api/self-check/models                （:68）
// （同族的 `settings/update`（:64）与 `trigger`（:66）是 superAdmin + 写操作，本页不碰。）
//
// ══════════════════════════════════════════════════════════════════════════
// ★★★★★★ 门控：前四段里有**三段跨租户**（详见 api/selfCheck.ts 文件头第 (1) 条）
// ══════════════════════════════════════════════════════════════════════════
//
// `self_check_runs` 表**有** `tenant_id text NOT NULL DEFAULT 'default'`
// （`01-schema.sql` 建表第 19 行），但六个 handler **一个都不引用它**：
//   `handleListRuns` 是 `WHERE 1=1` + 仅 model/status 可选条件（`:126-150`）
//   `handleGetRun` 是 `WHERE id=$1`（`:223`）
//   `handleStats` 三处是 `WHERE started_at >= $1`
//   `handleModels` 是 `GROUP BY model_name`
//
// 而注册是 `admin(...)` ⇒ **tenant_admin 能读所有租户的自检记录**，
// 含 `error_detail` / `upstream_error` / `attempted_models`；run 详情更进一步取
// `request_body` / `response_preview`（`:241-242`）——**别的租户的完整请求/响应正文**。
//
// ⇒ 这是本仓**第四次**「不隔离 + admin 档」，与前三次不同的是
//   **列就在表里，只是查询从不引用它** —— 不是「表没有租户概念」。
//
// ★ 前置决定：本页**不做 run 详情**（正文级内容最多的一条），留给后续批次单独收口。
// ★ 门控只挡跨租户的三段；`settings` 与 `trigger/availability` 是网关自身配置与能力探测，
//   不含别的租户的数据 ⇒ 保持 admin 档可见。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import {
  fetchSelfCheckRuns,
  fetchSelfCheckSettings,
  fetchSelfCheckTriggerAvailability,
  fetchSelfCheckStats,
  fetchSelfCheckModels,
  selfCheckCredentialIdFromLabel,
  selfCheckCredentialIdMatches,
  selfCheckCountedStatusTotal,
  selfCheckHasInFlightRuns,
  selfCheckSuccessRateIsMeaningless,
  selfCheckProbeSystemLooksUnqueried,
  selfCheckStatsRangeIsEffectiveRange,
  selfCheckStatsEffectiveRange,
  selfCheckModelCountsLeaveResidual,
  selfCheckUpstreamOutcome,
  SELF_CHECK_STATS_RANGES,
  SELF_CHECK_RUN_STATUSES,
  SELF_CHECK_RUNS_DEFAULT_LIMIT,
  type ScRunListResponse,
  type ScRun,
  type ScSettings,
  type ScTriggerAvailability,
  type ScStatsResponse,
  type ScModelsResponse,
} from '@/api/selfCheck'

useHyperPage({ title: () => t('sc.title') })

const auth = useAuthStore()
const isSuperAdmin = computed(() => auth.role === 'super_admin')

type SectionKey = 'runs' | 'stats' | 'models' | 'settings' | 'trigger'

/** ★★ 跨租户的三段 —— 非 super_admin 连**加载按钮**都不给。 */
const CROSS_TENANT: ReadonlySet<SectionKey> = new Set<SectionKey>(['runs', 'stats', 'models'])
function sectionAllowed(s: SectionKey): boolean {
  return !CROSS_TENANT.has(s) || isSuperAdmin.value
}

const runs = ref<ScRunListResponse | null>(null)
const stats = ref<ScStatsResponse | null>(null)
const models = ref<ScModelsResponse | null>(null)
const settings = ref<ScSettings | null>(null)
const trigger = ref<ScTriggerAvailability | null>(null)
const errors = ref<Partial<Record<SectionKey, string>>>({})
const loading = ref<SectionKey | null>(null)

/** ★ stats 的窗口选择。只发合法值（后端对未知 range **静默落 24h**，不回错）。 */
const range = ref<string>(SELF_CHECK_STATS_RANGES[2])

/** ★ runs 的 status 筛选（后端 `WHERE 1=1 + AND status = $n`，精确匹配）。 */
const runStatus = ref<string>('')
const RUN_STATUS_FILTERS = ['', ...SELF_CHECK_RUN_STATUSES] as const

/**
 * ★★★ 「绿灯」可能是探测管线已死。
 * 后端 `probe_system` 的两个查询错误都被 `_ =` 丢弃（`:941`/`:949`），
 * 查询失败 ⇒ 全零 ⇒ `queue_ready_unclaimable == 0 && last_activity_at == nil`
 * ⇒ `healthy = true`。而这个区块存在的理由（`:926-931`）恰恰是
 * 「页面绿灯但探测管线已死」（glm-5.2 事故）。
 */
/** ★★★ 两个探测查询都失败时的形状 —— 这时后端会算出 `healthy: true`。 */
const probeLooksUnqueried = computed(() =>
  stats.value ? selfCheckProbeSystemLooksUnqueried(stats.value.probe_system) : false,
)

/** ★★ `total_runs === 0` ⇒ `success_rate` 恒 0.0 ⇒ 与「全失败」同值。 */
const rateMeaningless = computed(() =>
  stats.value ? selfCheckSuccessRateIsMeaningless(stats.value.summary) : false,
)

/** ★★ 三项之和小于 total ⇒ 存在 running / retrying 的 run（status 五值，统计只数三个）。 */
const hasInFlight = computed(() =>
  stats.value ? selfCheckHasInFlightRuns(stats.value.summary) : false,
)

/** ★★ `range` 回显的是请求原值；未知值静默落 24h ⇒ 要说清实际窗口。 */
const echoedRangeEffective = computed(() =>
  stats.value ? selfCheckStatsRangeIsEffectiveRange(stats.value) : true,
)
const effectiveRange = computed(() => (stats.value ? selfCheckStatsEffectiveRange(stats.value) : ''))

/** ★★ 每行 `credential_id` 与 `model_name` 推导值是否一致（写操作漂移在这里能抓到）。 */
function credentialDrifted(r: ScRun): boolean {
  return !selfCheckCredentialIdMatches(r)
}

/**
 * ★★ `upstream_latency_ms` 是 `int` + omitempty ⇒ **0 毫秒是键缺失，不是 0**。
 * 对照第一百零五批的 `*int`（那里 0 会**出现**）—— 同一家族，**相反方向**。
 *
 * ★★★ 为什么用 `!== undefined` 而不是 `!`：**在本族所有可达取值上两者行为一致**
 *   —— 唯一能区分它们的 `upstream_latency_ms === 0` 被 `omitempty` 吃掉、后端产生不出来。
 *   ⇒ 所以这不是「一种实现更对」，而是**可证等价**；`!== undefined` 取的是防御形态
 *     （万一将来有人去掉 `omitempty`，它不会把 0 说成「没测到」）。
 *   ⇒ 因此**不为这个区别写判据** —— 能钉住它的那个夹具必须是后端发不出的值。
 */
function latencyText(r: ScRun): string {
  if (r.upstream_latency_ms === undefined) return t('sc.noLatency')
  return `${r.upstream_latency_ms} ms`
}

/** ★★ `credential_id` 不是 DB 列，是从 `model_name` 推导的；非 `cred-<正整数>` ⇒ 键缺失。 */
function credentialText(r: ScRun): string {
  const id = selfCheckCredentialIdFromLabel(r.model_name)
  if (id === null) return t('sc.credentialAbsent')
  return `${r.model_name} → ${id}`
}

/**
 * ★★ `/models` 没有 partial 计数 ⇒ `total - success - failed` 的残差拿不到归属，
 * 而 stats 的 by_model **有** partial ⇒ 两个端点同一口径的数字**不可直接比**。
 */
const modelsResidual = computed(() =>
  (models.value?.models ?? []).map((m) => selfCheckModelCountsLeaveResidual(m)),
)
const modelsHasResidual = computed(() => modelsResidual.value.some((n) => n !== 0))

async function load(section: SectionKey): Promise<void> {
  // ★★ 这里原先还有一句 `if (!sectionAllowed(section)) return` 作为第二道取数屏障。
  //   **实测它是死代码，已删**（第一百零六批）：`load(section)` 的唯一触发器是
  //   该段的加载按钮，而按钮随 `<section v-if="sectionAllowed(...)">` 一起不存在了，
  //   组件也没有 onMounted 自动加载 ⇒ 分支**可证不可达**。
  //   ⇒ 留着一个打不到的分支，再配一条「它没被调用」的判据，就是**恒真判据** ——
  //     判据全绿不代表有人在管，只代表没人能触发它。这正是第一百零四批的原话原教训。
  //   ⇒ 现在这一族的取数边界是「按钮只对 super_admin 存在」这一条事实本身。
  loading.value = section
  errors.value[section] = undefined
  try {
    if (section === 'runs') {
      runs.value = await fetchSelfCheckRuns({
        limit: SELF_CHECK_RUNS_DEFAULT_LIMIT,
        // ★ 空串 = 不发这个参数（模块的 fetch 只在非空时拼）。
        ...(runStatus.value ? { status: runStatus.value } : {}),
      })
    }
    else if (section === 'stats') stats.value = await fetchSelfCheckStats(range.value)
    else if (section === 'models') models.value = await fetchSelfCheckModels()
    else if (section === 'settings') settings.value = await fetchSelfCheckSettings()
    else trigger.value = await fetchSelfCheckTriggerAvailability()
  } catch (err) {
    // ★ 失败 ⇒ 清空该段，绝不保留上一次的成功结果冒充本次。
    if (section === 'runs') runs.value = null
    if (section === 'stats') stats.value = null
    if (section === 'models') models.value = null
    if (section === 'settings') settings.value = null
    if (section === 'trigger') trigger.value = null
    errors.value[section] = (err as Error)?.message || String(err)
  } finally {
    loading.value = null
  }
}

onBeforeUnmount(() => {
  runs.value = null
  stats.value = null
  models.value = null
  settings.value = null
  trigger.value = null
  errors.value = {}
})
</script>

<template>
  <div class="sc">
    <!-- ══ 1. 运行记录（跨租户 ⇒ super_admin） ══ -->
    <section v-if="sectionAllowed('runs')" class="sc__sec">
      <header class="sc__head">
        <h2 class="sc__h">{{ t('sc.runs.title') }}</h2>
        <button type="button" class="sc__btn" :disabled="loading === 'runs'" @click="load('runs')">
          {{ runs ? t('sc.reload') : t('sc.load') }}
        </button>
      </header>
      <div class="sc__chips">
        <button
          v-for="st in RUN_STATUS_FILTERS"
          :key="st || 'all'"
          type="button"
          class="sc__chip-btn"
          :class="{ 'sc__chip-btn--on': st === runStatus }"
          :disabled="loading === 'runs'"
          @click="runStatus = st; load('runs')"
        >
          {{ st === '' ? t('sc.runs.allStatuses') : t(`sc.status.${st}`) }}
        </button>
      </div>
      <p class="sc__msg sc__msg--warn">{{ t('sc.crossTenant') }}</p>
      <p v-if="errors.runs" class="sc__msg sc__msg--err">{{ errors.runs }}</p>
      <p v-if="loading === 'runs'" class="sc__msg">{{ t('common.loading') }}</p>
      <template v-if="runs">
        <p class="sc__msg">{{ t('sc.runs.count', { n: runs.items.length, total: runs.total }) }}</p>
        <p v-if="runs.items.length === 0" class="sc__msg">{{ t('sc.runs.empty') }}</p>
        <article v-for="r in runs.items" :key="r.id" class="sc__card">
          <div class="sc__row">
            <span class="sc__badge" :class="`sc__badge--${r.status}`">{{ t(`sc.status.${r.status}`) }}</span>
            <span class="sc__model">{{ r.model_name }}</span>
            <span class="sc__muted">{{ r.started_at }}</span>
          </div>
          <div class="sc__meta">
            <span>{{ t('sc.runs.rounds') }} {{ r.rounds_success }}/{{ r.rounds_total }}</span>
            <span>{{ t('sc.runs.duration') }} {{ r.duration_ms }} ms</span>
            <span>{{ t('sc.runs.avgLatency') }} {{ r.avg_latency_ms }} ms</span>
            <span>{{ t('sc.runs.tokens') }} {{ r.total_tokens }}</span>
          </div>
          <p class="sc__note">
            {{ t('sc.runs.credential') }} {{ credentialText(r) }}
            <span v-if="credentialDrifted(r)" class="sc__note--danger">★ {{ t('sc.runs.credentialDrift') }}</span>
          </p>
          <!-- ★★ upstream_latency_ms 的 0 是**键缺失**，与「确实测到 0」不同形 -->
          <p class="sc__note">
            {{ t('sc.runs.upstreamOutcome') }} {{ t(`sc.outcome.${selfCheckUpstreamOutcome(r)}`) }}
            <!-- ★★ 判据用 `upstream_tested` 这个真信号，不要用 outcome 联合里
                 那个**从不返回**的 'tested' 成员（见下方注释）。 -->
            <template v-if="r.upstream_tested">
              · {{ t('sc.runs.upstreamLatency') }} {{ latencyText(r) }}
            </template>
          </p>
          <p v-if="r.error_type" class="sc__note sc__note--danger">{{ t('sc.runs.errorType') }} {{ r.error_type }}</p>
          <p v-if="r.upstream_error" class="sc__note sc__note--danger">{{ t('sc.runs.upstreamError') }} {{ r.upstream_error }}</p>
        </article>
      </template>
    </section>

    <!-- ══ 2. 统计（跨租户 ⇒ super_admin） ══ -->
    <section v-if="sectionAllowed('stats')" class="sc__sec">
      <header class="sc__head">
        <h2 class="sc__h">{{ t('sc.stats.title') }}</h2>
        <button type="button" class="sc__btn" :disabled="loading === 'stats'" @click="load('stats')">
          {{ stats ? t('sc.reload') : t('sc.load') }}
        </button>
      </header>
      <!-- ★★ range 是请求原值，不是生效窗口；只发合法值（合法值表来自 i18n 之外的白名单） -->
      <div class="sc__chips">
        <button
          v-for="r in SELF_CHECK_STATS_RANGES"
          :key="r"
          type="button"
          class="sc__chip-btn"
          :class="{ 'sc__chip-btn--on': r === range }"
          :disabled="loading === 'stats'"
          @click="range = r; load('stats')"
        >
          {{ r }}
        </button>
      </div>
      <p class="sc__msg sc__msg--warn">{{ t('sc.crossTenant') }}</p>
      <p v-if="errors.stats" class="sc__msg sc__msg--err">{{ errors.stats }}</p>
      <p v-if="loading === 'stats'" class="sc__msg">{{ t('common.loading') }}</p>
      <template v-if="stats">
        <!-- ★★★ 这一段最严重：两个探测查询都失败时后端会算出 healthy=true -->
        <p v-if="probeLooksUnqueried" class="sc__msg sc__msg--warn">{{ t('sc.stats.probeUnqueried') }}</p>
        <p class="sc__msg">
          {{ t('sc.stats.echoedRange', { r: stats.range, eff: effectiveRange }) }}
        </p>
        <!-- ★★ 回显的不是生效窗口：只能由本地复刻算出来，且后端对未知值**静默**回落 -->
        <p v-if="!echoedRangeEffective" class="sc__msg sc__msg--warn">{{ t('sc.stats.rangeEchoInvalid') }}</p>
        <!-- ★★ 三项相加可能小于 total（status 五值，统计只数三个） -->
        <p class="sc__msg">{{ t('sc.stats.counts', {
          total: stats.summary.total_runs,
          s: stats.summary.success_runs,
          p: stats.summary.partial_runs,
          f: stats.summary.failed_runs,
          sub: selfCheckCountedStatusTotal(stats.summary),
        }) }}</p>
        <p v-if="hasInFlight" class="sc__msg sc__msg--warn">{{ t('sc.stats.hasInFlight') }}</p>
        <p class="sc__msg">{{ t('sc.stats.rate', { r: stats.summary.success_rate }) }}</p>
        <p v-if="rateMeaningless" class="sc__msg sc__msg--warn">{{ t('sc.stats.rateMeaningless') }}</p>
        <p v-if="stats.error_breakdown.length === 0" class="sc__msg sc__msg--warn">
          {{ t('sc.stats.noErrors') }}
        </p>
        <p v-if="stats.trend.length === 0" class="sc__msg sc__msg--warn">
          {{ t('sc.stats.noTrend') }}
        </p>
      </template>
    </section>

    <!-- ══ 3. 模型分布（跨租户 ⇒ super_admin） ══ -->
    <section v-if="sectionAllowed('models')" class="sc__sec">
      <header class="sc__head">
        <h2 class="sc__h">{{ t('sc.models.title') }}</h2>
        <button type="button" class="sc__btn" :disabled="loading === 'models'" @click="load('models')">
          {{ models ? t('sc.reload') : t('sc.load') }}
        </button>
      </header>
      <p class="sc__msg sc__msg--warn">{{ t('sc.crossTenant') }}</p>
      <p v-if="errors.models" class="sc__msg sc__msg--err">{{ errors.models }}</p>
      <p v-if="loading === 'models'" class="sc__msg">{{ t('common.loading') }}</p>
      <template v-if="models">
        <!-- ★★ 这个端点**没有 partial 计数** ⇒ 残差拿不到归属 -->
        <p v-if="modelsHasResidual" class="sc__msg sc__msg--warn">{{ t('sc.models.noPartial') }}</p>
        <p class="sc__msg">{{ t('sc.models.allTime') }}</p>
        <p v-if="models.models.length === 0" class="sc__msg">{{ t('sc.models.empty') }}</p>
        <ul class="sc__list">
          <li v-for="m in models.models" :key="m.model_name" class="sc__li">
            {{ m.model_name }} · {{ t('sc.models.counts', { total: m.total, s: m.success, f: m.failed }) }}
            <span v-if="m.last_run" class="sc__muted">{{ m.last_run }}</span>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 4. 设置（网关自身配置 ⇒ 不设门控） ══ -->
    <section class="sc__sec">
      <header class="sc__head">
        <h2 class="sc__h">{{ t('sc.settings.title') }}</h2>
        <button type="button" class="sc__btn" :disabled="loading === 'settings'" @click="load('settings')">
          {{ settings ? t('sc.reload') : t('sc.load') }}
        </button>
      </header>
      <p v-if="errors.settings" class="sc__msg sc__msg--err">{{ errors.settings }}</p>
      <p v-if="loading === 'settings'" class="sc__msg">{{ t('common.loading') }}</p>
      <template v-if="settings">
        <dl class="sc__kv">
          <div class="sc__kv-row"><dt>{{ t('sc.settings.enabled') }}</dt>
            <dd>{{ settings.enabled ? t('sc.yes') : t('sc.no') }}</dd></div>
          <div class="sc__kv-row"><dt>{{ t('sc.settings.normalInterval') }}</dt>
            <dd>{{ settings.normal_interval_seconds }} s</dd></div>
          <div class="sc__kv-row"><dt>{{ t('sc.settings.faultInterval') }}</dt>
            <dd>{{ settings.fault_interval_seconds }} s</dd></div>
          <div class="sc__kv-row"><dt>{{ t('sc.settings.modelSource') }}</dt>
            <dd>{{ settings.model_source }}</dd></div>
          <div class="sc__kv-row"><dt>{{ t('sc.settings.maxModels') }}</dt>
            <dd>{{ settings.max_models }}</dd></div>
        </dl>
      </template>
    </section>

    <!-- ══ 5. 触发可用性（能力探测 ⇒ 不设门控） ══ -->
    <section class="sc__sec">
      <header class="sc__head">
        <h2 class="sc__h">{{ t('sc.trigger.title') }}</h2>
        <button type="button" class="sc__btn" :disabled="loading === 'trigger'" @click="load('trigger')">
          {{ trigger ? t('sc.reload') : t('sc.load') }}
        </button>
      </header>
      <p v-if="errors.trigger" class="sc__msg sc__msg--err">{{ errors.trigger }}</p>
      <p v-if="loading === 'trigger'" class="sc__msg">{{ t('common.loading') }}</p>
      <template v-if="trigger">
        <dl class="sc__kv">
          <div class="sc__kv-row"><dt>{{ t('sc.trigger.available') }}</dt>
            <dd>{{ trigger.available ? t('sc.yes') : t('sc.no') }}</dd></div>
          <div class="sc__kv-row"><dt>{{ t('sc.trigger.newProbeMode') }}</dt>
            <dd>{{ trigger.new_probe_mode ? t('sc.yes') : t('sc.no') }}</dd></div>
          <div v-if="trigger.mode" class="sc__kv-row"><dt>{{ t('sc.trigger.mode') }}</dt>
            <dd>{{ trigger.mode }}</dd></div>
        </dl>
        <!-- ★ 不可用时必须说清原因，不能只给一个 false -->
        <p v-if="!trigger.available" class="sc__msg sc__msg--warn">
          {{ t('sc.trigger.reason', { code: trigger.error_code ?? t('sc.unknown'), reason: trigger.reason ?? '' }) }}
        </p>
      </template>
    </section>
  </div>
</template>

<style scoped>
.sc {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.sc__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.sc__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.sc__h {
  margin: 0;
  font-size: 0.9375rem;
  font-weight: 600;
}

/* R1：新增触控控件一律 ≥48 CSS px */
.sc__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sc__chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1);
}

.sc__chip-btn {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sc__chip-btn--on {
  color: var(--app-text);
  background: var(--app-surface-muted);
  font-weight: 600;
}

.sc__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.sc__msg--err {
  color: var(--app-danger);
}

.sc__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.sc__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.sc__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: baseline;
}

.sc__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
}

.sc__badge--success { color: var(--app-success, var(--app-text)); }
.sc__badge--failed { color: var(--app-danger); }
.sc__badge--partial { color: var(--app-warning, var(--app-danger)); }
.sc__badge--running,
.sc__badge--retrying { color: var(--app-text-secondary); }

.sc__model {
  font-size: 0.875rem;
  font-weight: 600;
  word-break: break-all;
}

.sc__meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.sc__note {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
  word-break: break-all;
}

.sc__note--danger {
  color: var(--app-danger);
}

.sc__muted {
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.sc__list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.sc__li {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  padding: var(--app-space-1) 0;
  word-break: break-all;
}

.sc__kv {
  margin: 0;
}

.sc__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.sc__kv-row dt {
  color: var(--app-text-muted);
}

.sc__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
}
</style>