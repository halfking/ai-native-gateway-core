<script setup lang="ts">
// SimpleOverviewView.vue — 简易模式总览页（2026-11-17）
//
// 设计稿：docs/03-design/02-feature-design/simple-mode-concept-v2.html §四
// 借鉴：docs/03-design/02-feature-design/simple-mode-v2-magpie-comparison.md
//
// ★ 这一页要回答初级用户的三个问题，顺序不能乱：
//   ① 现在能不能用？      ⇒ 状态条（一句话结论 + 状态点）
//   ② 今天跑了多少？      ⇒ 统计卡行（今日口径）
//   ③ 我现在该做什么？    ⇒ 常用操作 + 接入进度
//   ④ 我的客户端在用什么？ ⇒ 接入清单 + 上游健康
//
// ★ 为什么把 v2 的「接入清单化」保留、又在上面加一层：
//   v2 设计稿的首页是「一张接入清单」（借鉴 magpie 的 B1），
//   但它自己也在风险表 G1 里登记过：「首页换成清单后，今天花了多少变弱」。
//   ⇒ 这一页是 v2 稿的**完整兑现**：清单仍在（主体），
//     统计 / 状态 / 快捷操作补在上面（G1 的处置）。
//
// ★ 数据口径**与专家面完全一致**（同一批 API、同一张表）：
//   /api/admin/dashboard/board ⇒ summary / pies / operational
//   /api/providers              ⇒ 上游健康
//   /api/keys                   ⇒ 密钥清单
//   简易模式不新建任何表、不新造任何统计口径。
//
// ★ ★ 性能纪律（2026-11-17 修）：
//   旧实现对**每一个密钥**发一次 /api/usage/{id}/models（线上 208 个密钥
//   ⇒ 208 个请求）。这一页改为**三个请求**：board 一次、providers 一次、
//   keys 一次；「当前模型」改用 board 自带的 pies.clients / pies.models
//   （同一张聚合表，一并返回）⇒ 208 → 3。
//
// ★ 诚实边界（降级必须可见）：
//   · 看板 summary 降级 ⇒ 统计卡显示占位符，**不显示 0**
//   · 上游健康读不到（无权限 / 接口失败）⇒ 面板写「读不到」，
//     状态条也**不宣称健康**（magpie 的绿点语义：不知道 ≠ 正常）
//   · 三个数据源互相独立降级 ⇒ 一个失败不拖垮整页
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getKeys, getProviders, type ApiKey, type Provider } from '../api'
import { ApiError } from '../api/_core'
import { fetchDashboardBoard, type BoardPayload } from '../api/board'
import PageHeader from '../components/ui/PageHeader.vue'
import StatCard from '../components/ui/StatCard.vue'
import StatsRow from '../components/ui/StatsRow.vue'
import { useWorkMode } from '../composables/useWorkMode'
import { surfaceError } from '../i18n/errorSurface'
import { fmtDateTimeShort } from '../i18n/useFormat'
import {
  deriveOnboardingSteps,
  deriveOverviewStats,
  deriveOverviewStatus,
  deriveProviderHealth,
  onboardingComplete,
  type OverviewIssue,
  type OverviewStat,
  type ProviderHealthRow,
} from '../composables/simpleOverviewModel'

const { t } = useI18n()
const router = useRouter()
const { isSimple } = useWorkMode()

// ─── 状态 ────────────────────────────────────────────────────────────────────

const board = ref<BoardPayload | null>(null)
const providers = ref<Provider[] | null>(null)
const providersDenied = ref(false)
const keys = ref<ApiKey[]>([])
const loading = ref(false)
const error = ref('')

const boardFailed = ref(false)
const providersFailed = ref(false)
const keysFailed = ref(false)

// ─── 取数 ────────────────────────────────────────────────────────────────────

/**
 * 三个数据源**各自独立**加载：任一失败只降级自己那一块，
 * 不让一个接口把整页拖垮（Promise.all 会让最小的一个失败带走全部）。
 */
