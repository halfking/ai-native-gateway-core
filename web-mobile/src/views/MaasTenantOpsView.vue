<script setup lang="ts">
// MaasTenantOpsView — MaaS **租户运维面**（**superAdmin 档**）。
//
// GET /api/admin/maas/tenants/{code}/wallet
// GET /api/admin/maas/tenants/{code}/account
// GET /api/admin/maas/tenants/{code}/usage/summary?days=&limit=
// GET /api/admin/maas/tenants/{code}/usage/detail?owner_user=&days=
// GET /api/admin/maas/tenants/{code}/ledger?limit=
//
// ⚠️⚠️⚠️ 全部 `h.superAdmin(...)`（maas_handlers.go:14-24）⇒ 抽屉席**必须**设
// requiresRole: 'super_admin'。**别**照抄第三段那 5 条 admin 席的写法 ——
// 那是另一个前缀（/api/maas/**），档位正好相反。
//
// 六个最容易渲染错的语义（详见 api/maas.ts 坑 25~36）：
//
// (1) ★★★★★★ 两个 usage 端点**按 days 换物理表**：
//     days ≤ 7 ⇒ `request_logs_hot`；> 7 ⇒ `request_logs_with_current_month`。
//     ⇒ days=7 与 days=8 读的**不是同一张表**。页面必须显式标出本次读的哪张。
// (2) ★★★★★ 收入与毛利是**算出来的**（`credits × cents_per_credit / 100`），
//     而 `cents_per_credit` 在响应里**回显** ⇒ 本页复算并标出对不上的行。
// (3) ★★★★ `gross_margin_rate` 在**零收入**时保持 0（**无定义**）
//     ⇒ 「rate=0」有两种含义，页面不许直接渲染成「零毛利」。
// (4) ★★★★ `cost_usd` 是 float64 + omitempty ⇒ **恰好 0 时键不存在**
//     ⇒ 必须显示 0，而不是「—」（否则「零成本」被误报成「数据缺失」）。
// (5) ★★★ `cancelled_billed_requests` = 已计费 + 流中断 + 失败码 ∈
//     {client_cancel, client_disconnected} ⇒ 收入侧已知漏点，非零要说破。
// (6) ★★★ 三套限幅各不相同，且 usage 的**两端不对称**
//     （limit <1 ⇒ **回落 10**，>50 ⇒ clamp 50；ledger 越界 ⇒ **回落 50**）。
//
// ★ 本页只读：`tenants/{code}/adjust|grant` 是写操作，不碰。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum, fmtTime, relativeTime } from '@/utils/format'
import {
  fetchMaasTenantAccount,
  fetchMaasUsageSummary,
  fetchMaasConsumptionDetail,
  fetchMaasTenantLedger,
  maasUsageDaysClamped,
  maasUsageLimitEffective,
  maasLedgerLimitEffective,
  maasUsageReadsHotTable,
  maasCostUsd,
  maasTenantRevenueUsd,
  maasMarginRateUndefined,
  maasHasCancelledBilled,
  MAAS_USAGE_DAYS_DEFAULT,
  MAAS_USAGE_HOT_DAYS_MAX,
  MAAS_USAGE_LIMIT_MAX,
  MAAS_LEDGER_LIMIT_MAX,
  MAAS_ACCOUNT_LEDGER_COUNT,
  MAAS_ACCOUNT_ORDERS_COUNT,
  MAAS_USAGE_LIMIT_DEFAULT,
  MAAS_LEDGER_LIMIT_DEFAULT,
  type MaasTenantAccount,
  type MaasUsageSummary,
  type MaasConsumptionDetail,
  type MaasLedgerResponse,
  type MaasConsumptionRow,
} from '@/api/maas'

useHyperPage({ title: () => t('mt.title') })

const tenantCode = ref('')
const ownerUser = ref('')
const days = ref<number>(MAAS_USAGE_DAYS_DEFAULT)
const limit = ref<number>(MAAS_USAGE_LIMIT_MAX)
const ledgerLimit = ref<number>(MAAS_LEDGER_LIMIT_DEFAULT)

const account = ref<MaasTenantAccount | null>(null)
const summary = ref<MaasUsageSummary | null>(null)
const detail = ref<MaasConsumptionDetail | null>(null)
const ledger = ref<MaasLedgerResponse | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'tenantrequired' | 'other'>('none')
const loading = ref(false)

