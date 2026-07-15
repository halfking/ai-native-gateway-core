<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getReleaseArtifacts,
  getPublishRuns,
  publishDownloadRelease,
  type ReleaseArtifact,
  type PublishRun,
} from '../../api/ops'
import { getDownloadCatalog, getDownloadStats } from '../../api/public'

const { t } = useI18n()

const loading = ref(false)
const publishing = ref(false)
const stats = ref<Awaited<ReturnType<typeof getDownloadStats>> | null>(null)
const catalog = ref<Awaited<ReturnType<typeof getDownloadCatalog>> | null>(null)
const artifacts = ref<ReleaseArtifact[]>([])
const runs = ref<PublishRun[]>([])
const publishVersion = ref('')

function formatArtifactSize(row: ReleaseArtifact) {
  return row.size_bytes ? `${(row.size_bytes / 1024 / 1024).toFixed(1)} MB` : '—'
}

function formatTestPassed(row: PublishRun) {
  return row.test_passed ? '✓' : '—'
}

async function load() {
  loading.value = true
  try {
    const [st, cat, runList] = await Promise.all([
      getDownloadStats(),
      getDownloadCatalog(),
      getPublishRuns(),
    ])
    stats.value = st
    catalog.value = cat
    runs.value = runList.items
    publishVersion.value = cat.version
    const art = await getReleaseArtifacts(cat.version)
    artifacts.value = art.items
  } catch {
    ElMessage.error(t('ops.downloads.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function handlePublish() {
  publishing.value = true
  try {
    const res = await publishDownloadRelease({
      version: publishVersion.value,
      build_seq: catalog.value?.build_seq,
    })
    ElMessage.success(res.summary || t('ops.downloads.publishOk'))
    await load()
  } catch (e) {
    ElMessage.error((e as Error).message || t('ops.downloads.publishFailed'))
  } finally {
    publishing.value = false
  }
}

onMounted(load)
</script>

<template>
  <div v-loading="loading" class="ops-downloads">
    <header class="ops-downloads__head">
      <div>
        <h2>{{ t('ops.downloads.title') }}</h2>
        <p>{{ t('ops.downloads.subtitle') }}</p>
      </div>
      <el-button type="primary" :loading="publishing" @click="handlePublish">
        {{ t('ops.downloads.publishBtn') }}
      </el-button>
    </header>

    <el-row :gutter="16" class="ops-downloads__stats">
      <el-col :span="6"><el-statistic :title="t('ops.downloads.todayDl')" :value="stats?.today_downloads ?? 0" /></el-col>
      <el-col :span="6"><el-statistic :title="t('ops.downloads.totalDl')" :value="stats?.total_downloads ?? 0" /></el-col>
      <el-col :span="6"><el-statistic :title="t('ops.downloads.supporters')" :value="stats?.supporter_count ?? 0" /></el-col>
      <el-col :span="6"><el-statistic :title="t('ops.downloads.activationRate')" :value="stats?.activation_rate_pct ?? 0" suffix="%" /></el-col>
    </el-row>

    <el-card shadow="never" class="ops-downloads__card">
      <template #header>{{ t('ops.downloads.currentVersion') }}</template>
      <el-form inline>
        <el-form-item :label="t('ops.downloads.version')">
          <el-input v-model="publishVersion" style="width: 220px" />
        </el-form-item>
        <el-form-item v-if="catalog" :label="t('ops.downloads.buildSeq')">
          <span>{{ catalog.build_seq }}</span>
        </el-form-item>
        <el-form-item v-if="catalog?.git_repo_url" :label="t('ops.downloads.gitRepo')">
          <a :href="catalog.git_repo_url" target="_blank" rel="noopener">{{ catalog.git_repo_url }}</a>
        </el-form-item>
      </el-form>
      <p class="ops-downloads__hint">{{ t('ops.downloads.storageHint') }}</p>
      <div class="ops-downloads__links">
        <el-button link type="primary" @click="$router.push('/download')">{{ t('ops.downloads.openPublicDownload') }}</el-button>
        <el-button link type="primary" @click="$router.push('/activate')">{{ t('ops.downloads.openActivate') }}</el-button>
      </div>
    </el-card>

    <el-card shadow="never" class="ops-downloads__card">
      <template #header>{{ t('ops.downloads.artifacts') }}</template>
      <el-table :data="artifacts" size="small" stripe>
        <el-table-column prop="platform" :label="t('ops.downloads.platform')" width="100" />
        <el-table-column prop="arch" label="arch" width="90" />
        <el-table-column prop="artifact_name" :label="t('ops.downloads.file')" min-width="220" />
        <el-table-column prop="size_bytes" :label="t('ops.downloads.size')" width="100" :formatter="formatArtifactSize" />
        <el-table-column prop="sha256" label="SHA256" min-width="140">
          <template #default="scope">
            <code v-if="scope?.row?.sha256">{{ scope.row.sha256.slice(0, 12) }}…</code>
            <span v-else>—</span>
          </template>
        </el-table-column>
        <el-table-column prop="download_path" :label="t('ops.downloads.path')" min-width="160" />
      </el-table>
    </el-card>

    <el-card shadow="never" class="ops-downloads__card">
      <template #header>{{ t('ops.downloads.publishHistory') }}</template>
      <el-table :data="runs" size="small" stripe>
        <el-table-column prop="release_version" :label="t('ops.downloads.version')" width="140" />
        <el-table-column prop="build_seq" label="seq" width="70" />
        <el-table-column prop="status" :label="t('ops.downloads.status')" width="100" />
        <el-table-column prop="artifact_count" :label="t('ops.downloads.artifactCount')" width="90" />
        <el-table-column prop="test_passed" :label="t('ops.downloads.tests')" width="80" :formatter="formatTestPassed" />
        <el-table-column prop="created_by" label="by" width="90" />
        <el-table-column prop="created_at" :label="t('ops.downloads.createdAt')" min-width="160" />
        <el-table-column prop="log_summary" :label="t('ops.downloads.summary')" min-width="200" show-overflow-tooltip />
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.ops-downloads { padding: 8px 0 24px; }
.ops-downloads__head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
}
.ops-downloads__head h2 { margin: 0 0 4px; }
.ops-downloads__head p { margin: 0; color: var(--text-secondary, #8b949e); font-size: 14px; }
.ops-downloads__stats { margin-bottom: 16px; }
.ops-downloads__card { margin-bottom: 16px; }
.ops-downloads__hint {
  margin: 8px 0 0;
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
}
.ops-downloads__links {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 8px;
}
</style>
