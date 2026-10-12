<script setup lang="ts">
// MaasAdminCatalogView — MaaS **配置面**：完整 settings + 含停用行的套餐/充值包（**superAdmin 档**）。
//
// GET /api/admin/maas/settings
// GET /api/admin/maas/plans
// GET /api/admin/maas/topup-packages
//
// ⚠️ 这三条都在 `/api/admin/maas/**` 下、鉴权是 `h.superAdmin(...)`
// ⇒ 抽屉席**必须**设 requiresRole: 'super_admin'。
// ★ 与第三段 `/api/maas/**`（admin 档、只列 enabled、settings 只有 3 键）
//   是**同一族但不同档**：响应形状几乎相同，**内容差异很大** ——
//   本页能看到租户看不到的折扣、基价、停用行。
//
// 三个最容易渲染错的语义（详见 api/maas.ts 坑 19/36）：
//
// (1) ★★★★★ `settings` 是**裸全量 `Settings`**（12 键、**无** omitempty）
//     ⇒ 连空串与 0 都有键；且 `global_discount` 与 `base_credits_per_1m_in`
//     都是**租户面看不到**的成本数据。
// (2) ★★★★ `global_discount` 的 0 是「**不打折**」不是「全免」
//     （`normalizeDiscount`：`d<=0 || d>1 ⇒ 1`，见 §11.73 坑 5）。
// (3) ★★ admin 档 `enabledOnly=false` ⇒ **含停用行**，
//     页面必须显式标出停用，否则看不出租户那边买不到它们。
//
// ★ 写操作本页一律不碰（settings PUT、model-rates 增删改…）。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchMaasSettings,
  fetchMaasAdminPlans,
  fetchMaasAdminTopupPackages,
  maasGlobalBaseIn,
  maasEffectiveDiscount,
  maasCatalogPriceYuan,
  maasUnitPriceFenPerCredit,
  MAAS_HARDCODED_BASE_IN,
  type MaasSettings,
  type MaasPlan,
  type MaasTopupPackage,
} from '@/api/maas'

useHyperPage({ title: () => t('mac.title') })

const settings = ref<MaasSettings | null>(null)
const plans = ref<MaasPlan[] | null>(null)
const topups = ref<MaasTopupPackage[] | null>(null)
const error = ref<string | null>(null)
const errorKind = ref<'none' | 'unconfigured' | 'other'>('none')
const loading = ref(false)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  errorKind.value = 'none'
  try {
    const [s, p, tp] = await Promise.all([
      fetchMaasSettings(),
      fetchMaasAdminPlans(),
      fetchMaasAdminTopupPackages(),
    ])
    settings.value = s
    plans.value = p.items
    topups.value = tp.items
  } catch (e) {
    settings.value = null
    plans.value = null
    topups.value = null
    const msg = (e as Error)?.message || t('common.error')
    error.value = msg
    errorKind.value = /database not configured/i.test(msg) ? 'unconfigured' : 'other'
  } finally {
    loading.value = false
  }
}

const discount = computed(() => (settings.value ? maasEffectiveDiscount(settings.value) : 1))
const baseInInfo = computed(() =>
  settings.value ? maasGlobalBaseIn(settings.value) : { value: 0, source: 'configured' as const },
)

/** ★★ 折扣配 0 或 >1 的人以为「全免」，实际是「不打折」——点破。 */
const discountLooksFree = computed(() => {
  const raw = settings.value?.global_discount
  return typeof raw === 'number' && (raw <= 0 || raw > 1)
})

const disabledPlanCount = computed(() => (plans.value ?? []).filter((p) => !p.enabled).length)
const disabledTopupCount = computed(() => (topups.value ?? []).filter((p) => !p.enabled).length)

void load()

onBeforeUnmount(() => {
  settings.value = null
  plans.value = null
  topups.value = null
  error.value = null
  errorKind.value = 'none'
})
</script>

