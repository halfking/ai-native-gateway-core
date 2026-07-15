<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import PublicPortalLayout from '../../components/PublicPortalLayout.vue'
import PublicContactBox from '../../components/PublicContactBox.vue'
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

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(t('common.copied', '已复制'))
  } catch {
    ElMessage.warning(text)
  }
}

onMounted(load)
</script>

<template>
  <PublicPortalLayout
    :title="t('public.download.title')"
    :subtitle="t('public.download.subtitle')"
    :kicker="t('public.layout.download')"
  >
    <div v-loading="loading" class="dl-page">
      <PublicContactBox />

      <el-card v-if="catalog" shadow="never" class="pub-card dl-meta">
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
          class="pub-card dl-card"
        >
          <div class="dl-card__icon" aria-hidden="true">📦</div>
          <h3>{{ item.label }}</h3>
          <p class="dl-file">{{ item.artifact_name }}</p>
          <p v-if="item.size_label" class="dl-size">{{ item.size_label }}</p>
          <p v-if="item.sha256" class="dl-sha">
            {{ t('public.download.sha256') }}: <code>{{ item.sha256.slice(0, 16) }}…</code>
          </p>
          <div class="pub-actions">
            <el-button
              type="primary"
              :loading="downloading === itemKey(item)"
              @click="handleDownload(item)"
            >
              {{ t('public.download.downloadBtn') }}
            </el-button>
            <el-button link @click="copyText(item.artifact_name)">{{ t('public.download.copyLink') }}</el-button>
          </div>
        </el-card>
      </div>

      <el-card shadow="never" class="pub-card dl-extra">
        <h3>{{ t('public.download.nextSteps') }}</h3>
        <ol class="pub-steps">
          <li>{{ t('public.download.nextStepInstall') }}</li>
          <li>{{ t('public.download.nextStepActivate') }}</li>
        </ol>
        <div class="pub-actions">
          <el-button type="primary" @click="router.push('/activate')">
            {{ t('public.download.activateLink') }}
          </el-button>
          <el-button @click="router.push('/offline-activation')">
            {{ t('public.download.offlineLink') }}
          </el-button>
          <el-button link type="primary" @click="router.push('/support')">
            {{ t('public.download.supportUs') }}
          </el-button>
        </div>
      </el-card>
    </div>
  </PublicPortalLayout>
</template>

<style scoped>
.dl-meta { margin-bottom: 1rem; }
.dl-meta__row { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; }
.dl-hint, .dl-meta-line { margin: 0.75rem 0 0; color: #94a3b8; font-size: 0.875rem; }
.dl-grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); margin-bottom: 1rem; }
.dl-card { position: relative; }
.dl-card__icon { font-size: 28px; margin-bottom: 8px; }
.dl-card h3 { margin: 0 0 0.5rem; font-size: 1rem; }
.dl-file { font-family: ui-monospace, monospace; font-size: 0.8rem; color: #94a3b8; word-break: break-all; }
.dl-size, .dl-sha { font-size: 0.8rem; color: #64748b; }
.dl-extra h3 { margin: 0 0 0.75rem; font-size: 1rem; }
</style>