async function load(): Promise<void> {
  loading.value = true
  error.value = ''

  const [boardRes, providersRes, keysRes] = await Promise.allSettled([
    fetchDashboardBoard({ days: 1 }),
    getProviders(),
    getKeys(),
  ])

  if (boardRes.status === 'fulfilled') {
    board.value = boardRes.value
    boardFailed.value = false
  } else {
    board.value = null
    boardFailed.value = true
  }

  if (providersRes.status === 'fulfilled') {
    providers.value = providersRes.value
    providersFailed.value = false
    // 403 = 权限不足（ProviderConsoleMiddleware：非 default 租户的 tenant_admin）
    // ⇒ 与「服务异常」分开说：前者是权限问题，后者是故障。
    providersDenied.value = false
  } else {
    providers.value = null
    providersFailed.value = true
    providersDenied.value =
      providersRes.reason instanceof ApiError &&
      (providersRes.reason.status === 403 || providersRes.reason.status === 401)
  }

  if (keysRes.status === 'fulfilled') {
    keys.value = keysRes.value
    keysFailed.value = false
  } else {
    keys.value = []
    keysFailed.value = true
  }

  // 三个源全失败 ⇒ 这是真故障，给一条带恢复动作的错误（errorSurface 出口）
  if (boardFailed.value && providersFailed.value && keysFailed.value) {
    error.value = surfaceError(boardRes.status === 'rejected' ? boardRes.reason : new Error('load failed'), {
      key: 'simpleMode.overview.loadFailed',
    })
  }

  loading.value = false
}

onMounted(load)

// ─── 派生（全部走纯函数，便于单测钉住判据）────────────────────────────────────

const summaryDegraded = computed(() => {
  const b = board.value
  if (!b) return true
  // ★ 降级标记在 summary **内部**（BoardSummary.degraded_summary /
  //   summary_missing_view），不是顶层 —— 读错一层就会把「整屏不可信」
  //   当成「一切正常」，页面上就会出现 0 与真实数字混排。
  return b.summary?.degraded_summary === true || b.summary?.summary_missing_view != null
})

const providerHealth = computed(() =>
  deriveProviderHealth(providers.value, {
    unavailable: providersFailed.value,
  }),
)

const selfcheck = computed(() => board.value?.operational?.selfcheck ?? null)

const status = computed(() =>
  deriveOverviewStatus({
    providerHealth: providerHealth.value,
    selfcheck: selfcheck.value,
    statsDegraded: summaryDegraded.value,
    statsMissing: boardFailed.value,
  }),
)

const stats = computed<OverviewStat[]>(() =>
  deriveOverviewStats({
    summary: board.value?.summary ?? null,
    degraded: summaryDegraded.value,
    keyTotal: keys.value.length,
    keyEnabled: keys.value.filter((k) => k.enabled).length,
  }),
)

const enabledKeys = computed(() => keys.value.filter((k) => k.enabled).length)
const keysInUse = computed(() => keys.value.filter((k) => !!k.last_used_at).length)

const onboardingSteps = computed(() =>
  deriveOnboardingSteps({
    providerHealth: providerHealth.value,
    keysEnabled: enabledKeys.value,
    keysInUse: keysInUse.value,
  }),
)

/** 三步全完成 ⇒ 引导卡自动消失（magpie app.js:1740 同款） */
const showOnboarding = computed(() => !onboardingComplete(onboardingSteps.value))

// ─── 状态条文案 ──────────────────────────────────────────────────────────────

const statusHeadline = computed(() => {
  switch (status.value.tone) {
    case 'blocked':
      return t('simpleMode.overview.statusBlocked')
    case 'warn':
      return t('simpleMode.overview.statusWarn')
    default:
      return t('simpleMode.overview.statusOk')
  }
})

/** 状态条右侧的一行关键数字（读不到就不显示，绝不用 0 顶替） */
const statusDetail = computed(() => {
  const b = board.value
  if (!b || summaryDegraded.value) return t('simpleMode.overview.statusDetailUnknown')
  const requests = b.summary?.total_requests
  const rate = b.summary?.success_rate
  const usable = providerHealth.value.unavailable ? null : providerHealth.value.usable
  return t('simpleMode.overview.statusDetail', {
    requests: typeof requests === 'number' ? requests.toLocaleString('en-US') : '-',
    rate: typeof rate === 'number' ? `${(rate * 100).toFixed(1)}%` : '-',
    providers: usable == null ? '-' : String(usable),
  })
})

const statusIssues = computed<OverviewIssue[]>(() => status.value.issues)

// ─── 接入清单 ────────────────────────────────────────────────────────────────

interface AccessRow {
  key: ApiKey
  name: string
  stateKey: 'ok' | 'idle' | 'pending' | 'off'
  spend: string
  budget: string
  lastUsed: string
  requests: string
}

/** 在用的密钥排前面，已禁用/未使用的收进折叠（magpie 的 offCount 同款） */
const activeRows = computed<AccessRow[]>(() =>
  keys.value
    .filter((k) => k.enabled && (k.status === 'active' || k.status === 'pending'))
    .map(toAccessRow),
)