<template>
  <div class="mac">
    <section v-if="error" class="mac__panel">
      <p class="mac__msg mac__msg--err">
        <AppIcon name="alert" :size="14" />
        <span>{{ error }}</span>
      </p>
      <p v-if="errorKind === 'unconfigured'" class="mac__note mac__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('maas.unconfigured') }}</span>
      </p>
    </section>

    <p v-if="loading" class="mac__msg">{{ t('common.loading') }}</p>

    <template v-if="settings">
      <!-- ══════ 完整 settings ══════ -->
      <section class="mac__panel">
        <span class="mac__panel-title">{{ t('mac.settings') }}</span>

        <div class="mac__grid">
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.baseIn') }}</span>
            <span class="mac__cell-v">{{ fmtInt(baseInInfo.value) }}</span>
          </span>
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.discount') }}</span>
            <span class="mac__cell-v">{{ fmtNum(discount * 100, 1) }}%</span>
          </span>
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.centsPerCredit') }}</span>
            <span class="mac__cell-v">{{ fmtNum(settings.cents_per_credit, 4) }}</span>
          </span>
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.currency') }}</span>
            <span class="mac__cell-v">{{ settings.currency_display || t('maas.noValue') }}</span>
          </span>
        </div>

        <!-- ★★ 硬编码 10000 不是「没配」 -->
        <p v-if="baseInInfo.source === 'hardcoded'" class="mac__note mac__note--warn">
          <AppIcon name="alert" :size="14" />
          <span>{{ t('maas.hardcodedNote') }}</span>
        </p>

        <!-- ★★★★★ 折扣 0 / >1 被归一成「不打折」 -->
        <p v-if="discountLooksFree" class="mac__note mac__note--warn">
          <AppIcon name="alert" :size="14" />
          <span>{{ t('maas.discountTrap') }}</span>
        </p>

        <!-- ★★ 租户面看不到的正是这几个键 -->
        <p class="mac__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mac.tenantCannotSee') }}</span>
        </p>

        <!-- 支付与沙箱配置：租户面完全没有这一段 -->
        <p class="mac__panel-sub">{{ t('mac.paymentCfg') }}</p>
        <div class="mac__grid">
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.alipayAccount') }}</span>
            <span class="mac__cell-v">{{ settings.alipay_account || t('maas.noValue') }}</span>
          </span>
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.wechatMchId') }}</span>
            <span class="mac__cell-v">{{ settings.wechat_mch_id || t('maas.noValue') }}</span>
          </span>
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.stubAlipay') }}</span>
            <span class="mac__cell-v">{{ settings.stub_alipay_qr_url ? t('mac.configured') : t('mac.notConfigured') }}</span>
          </span>
          <span class="mac__cell">
            <span class="mac__cell-l">{{ t('mac.stubWechat') }}</span>
            <span class="mac__cell-v">{{ settings.stub_wechat_qr_url ? t('mac.configured') : t('mac.notConfigured') }}</span>
          </span>
        </div>
        <p class="mac__note">
          <AppIcon name="key" :size="13" />
          <span>{{ t('mac.hardcodedValue', { n: MAAS_HARDCODED_BASE_IN }) }}</span>
        </p>
      </section>

      <!-- ══════ 套餐（含停用） ══════ -->
      <section class="mac__panel">
        <span class="mac__panel-title">{{ t('mac.plans') }}</span>
        <p class="mac__note mac__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mac.includesDisabled', { n: disabledPlanCount, total: plans!.length }) }}</span>
        </p>
        <p v-if="!plans!.length" class="mac__msg">{{ t('mac.emptyPlans') }}</p>
        <ul v-else class="mac__list">
          <li v-for="p in plans!" :key="p.code" class="mac__item" :class="{ 'mac__item--off': !p.enabled }">
            <div class="mac__item-head">
              <span class="mac__title">{{ p.name }}</span>
              <span class="mac__price">¥{{ fmtNum(maasCatalogPriceYuan(p.price_cents), 2) }}</span>
            </div>
            <p class="mac__meta">
              {{ t('mac.code') }}: {{ p.code }}
              <span class="mac__sep">·</span>
              {{ t('mac.tier') }}: {{ p.tier }}
              <span class="mac__sep">·</span>
              {{ t('mac.credits') }}: {{ fmtInt(p.monthly_credits) }}
              <!-- ★★ enabled=false 的行**租户那边根本看不到** -->
              <span v-if="!p.enabled" class="mac__tag mac__tag--warn">{{ t('mac.disabledTag') }}</span>
            </p>
            <p v-if="maasUnitPriceFenPerCredit(p.price_cents, p.monthly_credits) !== null" class="mac__unit">
              {{ t('mac.unitPrice', { v: fmtNum(maasUnitPriceFenPerCredit(p.price_cents, p.monthly_credits)!, 4) }) }}
            </p>
          </li>
        </ul>
      </section>

      <!-- ══════ 充值包（含停用） ══════ -->
      <section class="mac__panel">
        <span class="mac__panel-title">{{ t('mac.topups') }}</span>
        <p class="mac__note mac__note--warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('mac.includesDisabled', { n: disabledTopupCount, total: topups!.length }) }}</span>
        </p>
        <p v-if="!topups!.length" class="mac__msg">{{ t('mac.emptyTopups') }}</p>
        <ul v-else class="mac__list">
          <li v-for="tp in topups!" :key="tp.code" class="mac__item" :class="{ 'mac__item--off': !tp.enabled }">
            <div class="mac__item-head">
              <span class="mac__title">{{ tp.name }}</span>
              <span class="mac__price">¥{{ fmtNum(maasCatalogPriceYuan(tp.price_cents), 2) }}</span>
            </div>
            <p class="mac__meta">
              {{ t('mac.code') }}: {{ tp.code }}
              <span class="mac__sep">·</span>
              {{ t('mac.tier') }}: {{ tp.tier }}
              <span class="mac__sep">·</span>
              {{ t('mac.credits') }}: {{ fmtInt(tp.credits_amount) }}
              <span v-if="!tp.enabled" class="mac__tag mac__tag--warn">{{ t('mac.disabledTag') }}</span>
            </p>
            <p v-if="maasUnitPriceFenPerCredit(tp.price_cents, tp.credits_amount) !== null" class="mac__unit">
              {{ t('mac.unitPrice', { v: fmtNum(maasUnitPriceFenPerCredit(tp.price_cents, tp.credits_amount)!, 4) }) }}
            </p>
          </li>
        </ul>
      </section>

      <p class="mac__note">
        <AppIcon name="key" :size="13" />
        <span>{{ t('mac.readOnlyNote') }}</span>
      </p>
    </template>
  </div>
