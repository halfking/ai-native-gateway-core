<script setup lang="ts">
// NodesView — 节点（凭据）健康：连续加载卡片列表 + 搜索（250ms debounce，
// 13 §5）+ 详情 Sheet（每模型探测宽表走专注模式，07 §8）+ **运维操作区**
// （状态修改 / 凭据检查 / 强制恢复，17 §2 desktopOnly 让位轮）。
import { computed, onBeforeUnmount, ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { fetchMonitorSummary, type CredentialMonitorSummary } from '@/api/nodes'
import {
  clearManualDisabled,
  fetchCredentialDecisions,
  forceRecoverCredential,
  resetCredentialState,
  resetStateOutcomeAmbiguous,
  resetStateProbeIndeterminate,
  setManualDisabled,
  submitCredentialProbe,
  type CredentialRoutingDecision,
} from '@/api/credentialsOps'
import {
  fetchRoutingBlockedDiagnostic,
  routingBlockedBreakdownEmptyKey,
  routingBlockedCredInternalMismatch,
  routingBlockedManualDisabledUnreliable,
  routingBlockedReasonAbsent,
  routingBlockedReasonEmpty,
  routingBlockedStateUnavailable,
  routingBlockedSumMismatch,
  routingBlockedTotalsDisagree,
  routingBlockedTruncated,
  type RoutingBlockedDiagnostic,
} from '@/api/routingBlocked'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppConfirm from '@/components/common/AppConfirm.vue'
import FocusLayer from '@/components/common/FocusLayer.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { relativeTime } from '@/utils/format'

useHyperPage({ title: () => t('nodes.title') })
const auth = useAuthStore()

// 全量缓存 + 前端过滤：monitor-summary 单端点返回全网凭据（量级 ~百），
// 分页在客户端切片；queryRevision 机制照常隔离 requery 前后响应。
let cache: CredentialMonitorSummary[] = []
const query = ref('')

const controller = new ContinuousListController<CredentialMonitorSummary>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: filtered().length }
    if (cache.length === 0) {
      cache = await fetchMonitorSummary()
    }
    const items = filtered()
    return { items, total: items.length }
  },
  stableKey: (c) => `cred-${c.id}`,
  scopeKey: 'nodes',
})

function filtered(): CredentialMonitorSummary[] {
  const q = query.value.trim().toLowerCase()
  if (!q) return cache
  return cache.filter(
    (c) => c.provider_name.toLowerCase().includes(q) || c.label.toLowerCase().includes(q),
  )
}

let debounceTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput(ev: Event): void {
  const value = (ev.target as HTMLInputElement).value
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    query.value = value
    controller.loadFirst('requery')
  }, 250)
}
onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer)
})

// 详情 Sheet + 专注宽表
const detail = ref<CredentialMonitorSummary | null>(null)
const focusActive = ref(false)

// ── 近期路由决策（GET /api/credentials/decisions，admin 档）──────────────
// 回答「这个凭据最近在承载什么流量」。**独立于详情主数据**：拉不到就只显示
// 决策区的错误，不牵连状态字段与操作区 —— 它们各自独立有用。
const decisions = ref<CredentialRoutingDecision[] | null>(null)
const decisionsLoading = ref(false)
const decisionsError = ref<string | null>(null)

async function loadDecisions(credId: number): Promise<void> {
  decisionsLoading.value = true
  decisionsError.value = null
  decisions.value = null
  try {
    decisions.value = await fetchCredentialDecisions(credId, { limit: 20 })
  } catch (err) {
    // 空数组 ≠ 拉取失败：空数组是「最近没有决策」的真实结论，要与错误区分开。
    decisionsError.value = describeError(err)
  } finally {
    decisionsLoading.value = false
  }
}

// ── 供应商级路由阻塞诊断（GET /api/admin/diagnostics/routing-blocked）──────
//
// 存在的理由是后端注释原话：*"credentials look healthy but routing can't find
// them"* —— monitor-summary 说这个凭据状态正常，而这个端点说它的每条
// (credential, model) 绑定到底 is_routable 与为什么不是。**这正是用户要的
// 「凭据检查」与「路由检查」的交汇点。**
//
// ★ 注册处 admin/handler.go:1464 是 `h.superAdmin` ⇒ tenant_admin 点下去必 403，
//   所以入口按角色分档渲染，不靠后端报错兜底。
//
// ★★ **按需加载，不随详情自动拉**：它最坏返回 500 条绑定（后端 maxBindings），
//   而详情是随手点开的；自动拉会让 90% 的打开动作付这个代价。
const routingBlocked = ref<RoutingBlockedDiagnostic | null>(null)
const routingBlockedLoading = ref(false)
const routingBlockedError = ref<string | null>(null)
const routingBlockedOpen = ref(false)

async function loadRoutingBlocked(providerId: number): Promise<void> {
  routingBlockedLoading.value = true
  routingBlockedError.value = null
  routingBlocked.value = null
  try {
    routingBlocked.value = await fetchRoutingBlockedDiagnostic(providerId)
  } catch (err) {
    routingBlockedError.value = describeError(err)
  } finally {
    routingBlockedLoading.value = false
  }
}

