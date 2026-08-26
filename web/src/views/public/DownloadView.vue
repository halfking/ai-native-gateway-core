<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import PublicPortalLayout from '../../components/PublicPortalLayout.vue'
import PublicContactBox from '../../components/PublicContactBox.vue'
import OperationAgreementDialog from '../../components/OperationAgreementDialog.vue'
import {
  getDownloadCatalog,
  createDownloadTicket,
  recordDownloadEvent,
  type DownloadCatalog,
  type CatalogItem,
  type VersionGroup,
} from '../../api/public'

const { t } = useI18n()
const router = useRouter()

const AGREEMENT_VERSION = '2026-07-15'
const AGREEMENT_STORAGE_KEY = `llmgw_op_agreement_download_${AGREEMENT_VERSION}`

const catalog = ref<DownloadCatalog | null>(null)
const loading = ref(false)
const downloading = ref<string | null>(null)
const showAgreement = ref(false)
// 等待门控弹窗同意后再执行的下载闭包
const pendingDownload = ref<null | (() => void)>(null)

const versionGroups = computed<VersionGroup[]>(() => {
  if (!catalog.value) return []
  if (catalog.value.versions?.length) return catalog.value.versions
  return [{
    version: catalog.value.version,
    build_seq: catalog.value.build_seq,
    release_date: catalog.value.release_date,
    items: catalog.value.items,
    install_doc_url: catalog.value.docs_url,
  }]
})

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

function itemKey(version: string, item: CatalogItem) {
  return `${version}-${item.platform}-${item.arch}`
}

function hasAgreedDownload(): boolean {
  try {
    return !!localStorage.getItem(AGREEMENT_STORAGE_KEY)
  } catch {
    return false
  }
}

function requestDownloadAgreementThen(run: () => void) {
  if (hasAgreedDownload()) {
    run()
    return
  }
  pendingDownload.value = run
  showAgreement.value = true
}

function onAgreementAgreed() {
  const run = pendingDownload.value
  pendingDownload.value = null
  if (run) run()
}

// 用户取消协议弹窗（点 X / Esc）→ 丢弃挂起的下载闭包，
// 否则下次点其它版本下载按钮时仍可能执行旧 action。
function onAgreementCancelled() {
  pendingDownload.value = null
}

async function performDownload(group: VersionGroup, item: CatalogItem) {
  const key = itemKey(group.version, item)
  downloading.value = key
  const started = Date.now()
  try {
    const ticket = await createDownloadTicket({
      version: group.version,
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

function handleDownload(group: VersionGroup, item: CatalogItem) {
  requestDownloadAgreementThen(() => performDownload(group, item))
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

      <p v-if="catalog" class="dl-supporters">
        {{ catalog.supporters }} {{ t('public.download.supporters') }}
      </p>

      <el-card v-if="catalog" shadow="never" class="dl-meta">
        <div class="dl-meta__row">
          <span>{{ t('public.download.version') }}: <strong>{{ catalog.version }}</strong> (build {{ catalog.build_seq }})</span>
          <span>{{ catalog.supporters }} {{ t('public.download.supporters') }}</span>
        </div>
        <p class="dl-hint">{{ t('public.download.platformHint') }}</p>
      </el-card>

      <section
        v-for="group in versionGroups"
        :key="group.version"
        class="dl-version"
      >
        <el-card shadow="never" class="pub-card dl-version__head">
          <div class="dl-version__row">
            <div>
              <h2>{{ group.version }}</h2>
              <p class="dl-version__meta">
                build {{ group.build_seq || '—' }}
                <span v-if="group.release_date"> · {{ group.release_date }}</span>
              </p>
            </div>
            <a
              v-if="group.install_doc_url"
              :href="group.install_doc_url"
              target="_blank"
              rel="noopener"
              class="dl-doc-link"
            >
              {{ t('public.download.installDoc') }}
            </a>
          </div>
          <p class="dl-hint">{{ t('public.download.platformHint') }}</p>
        </el-card>

        <div class="dl-grid">
          <el-card
            v-for="item in group.items"
            :key="itemKey(group.version, item)"
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
                :loading="downloading === itemKey(group.version, item)"
                @click="handleDownload(group, item)"
              >
                {{ t('public.download.downloadBtn') }}
              </el-button>
              <el-button link @click="copyText(item.artifact_name)">{{ t('public.download.copyLink') }}</el-button>
            </div>
          </el-card>
        </div>
      </section>

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

    <OperationAgreementDialog
      v-model="showAgreement"
      scope="download"
      :version="AGREEMENT_VERSION"
      @agreed="onAgreementAgreed"
      @cancelled="onAgreementCancelled"
    />
  </PublicPortalLayout>
</template>

<style scoped>
.dl-supporters { color: #94a3b8; margin: 0 0 1rem; }
.dl-version { margin-bottom: 1.5rem; }
.dl-version__head { margin-bottom: 0.75rem; }
.dl-version__row { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; flex-wrap: wrap; }
.dl-version h2 { margin: 0; font-size: 1.25rem; }
.dl-version__meta { margin: 4px 0 0; color: #94a3b8; font-size: 0.875rem; }
.dl-doc-link { color: var(--accent-h); font-weight: 500; text-decoration: none; white-space: nowrap; }
.dl-doc-link:hover { text-decoration: underline; }
.dl-meta { margin-bottom: 1.5rem; }
.dl-meta__row { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; }
.dl-hint { margin: 0.75rem 0 0; color: #94a3b8; font-size: 0.875rem; }
.dl-grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); }
.dl-card__icon { font-size: 28px; margin-bottom: 8px; }
.dl-card h3 { margin: 0 0 0.5rem; font-size: 1rem; }
.dl-file { font-family: ui-monospace, monospace; font-size: 0.8rem; color: #94a3b8; word-break: break-all; }
.dl-size, .dl-sha { font-size: 0.8rem; color: #64748b; }
.dl-extra h3 { margin: 0 0 0.75rem; font-size: 1rem; }
</style>