const tenantTrimmed = computed(() => tenantCode.value.trim())

/** ★★ 本页能选的天数**只给 1 / 7 / 30 / 90 —— 7 是**换表边界**，必须能点到。 */
const dayChoices = [1, MAAS_USAGE_HOT_DAYS_MAX, 30, 90] as const

/** ★★ 这是本批最要紧的一条：本次读的是哪张表。 */
const readsHotTable = computed(() => maasUsageReadsHotTable(days.value))
const effDays = computed(() => maasUsageDaysClamped(days.value))
const effLimit = computed(() => maasUsageLimitEffective(limit.value))
const effLedgerLimit = computed(() => maasLedgerLimitEffective(ledgerLimit.value))

async function load(): Promise<void> {
  const code = tenantTrimmed.value
  if (!code) {
    error.value = t('mt.needTenant')
    errorKind.value = 'tenantrequired'
    account.value = null
    summary.value = null
    detail.value = null
    ledger.value = null
    return
  }
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    const [a, s, d, l] = await Promise.all([
      fetchMaasTenantAccount(code),
      fetchMaasUsageSummary(code, { days: days.value, limit: limit.value }),
      fetchMaasConsumptionDetail(code, { days: days.value, ownerUser: ownerUser.value.trim() || undefined }),
      fetchMaasTenantLedger(code, { limit: ledgerLimit.value }),
    ])
    account.value = a
    summary.value = s
    detail.value = d
    ledger.value = l
  } catch (e) {
    account.value = null
    summary.value = null
    detail.value = null
    ledger.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    if (/database not configured/i.test(msg)) errorKind.value = 'unconfigured'
    // ★ service 对空 tenant_id 返 "tenant_id required" ⇒ handler 走 500
    else if (/tenant_id required/i.test(msg)) errorKind.value = 'tenantrequired'
    else errorKind.value = 'other'
  } finally {
    loading.value = false
  }
}

/** ★★★ 复算服务端给的 revenue；对不上就标出来（不信服务端算的数）。 */
function revenueMismatch(r: MaasConsumptionRow): boolean {
  if (!detail.value) return false
  const expect = maasTenantRevenueUsd(r.credits_charged, detail.value.cents_per_credit)
  return Math.abs(expect - r.tenant_revenue_usd) > 1e-6
}

const marginUndefinedCount = computed(
  () => (detail.value?.rows ?? []).filter(maasMarginRateUndefined).length,
)
const cancelledBilledTotal = computed(
  () => (detail.value?.rows ?? []).reduce((s, r) => s + r.cancelled_billed_requests, 0),
)

function subTone(): 'success' | 'muted' | 'warning' {
  const st = account.value?.wallet.subscription?.status
  if (st === 'active') return 'success'
  if (st === undefined) return 'muted'
  return 'warning'
}

function loadBtn(): void {
  void load()
}