/** 从详情进诊断：先记住 provider，再触发加载（避免 await 期间读 ref，见 §11.86）。 */
async function openRoutingBlocked(): Promise<void> {
  const pid = detail.value?.provider_id
  if (pid == null) return
  routingBlockedOpen.value = true
  await loadRoutingBlocked(pid)
}

const detailOpenProxy = computed({
  get: () => detail.value != null,
  set: (v: boolean) => {
    if (!v) {
      detail.value = null
      focusActive.value = false
      decisions.value = null
      decisionsError.value = null
      routingBlocked.value = null
      routingBlockedError.value = null
      routingBlockedOpen.value = false
    }
  },
})

/** 打开详情时顺带拉决策；关闭时由 detailOpenProxy 复位。 */
function openDetail(c: CredentialMonitorSummary): void {
  detail.value = c
  void loadDecisions(c.id)
}

function healthTone(c: CredentialMonitorSummary): 'success' | 'warning' | 'danger' | 'muted' {
  if (c.manual_disabled) return 'muted'
  if (c.availability_state === 'down' || c.health_status === 'down') return 'danger'
  if (c.availability_state === 'degraded' || c.consecutive_failures > 0 || (c.broken_model_count ?? 0) > 0)
    return 'warning'
  return 'success'
}

// 2026-10-06：model_available / model_total 对一部分凭据是**整个键不存在**
// （245 实测 65 条里 7 条缺失，如 canary-cred-A/B，auth_failed 且 14h 未检查）。
// 原先无守卫插值，那几条直接渲染出字面量 "Models undefined/undefined"。
//
// 缺数据时**整段不渲染**，而不是回落成 "0/0" —— 对一个没被测量过的凭据断言
// 「0 个可用 / 共 0 个」是句我们没有依据的话。
// 2026-10-07：桌面端 CredentialMonitorView 的 `?? 0` 已一并改掉，两端口径现在一致；
// 桌面侧显示为「—」（同表「broken 模型」列对未测量值的处理），见
// web/src/components/credential-monitor/CredentialMonitorTable.vue 的 modelsLabel。
//
// 这里返回整段文案而不是让模板插值：Vue 的类型检查器**不会**因为 v-if 上的
// 另一个表达式去收窄 `c`，把收窄放进函数里才能让 vue-tsc 真正看到（实测：
// 写成 v-if="hasModelCounts(c)" + 模板插值，vue-tsc -b 报 TS2322 2 处）。
function modelsLabel(c: CredentialMonitorSummary): string | null {
  if (typeof c.model_available !== 'number' || typeof c.model_total !== 'number') return null
  return t('nodes.modelsAvailable', { available: c.model_available, total: c.model_total })
}

function stateBadge(c: CredentialMonitorSummary): { cls: string; label: string } {
  if (c.manual_disabled) return { cls: 'badge--muted', label: t('nodes.down') }
  const s = c.availability_state || c.health_status
  if (s === 'down' || s === 'failed') return { cls: 'badge--danger', label: t('nodes.down') }
  if (s === 'degraded' || s === 'recovering') return { cls: 'badge--warning', label: t('nodes.degraded') }
  if (s === 'up' || s === 'healthy' || s === 'ok') return { cls: 'badge--success', label: t('nodes.healthy') }
  return { cls: 'badge--muted', label: s || t('common.unknown') }
}

function probeBadge(state: string): { cls: string; label: string } {
  switch (state) {
    case 'healthy_confirmed':
      return { cls: 'badge--success', label: t('nodes.healthy') }
    case 'broken_confirmed':
      return { cls: 'badge--danger', label: t('nodes.broken') }
    case 'recovering':
      return { cls: 'badge--warning', label: t('nodes.recovering') }
    default:
      return { cls: 'badge--muted', label: t('nodes.unknownState') }
  }
}

const detailTitle = computed(() => detail.value ? `${detail.value.provider_name} · ${detail.value.label}` : '')

// ── 运维操作区（17 §2 desktopOnly 让位轮）──────────────────────────────────
//
// **权限分档是硬约束，不是 UI 偏好**：后端中间件实测（注册处 admin/handler.go:880-888
// 注释明写 tenant_admin 对 /api/admin/** 直接 403）：
//   · set/clear-manual-disabled 走 h.admin      → tenant_admin 可用（限本 tenant）
//   · /api/credentials/{id}/test、force-recover 走 h.superAdmin → 仅 super_admin
// ⇒ 一律显示再吃 403 会让 tenant_admin 看到一堆必然失败的按钮，
//   而移动端没有桌面端那种「打开抽屉才发现没权限」的过程。所以按 role 分档渲染。
const isSuperAdmin = computed(() => auth.role === 'super_admin')

type PendingOp = 'disable' | 'enable' | 'probe' | 'recover' | 'resetState' | null
const pendingOp = ref<PendingOp>(null)
const opError = ref<string | null>(null)
const opOk = ref<string | null>(null)
const confirmOpen = ref(false)
const confirmOp = ref<PendingOp>(null)