const inactiveRows = computed<AccessRow[]>(() =>
  keys.value
    .filter((k) => !(k.enabled && (k.status === 'active' || k.status === 'pending')))
    .map(toAccessRow),
)

function toAccessRow(k: ApiKey): AccessRow {
  return {
    key: k,
    name: k.key_alias || k.application_code || k.key_prefix,
    stateKey: stateOf(k),
    spend: fmtUsd(k.total_cost_usd),
    budget: k.budget_usd == null ? '-' : fmtUsd(k.budget_usd),
    lastUsed: k.last_used_at ? fmtDateTimeShort(k.last_used_at) : t('simpleMode.accessList.neverUsed'),
    requests: k.total_requests.toLocaleString('en-US'),
  }
}

function stateOf(k: ApiKey): AccessRow['stateKey'] {
  if (k.status === 'pending') return 'pending'
  if (!k.enabled) return 'off'
  if (!k.last_used_at) return 'idle'
  return 'ok'
}

function stateLabel(state: AccessRow['stateKey']): string {
  switch (state) {
    case 'ok':
      return t('simpleMode.accessList.healthOk')
    case 'idle':
      return t('simpleMode.accessList.healthIdle')
    case 'pending':
      return t('common.status.pending')
    default:
      return t('common.status.disabled')
  }
}

function stateClass(state: AccessRow['stateKey']): string {
  switch (state) {
    case 'ok':
      return 'badge-green'
    case 'idle':
      return 'badge-gray'
    case 'pending':
      return 'badge-yellow'
    default:
      return 'badge-red'
  }
}

function fmtUsd(v: number | null | undefined): string {
  if (v == null) return '-'
  return `$${v.toFixed(2)}`
}

// ─── 上游健康 ────────────────────────────────────────────────────────────────

function providerToneClass(row: ProviderHealthRow): string {
  switch (row.tone) {
    case 'ok':
      return 'badge-green'
    case 'warn':
      return 'badge-yellow'
    case 'danger':
      return 'badge-red'
    default:
      return 'badge-gray'
  }
}

function providerToneLabel(row: ProviderHealthRow): string {
  switch (row.tone) {
    case 'ok':
      return t('simpleMode.overview.providerUsable')
    case 'warn':
      return t('simpleMode.overview.providerWarning')
    case 'danger':
      return t('simpleMode.overview.providerUnreachable')
    default:
      return t('simpleMode.overview.providerUnknown')
  }
}

// ─── 最常用的模型（今日）─────────────────────────────────────────────────────

const topModels = computed(() => {
  const models = board.value?.pies?.models
  if (!Array.isArray(models)) return []
  return models.slice(0, 5)
})

// ─── 操作 ────────────────────────────────────────────────────────────────────

function go(path: string): void {
  void router.push(path)
}

function openSignKey(): void {
  // ★ 直接调 router.push（不经过 go 包装）：navCoverage 的孤儿路由扫描器
  //   按「行内出现 router.push + 路径字面量」判定可达性，包一层就看不见了。
  void router.push('/simple/keys')
}

/**
 * 「查看请求」→ 请求日志页（/request-logs）。
 *
 * ★ 2026-11-17 实测修正：本按钮原先跳 `/usage`，而 **`/usage` 从来没有路由定义**
 *   （router.ts 里没有；appNav 的 requests-sessions 组里也没有）⇒ 点进去是
 *   vue-router 的 no-match 空白页。同一处幻觉还出现在 simpleNav.ts 与
 *   scripts/audit/gen-simple-mode-ledger.mjs 的分组清单里（两边同源 ⇒ 互相掩盖）。
 *
 * ★ 为什么**不**跳 `/tenant/usage`（并行列车把幻觉改成了这个真实存在的路径）：
 *   取证（super_admin 实开，2026-11-17）：
 *     · /tenant/usage ⇒ 「我的消耗 / 积分流水」，积分消耗 **0 积分**、
 *       「最近 50 条 consume 记录：**0 笔**」、空态「暂无流水记录。去购买积分」
 *       —— 它是**租户积分账单**视角，super_admin 没有积分账户 ⇒ 落到空态。
 *     · /request-logs ⇒ 「请求日志」，17,674 条真实流水 + 模型统计，
 *       **全角色可用**（router.ts:300 无 meta 限定），与统计卡「今日请求」呼应。
 *   ⇒ 按钮走 /request-logs：信息量足、没有空态陷阱，也不与本轮
 *     「读不到 ≠ 0、不给用户看假零」的诚实口径冲突。
 *   ★ 两条口径别混：simpleNav 的分组清单仍保留 '/tenant/usage'
 *     （那是**导航投影**口径 —— 对租户用户它是对的入口）。
 *   文案用**已存在**的 `action.requests`（8 语种齐备，无需新增键）。
 */