onBeforeUnmount(() => {
  account.value = null
  summary.value = null
  detail.value = null
  ledger.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="mt">
    <!-- ══════ 参数区 ══════ -->
    <section class="mt__panel">
      <label class="mt__label" for="mt-code">{{ t('mt.tenantCode') }}</label>
      <input
        id="mt-code"
        v-model="tenantCode"
        class="mt__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('mt.tenantPlaceholder')"
      />

      <label class="mt__label" for="mt-owner">{{ t('mt.ownerUser') }}</label>
      <input
        id="mt-owner"
        v-model="ownerUser"
        class="mt__input"
        type="text"
        autocapitalize="off"
        autocorrect="off"
        spellcheck="false"
        :placeholder="t('mt.ownerPlaceholder')"
      />

      <!-- 天数：★ 7 是**换表边界**，必须能点到 -->
      <label class="mt__label">{{ t('mt.days') }}</label>
      <div class="mt__seg" role="group" :aria-label="t('mt.days')">
        <button
          v-for="n in dayChoices"
          :key="n"
          type="button"
          class="mt__seg-btn"
          :class="{ 'mt__seg-btn--on': days === n, 'mt__seg-btn--hot': n === MAAS_USAGE_HOT_DAYS_MAX }"
          :aria-pressed="days === n"
          @click="days = n"
        >
          {{ n }}
        </button>
      </div>

      <label class="mt__label" for="mt-limit">{{ t('mt.modelLimit') }}</label>
      <div id="mt-limit" class="mt__seg" role="group" :aria-label="t('mt.modelLimit')">
        <button
          v-for="n in [MAAS_USAGE_LIMIT_DEFAULT, MAAS_USAGE_LIMIT_MAX]"
          :key="n"
          type="button"
          class="mt__seg-btn"
          :class="{ 'mt__seg-btn--on': limit === n }"
          :aria-pressed="limit === n"
          @click="limit = n"
        >
          {{ n }}
        </button>
      </div>

      <button type="button" class="mt__btn mt__btn--primary" @click="loadBtn">{{ t('mt.load') }}</button>

      <!-- ★★★★ 本次读的是哪张表 —— 这条端点按 days 换源 -->
      <p class="mt__note" :class="readsHotTable ? 'mt__note' : 'mt__note--warn'">
        <AppIcon :name="readsHotTable ? 'server' : 'alert'" :size="13" />
        <span>{{ readsHotTable ? t('mt.hotTable', { n: effDays }) : t('mt.monthTable', { n: effDays }) }}</span>
      </p>
      <!-- ★★ days > 7 时必须点破「换了数据源」而不只是「窗口更长」 -->
      <p v-if="!readsHotTable" class="mt__note mt__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mt.tableSwitchNote') }}</span>
      </p>
      <p class="mt__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mt.effectiveNote', { d: effDays, l: effLimit, g: effLedgerLimit }) }}</span>
      </p>
    </section>

    <!-- ══════ 错误 ══════ -->
    <section v-if="error" class="mt__panel">
      <p class="mt__msg mt__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mt__note mt__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
      <p v-else-if="errorKind === 'tenantrequired'" class="mt__note mt__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('mt.tenantRequiredNote') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mt__msg">{{ t('common.loading') }}</p>

    <template v-if="account && summary && detail">
      <!-- ══════ 钱包 ══════ -->
      <section class="mt__panel">
        <span class="mt__panel-title">{{ t('mt.wallet') }}</span>
        <div class="mt__grid">
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.totalAvailable') }}</span>
            <span class="mt__cell-v">{{ fmtInt(account.wallet.total_available) }}</span>
          </span>
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.quota') }}</span>
            <span class="mt__cell-v">{{ fmtInt(account.wallet.quota_remaining) }}</span>
          </span>
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.granted') }}</span>
            <span class="mt__cell-v">{{ fmtInt(account.wallet.granted_balance) }}</span>
          </span>
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.purchased') }}</span>
            <span class="mt__cell-v">{{ fmtInt(account.wallet.purchased_balance) }}</span>
          </span>
        </div>

        <!-- ★★★ 「键缺失」与「status 不是 active」是两回事 -->
        <p v-if="account.wallet.subscription === undefined" class="mt__msg">{{ t('mt.noSubscription') }}</p>
        <p v-else class="mt__note">
          <StatusDot :tone="subTone()" />
          <span>
            {{ t('mt.subscription') }}: {{ account.wallet.subscription.plan_name || '—' }}
            ({{ account.wallet.subscription.status }})
            <template v-if="account.wallet.subscription.period_end">
              · {{ t('mt.periodEnd') }}: {{ relativeTime(account.wallet.subscription.period_end) }}
            </template>
          </span>
        </p>
      </section>

      <!-- ══════ 用量汇总 ══════ -->
      <section class="mt__panel">
        <span class="mt__panel-title">{{ t('mt.summary') }}</span>
        <div class="mt__grid">
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.requests') }}</span>
            <span class="mt__cell-v">{{ fmtInt(summary.total_requests) }}</span>
          </span>
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.credits') }}</span>
            <span class="mt__cell-v">{{ fmtInt(summary.total_credits) }}</span>
          </span>
          <span class="mt__cell">
            <!-- ★★ cost 键缺失 = 恰好 0 ⇒ 必须显示 0，不是「—」 -->
            <span class="mt__cell-l">{{ t('mt.cost') }}</span>
            <span class="mt__cell-v">${{ fmtNum(maasCostUsd({ cost_usd: summary.total_cost_usd }), 4) }}</span>
          </span>
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.echoedDays') }}</span>
            <span class="mt__cell-v">{{ summary.days }}</span>
          </span>
        </div>
        <p class="mt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mt.echoNote') }}</span>
        </p>

        <table class="mt__table">
          <thead>
            <tr>
              <th>{{ t('mt.model') }}</th>
              <th>{{ t('mt.requests') }}</th>
              <th>{{ t('mt.credits') }}</th>
              <th>{{ t('mt.cost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in summary.by_model" :key="m.model">
              <td class="mt__td-l">{{ m.model }}</td>
              <td class="mt__td-v">{{ fmtInt(m.requests) }}</td>
              <td class="mt__td-v">{{ fmtInt(m.credits) }}</td>
              <!-- ★★ 零成本显示 0 -->
              <td class="mt__td-v">${{ fmtNum(maasCostUsd(m), 4) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="!summary.by_model.length" class="mt__msg">{{ t('mt.emptyModels') }}</p>
      </section>

      <!-- ══════ 消费明细 ══════ -->
      <section class="mt__panel">
        <span class="mt__panel-title">{{ t('mt.detail') }}</span>

        <div class="mt__grid">
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.centsPerCredit') }}</span>
            <span class="mt__cell-v">{{ fmtNum(detail.cents_per_credit, 4) }}</span>
          </span>
          <span class="mt__cell">
            <span class="mt__cell-l">{{ t('mt.echoedDays') }}</span>
            <span class="mt__cell-v">{{ detail.days }}</span>
          </span>
        </div>
        <p class="mt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mt.cpcNote') }}</span>
        </p>

        <!-- ★★ 零收入 ⇒ rate 无定义，不是零毛利 -->
        <p v-if="marginUndefinedCount > 0" class="mt__note mt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mt.marginUndefined', { n: marginUndefinedCount }) }}</span>
        </p>

        <!-- ★★ 已计费但被客户端取消 -->
        <p v-if="cancelledBilledTotal > 0" class="mt__note mt__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mt.cancelledBilled', { n: cancelledBilledTotal }) }}</span>
        </p>

        <p v-if="!detail.rows.length" class="mt__msg">{{ t('mt.emptyDetail') }}</p>
        <ul v-else class="mt__list">
          <li v-for="(r, i) in detail.rows" :key="`${r.model}-${r.owner_user ?? ''}-${i}`" class="mt__item">
            <div class="mt__item-head">
              <span class="mt__title">{{ r.model }}</span>
              <span class="mt__badge">
                <StatusDot :tone="maasHasCancelledBilled(r) ? 'warning' : 'success'" />
                <span class="mt__badge-t">{{ fmtInt(r.requests) }}</span>
              </span>
            </div>
            <p class="mt__meta">
              {{ t('mt.owner') }}: {{ r.owner_user || t('mt.unknownOwner') }}
              <span class="mt__sep">·</span>
              {{ t('mt.provider') }}: {{ r.provider_name || t('maas.noValue') }}
              <span class="mt__sep">·</span>
              {{ t('mt.credential') }}: {{ r.credential_label || t('maas.noValue') }}
            </p>
            <div class="mt__grid">
              <span class="mt__cell">
                <span class="mt__cell-l">{{ t('mt.credits') }}</span>
                <span class="mt__cell-v">{{ fmtInt(r.credits_charged) }}</span>
              </span>
              <span class="mt__cell">
                <span class="mt__cell-l">{{ t('mt.revenue') }}</span>
                <span class="mt__cell-v">${{ fmtNum(r.tenant_revenue_usd, 4) }}</span>
              </span>
              <span class="mt__cell">
                <span class="mt__cell-l">{{ t('mt.upstreamCost') }}</span>
                <span class="mt__cell-v">${{ fmtNum(r.upstream_cost_usd, 4) }}</span>
              </span>
              <span class="mt__cell">
                <span class="mt__cell-l">{{ t('mt.margin') }}</span>
                <span class="mt__cell-v">
                  {{ maasMarginRateUndefined(r) ? t('mt.marginNA') : fmtNum(r.gross_margin_rate * 100, 1) + '%' }}
                </span>
              </span>
            </div>
            <!-- ★★★ 服务端算的 revenue 对不上复算值 ⇒ 标出来 -->
            <p v-if="revenueMismatch(r)" class="mt__note mt__note--warn">
              <AppIcon name="alert" :size="13" />
              <span>{{ t('mt.revenueMismatch') }}</span>
            </p>
          </li>
        </ul>
      </section>

      <!-- ══════ 流水 ══════ -->
      <section class="mt__panel">
        <span class="mt__panel-title">{{ t('mt.ledger') }}</span>
        <p class="mt__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mt.ledgerNote', { n: effLedgerLimit, m: MAAS_LEDGER_LIMIT_MAX }) }}</span>
        </p>

        <!-- ★★ account 段自带最近 N 条（写死），与 ledger 段**不是同一批数据** -->
        <p class="mt__note">
          <AppIcon name="key" :size="13" />
          <span>
            {{ t('mt.accountRecent', { l: MAAS_ACCOUNT_LEDGER_COUNT, o: MAAS_ACCOUNT_ORDERS_COUNT }) }}
          </span>
        </p>

        <p v-if="!ledger?.items.length" class="mt__msg">{{ t('mt.emptyLedger') }}</p>
        <ul v-else class="mt__list">
          <li v-for="e in ledger.items" :key="e.id" class="mt__item">
            <div class="mt__item-head">
              <span class="mt__title">{{ e.entry_type }}</span>
              <span class="mt__amount" :class="e.amount < 0 ? 'mt__amount--neg' : 'mt__amount--pos'">
                {{ e.amount < 0 ? '' : '+' }}{{ fmtInt(e.amount) }}
              </span>
            </div>
            <p class="mt__meta">
              {{ fmtTime(e.created_at) }}
              <span class="mt__sep">·</span>
              {{ t('mt.balanceAfter') }}: {{ fmtInt(e.balance_after) }}
            </p>
            <!-- ★★ LedgerEntry 的三个指针**无** omitempty ⇒ 键在、值可为 null -->
            <p class="mt__meta">
              {{ t('mt.pool') }}: {{ e.pool ?? t('mt.nullPool') }}
              <template v-if="e.ref_type !== null || e.ref_id !== null">
                <span class="mt__sep">·</span>
                {{ e.ref_type ?? '—' }}/{{ e.ref_id ?? '—' }}
              </template>
            </p>
          </li>
        </ul>
      </section>

      <p class="mt__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mt.readOnlyNote') }}</span>
      </p>
    </template>
  </div>
</template>

<style scoped>
.mt__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mt__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mt__label { display: block; font-size: 12px; color: var(--app-text-muted); margin: 8px 0 4px; }

/* ★ R1：新增交互控件 ≥48 CSS px */
.mt__input { width: 100%; min-height: 48px; padding: 0 12px; font-size: 14px;
  border: 1px solid var(--border, #ddd); border-radius: 8px; background: transparent; color: inherit; }
.mt__seg { display: flex; gap: 6px; margin-bottom: 8px; }
.mt__seg-btn { flex: 1; min-height: 48px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--border, #ddd); background: transparent; color: inherit; }
.mt__seg-btn--on { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }
/* ★ 7 = 换表边界，按钮上标出来 */
.mt__seg-btn--hot { border-style: dashed; }
.mt__btn { width: 100%; min-height: 48px; margin-top: 8px; font-size: 14px; border-radius: 8px;
  border: 1px solid var(--border, #ddd); background: transparent; color: inherit; }
.mt__btn--primary { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }

.mt__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mt__msg--err { color: var(--app-danger); }
.mt__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mt__note--warn { color: var(--app-warning); }
.mt__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.mt__cell { display: flex; flex-direction: column; }
.mt__cell-l { font-size: 11px; color: var(--app-text-muted); }
.mt__cell-v { font-size: 14px; font-weight: 600; }

.mt__list { list-style: none; margin: 0; padding: 0; }
.mt__item { padding: 10px 0; border-top: 1px solid var(--border, #eee); }
.mt__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mt__title { font-size: 14px; font-weight: 600; }
.mt__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.mt__badge-t { color: var(--app-text-secondary); }
.mt__amount { font-size: 15px; font-weight: 700; }
.mt__amount--neg { color: var(--app-danger); }
.mt__amount--pos { color: var(--success, #2e7d32); }
.mt__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.mt__sep { margin: 0 4px; opacity: 0.5; }

.mt__table { width: 100%; border-collapse: collapse; margin-top: 8px; font-size: 12px; }
.mt__table th { text-align: left; font-weight: 500; color: var(--app-text-muted); font-size: 11px;
  border-bottom: 1px solid var(--border, #eee); padding: 4px 2px; }
.mt__table td { padding: 4px 2px; vertical-align: middle; }
.mt__td-l { color: var(--app-text-secondary); }
.mt__td-v { font-weight: 600; text-align: right; }
</style>