/**
 * reason 必填：后端 admin/credential_monitor.go:1785-1792 对空串直接 400。
 *
 * ★ `resetState` 也在内 —— 它的 reason 是**审计留痕**（routing_reset.go:69-72
 * `reason is required for audit trail`），不填就 400。
 */
const reasonText = ref('')
const reasonForOp = ref<PendingOp>(null)
const needReason = computed(
  () =>
    reasonForOp.value === 'disable' ||
    reasonForOp.value === 'enable' ||
    reasonForOp.value === 'resetState',
)

/**
 * ★ `trigger_probe` 勾选位（后端 `req.TriggerProbe`）。
 *
 * ★★ 为什么 UI 不能对它的结果打包票：后端回的是
 *   `probe_triggered = req.TriggerProbe && h.probeSubmitter != nil`
 * （routing_reset.go:151）—— 提交器没接线时**静默**降级成 false，
 * 而客户端不知道后端接没接 ⇒ 请求了却拿到 false 时**无法区分**「没生效」
 * 与「压根没请求」。这个二义在下面 `resetProbeNote` 里如实呈现，不吞掉。
 */
const resetTriggerProbe = ref(false)

/** ★ reset-state 的「探测到底有没有被触发」提示；非该操作时为空串。 */
const resetProbeNote = ref('')

function resetOpState(): void {
  opError.value = null
  opOk.value = null
  reasonText.value = ''
  reasonForOp.value = null
  resetTriggerProbe.value = false
  resetProbeNote.value = ''
}

/** 动作 → 后端 reason。留空时给一个带凭据 id 的默认理由，不让用户空手提交。 */
function effectiveReason(op: PendingOp): string {
  const typed = reasonText.value.trim()
  if (typed) return typed
  const id = detail.value?.id
  const who = auth.userInfo?.display_name || auth.userInfo?.username || 'mobile'
  // ★ 用映射而不是三元链：新增 resetState 时三元链会静默落到 reasonDefaultRecover，
  //   而那是个**语义错误**的默认理由（审计会记成「强制恢复」）。默认理由要能对得上动作。
  const defaults: Record<Exclude<PendingOp, null>, string> = {
    disable: t('nodes.reasonDefaultDisable'),
    enable: t('nodes.reasonDefaultEnable'),
    probe: t('nodes.reasonDefaultProbe'),
    recover: t('nodes.reasonDefaultRecover'),
    resetState: t('nodes.reasonDefaultResetState'),
  }
  if (!op) return ''
  const base = defaults[op]
  return id != null ? `${base} (#${id}, ${who})` : base
}

function requestOp(op: Exclude<PendingOp, null>): void {
  resetOpState()
  confirmOp.value = op
  reasonForOp.value = op
  confirmOpen.value = true
}

const confirmMeta = computed(() => {
  switch (confirmOp.value) {
    case 'disable':
      return { title: t('nodes.confirmDisableTitle'), body: t('nodes.confirmDisableBody', { name: detailName.value }), label: t('nodes.disable'), danger: true }
    case 'enable':
      return { title: t('nodes.confirmEnableTitle'), body: t('nodes.confirmEnableBody', { name: detailName.value }), label: t('nodes.enable'), danger: false }
    case 'probe':
      return { title: t('nodes.confirmProbeTitle'), body: t('nodes.confirmProbeBody', { name: detailName.value }), label: t('nodes.probe'), danger: false }
    case 'recover':
      return { title: t('nodes.confirmRecoverTitle'), body: t('nodes.confirmRecoverBody', { name: detailName.value }), label: t('nodes.forceRecover'), danger: true }
    case 'resetState':
      // ★ 后端 routing_reset.go:69-72 要求 reason 是**审计留痕**，不是备注；
      //   且 :110-119 存在「DB 已改但请求以 5xx 结束」这一支 ⇒ 危险档。
      return { title: t('nodes.confirmResetStateTitle'), body: t('nodes.confirmResetStateBody', { name: detailName.value }), label: t('nodes.resetState'), danger: true }
    default:
      return { title: '', body: '', label: t('common.confirm'), danger: false }
  }
})

const detailName = computed(() => (detail.value ? `${detail.value.provider_name} · ${detail.value.label}` : ''))

