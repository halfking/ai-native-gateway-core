<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getProviderDetail, getProviderCredentials, diagnoseProvider, toggleProvider, setProviderManualDisabled, type ProviderCredential, type DiagnoseProviderResponse, getProviderRecentProbeFailures } from '../api'
import OverviewCards from './provider-detail/OverviewCards.vue'
import CredsTab from './provider-detail/CredsTab.vue'
import ModelsTab from './provider-detail/ModelsTab.vue'
import LogsTab from './provider-detail/LogsTab.vue'
import DiagTab from './provider-detail/DiagTab.vue'
import SettingsTab from './provider-detail/SettingsTab.vue'
import ProbeHistoryTab from './provider-detail/ProbeHistoryTab.vue'

const route = useRoute()
const router = useRouter()
const { t: td } = useI18n()
const pp = (k: string, params?: Record<string, unknown>): string => td(`providerDetailPage.${k}` as never, params as never)

const providerId = computed(() => Number(route.params.id))

const provider = ref<any>(null)
const creds = ref<ProviderCredential[]>([])
const loading = ref(false)
const error = ref('')
const tab = ref('creds')
const probeFailureCount = ref(0)

const diagLoading = ref(false)
const diagResult = ref<DiagnoseProviderResponse | null>(null)
const diagError = ref('')

const modelsFocusOffer = ref<{ credential_id: number; raw_model_name: string } | null>(null)

function onOpenModelsTab(payload: { credential_id: number; raw_model_name: string }) {
  modelsFocusOffer.value = payload
  tab.value = 'models'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [providerData, credsData, failuresData] = await Promise.all([
      getProviderDetail(providerId.value),
      getProviderCredentials(providerId.value),
      getProviderRecentProbeFailures(providerId.value).catch(() => ({ models: [] }))
    ])

    provider.value = providerData
    creds.value = credsData
    probeFailureCount.value = failuresData.models.reduce((sum, m) => sum + m.failed_count, 0)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pp('loadFailed')
  } finally {
    loading.value = false
  }
}

// 2026-07-03: background refresh used by inline drawer edits (plan type
// select, immediate probe check, manual disable toggle, lifecycle
// change, default probe model pick). Unlike `load()` it MUST NOT set
// `loading=true` — that flag drives the `<template v-if="provider &&
// !loading">` wrapper that mounts the tab bodies, so flipping it would
// unmount CredsTab mid-edit and silently destroy the open drawer.
// We only need to refresh the creds list so badges in the table stay
// consistent; the drawer reads its own `selected` ref.
async function refreshCredsSilent() {
  try {
    const credsData = await getProviderCredentials(providerId.value)
    creds.value = credsData
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pp('loadFailed')
  }
}

async function toggle() {
  if (!provider.value) return
  try {
    await toggleProvider(provider.value.id)
    provider.value.enabled = !provider.value.enabled
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pp('operationFailed')
  }
}

async function toggleProviderManual() {
  if (!provider.value) return
  const next = !provider.value.manual_disabled
  const action = next ? 'disable' : 'enable'
  const raw = prompt(pp(`manualToggle.${action}Prompt`, { name: provider.value.display_name }), '')
  if (raw === null) return
  const reason = raw.trim()
  try {
    await setProviderManualDisabled(provider.value.id, next, reason)
    provider.value.manual_disabled = next
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pp('operationFailed')
  }
}

async function runDiagnose() {
  diagLoading.value = true
  diagError.value = ''
  diagResult.value = null
  try {
    diagResult.value = await diagnoseProvider(providerId.value, { force: true }) as never
  } catch (e: unknown) {
    diagError.value = e instanceof Error ? e.message : pp('diagFailed')
  } finally {
    diagLoading.value = false
  }
}

function back() { router.push('/providers') }

onMounted(load)
watch(providerId, () => {
  if (!Number.isNaN(providerId.value)) {
    load()
  }
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center">
      <div style="display:flex;align-items:center;gap:12px">
        <button class="btn btn-ghost" @click="back">{{ pp('back') }}</button>
        <h2 style="margin:0">{{ provider?.display_name || '...' }}</h2>
        <span v-if="provider?.manual_disabled" class="badge badge-red" :title="pp('manualDisabledTitle')">🔒 {{ pp('manualDisabledBadge') }}</span>
        <span v-else-if="!provider?.enabled" class="badge badge-gray">{{ pp('disabledBadge') }}</span>
      </div>
      <div style="display:flex;gap:8px">
        <button
          class="btn btn-ghost btn-sm"
          :style="provider?.manual_disabled ? 'color:var(--danger);border-color:var(--danger)' : ''"
          @click="toggleProviderManual"
          :title="provider?.manual_disabled ? pp('manualToggle.releaseTitle') : pp('manualToggle.setTitle')"
        >{{ provider?.manual_disabled ? pp('manualToggle.release') : pp('manualToggle.set') }}</button>
        <button class="btn btn-ghost btn-sm" @click="toggle">{{ provider?.enabled ? pp('disable') : pp('enable') }}</button>
        <button class="btn btn-ghost btn-sm" @click="load">{{ pp('refresh') }}</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">{{ pp('loading') }}</div>

    <template v-if="provider && !loading">
      <OverviewCards :provider="provider" />

      <div class="tabs">
        <button type="button" class="tab-btn" :class="{ active: tab === 'creds' }" @click="tab = 'creds'">{{ pp('tabCreds', { n: creds.length }) }}</button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'models' }" @click="tab = 'models'">{{ pp('tabModels') }}</button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'logs' }" @click="tab = 'logs'">{{ pp('tabLogs') }}</button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'diag' }" @click="tab = 'diag'">{{ pp('tabDiag') }}</button>
        <button
          type="button"
          class="tab-btn"
          :class="{ active: tab === 'probe' }"
          @click="tab = 'probe'"
          :title="pp('tabProbeTitle')"
        >
          {{ pp('tabProbe') }}
          <span v-if="probeFailureCount > 0" class="tab-badge tab-badge-red">{{ probeFailureCount }}</span>
        </button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'settings' }" @click="tab = 'settings'">{{ pp('tabSettings') }}</button>
      </div>

      <!-- 2026-07-03: `@silent-refresh` lets inline drawer edits (plan
           type select, 立即检测) refresh the creds list without
           flipping `loading=true`, which would unmount CredsTab and
           close the drawer mid-edit. -->
      <CredsTab
        v-if="tab==='creds'"
        :provider="provider"
        :creds="creds"
        @refresh="load"
        @silent-refresh="refreshCredsSilent"
      />
      <ModelsTab
        v-if="tab==='models'"
        :provider-id="providerId"
        :focus-offer="modelsFocusOffer"
      />
      <LogsTab v-if="tab==='logs'" :provider-id="providerId" />
      <DiagTab v-if="tab==='diag'" :provider-id="providerId" />
      <ProbeHistoryTab v-if="tab==='probe'" :provider-id="providerId" @open-models-tab="onOpenModelsTab" />
      <SettingsTab v-if="tab==='settings'" :provider="provider" @refresh="load" />
    </template>
  </div>
</template>
