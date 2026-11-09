<script setup lang="ts">
// OperationsMarketingView.vue — 运营营销管理（2026-11-09 运营审计轮项4）。
// 套餐 / 加油包 / 优惠码 / 活动 四页签；写路径全部 superAdmin（服务端闸）。
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted } from 'vue'
import {
  getAdminMaasPlans,
  putAdminMaasPlan,
  patchAdminMaasPlan,
  getAdminMaasTopupPackages,
  putAdminMaasTopup,
  patchAdminMaasTopup,
  getAdminMaasCoupons,
  patchAdminMaasCoupon,
  getAdminMaasCampaigns,
  postAdminMaasCampaign,
  patchAdminMaasCampaignStatus,
  type AdminMaasPlan,
  type AdminMaasTopup,
  type AdminMaasCoupon,
  type AdminMaasCampaign,
} from '../../api'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import { useActionMessage } from '../../composables/useActionMessage'

const { t } = useI18n()
const { message: actionMsg, notifySuccess, notifyError, clear: clearActionMsg } = useActionMessage()

const tab = ref<'plans' | 'topups' | 'coupons' | 'campaigns'>('plans')
const loading = ref(false)

const plans = ref<AdminMaasPlan[]>([])
const topups = ref<AdminMaasTopup[]>([])
const coupons = ref<AdminMaasCoupon[]>([])
const campaigns = ref<AdminMaasCampaign[]>([])

