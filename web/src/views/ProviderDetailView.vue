<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
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
// providerId must be reactive — Vue Router reuses the component when
// navigating between /providers/1 and /providers/2, so a non-reactive
// `Number(route.params.id)` would never update.  Use a computed and a
// watcher to reload on change.
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

// modelsFocusOffer is set when the user clicks the inline "go to Models tab"
// link from a `endpoint_id_required` probe entry.  ModelsTab watches this
// and opens the matching drawer once offers are loaded.
const modelsFocusOffer = ref<{ credential_id: number; raw_model_name: string } | null>(null)

function onOpenModelsTab(payload: { credential_id: number; raw_model_name: string }) {
  modelsFocusOffer.value = payload
  tab.value = 'models'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    provider.value = await getProviderDetail(providerId.value)
    creds.value = await getProviderCredentials(providerId.value)
    // Best-effort: badge count for the "自动测试" tab. Failure here
    // must not block the main load.
    try {
      const failures = await getProviderRecentProbeFailures(providerId.value)
      probeFailureCount.value = failures.models.reduce((sum, m) => sum + m.failed_count, 0)
    } catch {
      probeFailureCount.value = 0
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function toggle() {
  if (!provider.value) return
  try {
    await toggleProvider(provider.value.id)
    provider.value.enabled = !provider.value.enabled
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

// 900-series: provider-level manual disable (spec §6.2)
async function toggleProviderManual() {
  if (!provider.value) return
  const next = !provider.value.manual_disabled
  // prompt() returns null on Cancel.  Check it BEFORE coalescing to ''
  // — using `prompt(...) ?? ''` swallowed the cancel signal.
  const raw = prompt(`手工${next ? '禁用' : '启用'}提供商 ${provider.value.display_name} 的原因：`, '')
  if (raw === null) return
  const reason = raw.trim()
  try {
    await setProviderManualDisabled(provider.value.id, next, reason)
    provider.value.manual_disabled = next
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

async function runDiagnose() {
  diagLoading.value = true
  diagError.value = ''
  diagResult.value = null
  try {
    diagResult.value = await diagnoseProvider(providerId.value, { force: true }) as never
  } catch (e: unknown) {
    diagError.value = e instanceof Error ? e.message : '诊断失败'
  } finally {
    diagLoading.value = false
  }
}

function back() { router.push('/providers') }

onMounted(load)
// Reload when the route changes (e.g. user clicks a different provider
// link without remounting the component).
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
        <button class="btn btn-ghost" @click="back">&larr; 返回</button>
        <h2 style="margin:0">{{ provider?.display_name || '...' }}</h2>
        <span v-if="provider?.manual_disabled" class="badge badge-red" title="提供商级手工禁用 — 整个 provider 不可路由">🔒 手工已禁用</span>
        <span v-else-if="!provider?.enabled" class="badge badge-gray">已禁用</span>
      </div>
      <div style="display:flex;gap:8px">
        <button
          class="btn btn-ghost btn-sm"
          :style="provider?.manual_disabled ? 'color:var(--danger);border-color:var(--danger)' : ''"
          @click="toggleProviderManual"
          :title="provider?.manual_disabled ? '取消手工禁用 (恢复自动)' : '手工禁用整个 provider'"
        >{{ provider?.manual_disabled ? '解除手工禁用' : '手工禁用' }}</button>
        <button class="btn btn-ghost btn-sm" @click="toggle">{{ provider?.enabled ? '禁用' : '启用' }}</button>
        <button class="btn btn-ghost btn-sm" @click="load">刷新</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <template v-if="provider && !loading">
      <OverviewCards :provider="provider" />

      <div class="tabs">
        <button type="button" class="tab-btn" :class="{ active: tab === 'creds' }" @click="tab = 'creds'">凭据 ({{ creds.length }})</button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'models' }" @click="tab = 'models'">模型</button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'logs' }" @click="tab = 'logs'">请求日志</button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'diag' }" @click="tab = 'diag'">诊断</button>
        <button
          type="button"
          class="tab-btn"
          :class="{ active: tab === 'probe' }"
          @click="tab = 'probe'"
          :title="'查看自动测试记录（每 10 分钟对失败绑定重新探测）'"
        >
          自动测试
          <span v-if="probeFailureCount > 0" class="tab-badge tab-badge-red">{{ probeFailureCount }}</span>
        </button>
        <button type="button" class="tab-btn" :class="{ active: tab === 'settings' }" @click="tab = 'settings'">设置</button>
      </div>

      <CredsTab v-if="tab==='creds'" :provider="provider" :creds="creds" @refresh="load" />
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
