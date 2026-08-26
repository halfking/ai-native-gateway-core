<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import PublicPortalLayout from '../../components/PublicPortalLayout.vue'
import PublicContactBox from '../../components/PublicContactBox.vue'
import { usePublicCatalog } from '../../composables/usePublicCatalog'
import { createDonation, confirmStubDonation, getDownloadCatalog } from '../../api/public'

const { t, tm } = useI18n()
const router = useRouter()
const { catalog, ensureCatalog } = usePublicCatalog()
ensureCatalog()

const email = ref('')
const channel = ref('alipay')
const selectedTier = ref('recommend')
const customAmount = ref(50)
const loading = ref(false)
const showPayDialog = ref(false)
const orderNo = ref('')
const paymentHint = ref('')
const qrUrl = ref('')
const stubMode = ref(false)
const supporterCount = ref(0)

const tiers = computed(() => {
  const raw = tm('public.support.tiers') as Record<string, { label: string; amount: number; desc: string }>
  return [
    { key: 'coffee', ...raw.coffee },
    { key: 'recommend', ...raw.recommend },
    { key: 'enterprise', ...raw.enterprise },
    { key: 'custom', ...raw.custom },
  ]
})

const impacts = computed(() => tm('public.support.impacts') as string[])
const customPoints = computed(() => tm('public.support.customPoints') as string[])

const amountCents = computed(() => {
  if (selectedTier.value === 'custom') return Math.max(100, Math.round(customAmount.value * 100))
  const tier = tiers.value.find((x) => x.key === selectedTier.value)
  return tier?.amount ?? 9900
})

const amountYuan = computed(() => (amountCents.value / 100).toFixed(2))

async function loadSupporters() {
  try {
    const cat = await getDownloadCatalog()
    supporterCount.value = cat.supporters
  } catch { /* optional */ }
}

loadSupporters()

async function openPaymentDialog() {
  if (channel.value !== 'alipay') {
    ElMessage.info(t('public.support.alipayOnlyHint'))
    channel.value = 'alipay'
  }
  loading.value = true
  try {
    const res = await createDonation({
      email: email.value || undefined,
      amount_cents: amountCents.value,
      channel: channel.value,
      tier_label: selectedTier.value,
    })
    orderNo.value = res.donation.order_no
    paymentHint.value = res.payment.hint
    qrUrl.value = res.payment.qr_url
    stubMode.value = res.payment.stub_mode
    showPayDialog.value = true
  } catch {
    ElMessage.error(t('public.support.creating'))
  } finally {
    loading.value = false
  }
}

watch(selectedTier, () => {
  orderNo.value = ''
  showPayDialog.value = false
})

async function confirmStub() {
  if (!orderNo.value) return
  try {
    await confirmStubDonation(orderNo.value)
    ElMessage.success(t('public.support.created'))
    showPayDialog.value = false
    router.push('/download')
  } catch {
    ElMessage.error('confirm failed')
  }
}

function skip() {
  router.push('/download')
}
</script>

