<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getProviderDetail, getProviderCredentials, diagnoseProvider, toggleProvider, type ProviderCredential, type DiagnoseProviderResponse } from '../api'
import OverviewCards from './provider-detail/OverviewCards.vue'
import CredsTab from './provider-detail/CredsTab.vue'
import ModelsTab from './provider-detail/ModelsTab.vue'
import LogsTab from './provider-detail/LogsTab.vue'
import DiagTab from './provider-detail/DiagTab.vue'
import SettingsTab from './provider-detail/SettingsTab.vue'

const route = useRoute()
const router = useRouter()
const providerId = Number(route.params.id)

const provider = ref<any>(null)
const creds = ref<ProviderCredential[]>([])
const loading = ref(false)
const error = ref('')
const tab = ref('creds')

const diagLoading = ref(false)
const diagResult = ref<DiagnoseProviderResponse | null>(null)
const diagError = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    provider.value = await getProviderDetail(providerId)
    creds.value = await getProviderCredentials(providerId)
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

async function runDiagnose() {
  diagLoading.value = true
  diagError.value = ''
  diagResult.value = null
  try {
    diagResult.value = await diagnoseProvider(providerId, { force: true })
  } catch (e: unknown) {
    diagError.value = e instanceof Error ? e.message : '诊断失败'
  } finally {
    diagLoading.value = false
  }
}

function back() { router.push('/providers') }

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center">
      <div style="display:flex;align-items:center;gap:12px">
        <button class="btn btn-ghost" @click="back">&larr; 返回</button>
        <h2 style="margin:0">{{ provider?.display_name || '...' }}</h2>
      </div>
      <div style="display:flex;gap:8px">
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
        <button type="button" class="tab-btn" :class="{ active: tab === 'settings' }" @click="tab = 'settings'">设置</button>
      </div>

      <CredsTab v-if="tab==='creds'" :provider="provider" :creds="creds" @refresh="load" />
      <ModelsTab v-if="tab==='models'" :provider-id="providerId" />
      <LogsTab v-if="tab==='logs'" :provider-id="providerId" />
      <DiagTab v-if="tab==='diag'" :provider-id="providerId" />
      <SettingsTab v-if="tab==='settings'" :provider="provider" @refresh="load" />
    </template>
  </div>
</template>