async function load() {
  loading.value = true
  try {
    const [p, tp, cp, c] = await Promise.all([
      getAdminMaasPlans(),
      getAdminMaasTopupPackages(),
      getAdminMaasCoupons(100),
      getAdminMaasCampaigns(),
    ])
    plans.value = p.items || []
    topups.value = tp.items || []
    coupons.value = cp.items || []
    campaigns.value = c.items || []
  } catch {
    notifyError(t('operationsMarketing.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- plans / topups ----
const showPlanModal = ref(false)
const showTopupModal = ref(false)
const saving = ref(false)
const planForm = ref(emptyPlan())
const topupForm = ref(emptyTopup())

function emptyPlan() {
  return { code: '', tier: 'basic', name: '', price_cents: 0, monthly_credits: 1000000, billing_period: 'monthly', enabled: true, sort_order: 0 }
}
function emptyTopup() {
  return { code: '', tier: 'small', name: '', price_cents: 0, credits_amount: 1000000, bonus_credits: 0, first_time_only: false, enabled: true, sort_order: 0 }
}

function openPlanModal() {
  clearActionMsg()
  planForm.value = emptyPlan()
  showPlanModal.value = true
}
function openTopupModal() {
  clearActionMsg()
  topupForm.value = emptyTopup()
  showTopupModal.value = true
}

async function savePlan() {
  saving.value = true
  try {
    await putAdminMaasPlan(planForm.value)
    notifySuccess(t('operationsMarketing.saved'))
    showPlanModal.value = false
    await load()
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function saveTopup() {
  saving.value = true
  try {
    await putAdminMaasTopup(topupForm.value)
    notifySuccess(t('operationsMarketing.saved'))
    showTopupModal.value = false
    await load()
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function togglePlan(p: AdminMaasPlan) {
  try {
    await patchAdminMaasPlan(p.id, !p.enabled)
    p.enabled = !p.enabled
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  }
}

async function toggleTopup(p: AdminMaasTopup) {
  try {
    await patchAdminMaasTopup(p.id, !p.enabled)
    p.enabled = !p.enabled
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  }
}

// ---- coupons ----
async function toggleCoupon(c: AdminMaasCoupon) {
  try {
    const next = c.status === 'active' ? 'disabled' : 'active'
    await patchAdminMaasCoupon(c.id, next)
    c.status = next
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  }
}

// ---- campaigns ----
const showCampaignModal = ref(false)
const campaignForm = ref(emptyCampaign())

function emptyCampaign() {
  return {
    code: '',
    name: '',
    description: '',
    benefit_type: 'credits' as 'credits' | 'discount' | 'gift',
    credits: 100000,
    percent: 20,
    max_redemptions_per_tenant: 1,
    total_budget_credits: 0,
  }
}

const campaignBenefitLabel = computed(() => {
  switch (campaignForm.value.benefit_type) {
    case 'discount': return t('operationsMarketing.benefitDiscount')
    case 'gift': return t('operationsMarketing.benefitGift')
    default: return t('operationsMarketing.benefitCredits')
  }
})

function openCampaignModal() {
  clearActionMsg()
  campaignForm.value = emptyCampaign()
  showCampaignModal.value = true
}

async function saveCampaign() {
  saving.value = true
  try {
    const benefitValue: Record<string, number> = campaignForm.value.benefit_type === 'discount'
      ? { percent: campaignForm.value.percent }
      : { credits: campaignForm.value.credits }
    await postAdminMaasCampaign({
      code: campaignForm.value.code,
      name: campaignForm.value.name,
      description: campaignForm.value.description,
      benefit_type: campaignForm.value.benefit_type,
      benefit_value: benefitValue,
      max_redemptions_per_tenant: campaignForm.value.max_redemptions_per_tenant,
      total_budget_credits: campaignForm.value.total_budget_credits,
    })
    notifySuccess(t('operationsMarketing.saved'))
    showCampaignModal.value = false
    await load()
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function setCampaignStatus(c: AdminMaasCampaign, status: 'draft' | 'active' | 'paused' | 'ended') {
  try {
    await patchAdminMaasCampaignStatus(c.id, status)
    c.status = status
  } catch {
    notifyError(t('operationsMarketing.saveFailed'))
  }
}

const campaignStatuses: Array<'draft' | 'active' | 'paused' | 'ended'> = ['draft', 'active', 'paused', 'ended']

function statusKey(s: string): string {
  switch (s) {
    case 'active': return 'operationsMarketing.statusActive'
    case 'paused': return 'operationsMarketing.statusPaused'
    case 'ended': return 'operationsMarketing.statusEnded'
    case 'disabled': return 'common.status.disabled'
    default: return 'operationsMarketing.statusDraft'
  }
}

function benefitKey(s: string): string {
  switch (s) {
    case 'discount': return 'operationsMarketing.benefitDiscount'
    case 'gift': return 'operationsMarketing.benefitGift'
    default: return 'operationsMarketing.benefitCredits'
  }
}

function fmtWindow(from?: string | null, to?: string | null): string {
  if (!from && !to) return '—'
  const parts: string[] = []
  if (from) parts.push(new Date(from).toLocaleDateString())
  if (to) parts.push(`→ ${new Date(to).toLocaleDateString()}`)
  return parts.join(' ')
}

function fmtBenefitValue(c: { benefit_type: string; benefit_value?: Record<string, number> }): string {
  if (c.benefit_type === 'discount') return `-${c.benefit_value?.percent ?? 0}%`
  return `${c.benefit_value?.credits ?? 0}`
}
const priceField = computed<CardField>(() => ({
  key: 'price_cents', label: t('operationsMarketing.colPrice'), type: 'metric',
  format: value => `¥${(Number(value) / 100).toFixed(2)}`,
}))
const windowField = computed<CardField>(() => ({
  key: 'effective_from', label: t('operationsMarketing.colWindow'),
  format: (_, row) => fmtWindow(row.effective_from as string | null, row.effective_to as string | null),
}))
const enabledField = computed<CardField>(() => ({
  key: 'enabled', label: t('operationsMarketing.colEnabled'), type: 'badge',
  tone: row => row.enabled ? 'good' : 'neutral',
  format: value => t(value ? 'operationsMarketing.enabled' : 'operationsMarketing.disabled'),
}))
const planFields = computed<CardField[]>(() => [
  { key: 'code', label: t('operationsMarketing.colCode') },
  { key: 'monthly_credits', label: t('operationsMarketing.colCredits'), format: value => Number(value).toLocaleString() },
  windowField.value, enabledField.value,
])
const topupFields = computed<CardField[]>(() => [
  { key: 'code', label: t('operationsMarketing.colCode') },
  { key: 'credits_amount', label: t('operationsMarketing.colCredits'), format: value => Number(value).toLocaleString() },
  { key: 'bonus_credits', label: t('operationsMarketing.colBonus'), format: value => `+${Number(value || 0).toLocaleString()}` },
  { key: 'first_time_only', label: t('operationsMarketing.colFirstOnly'), format: value => value ? t('operationsMarketing.colFirstOnly') : '—' },
  windowField.value, enabledField.value,
])
const statusField = computed<CardField>(() => ({
  key: 'status', label: t('operationsMarketing.colStatus'), type: 'badge',
  tone: row => row.status === 'active' ? 'good' : 'neutral', format: value => t(statusKey(String(value))),
}))
const couponFields = computed<CardField[]>(() => [
  { key: 'credits_amount', label: t('operationsMarketing.colCredits'), format: value => Number(value).toLocaleString() },
  { key: 'redemption_count', label: t('operationsMarketing.colRedemptions'), format: (value, row) => `${value} / ${row.max_redemptions}` },
  statusField.value,
])
const campaignFields = computed<CardField[]>(() => [
  { key: 'code', label: t('operationsMarketing.colCode') },
  { key: 'benefit_type', label: t('operationsMarketing.colBenefit'), format: (value, row) => `${t(benefitKey(String(value)))} ${fmtBenefitValue({ benefit_type: String(value), benefit_value: row.benefit_value as Record<string, number> })}` },
  { key: 'granted_credits', label: t('operationsMarketing.colBudget'), format: (value, row) => `${Number(value).toLocaleString()} / ${Number(row.total_budget_credits) > 0 ? Number(row.total_budget_credits).toLocaleString() : '∞'}` },
  statusField.value,
])

</script>

<template>
  <div class="page">
    <h2 class="page-title">{{ t('operationsMarketing.title') }}</h2>
    <p class="page-sub">{{ t('operationsMarketing.subtitle') }}</p>

    <div class="tab-row">
      <button class="btn btn-sm" :class="tab === 'plans' ? 'btn-primary' : 'btn-ghost'" @click="tab = 'plans'">{{ t('operationsMarketing.tabPlans') }}</button>
      <button class="btn btn-sm" :class="tab === 'topups' ? 'btn-primary' : 'btn-ghost'" @click="tab = 'topups'">{{ t('operationsMarketing.tabTopups') }}</button>
      <button class="btn btn-sm" :class="tab === 'coupons' ? 'btn-primary' : 'btn-ghost'" @click="tab = 'coupons'">{{ t('operationsMarketing.tabCoupons') }}</button>
      <button class="btn btn-sm" :class="tab === 'campaigns' ? 'btn-primary' : 'btn-ghost'" @click="tab = 'campaigns'">{{ t('operationsMarketing.tabCampaigns') }}</button>
    </div>

    <!-- 套餐 -->
    <div v-if="tab === 'plans'" class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('operationsMarketing.tabPlans') }}</h3>
        <button class="btn btn-primary btn-sm" @click="openPlanModal">{{ t('operationsMarketing.add') }}</button>
      </div>
      <ResponsiveDataView :rows="plans" title-key="id" :title-format="row => String(row.name || row.code)" :fields="planFields" :primary-field="priceField" :loading="loading" :empty="!plans.length">
        <template #table>
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('operationsMarketing.colCode') }}</th>
            <th>{{ t('operationsMarketing.colName') }}</th>
            <th>{{ t('operationsMarketing.colPrice') }}</th>
            <th>{{ t('operationsMarketing.colCredits') }}</th>
            <th>{{ t('operationsMarketing.colWindow') }}</th>
            <th>{{ t('operationsMarketing.colEnabled') }}</th>
            <th>{{ t('operationsMarketing.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in plans" :key="p.id">
            <td><code class="mono-sm">{{ p.code }}</code></td>
            <td>{{ p.name }}</td>
            <td class="num">¥{{ (p.price_cents / 100).toFixed(2) }}</td>
            <td class="num">{{ p.monthly_credits.toLocaleString() }}</td>
            <td>{{ fmtWindow(p.effective_from, p.effective_to) }}</td>
            <td><span class="badge" :class="p.enabled ? 'badge-blue' : 'badge-gray'">{{ p.enabled ? t('operationsMarketing.enabled') : t('operationsMarketing.disabled') }}</span></td>
            <td class="actions"><button class="btn btn-ghost btn-sm" @click="togglePlan(p)">{{ p.enabled ? t('operationsMarketing.disable') : t('operationsMarketing.enable') }}</button></td>
          </tr>
        </tbody>
      </table>
        </template>
        <template #actions="{ row }">
          <button class="btn btn-ghost marketing-card-action" @click="togglePlan(row)">{{ row.enabled ? t('operationsMarketing.disable') : t('operationsMarketing.enable') }}</button>
        </template>
      </ResponsiveDataView>
    </div>

    <!-- 加油包 -->
    <div v-if="tab === 'topups'" class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('operationsMarketing.tabTopups') }}</h3>
        <button class="btn btn-primary btn-sm" @click="openTopupModal">{{ t('operationsMarketing.add') }}</button>
      </div>
      <ResponsiveDataView :rows="topups" title-key="id" :title-format="row => String(row.name || row.code)" :fields="topupFields" :primary-field="priceField" :loading="loading" :empty="!topups.length">
        <template #table>
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('operationsMarketing.colCode') }}</th>
            <th>{{ t('operationsMarketing.colName') }}</th>
            <th>{{ t('operationsMarketing.colPrice') }}</th>
            <th>{{ t('operationsMarketing.colCredits') }}</th>
            <th>{{ t('operationsMarketing.colBonus') }}</th>
            <th>{{ t('operationsMarketing.colFirstOnly') }}</th>
            <th>{{ t('operationsMarketing.colWindow') }}</th>
            <th>{{ t('operationsMarketing.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in topups" :key="p.id">
            <td><code class="mono-sm">{{ p.code }}</code></td>
            <td>{{ p.name }}</td>
            <td class="num">¥{{ (p.price_cents / 100).toFixed(2) }}</td>
            <td class="num">{{ p.credits_amount.toLocaleString() }}</td>
            <td class="num">+{{ (p.bonus_credits || 0).toLocaleString() }}</td>
            <td><span v-if="p.first_time_only" class="badge badge-yellow">{{ t('operationsMarketing.colFirstOnly') }}</span><span v-else>—</span></td>
            <td>{{ fmtWindow(p.effective_from, p.effective_to) }}</td>
            <td class="actions"><button class="btn btn-ghost btn-sm" @click="toggleTopup(p)">{{ p.enabled ? t('operationsMarketing.disable') : t('operationsMarketing.enable') }}</button></td>
          </tr>
        </tbody>
      </table>
        </template>
        <template #actions="{ row }">
          <button class="btn btn-ghost marketing-card-action" @click="toggleTopup(row)">{{ row.enabled ? t('operationsMarketing.disable') : t('operationsMarketing.enable') }}</button>
        </template>
      </ResponsiveDataView>
    </div>

    <!-- 优惠码 -->
    <div v-if="tab === 'coupons'" class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('operationsMarketing.tabCoupons') }}</h3>
      </div>
      <ResponsiveDataView :rows="coupons" title-key="id" :title-format="row => String(row.code)" :fields="couponFields"  :loading="loading" :empty="!coupons.length">
        <template #table>
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('operationsMarketing.colCode') }}</th>
            <th>{{ t('operationsMarketing.colCredits') }}</th>
            <th>{{ t('operationsMarketing.colRedemptions') }}</th>
            <th>{{ t('operationsMarketing.colStatus') }}</th>
            <th>{{ t('operationsMarketing.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in coupons" :key="c.id">
            <td><code class="mono-sm">{{ c.code }}</code></td>
            <td class="num">{{ c.credits_amount.toLocaleString() }}</td>
            <td class="num">{{ c.redemption_count }} / {{ c.max_redemptions }}</td>
            <td><span class="badge" :class="c.status === 'active' ? 'badge-blue' : 'badge-gray'">{{ t(statusKey(c.status)) }}</span></td>
            <td class="actions">
              <button v-if="c.status === 'active' || c.status === 'disabled'" class="btn btn-ghost btn-sm" @click="toggleCoupon(c)">
                {{ c.status === 'active' ? t('operationsMarketing.couponDisable') : t('operationsMarketing.couponEnable') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
        </template>
        <template #actions="{ row }">
          <button v-if="row.status === 'active' || row.status === 'disabled'" class="btn btn-ghost marketing-card-action" @click="toggleCoupon(row)">{{ row.status === 'active' ? t('operationsMarketing.couponDisable') : t('operationsMarketing.couponEnable') }}</button>
        </template>
      </ResponsiveDataView>
    </div>

    <!-- 活动 -->
    <div v-if="tab === 'campaigns'" class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('operationsMarketing.tabCampaigns') }}</h3>
        <button class="btn btn-primary btn-sm" @click="openCampaignModal">{{ t('operationsMarketing.add') }}</button>
      </div>
      <ResponsiveDataView :rows="campaigns" title-key="id" :title-format="row => String(row.name || row.code)" :fields="campaignFields"  :loading="loading" :empty="!campaigns.length">
        <template #table>
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('operationsMarketing.colCode') }}</th>
            <th>{{ t('operationsMarketing.colName') }}</th>
            <th>{{ t('operationsMarketing.colBenefit') }}</th>
            <th>{{ t('operationsMarketing.colBudget') }}</th>
            <th>{{ t('operationsMarketing.colStatus') }}</th>
            <th>{{ t('operationsMarketing.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="c in campaigns" :key="c.id">
            <td><code class="mono-sm">{{ c.code }}</code></td>
            <td>{{ c.name }}</td>
            <td>
              <span class="badge badge-blue">{{ t(benefitKey(c.benefit_type)) }}</span>
              <span class="num">{{ fmtBenefitValue(c) }}</span>
            </td>
            <td class="num">{{ c.granted_credits.toLocaleString() }} / {{ c.total_budget_credits > 0 ? c.total_budget_credits.toLocaleString() : '∞' }}</td>
            <td><span class="badge" :class="c.status === 'active' ? 'badge-blue' : 'badge-gray'">{{ t(statusKey(c.status)) }}</span></td>
            <td class="actions">
              <select class="input compact" :value="c.status" @change="setCampaignStatus(c, ($event.target as HTMLSelectElement).value as 'draft'|'active'|'paused'|'ended')">
                <option v-for="s in campaignStatuses" :key="s" :value="s">{{ t(statusKey(s)) }}</option>
              </select>
            </td>
          </tr>
        </tbody>
      </table>
        </template>
        <template #actions="{ row }">
          <select class="input marketing-card-action" :aria-label="t('operationsMarketing.colStatus')" :value="row.status" @change="setCampaignStatus(row, ($event.target as HTMLSelectElement).value as 'draft'|'active'|'paused'|'ended')">
              <option v-for="status in campaignStatuses" :key="status" :value="status">{{ t(statusKey(status)) }}</option>
            </select>
        </template>
      </ResponsiveDataView>
    </div>

    <p v-if="actionMsg" class="action-msg">{{ actionMsg }}</p>

    <!-- 新建套餐 -->
    <div v-if="showPlanModal" class="modal-backdrop" @click.self="showPlanModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('operationsMarketing.addPlanTitle') }}</h3>
        <div class="edit-grid">
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colCode') }}</span><input v-model="planForm.code" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colName') }}</span><input v-model="planForm.name" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.priceMinorUnit') }}</span><input v-model.number="planForm.price_cents" type="number" min="0" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colCredits') }}</span><input v-model.number="planForm.monthly_credits" type="number" min="1" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colEnabled') }}</span><input v-model="planForm.enabled" type="checkbox" /></label>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="saving || !planForm.code || !planForm.name" @click="savePlan">{{ t('operationsMarketing.save') }}</button>
          <button class="btn btn-ghost btn-sm" @click="showPlanModal = false">{{ t('operationsMarketing.cancel') }}</button>
        </div>
      </div>
    </div>

    <!-- 新建加油包 -->
    <div v-if="showTopupModal" class="modal-backdrop" @click.self="showTopupModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('operationsMarketing.addTopupTitle') }}</h3>
        <div class="edit-grid">
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colCode') }}</span><input v-model="topupForm.code" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colName') }}</span><input v-model="topupForm.name" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.priceMinorUnit') }}</span><input v-model.number="topupForm.price_cents" type="number" min="0" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colCredits') }}</span><input v-model.number="topupForm.credits_amount" type="number" min="1" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colBonus') }}</span><input v-model.number="topupForm.bonus_credits" type="number" min="0" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colFirstOnly') }}</span><input v-model="topupForm.first_time_only" type="checkbox" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colEnabled') }}</span><input v-model="topupForm.enabled" type="checkbox" /></label>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="saving || !topupForm.code || !topupForm.name" @click="saveTopup">{{ t('operationsMarketing.save') }}</button>
          <button class="btn btn-ghost btn-sm" @click="showTopupModal = false">{{ t('operationsMarketing.cancel') }}</button>
        </div>
      </div>
    </div>

    <!-- 新建活动 -->
    <div v-if="showCampaignModal" class="modal-backdrop" @click.self="showCampaignModal = false">
      <div class="modal card">
        <h3 class="section-title">{{ t('operationsMarketing.addCampaignTitle') }}</h3>
        <p class="modal-hint">{{ t('operationsMarketing.campaignHint') }}</p>
        <div class="edit-grid">
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colCode') }}</span><input v-model="campaignForm.code" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colName') }}</span><input v-model="campaignForm.name" class="input compact" /></label>
          <label class="edit-field">
            <span class="edit-head">{{ t('operationsMarketing.colBenefit') }}（{{ campaignBenefitLabel }}）</span>
            <select v-model="campaignForm.benefit_type" class="input compact">
              <option value="credits">{{ t('operationsMarketing.benefitCredits') }}</option>
              <option value="gift">{{ t('operationsMarketing.benefitGift') }}</option>
              <option value="discount">{{ t('operationsMarketing.benefitDiscount') }}</option>
            </select>
          </label>
          <label v-if="campaignForm.benefit_type !== 'discount'" class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colCredits') }}</span><input v-model.number="campaignForm.credits" type="number" min="1" class="input compact" /></label>
          <label v-else class="edit-field"><span class="edit-head">{{ t('operationsMarketing.percent') }} (%)</span><input v-model.number="campaignForm.percent" type="number" min="1" max="99" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.colBudget') }} (0=∞)</span><input v-model.number="campaignForm.total_budget_credits" type="number" min="0" class="input compact" /></label>
          <label class="edit-field"><span class="edit-head">{{ t('operationsMarketing.perTenant') }}</span><input v-model.number="campaignForm.max_redemptions_per_tenant" type="number" min="1" class="input compact" /></label>
        </div>
        <div class="modal-actions">
          <button class="btn btn-primary btn-sm" :disabled="saving || !campaignForm.code || !campaignForm.name" @click="saveCampaign">{{ t('operationsMarketing.save') }}</button>
          <button class="btn btn-ghost btn-sm" @click="showCampaignModal = false">{{ t('operationsMarketing.cancel') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.marketing-card-action { min-height: 48px; }
.tab-row { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.table-head-row { display: flex; justify-content: space-between; align-items: center; }
.page-sub { color: var(--kx-text-secondary); font-size: 13px; margin-top: -6px; }
.action-msg { color: var(--kx-text-secondary); font-size: 13px; }
</style>