</template>

<style scoped>
.mac__panel { background: var(--app-surface); border-radius: 12px; padding: 12px; margin-bottom: 12px; }
.mac__panel-title { display: block; font-size: 15px; font-weight: 600; margin-bottom: 8px; }
.mac__panel-sub { display: block; font-size: 13px; font-weight: 600; margin: 12px 0 4px; }

.mac__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.mac__msg--err { color: var(--app-danger); }
.mac__note { display: flex; gap: 6px; align-items: flex-start; font-size: 12px; line-height: 1.5;
  color: var(--app-text-secondary); margin: 6px 0; }
.mac__note--warn { color: var(--app-warning); }
.mac__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
.mac__cell { display: flex; flex-direction: column; }
.mac__cell-l { font-size: 11px; color: var(--app-text-muted); }
.mac__cell-v { font-size: 14px; font-weight: 600; }

.mac__list { list-style: none; margin: 0; padding: 0; }
.mac__item { padding: 10px 0; border-top: 1px solid var(--app-border); }
/* ★ 停用行整体压暗 */
.mac__item--off { opacity: 0.6; }
.mac__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.mac__title { font-size: 14px; font-weight: 600; }
.mac__price { font-size: 15px; font-weight: 700; }
.mac__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.mac__sep { margin: 0 4px; opacity: 0.5; }
.mac__unit { font-size: 11px; color: var(--app-text-muted); margin: 4px 0 0; }
.mac__tag { font-size: 10px; padding: 1px 5px; border-radius: 4px; background: var(--app-surface-muted); margin-left: 4px; }
.mac__tag--warn { background: var(--app-warning); color: var(--app-on-warning); }
</style>