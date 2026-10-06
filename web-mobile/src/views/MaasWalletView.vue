<script setup lang="ts">
// MaasWalletView — MaaS **本租户**钱包（**admin 档**）。
//
// GET /api/maas/wallet
//
// ⚠️⚠️⚠️ 这条端点**看着只读，实际会写库**，而且响应里有两处算出来的值
// （详见 api/maas.ts 坑 23）：
//
// (1) ★★★★★★ **GET 会写库**：`GetWallet` 第一行就是 `ensureWalletDirect`
//     （`INSERT INTO tenant_credit_wallets … ON CONFLICT DO NOTHING`）
//     ⇒ 这是一个「看着只读、实际会建行」的端点。页面因此**不承诺**「刷新不会改数据」。
// (2) ★★★★★★ 只看**本租户**：`tenantID = GetTenantID(r)`
//     ⇒ 与 superAdmin 侧 `ListOrders(ctx, "", …)` 的**跨租户**语义**正好相反**。
// (3) ★★★★ `balance_credits` **不是原始列**：
//     `if w.BalanceCredits == 0 { w.BalanceCredits = Granted + Purchased }`
//     ⇒ 列值是 0 时会被两个余额之和**顶替**，客户端不得把它当原始列读。
// (4) ★★★★ `total_available = quota_remaining + granted + purchased`
//     ⇒ 把**订阅额度**（quota）和**积分余额**（两种单位）**相加**。
// (5) ★★★ `subscription` 是 `*SubscriptionView` + omitempty
//     ⇒ 没有生效订阅时**键整个不存在**，与「有订阅但 status 变了」是两回事。
//
// ★ 本页只读：调整/发放积分（POST adjust / grant）**不碰**。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtTime, relativeTime } from '@/utils/format'
import {
  fetchMaasWallet,
  maasWalletHasSubscription,
  maasWalletTotalIsMixedUnit,
  maasWalletBalanceIsSubstituted,
  type MaasWallet,
} from '@/api/maas'

useHyperPage({ title: () => t('mw.title') })