<template>
  <PublicPortalLayout
    :title="t('public.support.title')"
    :subtitle="t('public.support.subtitle')"
    :kicker="t('public.layout.support')"
  >
    <div class="sup-page">
      <PublicContactBox />
      <p class="sup-count">{{ t('public.support.supporterCount', { n: supporterCount }) }}</p>

      <el-card shadow="never" class="pub-card sup-impact">
        <h3>{{ t('public.support.impactTitle') }}</h3>
        <ul>
          <li v-for="(line, i) in impacts" :key="i">{{ line }}</li>
        </ul>
      </el-card>

      <div class="sup-tiers">
        <el-card
          v-for="tier in tiers"
          :key="tier.key"
          shadow="hover"
          class="pub-card sup-tier"
          :class="{ 'sup-tier--active': selectedTier === tier.key }"
          @click="selectedTier = tier.key"
        >
          <div class="sup-tier__label">{{ tier.label }}</div>
          <div v-if="tier.key !== 'custom'" class="sup-tier__price">¥{{ (tier.amount / 100).toFixed(0) }}</div>
          <div v-else class="sup-tier__price">{{ t('public.support.tiers.custom.label') }}</div>
          <div class="sup-tier__desc">{{ tier.desc }}</div>
        </el-card>
      </div>

      <el-input-number
        v-if="selectedTier === 'custom'"
        v-model="customAmount"
        :min="1"
        :max="9999"
        :step="10"
        class="sup-custom"
      />

      <el-form label-position="top" class="sup-form">
        <el-form-item :label="t('public.support.email')">
          <el-input v-model="email" :placeholder="t('public.support.emailPlaceholder')" />
        </el-form-item>
      </el-form>

      <p class="sup-note">{{ t('public.support.noBlock') }}</p>

      <div class="sup-actions">
        <el-button type="primary" size="large" :loading="loading" @click="openPaymentDialog">
          {{ t('public.support.showQr') }} · ¥{{ amountYuan }}
        </el-button>
        <el-button size="large" @click="skip">{{ t('public.support.skipBtn') }}</el-button>
      </div>

      <el-card shadow="never" class="pub-card sup-custom-box">
        <h3>{{ t('public.support.customTitle') }}</h3>
        <p>{{ t('public.support.customDesc') }}</p>
        <ul>
          <li v-for="(line, i) in customPoints" :key="i">{{ line }}</li>
        </ul>
        <div class="pub-link-row" v-if="catalog?.contact_email">
          <a :href="`mailto:${catalog.contact_email}?subject=${encodeURIComponent(t('public.support.customMailSubject'))}`">
            {{ catalog.contact_email }}
          </a>
        </div>
      </el-card>
    </div>

    <el-dialog
      v-model="showPayDialog"
      :title="t('public.support.payDialogTitle')"
      width="min(420px, 92vw)"
      destroy-on-close
    >
      <div class="sup-pay-dialog">
        <p class="sup-pay-amount">¥{{ amountYuan }}</p>
        <p class="sup-pay-order" v-if="orderNo">{{ t('public.support.orderNo') }}: <code>{{ orderNo }}</code></p>
        <p class="sup-pay-hint">{{ paymentHint }}</p>
        <img v-if="qrUrl" :src="qrUrl" alt="Alipay QR" class="sup-qr" />
        <p class="sup-pay-tip">{{ t('public.support.payAmountTip', { amount: amountYuan }) }}</p>
        <template v-if="stubMode">
          <p class="sup-stub">{{ t('public.support.stubHint') }}</p>
          <el-button type="success" @click="confirmStub">{{ t('public.support.confirmStub') }}</el-button>
        </template>
      </div>
    </el-dialog>
  </PublicPortalLayout>
</template>

<style scoped>
.sup-count, .sup-note { color: #94a3b8; }
.sup-impact ul, .sup-custom-box ul { margin: 0.5rem 0 0; padding-left: 1.25rem; }
.sup-tiers { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 0.75rem; margin: 1.5rem 0; }
.sup-tier { cursor: pointer; text-align: center; transition: border-color 0.2s; }
.sup-tier--active { border-color: var(--el-color-primary); }
.sup-tier__label { font-weight: 600; }
.sup-tier__price { font-size: 1.25rem; margin: 0.25rem 0; color: var(--el-color-primary); }
.sup-tier__desc { font-size: 0.75rem; color: #64748b; }
.sup-custom { margin-bottom: 1rem; }
.sup-actions { display: flex; gap: 1rem; flex-wrap: wrap; margin-top: 1rem; }
.sup-custom-box { margin-top: 1.5rem; }
.sup-custom-box h3 { margin: 0 0 8px; }
.sup-pay-dialog { text-align: center; }
.sup-pay-amount { font-size: 2rem; font-weight: 700; color: #1677ff; margin: 0; }
.sup-pay-order { font-size: 0.8rem; color: #64748b; }
.sup-pay-hint, .sup-pay-tip { font-size: 0.875rem; color: #94a3b8; line-height: 1.5; }
.sup-qr { max-width: 260px; width: 100%; margin: 12px auto; display: block; }
.sup-stub { color: #b45309; font-size: 0.875rem; }
</style>
