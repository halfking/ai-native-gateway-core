<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { getProviderCredentials, getProviderDetail, getProviderModels, providerName, setProviderManualDisabled, toggleProvider, type Provider, type ProviderCredentialDetail, type ProviderDetail as ProviderDetailData, type ProviderModelOffer } from '@/api/providers'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import AppSheet from '@/components/common/AppSheet.vue'
import AppStateView from '@/components/common/AppStateView.vue'
import NodeAuditSheet from '@/components/shell/NodeAuditSheet.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { useTopbarAction } from '@/components/shell/topbarActions'
import { fmtInt, fmtUsd } from '@/utils/format'

const props = defineProps<{ modelValue: boolean; provider: Provider }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; close: []; changed: [provider: ProviderDetailData] }>()
const auth = useAuthStore()
const canManage = computed(() => auth.role === 'super_admin')
const tab = ref<'overview' | 'credentials' | 'models'>('overview')
const detail = ref<ProviderDetailData | null>(null)
const credentials = ref<ProviderCredentialDetail[]>([])
const models = ref<ProviderModelOffer[]>([])
const loading = ref(false)
const error = ref('')
const updating = ref(false)
const auditOpen = ref(false)
let generation = 0
let controller: AbortController | null = null

// 刷新进顶栏右侧，避免详情头再占一整行「刷新」按钮（移动端验收契约）。
useTopbarAction({
  key: 'provider-detail-refresh',
  label: () => t('common.refresh'),
  icon: 'refresh',
  disabled: () => loading.value || updating.value,
  onClick: () => { void load() },
})

const open = computed({
  get: () => props.modelValue,
  set: (value: boolean) => {
    if (!value) close()
    else emit('update:modelValue', true)
  },
})

function close(): void {
  generation++
  controller?.abort()
  auditOpen.value = false
  emit('update:modelValue', false)
  emit('close')
}

async function load(): Promise<void> {
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  const current = ++generation
  loading.value = true
  error.value = ''
  detail.value = null
  credentials.value = []
  models.value = []
  try {
    const [provider, providerCredentials, offers] = await Promise.all([
      getProviderDetail(props.provider.id, { signal }),
      getProviderCredentials(props.provider.id, { signal }),
      getProviderModels(props.provider.id, { signal }),
    ])
    if (current !== generation || signal.aborted) return
    detail.value = provider
    credentials.value = providerCredentials
    models.value = offers
  } catch (cause) {
    if (current === generation && !signal.aborted) error.value = cause instanceof Error ? cause.message : t('providers.detailLoadFailed')
  } finally {
    if (current === generation && !signal.aborted) loading.value = false
  }
}

async function toggleEnabled(): Promise<void> {
  if (!detail.value || !canManage.value || updating.value) return
  const next = !detail.value.enabled
  if (!window.confirm(t(next ? 'providers.enableConfirm' : 'providers.disableConfirm', { name: providerName(detail.value) }))) return
  updating.value = true
  error.value = ''
  try {
    await toggleProvider(detail.value.id)
    detail.value.enabled = next
    emit('changed', detail.value)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('providers.operationFailed')
  } finally {
    updating.value = false
  }
}

async function toggleManualDisabled(): Promise<void> {
  if (!detail.value || !canManage.value || updating.value) return
  const next = !detail.value.manual_disabled
  const reason = window.prompt(t(next ? 'providers.manualDisablePrompt' : 'providers.manualEnablePrompt', { name: providerName(detail.value) }), '')
  if (reason === null) return
  if (!reason.trim()) {
    error.value = t('providers.reasonRequired')
    return
  }
  updating.value = true
  error.value = ''
  try {
    await setProviderManualDisabled(detail.value.id, next, reason.trim())
    detail.value.manual_disabled = next
    emit('changed', detail.value)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('providers.operationFailed')
  } finally {
    updating.value = false
  }
}

watch(() => [props.modelValue, props.provider.id] as const, ([visible]) => {
  if (visible) { tab.value = 'overview'; void load() }
  else { controller?.abort(); generation++ }
}, { immediate: true })
onBeforeUnmount(() => { controller?.abort(); generation++ })

function statusTone(status?: string | null): 'success' | 'warning' | 'danger' | 'muted' {
  if (status === 'ready' || status === 'healthy' || status === 'active' || status === 'available') return 'success'
  if (status === 'cooling' || status === 'warning' || status === 'degraded' || status === 'suspended') return 'warning'
  if (status === 'auth_failed' || status === 'unreachable' || status === 'down' || status === 'unavailable') return 'danger'
  return 'muted'
}
</script>