const wallet = ref<MaasWallet | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    wallet.value = await fetchMaasWallet()
  } catch (e) {
    // ★★ 抛错**不许**退化成「余额 0」：那会让人以为账被清空了。
    wallet.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

const hasSub = computed(() => (wallet.value ? maasWalletHasSubscription(wallet.value) : false))
const totalMixed = computed(() => (wallet.value ? maasWalletTotalIsMixedUnit(wallet.value) : false))
const balanceSubstituted = computed(() =>
  wallet.value ? maasWalletBalanceIsSubstituted(wallet.value) : false,
)

/** ★★ 「扣款顺序」：quota（订阅额度）先于积分余额 —— 总额相加暗示的就是这个顺序。 */
function subTone(): 'success' | 'muted' | 'warning' {
  const st = wallet.value?.subscription?.status
  if (st === 'active') return 'success'
  if (st === undefined) return 'muted'
  return 'warning'
}

void load()

onBeforeUnmount(() => {
  wallet.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="mw">
    <!-- ══════ 错误 ══════ -->
    <section v-if="error" class="mw__panel">
      <p class="mw__msg mw__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mw__note mw__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mw__msg">{{ t('common.loading') }}</p>

    <template v-if="wallet">
      <!-- ══════ 总额 ══════ -->
      <section class="mw__panel">
        <span class="mw__panel-title">{{ t('mw.balance') }}</span>
        <p class="mw__total">{{ fmtInt(wallet.total_available) }}</p>
        <p class="mw__total-l">{{ t('mw.totalAvailable') }}</p>

        <!-- ★★★ 总额把两种单位加在一起，必须说破 -->
        <p v-if="totalMixed" class="mw__note mw__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mw.totalMixedNote', { quota: wallet.quota_remaining }) }}</span>
        </p>

        <div class="mw__grid">
          <span class="mw__cell">
            <span class="mw__cell-l">{{ t('mw.quotaRemaining') }}</span>
            <span class="mw__cell-v">{{ fmtInt(wallet.quota_remaining) }}</span>
          </span>
          <span class="mw__cell">
            <span class="mw__cell-l">{{ t('mw.granted') }}</span>
            <span class="mw__cell-v">{{ fmtInt(wallet.granted_balance) }}</span>
          </span>
          <span class="mw__cell">
            <span class="mw__cell-l">{{ t('mw.purchased') }}</span>
            <span class="mw__cell-v">{{ fmtInt(wallet.purchased_balance) }}</span>
          </span>
          <span class="mw__cell">
            <span class="mw__cell-l">{{ t('mw.balanceCredits') }}</span>
            <span class="mw__cell-v">{{ fmtInt(wallet.balance_credits) }}</span>
          </span>
        </div>

        <!-- ★★ 这一栏可能被兜底顶替过，不是列里的原始值 -->
        <p v-if="balanceSubstituted" class="mw__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mw.balanceSubstitutedNote') }}</span>
        </p>

        <p class="mw__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mw.tenantScopeNote', { t: wallet.tenant_id || '—' }) }}</span>
        </p>

        <!-- ★★ 这条 GET 端点会建行，必须说破 -->
        <p class="mw__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mw.getWritesNote') }}</span>
        </p>
      </section>

      <!-- ══════ 订阅 ══════ -->
      <section class="mw__panel">
        <span class="mw__panel-title">{{ t('mw.subscription') }}</span>

        <!-- ★★★ 键整个不存在（指针 + omitempty）⇒ 与 status 不是 active 分开说 -->
        <p v-if="!hasSub" class="mw__msg">{{ t('mw.noSubscription') }}</p>
        <template v-else>
          <div class="mw__item-head">
            <span class="mw__title">{{ wallet.subscription?.plan_name || '—' }}</span>
            <span class="mw__badge">
              <StatusDot :tone="subTone()" />
              <span class="mw__badge-t">{{ wallet.subscription?.status || '—' }}</span>
            </span>
          </div>
          <div class="mw__grid">
            <span class="mw__cell">
              <span class="mw__cell-l">{{ t('mw.planId') }}</span>
              <span class="mw__cell-v">{{ wallet.subscription?.plan_id }}</span>
            </span>
            <span class="mw__cell">
              <span class="mw__cell-l">{{ t('mw.periodEnd') }}</span>
              <span class="mw__cell-v">{{ fmtTime(wallet.subscription?.period_end) }}</span>
            </span>
          </div>
          <p v-if="wallet.subscription?.period_end" class="mw__note">
            <AppIcon name="clock" :size="13" />
            <span>{{ t('mw.periodEndsIn', { v: relativeTime(wallet.subscription?.period_end) }) }}</span>
          </p>
          <!-- ★★ 「键在但 status 不是 active」是另一回事，必须和上面那条区分开 -->
          <p v-if="wallet.subscription && wallet.subscription.status !== 'active'" class="mw__note mw__note--warn">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('mw.subNotActive', { s: wallet.subscription.status }) }}</span>
          </p>
        </template>
      </section>

      <p class="mw__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mw.readOnlyNote') }}</span>
      </p>
    </template>
  </div>
</template>

<style scoped>
.mw__panel { background: var(--surface, #fff); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mw__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }

.mw__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mw__msg--err { color: var(--app-danger); }
.mw__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mw__note--warn { color: var(--app-warning); }

.mw__total { font-size: 30px; font-weight: 700; margin: 4px 0 0; }
.mw__total-l { font-size: 12px; color: var(--app-text-muted); margin: 2px 0 0; }
.mw__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 8px; }
.mw__cell { display: flex; flex-direction: column; }
.mw__cell-l { font-size: 11px; color: var(--app-text-muted); }
.mw__cell-v { font-size: 14px; font-weight: 600; }

.mw__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mw__title { font-size: 15px; font-weight: 700; }
.mw__badge { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; }
.mw__badge-t { color: var(--app-text-secondary); }
</style>