function openRequestLogs(): void {
  void router.push('/request-logs')
}

function openDetail(k: ApiKey): void {
  go(`/keys/${k.id}`)
}
</script>

<template>
  <div class="simple-overview">
    <PageHeader :title="t('simpleMode.overview.title')" :subtitle="t('simpleMode.overview.subtitle')">
      <template #actions>
        <span class="chip">{{ t('simpleMode.mode.label') }} · {{ isSimple ? t('simpleMode.mode.simple') : t('simpleMode.mode.expert') }}</span>
        <button class="btn btn-ghost" :disabled="loading" @click="load">
          {{ t('common.button.refresh') }}
        </button>
        <button class="btn btn-primary" @click="openSignKey">
          {{ t('simpleMode.accessList.signKey') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="error" class="alert alert-danger" data-testid="overview-error">{{ error }}</div>

    <!-- ① 状态条：一句话结论 -->
    <section
      class="status-bar"
      :class="`status-bar--${status.tone}`"
      data-testid="overview-status"
      :data-tone="status.tone"
    >
      <span class="status-dot" aria-hidden="true"></span>
      <div class="status-text">
        <strong class="status-headline">{{ statusHeadline }}</strong>
        <span class="status-detail">{{ statusDetail }}</span>
      </div>
      <ul v-if="statusIssues.length > 0" class="status-issues" data-testid="overview-status-issues">
        <li v-for="issue in statusIssues" :key="issue">
          {{ t(`simpleMode.overview.issue.${issue}`) }}
        </li>
      </ul>
    </section>

    <!-- ② 统计卡：今日口径 -->
    <section class="overview-stats" data-testid="overview-stats">
      <StatsRow :cols="{ mobile: 2, tablet: 3, desktop: 6 }">
        <StatCard
          v-for="stat in stats"
          :key="stat.key"
          :label="t(`simpleMode.overview.stat.${stat.key}`)"
          :value="stat.value ?? t('simpleMode.overview.statUnavailable')"
          :sub="stat.scope ? t(`simpleMode.overview.statScope.${stat.scope}`) : ''"
          :tone="stat.tone"
          compact
        />
      </StatsRow>
    </section>

    <!-- ③ 常用操作 -->
    <section class="quick-actions" data-testid="overview-quick-actions">
      <button class="btn btn-secondary" data-testid="quick-add-provider" @click="go('/providers')">
        {{ t('simpleMode.overview.action.addProvider') }}
      </button>
      <button class="btn btn-secondary" data-testid="quick-sign-key" @click="openSignKey">
        {{ t('simpleMode.overview.action.signKey') }}
      </button>
      <button class="btn btn-secondary" data-testid="quick-requests" @click="openRequestLogs">
        {{ t('simpleMode.overview.action.requests') }}
      </button>
      <button class="btn btn-secondary" data-testid="quick-chat" @click="go('/chat')">
        {{ t('simpleMode.overview.action.chat') }}
      </button>
    </section>

    <!-- ④ 接入进度：三步都完成就自动消失 -->
    <section v-if="showOnboarding" class="onboarding" data-testid="overview-onboarding">
      <div class="onboarding-head">{{ t('simpleMode.overview.onboardingTitle') }}</div>
      <ol class="onboarding-steps">
        <li
          v-for="step in onboardingSteps"
          :key="step.key"
          class="onboarding-step"
          :class="step.done ? 'is-done' : 'is-pending'"
          :data-step="step.key"
          :data-done="step.done ? '1' : '0'"
        >
          <span class="step-dot" aria-hidden="true"></span>
          <span class="step-label">
            {{ step.done ? t(`simpleMode.overview.onboarding.${step.key}Done`, { count: step.count }) : t(`simpleMode.overview.onboarding.${step.key}`) }}
          </span>
        </li>
      </ol>
    </section>

    <!-- ⑤ 主体：接入清单 + 上游健康 -->
    <div class="overview-cols">
      <section class="card overview-panel" data-testid="overview-access-list">
        <div class="panel-head">
          <span class="panel-title">{{ t('simpleMode.accessList.title') }}</span>
          <span class="panel-hint">{{ t('simpleMode.accessList.subtitle') }}</span>
        </div>

        <div v-if="loading" class="empty">{{ t('common.feedback.loading') }}</div>
        <div v-else-if="keysFailed" class="empty">{{ t('simpleMode.overview.accessListUnavailable') }}</div>
        <div v-else-if="activeRows.length === 0 && inactiveRows.length === 0" class="empty">
          <p>{{ t('simpleMode.accessList.empty') }}</p>
          <p class="section-hint">{{ t('simpleMode.accessList.emptyHint') }}</p>
        </div>
        <template v-else>
          <div v-if="activeRows.length > 0" class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>{{ t('simpleMode.accessList.client') }}</th>
                  <th style="width:110px">{{ t('common.table.status') }}</th>
                  <th style="width:110px">{{ t('simpleMode.accessList.requests') }}</th>
                  <th style="width:170px">{{ t('simpleMode.accessList.spend') }}</th>
                  <th style="width:160px">{{ t('simpleMode.accessList.lastUsed') }}</th>
                  <th style="width:90px"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in activeRows" :key="r.key.id">
                  <td>
                    <strong>{{ r.name }}</strong>
                    <span class="section-hint">{{ r.key.key_prefix }}</span>
                  </td>
                  <td><span class="badge" :class="stateClass(r.stateKey)">{{ stateLabel(r.stateKey) }}</span></td>
                  <td>{{ r.requests }}</td>
                  <td>{{ r.spend }} / {{ r.budget }}</td>
                  <td>{{ r.lastUsed }}</td>
                  <td>
                    <button class="btn btn-ghost btn-sm" @click="openDetail(r.key)">
                      {{ t('common.button.detail') }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <details v-if="inactiveRows.length > 0" class="fold" data-testid="overview-access-fold">
            <summary>
              <span>{{ t('simpleMode.overview.inactiveLabel') }}</span>
              <span class="count-badge">{{ inactiveRows.length }}</span>
            </summary>
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{{ t('simpleMode.accessList.client') }}</th>
                    <th style="width:110px">{{ t('common.table.status') }}</th>
                    <th style="width:110px">{{ t('simpleMode.accessList.requests') }}</th>
                    <th style="width:170px">{{ t('simpleMode.accessList.spend') }}</th>
                    <th style="width:160px">{{ t('simpleMode.accessList.lastUsed') }}</th>
                    <th style="width:90px"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in inactiveRows" :key="r.key.id">
                    <td>
                      <strong>{{ r.name }}</strong>
                      <span class="section-hint">{{ r.key.key_prefix }}</span>
                    </td>
                    <td><span class="badge" :class="stateClass(r.stateKey)">{{ stateLabel(r.stateKey) }}</span></td>
                    <td>{{ r.requests }}</td>
                    <td>{{ r.spend }} / {{ r.budget }}</td>
                    <td>{{ r.lastUsed }}</td>
                    <td>
                      <button class="btn btn-ghost btn-sm" @click="openDetail(r.key)">
                        {{ t('common.button.detail') }}
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </details>

          <div class="card-toolbar">
            <span class="section-hint">{{ t('simpleMode.accessList.usageHint') }}</span>
          </div>
        </template>
      </section>

      <div class="overview-side">
        <section class="card overview-panel" data-testid="overview-provider-health">
          <div class="panel-head">
            <span class="panel-title">{{ t('simpleMode.overview.providerTitle') }}</span>
            <span v-if="!providerHealth.unavailable" class="count-badge">{{ providerHealth.usable }}</span>
          </div>

          <div v-if="providerHealth.unavailable" class="empty">
            {{ providersDenied ? t('simpleMode.overview.providerDenied') : t('simpleMode.overview.providerUnavailable') }}
          </div>
          <div v-else-if="providerHealth.total === 0" class="empty">
            {{ t('simpleMode.overview.providerEmpty') }}
          </div>
          <template v-else>
            <ul class="provider-health-list">
              <li v-for="row in providerHealth.rows" :key="row.id" class="provider-health-row">
                <span class="provider-name">{{ row.name }}</span>
                <span class="badge" :class="providerToneClass(row)">{{ providerToneLabel(row) }}</span>
                <span class="provider-count">{{ row.credentialCount }}</span>
              </li>
            </ul>
            <div class="card-toolbar">
              <span class="section-hint">
                {{ t('simpleMode.overview.providerSummary', {
                  usable: providerHealth.usable,
                  warning: providerHealth.warning,
                  unreachable: providerHealth.unreachable,
                  unknown: providerHealth.unknown,
                }) }}
              </span>
            </div>
          </template>
        </section>

        <section class="card overview-panel" data-testid="overview-top-models">
          <div class="panel-head">
            <span class="panel-title">{{ t('simpleMode.overview.topModelsTitle') }}</span>
          </div>
          <div v-if="summaryDegraded" class="empty">{{ t('simpleMode.overview.statUnavailable') }}</div>
          <div v-else-if="topModels.length === 0" class="empty">{{ t('simpleMode.overview.topModelsEmpty') }}</div>
          <ul v-else class="model-list">
            <li v-for="m in topModels" :key="m.key" class="model-row">
              <span class="model-name mono">{{ m.key }}</span>
              <span class="model-count">{{ m.requests.toLocaleString('en-US') }}</span>
            </li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.simple-overview {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-4);
}

/* ── 状态条 ────────────────────────────────────────────────────────────────
 * 状态点用「圆点 + 文字结论」两重信号：颜色不是唯一线索（色盲可读）。
 * 不用彩色侧边条（craft-floor 禁 ≥2px 彩色侧条）。 */
.status-bar {
  display: flex;
  align-items: flex-start;
  gap: var(--kx-space-3);
  padding: var(--kx-space-3) var(--kx-space-4);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md);
  background: var(--kx-surface);
}

.status-dot {
  flex: none;
  width: 10px;
  height: 10px;
  margin-top: var(--kx-space-1);
  border-radius: 50%;
  background: var(--kx-muted);
}

.status-bar--ok .status-dot { background: var(--success); }
.status-bar--warn .status-dot { background: var(--warning); }
.status-bar--blocked .status-dot { background: var(--danger); }

.status-text {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  min-width: 0;
  flex: 1;
}

.status-headline {
  font-size: var(--kx-text-md);
  font-weight: 600;
  color: var(--kx-fg);
}

.status-detail {
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
}

.status-issues {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
}

/* ── 常用操作 ────────────────────────────────────────────────────────────── */
.quick-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-2);
}

