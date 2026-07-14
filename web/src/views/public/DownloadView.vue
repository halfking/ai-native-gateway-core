<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import PublicPortalLayout from '../../components/PublicPortalLayout.vue'
import {
  getDownloadCatalog,
  createDownloadTicket,
  recordDownloadEvent,
  type DownloadCatalog,
  type CatalogItem,
} from '../../api/public'

const { t } = useI18n()
const router = useRouter()

const catalog = ref<DownloadCatalog | null>(null)
const loading = ref(false)
const downloading = ref<string | null>(null)

async function load() {
  loading.value = true
  try {
    catalog.value = await getDownloadCatalog()
  } catch {
    ElMessage.error(t('public.download.loadFailed'))
  } finally {
    loading.value = false
  }
}

function itemKey(item: CatalogItem) {
  return `${item.platform}-${item.arch}`
}

async function handleDownload(item: CatalogItem) {
  const key = itemKey(item)
  downloading.value = key
  const started = Date.now()
  try {
    const ticket = await createDownloadTicket({
      version: catalog.value?.version,
      platform: item.platform,
      arch: item.arch,
    })
    window.open(ticket.url, '_blank', 'noopener')
    ElMessage.success(t('public.download.started'))
    setTimeout(() => {
      recordDownloadEvent({
        request_id: ticket.request_id,
        result: 'completed',
        duration_ms: Date.now() - started,
      }).catch(() => {})
    }, 3000)
  } catch {
    ElMessage.error(t('public.download.ticketFailed'))
  } finally {
    downloading.value = null
  }
}

async function copyArtifactName(item: CatalogItem) {
  try {
    await navigator.clipboard.writeText(item.artifact_name)
    ElMessage.success(t('common.copied', '已复制'))
  } catch {
    ElMessage.warning(item.artifact_name)
  }
}

onMounted(load)
</script>

<template>
  <PublicPortalLayout>
    <div v-loading="loading" class="dl-page">
      <h1>{{ t('public.download.title') }}</h1>
      <p class="dl-sub">{{ t('public.download.subtitle') }}</p>

      <el-card v-if="catalog" shadow="never" class="dl-meta">
        <div class="dl-meta__row">
          <span>{{ t('public.download.version') }}: <strong>{{ catalog.version }}</strong> (build {{ catalog.build_seq }})</span>
          <span>{{ catalog.supporters }} {{ t('public.download.supporters') }}</span>
        </div>
        <p class="dl-hint">{{ t('public.download.platformHint') }}</p>
      </el-card>

      <div v-if="catalog" class="dl-grid">
        <el-card
          v-for="item in catalog.items"
          :key="itemKey(item)"
          shadow="hover"
          class="dl-card"
        >
          <h3>{{ item.label }}</h3>
          <p class="dl-file">{{ item.artifact_name }}</p>
          <p v-if="item.size_label" class="dl-size">{{ item.size_label }}</p>
          <p v-if="item.sha256" class="dl-sha">{{ t('public.download.sha256') }}: <code>{{ item.sha256.slice(0, 16) }}…</code></p>
          <div class="dl-actions">
            <el-button
              type="primary"
              :loading="downloading === itemKey(item)"
              @click="handleDownload(item)"
            >
              {{ t('public.download.downloadBtn') }}
            </el-button>
            <el-button link @click="copyArtifactName(item)">{{ t('public.download.copyLink') }}</el-button>
          </div>
        </el-card>
      </div>

      <el-card shadow="never" class="dl-extra">
        <h3>{{ t('public.download.offlineTools') }}</h3>
        <el-button link type="primary" @click="router.push('/offline-activation')">
          {{ t('public.download.offlineLink') }}
        </el-button>
        <el-button link type="primary" @click="router.push('/support')">
          {{ t('public.download.supportUs') }}
        </el-button>
      </el-card>
    </div>
  </PublicPortalLayout>
</template>

<style scoped>
.dl-page h1 { margin: 0 0 0.5rem; font-size: 1.75rem; color: #0f172a; }
.dl-sub { color: #64748b; margin-bottom: 1.5rem; }
.dl-meta { margin-bottom: 1.5rem; }
.dl-meta__row { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; }
.dl-hint { margin: 0.75rem 0 0; color: #64748b; font-size: 0.875rem; }
.dl-grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); }
.dl-card h3 { margin: 0 0 0.5rem; font-size: 1rem; }
.dl-file { font-family: monospace; font-size: 0.8rem; color: #475569; word-break: break-all; }
.dl-size, .dl-sha { font-size: 0.8rem; color: #64748b; }
.dl-actions { margin-top: 1rem; display: flex; gap: 0.5rem; flex-wrap: wrap; }
.dl-extra { margin-top: 2rem; }
.dl-extra h3 { margin: 0 0 0.5rem; font-size: 1rem; }
</style>