<template>
  <AppSheet v-model="open" presentation="sheet" :title="providerName(provider)">
    <div class="provider-detail">
      <!-- 名称已在 AppSheet 顶栏标题；这里只留 code/catalog，避免「OpenRouter / OpenRouter」双标题。 -->
      <div class="provider-detail__head">
        <div><small>{{ detail?.code || provider.code }} · {{ detail?.catalog_code || provider.catalog_code }}</small></div>
        <div class="provider-detail__head-actions">
          <button v-if="canManage" type="button" class="btn" :disabled="updating" @click="toggleManualDisabled">{{ detail?.manual_disabled ? t('providers.releaseManualDisable') : t('providers.manualDisable') }}</button>
          <button v-if="canManage" type="button" class="btn" :disabled="updating" @click="toggleEnabled">{{ detail?.enabled ? t('providers.disable') : t('providers.enable') }}</button>
        </div>
      </div>
      <!-- ⚠️ 2026-10-11：这三张小卡的标题必须用 **不带计数** 的 tab.* 键。
           providers.credentials / providers.models 的文案是「{n} 个凭据」
           （计数内嵌在文案里，给列表页那种整句用的），而这里 t() 没传参，
           于是 {n} 原样显示成「{n} 个凭据」—— iOS 26 模拟器真机截图实测。
           数字由下面的 <strong> 单独呈现，所以标题要的是纯名词。 -->
      <div v-if="detail" class="provider-detail__kpis">
        <article><small>{{ t('providers.tab.credentials') }}</small><strong>{{ detail.active_cred_count ?? credentials.length }}</strong></article>
        <article><small>{{ t('providers.tab.models') }}</small><strong>{{ detail.available_model_count ?? models.filter((m) => m.available).length }}</strong></article>
        <article><small>{{ t('providers.errorRate') }}</small><strong>{{ detail.error_rate_24h == null ? '—' : `${(detail.error_rate_24h * 100).toFixed(1)}%` }}</strong></article>
      </div>
      <div class="provider-detail__tabs" role="tablist">
        <button v-for="item in (['overview','credentials','models'] as const)" :key="item" type="button" role="tab" :aria-selected="tab === item" :class="{ active: tab === item }" @click="tab = item">{{ t(`providers.tab.${item}`) }}<span v-if="item === 'credentials'"> {{ credentials.length }}</span><span v-if="item === 'models'"> {{ models.length }}</span></button>
      </div>
      <p v-if="error" class="provider-detail__error" role="alert">{{ error }}</p>
      <AppStateView v-if="loading && !detail" :loading="true" />

      <template v-if="detail && !loading">
        <section v-if="tab === 'overview'" class="provider-detail__section">
          <div class="provider-detail__field"><span>{{ t('providers.protocol') }}</span><strong>{{ detail.protocol || '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.category') }}</span><strong>{{ detail.category || detail.kind || '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.catalog') }}</span><strong>{{ detail.catalog_code || '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.headerProfile') }}</span><strong>{{ detail.header_profile_code || '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.baseUrl') }}</span><code>{{ detail.base_url || '—' }}</code></div>
          <div class="provider-detail__field"><span>{{ t('providers.status') }}</span><span class="badge" :class="detail.manual_disabled || !detail.enabled ? 'badge--muted' : detail.routability === 'unavailable' ? 'badge--danger' : 'badge--success'">{{ detail.manual_disabled ? t('providers.manualDisabled') : detail.enabled ? t('providers.enabled') : t('providers.disabled') }}</span></div>
          <div class="provider-detail__field"><span>{{ t('providers.healthyCredentials') }}</span><strong>{{ detail.healthy_cred_count ?? '—' }} / {{ detail.active_cred_count ?? '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.coolingCredentials') }}</span><strong>{{ detail.cooling_cred_count ?? '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.warningCredentials') }}</span><strong>{{ detail.warning_cred_count ?? '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.unreachableCredentials') }}</span><strong>{{ detail.unreachable_cred_count ?? '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.unavailableModels') }}</span><strong>{{ detail.unavailable_model_count ?? '—' }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.discount') }}</span><strong>{{ detail.discount_rate == null ? '—' : `${(detail.discount_rate * 100).toFixed(2)}%` }}</strong></div>
          <div class="provider-detail__field"><span>{{ t('providers.lastChecked') }}</span><strong>{{ detail.health_checked_at ? new Date(detail.health_checked_at).toLocaleString() : '—' }}</strong></div>
          <div v-if="detail.notes" class="provider-detail__field"><span>{{ t('providers.notes') }}</span><strong>{{ detail.notes }}</strong></div>
          <button type="button" class="btn provider-detail__audit" @click="auditOpen = true">{{ t('providers.audit') }}</button>
        </section>

        <section v-else-if="tab === 'credentials'" class="provider-detail__list">
          <p v-if="!credentials.length" class="provider-detail__empty">{{ t('providers.noCredentials') }}</p>
          <article v-for="credential in credentials" :key="credential.id" class="provider-detail__card">
            <header><span><StatusDot :tone="statusTone(credential.availability_state || credential.health_status || credential.status)" /> <strong>{{ credential.label || `#${credential.id}` }}</strong></span><span class="badge" :class="credential.manual_disabled ? 'badge--muted' : statusTone(credential.availability_state || credential.health_status) === 'success' ? 'badge--success' : statusTone(credential.availability_state || credential.health_status) === 'danger' ? 'badge--danger' : 'badge--warning'">{{ credential.manual_disabled ? t('providers.manualDisabled') : credential.availability_state || credential.health_status || credential.status }}</span></header>
            <p v-if="credential.state_reason_detail || credential.health_error" class="provider-detail__reason">{{ credential.state_reason_detail || credential.health_error }}</p>
            <div class="provider-detail__metrics"><span>{{ t('keys.requests') }} <b>{{ fmtInt(credential.total_requests ?? 0) }}</b></span><span>{{ t('keys.used') }} <b>{{ fmtUsd(Number(credential.total_cost_usd ?? 0)) }}</b></span><span v-if="credential.balance_usd != null">{{ t('providers.balance') }} <b>{{ fmtUsd(Number(credential.balance_usd)) }}</b></span></div>
          </article>
        </section>

        <section v-else class="provider-detail__list">
          <p v-if="!models.length" class="provider-detail__empty">{{ t('providers.noModels') }}</p>
          <article v-for="model in models" :key="model.id" class="provider-detail__card">
            <header><span class="provider-detail__model"><strong>{{ model.display_name || model.canonical_name || model.raw_model_name }}</strong><code>{{ model.raw_model_name }}</code></span><span class="badge" :class="model.available ? 'badge--success' : 'badge--danger'">{{ model.available ? t('providers.available') : t('providers.unavailable') }}</span></header>
            <p class="provider-detail__reason">{{ model.credential_label }} · {{ model.routing_tier }}<span v-if="model.unavailable_reason"> · {{ model.unavailable_reason }}</span></p>
            <div class="provider-detail__metrics"><span>{{ t('nodes.successRate') }} <b>{{ model.success_rate == null ? '—' : `${(model.success_rate * 100).toFixed(1)}%` }}</b></span><span>{{ t('nodes.latency') }} <b>{{ model.p95_latency_ms == null ? '—' : `${fmtInt(model.p95_latency_ms)}ms` }}</b></span><span>{{ t('providers.inputPrice') }} <b>{{ model.input_price == null ? '—' : fmtUsd(model.input_price) }}</b></span><span>{{ t('providers.outputPrice') }} <b>{{ model.output_price == null ? '—' : fmtUsd(model.output_price) }}</b></span></div>
          </article>
        </section>
      </template>
      <NodeAuditSheet v-if="auditOpen" :provider-id="provider.id" :provider-name="providerName(provider)" @close="auditOpen = false" />
    </div>
  </AppSheet>
</template>

<style scoped>
.provider-detail { display: grid; gap: 12px; padding-bottom: var(--app-safe-bottom); }
.provider-detail__head, .provider-detail__head div, .provider-detail__kpis, .provider-detail__card header, .provider-detail__card header>span, .provider-detail__metrics, .provider-detail__field, .provider-detail__tabs, .provider-detail__head-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-width: 0; }
.provider-detail__head-actions { flex-wrap: wrap; justify-content: flex-end; }
.provider-detail__head div { align-items: flex-start; flex-direction: column; }
.provider-detail__head small, .provider-detail__field>span, .provider-detail__metrics { color: var(--app-text-muted); font-size: 12px; }
.provider-detail__kpis article { display: grid; flex: 1; min-width: 0; gap: 4px; padding: 10px; border: 1px solid var(--app-border); border-radius: 10px; }
.provider-detail__kpis small { color: var(--app-text-muted); font-size: 11px; }
.provider-detail__kpis strong { font-size: 16px; }
.provider-detail__tabs { overflow-x: auto; justify-content: flex-start; }
.provider-detail__tabs button { flex: 0 0 auto; min-height: 48px; padding: 0 12px; border: 0; border-bottom: 2px solid transparent; background: none; color: var(--app-text-muted); white-space: nowrap; }
.provider-detail__tabs button.active { border-color: var(--app-primary); color: var(--app-primary); }
.provider-detail__section, .provider-detail__list { display: grid; gap: 8px; }
.provider-detail__field { align-items: flex-start; padding: 10px 0; border-bottom: 1px solid var(--app-border); }
.provider-detail__field code { max-width: 65%; overflow-wrap: anywhere; text-align: right; }
.provider-detail__card { display: grid; gap: 8px; padding: 12px; border: 1px solid var(--app-border); border-radius: 12px; background: var(--app-surface); }
.provider-detail__card header>span:first-child { justify-content: flex-start; }
.provider-detail__card header strong, .provider-detail__model { overflow-wrap: anywhere; }
.provider-detail__model { display: grid; gap: 4px; }
.provider-detail__model code { color: var(--app-text-muted); font-size: 11px; }
.provider-detail__reason { margin: 0; color: var(--app-text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.provider-detail__metrics { justify-content: flex-start; flex-wrap: wrap; gap: 12px; }
.provider-detail__metrics b { margin-left: 4px; color: var(--app-text); }
.provider-detail__audit { min-height: 48px; }
.provider-detail__error { color: var(--app-danger); overflow-wrap: anywhere; }
.provider-detail__empty { color: var(--app-text-muted); text-align: center; }
</style>