async function runConfirmedOp(): Promise<void> {
  const op = confirmOp.value
  const cred = detail.value
  if (!op || !cred || pendingOp.value) return
  confirmOpen.value = false
  pendingOp.value = op
  opError.value = null
  opOk.value = null
  try {
    if (op === 'disable') {
      await setManualDisabled(cred.id, true, effectiveReason(op))
      opOk.value = t('nodes.opDisabled')
    } else if (op === 'enable') {
      await clearManualDisabled(cred.id, effectiveReason(op))
      opOk.value = t('nodes.opEnabled')
    } else if (op === 'probe') {
      // ⚠️ 后端 202 异步（credential_state_handlers.go:30-51），返回的是
      // 「已提交」而不是探测结果。文案不能说成「探测通过」。
      await submitCredentialProbe(cred.id)
      opOk.value = t('nodes.opProbeSubmitted')
    } else if (op === 'recover') {
      await forceRecoverCredential(cred.id)
      opOk.value = t('nodes.opRecovered')
    } else if (op === 'resetState') {
      // ★ rawModel 留空 = **整凭据**复位（routing_reset.go:37-39）：
      //   覆盖该凭据的所有绑定模型。移动端不暴露「只复位某个模型」这个口子 ——
      //   单模型复位会让「这个节点还是不通」的原因更难查。
      //
      // ★★★★★★ 必须把 trigger_probe **快照**下来，不能在 await 之后再读 ref。
      //   实测（2026-10-08）：`confirmOpen.value = false` 会让 AppConfirm emit
      //   `update:model-value(false)` → 视图的 `resetOpState()` 把
      //   `resetTriggerProbe` 清成 false；而 Vue 的响应式 flush 发生在
      //   **await 期间** ⇒ 请求体里明明带了 trigger_probe=true，
      //   回调里读到的却是 false ⇒ 「未能确定」那句提示**永远不出现**。
      //   同理 `reasonText` 也在 await 期间被清空 —— 只是 reason 是**作为实参**
      //   在 await 之前求值的，才没出事。
      const wantedProbe = resetTriggerProbe.value
      const r = await resetCredentialState(cred.id, effectiveReason(op), '', wantedProbe)
      opOk.value = t('nodes.opResetStateDone')
      // ★★ probe_triggered 有三义，false 那一支尤其不能吞：
      //   后端写的是 `req.TriggerProbe && probeSubmitter != nil`，客户端无从知道
      //   提交器接没接 ⇒ 请求了却拿到 false = **未能确定**，不是「没触发」。
      if (resetStateProbeIndeterminate(r, wantedProbe)) {
        resetProbeNote.value = t('nodes.resetProbeIndeterminate')
      } else if (r.probe_triggered) {
        // 只代表「已提交」（fire-and-forget），不代表探测通过。
        resetProbeNote.value = t('nodes.resetProbeSubmitted')
      }
    }
    // 写操作后重新拉全量：后端有 15s 缓存（monitor-summary TTL），
    // 立即重查可能拿到旧值 —— 但仍要重查，因为缓存过期后自然刷新。
    cache = await fetchMonitorSummary()
    controller.loadFirst('requery')
    // 就地更新 detail 引用，避免 Sheet 里继续显示旧状态
    if (detail.value) {
      const fresh = cache.find((c) => c.id === detail.value?.id)
      if (fresh) detail.value = fresh
    }
  } catch (err) {
    // ★★★★★★ reset-state 失败**不能**一律说「操作失败」：
    //   routing_reset.go:110-119 存在「DB 效果已落地但请求以 5xx 结束」这一支，
    //   而那一支走 writeInternalErr ⇒ 响应里**没有** db_committed/audit_outcome，
    //   客户端拿到的报文与「完全没动」逐字节相同 ⇒ 只能对二义档说「去核实」。
    //   4xx 则都发生在 applyForceEnable 之前（:56-89），可以确定地报「未执行」。
    if (op === 'resetState' && resetStateOutcomeAmbiguous((err as { status?: number })?.status)) {
      opError.value = t('nodes.resetStateMaybeApplied')
    } else {
      opError.value = describeError(err)
    }
  } finally {
    pendingOp.value = null
    reasonForOp.value = null
    reasonText.value = ''
    resetTriggerProbe.value = false
  }
}

/** 403 单独说人话：这是权限档位问题，不是网络或服务端故障。 */
function describeError(err: unknown): string {
  const status = (err as { status?: number })?.status
  if (status === 403) return t('nodes.errForbidden')
  if (status === 401) return t('nodes.errUnauthorized')
  const msg = err instanceof Error ? err.message : String(err)
  return msg || t('common.error')
}
</script>