/* ── 接入进度 ────────────────────────────────────────────────────────────── */
.onboarding {
  padding: var(--kx-space-3) var(--kx-space-4);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md);
  background: var(--kx-surface);
}

.onboarding-head {
  font-size: var(--kx-text-sm);
  font-weight: 600;
  color: var(--kx-muted);
  margin-bottom: var(--kx-space-2);
}

.onboarding-steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-4);
}

.onboarding-step {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  font-size: var(--kx-text-sm);
}

.step-dot {
  flex: none;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--kx-border);
}

.onboarding-step.is-done .step-dot { background: var(--success); }
.onboarding-step.is-pending .step-dot { background: var(--warning); }

.onboarding-step.is-pending .step-label { color: var(--kx-fg); }
.onboarding-step.is-done .step-label { color: var(--kx-muted); }

/* ── 主体两栏 ────────────────────────────────────────────────────────────── */
.overview-cols {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
  gap: var(--kx-space-4);
  align-items: start;
}

.overview-side {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-4);
}

.overview-panel {
  padding: var(--kx-space-4);
}

.panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--kx-space-3);
  margin-bottom: var(--kx-space-3);
}

.panel-title {
  font-size: var(--kx-text-md);
  font-weight: 600;
  color: var(--kx-fg);
}

.panel-hint {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

/* ── 上游健康列表 ────────────────────────────────────────────────────────── */
.provider-health-list,
.model-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}

.provider-health-row,
.model-row {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  padding: var(--kx-space-2) 0;
  border-bottom: 1px solid var(--kx-border);
  font-size: var(--kx-text-sm);
}

.provider-name,
.model-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-count,
.model-count {
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

/* ── 折叠（已禁用 / 未使用）──────────────────────────────────────────────── */
.fold {
  margin-top: var(--kx-space-3);
}

.fold > summary {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  cursor: pointer;
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
  padding: var(--kx-space-2) 0;
}

.count-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  padding: 0 var(--kx-space-1);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-accent);
  color: var(--kx-fg);
  font-size: var(--kx-text-xs);
  font-variant-numeric: tabular-nums;
}

/* ── 窄屏：两栏塌成一栏 ─────────────────────────────────────────────────── */
@media (max-width: 1024px) {
  .overview-cols {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 768px) {
  .status-bar {
    flex-direction: column;
    gap: var(--kx-space-2);
  }

  .onboarding-steps {
    flex-direction: column;
    gap: var(--kx-space-2);
  }
}
</style>