<template>
  <div class="view-root">
  <HyperList
    :controller="controller"
    :item-key="(c: CredentialMonitorSummary) => `cred-${c.id}`"
    :on-refresh="async () => { cache = await fetchMonitorSummary() }"
    :empty-hint="t('common.empty')"
  >
    <template #header>
      <div class="nodes__search">
        <AppIcon name="search" :size="18" />
        <input
          type="search"
          :placeholder="t('nodes.searchPlaceholder')"
          :aria-label="t('common.search')"
          @input="onSearchInput"
        />
      </div>
    </template>

    <template #item="{ item: c }">
      <button type="button" class="data-card node-card" @click="openDetail(c)">
        <div class="card-row">
          <span class="node-card__name">
            <StatusDot :tone="healthTone(c)" />
            {{ c.provider_name }} · {{ c.label }}
          </span>
          <span class="badge" :class="stateBadge(c).cls">{{ stateBadge(c).label }}</span>
        </div>
        <div class="node-card__fields">
          <span v-if="modelsLabel(c)" class="node-card__field">{{ modelsLabel(c) }}</span>
          <span class="node-card__field">{{ t('nodes.concurrency') }} {{ c.effective_concurrency }}</span>
          <span class="node-card__field">{{ t('nodes.lastChecked') }} {{ relativeTime(c.health_checked_at) }}</span>
        </div>
        <div v-if="c.consecutive_failures > 0" class="node-card__warn">
          {{ t('nodes.consecutiveFailures', { n: c.consecutive_failures }) }}
        </div>
      </button>
    </template>
  </HyperList>

  <!-- 节点详情：字段 + 每模型宽表（专注入口） -->
  <AppSheet v-model="detailOpenProxy" presentation="sheet" :title="detailTitle">
    <template v-if="detail">
      <div class="data-card">
        <div class="card-field">
          <span>{{ t('nodes.effectiveState') }}</span>
          <span class="card-field__value">{{ detail.effective_state || detail.status }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('nodes.availability') }}</span>
          <span class="card-field__value">{{ detail.availability_state }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('nodes.quota') }}</span>
          <span class="card-field__value">{{ detail.quota_state }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('nodes.concurrency') }}</span>
          <span class="card-field__value num">{{ detail.effective_concurrency }} / {{ detail.concurrency_limit ?? '—' }}</span>
        </div>
        <div class="card-field">
          <span>{{ t('keys.requests') }}</span>
          <span class="card-field__value num">{{ detail.total_requests }}</span>
        </div>
        <div v-if="detail.effective_reason || detail.state_reason_code" class="card-field">
          <span>{{ t('nodes.reason') }}</span>
          <span class="card-field__value">{{ detail.effective_reason || detail.state_reason_code }}</span>
        </div>
      </div>

      <!-- 运维操作区（17 §2 desktopOnly 让位轮）。
           权限分档渲染：状态修改 admin 档人人可用，探测/强恢仅 super_admin
           （后端 h.superAdmin 对 tenant_admin 直接 403，见脚本注释）。 -->
      <div class="nodes__ops">
        <h3 class="page__section-title" style="margin-inline: 0">{{ t('nodes.operations') }}</h3>

        <p v-if="opOk" class="nodes__op-msg nodes__op-msg--ok" role="status">{{ opOk }}</p>
        <!-- ★ 只在 reset-state 且探测状态有话可说时出现；成功文案下方，
             不替换成功文案本身 —— 两件事都要让用户看到。 -->
        <p v-if="resetProbeNote" class="nodes__op-msg nodes__op-msg--warn" role="status">{{ resetProbeNote }}</p>
        <p v-if="opError" class="nodes__op-msg nodes__op-msg--err" role="alert">{{ opError }}</p>

        <div class="nodes__ops-row">
          <button
            v-if="!detail.manual_disabled"
            type="button"
            class="btn btn--danger"
            :disabled="pendingOp !== null"
            @click="requestOp('disable')"
          >
            <AppIcon name="pause" :size="16" />
            {{ t('nodes.disable') }}
          </button>
          <button
            v-else
            type="button"
            class="btn btn--primary"
            :disabled="pendingOp !== null"
            @click="requestOp('enable')"
          >
            <AppIcon name="play" :size="16" />
            {{ t('nodes.enable') }}
          </button>

          <button
            v-if="isSuperAdmin"
            type="button"
            class="btn"
            :disabled="pendingOp !== null"
            @click="requestOp('probe')"
          >
            <AppIcon name="refresh" :size="16" />
            {{ t('nodes.probe') }}
          </button>

          <button
            v-if="isSuperAdmin"
            type="button"
            class="btn btn--danger"
            :disabled="pendingOp !== null"
            @click="requestOp('recover')"
          >
            {{ t('nodes.forceRecover') }}
          </button>

          <!-- ★ reset-state 与 force-recover 的分工：那个是「凭据级 5 步全清」，
               这个是「带审计 reason 的聚焦复位」，可顺带请求一次探测。
               注册处 admin/handler.go:946 是 h.superAdmin ⇒ 与探测/强恢同档。 -->
          <button
            v-if="isSuperAdmin"
            type="button"
            class="btn btn--danger"
            :disabled="pendingOp !== null"
            @click="requestOp('resetState')"
          >
            {{ t('nodes.resetState') }}
          </button>
        </div>

        <p v-if="!isSuperAdmin" class="nodes__ops-hint">{{ t('nodes.opsAdminOnlyHint') }}</p>
        <p v-if="detail.manual_disabled" class="nodes__ops-warn">{{ t('nodes.manualDisabledWarn') }}</p>
      </div>

      <!-- 近期路由决策：回答「这个凭据最近在承载什么流量」。与状态字段/操作区
           彼此独立 —— 本区拉取失败不影响其余部分可用。 -->
      <div class="nodes__decisions">
        <h3 class="page__section-title" style="margin-inline: 0">{{ t('nodes.recentDecisions') }}</h3>
        <p v-if="decisionsLoading" class="nodes__decisions-state">{{ t('common.loading') }}</p>
        <p v-else-if="decisionsError" class="nodes__decisions-state nodes__decisions-state--err">
          {{ decisionsError }}
        </p>
        <p v-else-if="decisions && decisions.length === 0" class="nodes__decisions-state">
          {{ t('nodes.noDecisions') }}
        </p>
        <ul v-else-if="decisions" class="nodes__decisions-list">
          <li v-for="d in decisions" :key="d.request_id" class="nodes__decision">
            <span class="nodes__decision-dot" :class="d.success ? 'ok' : 'bad'" aria-hidden="true" />
            <span class="nodes__decision-model">{{ d.model }}</span>
            <span class="nodes__decision-meta">
              {{ relativeTime(d.ts) }}
              <template v-if="d.latency_ms != null"> · {{ d.latency_ms }}ms</template>
              <template v-if="d.sticky_hit"> · {{ t('nodes.stickyHit') }}</template>
            </span>
            <span v-if="!d.success && d.error_class" class="nodes__decision-err">{{ d.error_class }}</span>
          </li>
        </ul>
      </div>

      <!-- 供应商级路由阻塞诊断。按需加载（最坏 500 条绑定），失败不牵连上面几区。
           ★ 这一区的每个「计数」都必须先过语义判据再显示 —— 后端的钳位
           会让 total / routable / blocked 三个数互相矛盾（见 routingBlocked.ts）。 -->
      <div class="nodes__rblocked">
        <div class="nodes__per-model-head">
          <h3 class="page__section-title" style="margin-inline: 0">{{ t('nodes.routingBlocked') }}</h3>
          <button
            v-if="isSuperAdmin && !routingBlockedOpen"
            type="button"
            class="btn btn--sm"
            :disabled="routingBlockedLoading"
            @click="openRoutingBlocked"
          >
            {{ t('nodes.routingBlockedOpen') }}
          </button>
        </div>

        <p v-if="routingBlockedLoading" class="nodes__decisions-state">{{ t('common.loading') }}</p>
        <p v-else-if="routingBlockedError" class="nodes__decisions-state nodes__decisions-state--err">
          {{ routingBlockedError }}
        </p>

        <template v-else-if="routingBlocked">
          <!-- ★ 截断：后端只把 total 钳到 500，routable 没钳 ⇒ 三个数可能互相矛盾。
               这里如实说明「以下数字不可直接相加」，而不是把矛盾的数字原样显示。 -->
          <p v-if="routingBlockedTruncated(routingBlocked)" class="nodes__decisions-state nodes__decisions-state--err">
            {{ t('nodes.routingTruncated') }}
          </p>
          <p v-if="routingBlockedTotalsDisagree(routingBlocked)" class="nodes__decisions-state nodes__decisions-state--err">
            {{ t('nodes.routingCountsInconsistent') }}
          </p>
          <p v-if="routingBlockedSumMismatch(routingBlocked)" class="nodes__decisions-state nodes__decisions-state--err">
            {{ t('nodes.routingSumMismatch') }}
          </p>
          <!-- ★★ 状态整段缺失时 manual_disabled=false 是**错值**，不能当结论显示 -->
          <p v-if="routingBlockedStateUnavailable(routingBlocked)" class="nodes__decisions-state nodes__decisions-state--err">
            {{ t('nodes.routingStateUnavailable') }}
          </p>
          <p v-if="routingBlockedBreakdownEmptyKey(routingBlocked)" class="nodes__decisions-state nodes__decisions-state--err">
            {{ t('nodes.routingEmptyReasonKey') }}
          </p>

          <p v-if="routingBlocked.credentials.length === 0" class="nodes__decisions-state">
            {{ t('nodes.routingNoBindings') }}
          </p>

          <ul v-else class="nodes__rblocked-list">
            <li v-for="c in routingBlocked.credentials" :key="c.credential_id" class="nodes__rblocked-cred">
              <div class="nodes__rblocked-head">
                <span class="nodes__rblocked-label">{{ c.credential_label }}</span>
                <span v-if="routingBlockedManualDisabledUnreliable(c)" class="nodes__rblocked-unknown">
                  {{ t('nodes.routingStateUnknownShort') }}
                </span>
                <span v-else-if="c.manual_disabled" class="nodes__rblocked-disabled">
                  {{ t('nodes.routingManuallyDisabled') }}
                </span>
              </div>
              <!-- ★ 状态缺失时不要把空串渲染成「状态：」这种看不出异常的形态 -->
              <p v-if="routingBlockedManualDisabledUnreliable(c)" class="nodes__rblocked-meta">
                {{ t('nodes.routingStateUnknownShort') }}
              </p>
              <p v-else class="nodes__rblocked-meta">
                {{ c.status || '—' }} · {{ c.availability_state || '—' }} · {{ c.health_status || '—' }}
              </p>
              <p v-if="routingBlockedCredInternalMismatch(c)" class="nodes__rblocked-meta nodes__decisions-state--err">
                {{ t('nodes.routingCredCountsBad') }}
              </p>
              <ul class="nodes__rblocked-bindings">
                <li v-for="b in c.bindings" :key="`${c.credential_id}-${b.raw_model_name}`" class="nodes__rblocked-binding">
                  <span class="nodes__decision-dot" :class="b.is_routable ? 'ok' : 'bad'" aria-hidden="true" />
                  <span class="nodes__rblocked-model">{{ b.raw_model_name }}</span>
                  <!-- ★★ 两种「拿不到原因」必须画得不一样：键不存在（NULL，后端会
                       归成 unknown）vs 空串（后端确实存了空串）。 -->
                  <span v-if="b.is_routable" class="nodes__rblocked-meta">{{ t('nodes.routingRoutable') }}</span>
                  <span v-else-if="routingBlockedReasonAbsent(b)" class="nodes__rblocked-reason">{{ t('nodes.routingReasonNull') }}</span>
                  <span v-else-if="routingBlockedReasonEmpty(b)" class="nodes__rblocked-reason">{{ t('nodes.routingReasonEmpty') }}</span>
                  <span v-else class="nodes__rblocked-reason">{{ b.unavailable_reason }}</span>
                </li>
              </ul>
            </li>
          </ul>
        </template>
      </div>

      <div class="nodes__per-model-head">
        <h3 class="page__section-title" style="margin-inline: 0">{{ t('nodes.perModel') }}</h3>
        <button type="button" class="btn btn--sm" @click="focusActive = true">
          <AppIcon name="expand" :size="16" />
          {{ t('common.focusView') }}
        </button>
      </div>

      <!-- 专注层：单实例 Teleport，inactive 原位渲染 -->
      <FocusLayer v-model:active="focusActive" :title="detailTitle">
        <div class="table-scroll">
          <table class="table">
            <thead>
              <tr>
                <th>{{ t('usage.model') }}</th>
                <th>{{ t('nodes.probeState') }}</th>
                <th class="num">{{ t('nodes.latency') }}</th>
                <th class="num">{{ t('nodes.successRate') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in detail.models ?? []" :key="m.raw_model_name">
                <td>{{ m.canonical_name || m.raw_model_name }}</td>
                <td>
                  <span class="badge" :class="probeBadge(m.probe_state).cls">{{ probeBadge(m.probe_state).label }}</span>
                </td>
                <td class="num">{{ m.p95_latency_ms != null ? Math.round(m.p95_latency_ms) + 'ms' : '—' }}</td>
                <td class="num">
                  {{ m.recent_success_rate != null ? (m.recent_success_rate * 100).toFixed(1) + '%' : '—' }}
                </td>
              </tr>
              <tr v-if="(detail.models ?? []).length === 0">
                <td colspan="4">{{ t('common.empty') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </FocusLayer>
    </template>
  </AppSheet>

  <!-- 写操作二次确认。后端 force-recover 只有 query id、无 X-Confirm 门禁
       （对比 PATCH /api/admin/providers/{id}/enable 有），所以这层是唯一防线。 -->
  <AppConfirm
    :model-value="confirmOpen"
    :title="confirmMeta.title"
    :body="confirmMeta.body"
    :confirm-label="confirmMeta.label"
    :danger="confirmMeta.danger"
    @confirm="runConfirmedOp"
    @update:model-value="(v: boolean) => { if (!v) resetOpState() }"
  >
    <!-- reason：set/clear-manual-disabled 后端强制必填（credential_monitor.go:1785），
         空串直接 400。探测/强恢不需要 reason，不渲染输入框。 -->
    <label v-if="needReason" class="nodes__reason">
      <span class="nodes__reason-label">{{ t('nodes.reasonLabel') }}</span>
      <textarea
        v-model="reasonText"
        class="nodes__reason-input"
        rows="2"
        :placeholder="t('nodes.reasonPlaceholder')"
      />
      <span class="nodes__reason-hint">{{ t('nodes.reasonHint') }}</span>
    </label>

    <!-- ★ trigger_probe：后端 req.TriggerProbe。勾了只是「请求一次探测」，
         不是保证触发 —— 提交器没接线时后端静默降级成 false（routing_reset.go:125）。
         所以这里**不写**任何承诺性文案，只描述请求本身。 -->
    <label v-if="confirmOp === 'resetState'" class="nodes__probe-toggle">
      <input v-model="resetTriggerProbe" type="checkbox" class="nodes__probe-checkbox" />
      <span class="nodes__probe-label">{{ t('nodes.resetTriggerProbe') }}</span>
    </label>
    <p v-if="confirmOp === 'resetState'" class="nodes__reason-hint">{{ t('nodes.resetTriggerProbeHint') }}</p>
  </AppConfirm>
  </div>
</template>

<style scoped>
.nodes__search {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-muted);
  margin-bottom: var(--app-space-3);
}

.nodes__search input {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--app-text);
  font-size: var(--app-font-input);
  min-width: 0;
  /* 2026-10-04：与 ModelsView 同一处缺陷。容器 48px 但 input 自身只有 20px 高，
     而容器是 div 不是 label —— 点容器的 padding 不会聚焦，真实热区就是那 20px。 */
  height: 100%;
  align-self: stretch;
}

.nodes__search input:focus {
  outline: none;
}

.node-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  width: 100%;
  text-align: left;
  border: 1px solid var(--app-border-subtle);
  cursor: pointer;
  font: inherit;
  color: inherit;
}

.node-card:active {
  background: var(--app-primary-softer);
}

.node-card__name {
  display: inline-flex;
  align-items: center;
  gap: var(--app-space-2);
  font-weight: 600;
  font-size: 0.9375rem;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.node-card__fields {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
}

.node-card__field {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.node-card__warn {
  font-size: 0.75rem;
  color: var(--app-warning);
}

.nodes__per-model-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: var(--app-space-4) 0 var(--app-space-2);
}

/* ── 运维操作区（17 §2 desktopOnly 让位轮）── */
.nodes__ops {
  margin-top: var(--app-space-4);
  padding-top: var(--app-space-3);
  border-top: 1px solid var(--app-border-subtle);
}

.nodes__ops-row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  margin-top: var(--app-space-2);
}

.nodes__ops-row .btn {
  /* 触控热区下限 48px（06 §7 R1：新控件一律 ≥48） */
  min-height: 48px;
  flex: 1 1 auto;
}

.nodes__op-msg {
  margin-top: var(--app-space-2);
  font-size: 0.8125rem;
  padding: var(--app-space-2) var(--app-space-3);
  border-radius: var(--app-radius);
}

.nodes__op-msg--ok {
  color: var(--app-success);
  background: color-mix(in srgb, var(--app-success) 12%, transparent);
}

.nodes__op-msg--err {
  color: var(--app-danger);
  background: color-mix(in srgb, var(--app-danger) 12%, transparent);
}

/* ★ 「探测是否被触发」是二义结论，不是成功也不是失败 ⇒ 单独一档，
   复用 --app-warning，不复用 ok/err（那两档都会给出错误的安全感）。 */
.nodes__op-msg--warn {
  color: var(--app-warning);
  background: color-mix(in srgb, var(--app-warning) 12%, transparent);
}

/* R1：新增触控控件 ≥48 CSS px。整行 label 是命中区，勾选框本身放大到 24px
   视觉尺寸但由 label 承担点击面 —— 移动端直接点 16px 勾选框会点不中。 */
.nodes__probe-toggle {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  min-height: 48px;
  margin-top: var(--app-space-2);
  cursor: pointer;
}

.nodes__probe-checkbox {
  width: 24px;
  height: 24px;
  accent-color: var(--app-primary);
  flex: none;
}

.nodes__probe-label {
  font-size: 0.8125rem;
  color: var(--app-text-primary);
}

/* ── 路由阻塞诊断区 ────────────────────────────────────────────────── */
.nodes__rblocked {
  margin-top: var(--app-space-3);
}

.nodes__rblocked-list,
.nodes__rblocked-bindings {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}

.nodes__rblocked-cred {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}

.nodes__rblocked-head {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
}

.nodes__rblocked-label {
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text-primary);
}

.nodes__rblocked-meta,
.nodes__rblocked-reason,
.nodes__rblocked-unknown,
.nodes__rblocked-disabled {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.nodes__rblocked-unknown {
  color: var(--app-warning);
}

.nodes__rblocked-disabled {
  color: var(--app-danger);
}

.nodes__rblocked-binding {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  padding: 2px 0;
}

.nodes__rblocked-model {
  font-size: 0.75rem;
  color: var(--app-text-primary);
}

.nodes__ops-hint,
.nodes__ops-warn {
  margin-top: var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.nodes__ops-warn {
  color: var(--app-warning);
}

/* ── 近期路由决策 ── */
.nodes__decisions {
  margin-top: var(--app-space-4);
  padding-top: var(--app-space-3);
  border-top: 1px solid var(--app-border-subtle);
}

.nodes__decisions-state {
  margin-top: var(--app-space-2);
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.nodes__decisions-state--err {
  color: var(--app-danger);
}

.nodes__decisions-list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.nodes__decision {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: var(--app-space-1) var(--app-space-2);
  font-size: 0.8125rem;
}

.nodes__decision-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: 0 0 auto;
}

.nodes__decision-dot.ok {
  background: var(--app-success);
}

.nodes__decision-dot.bad {
  background: var(--app-danger);
}

.nodes__decision-model {
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 55%;
}

.nodes__decision-meta {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.nodes__decision-err {
  color: var(--app-danger);
  font-size: 0.75rem;
}

.nodes__reason {
  display: block;
  margin-bottom: var(--app-space-3);
}

.nodes__reason-label {
  display: block;
  font-size: 0.75rem;
  color: var(--app-text-secondary);
  margin-bottom: var(--app-space-1);
}

.nodes__reason-input {
  width: 100%;
  min-height: 48px;
  padding: var(--app-space-2) var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text);
  /* 16px：iOS Safari 聚焦时字号 <16px 会触发自动放大（UI规范 17 §2 登录页同款） */
  font: inherit;
  font-size: 16px;
  resize: vertical;
}

.nodes__reason-hint {
  display: block;
  margin-top: var(--app-space-1);
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}
</style>
