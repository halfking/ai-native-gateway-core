<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getReleases,
  createRelease,
  publishRelease,
  rollbackRelease,
  getUpgradeLogs,
  type Release,
  type UpgradeLog,
} from '../../api/ops'

const { t } = useI18n()

const releases = ref<Release[]>([])
const upgradeLogs = ref<UpgradeLog[]>([])
const loading = ref(false)
const selectedReleaseVersion = ref<string | null>(null)

// Create dialog state
const showCreateDialog = ref(false)
const createForm = ref({
  version: '',
  build_seq: 1,
  channel: 'stable' as 'stable' | 'beta' | 'canary',
  title: '',
  description: '',
  image_tag: '',
  image_digest: '',
  changelog: '',
})

// Publish dialog state
const showPublishDialog = ref(false)
const publishForm = ref({
  version: '',
  rolloutPercentage: 100,
})

async function load() {
  loading.value = true
  try {
    releases.value = await getReleases()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function loadUpgradeLogs(_releaseId?: number) {
  try {
    // 后端按 instance_id 过滤；releaseId 暂时忽略
    upgradeLogs.value = await getUpgradeLogs()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.loadLogsFailed'))
    console.error(error)
  }
}

async function handleCreate() {
  if (!createForm.value.version || !createForm.value.image_tag || !createForm.value.title) {
    ElMessage.warning(t('ops.autoupdate.fillRequired'))
    return
  }

  loading.value = true
  try {
    await createRelease({
      version: createForm.value.version,
      build_seq: createForm.value.build_seq,
      channel: createForm.value.channel,
      title: createForm.value.title,
      description: createForm.value.description,
      image_tag: createForm.value.image_tag,
      image_digest: createForm.value.image_digest,
      changelog: createForm.value.changelog,
      created_by: 'admin',
    })
    ElMessage.success(t('ops.autoupdate.createSuccess'))
    showCreateDialog.value = false
    createForm.value = {
      version: '',
      build_seq: 1,
      channel: 'stable',
      title: '',
      description: '',
      image_tag: '',
      image_digest: '',
      changelog: '',
    }
    await load()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.createFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function openPublishDialog(release: Release) {
  publishForm.value.version = release.version
  publishForm.value.rolloutPercentage = release.rollout_percentage || 100
  showPublishDialog.value = true
}

async function handlePublish() {
  loading.value = true
  try {
    await publishRelease(publishForm.value.version, publishForm.value.rolloutPercentage)
    ElMessage.success(t('ops.autoupdate.publishSuccess'))
    showPublishDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.publishFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleRollback(release: Release) {
  try {
    await ElMessageBox.confirm(
      t('ops.autoupdate.rollbackConfirm', { version: release.version }),
      t('common.warning'),
      { type: 'warning' }
    )
    await rollbackRelease(release.version)
    ElMessage.success(t('ops.autoupdate.rollbackSuccess'))
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.autoupdate.rollbackFailed'))
      console.error(error)
    }
  }
}

async function viewLogs(release: Release) {
  selectedReleaseVersion.value = release.version
  await loadUpgradeLogs()
}

function channelType(channel: string) {
  // 后端是 stable/beta/canary；前端 alpha 兼容
  const map: Record<string, 'success' | 'warning' | 'info'> = {
    stable: 'success',
    beta: 'warning',
    canary: 'info',
    alpha: 'info', // legacy alias
  }
  return map[channel] || 'info'
}

function channelLabel(channel: string) {
  const alias: Record<string, string> = { canary: 'alpha' }
  const key = `ops.autoupdate.channel.${alias[channel] || channel}`
  return t(key)
}

function statusType(status: string) {
  const map: Record<string, 'info' | 'success' | 'warning'> = {
    draft: 'info',
    published: 'success',
    archived: 'warning',
  }
  return map[status] || 'info'
}

function logStatusType(status: string) {
  const map: Record<string, 'info' | 'warning' | 'success' | 'danger'> = {
    pending: 'info',
    downloading: 'warning',
    ready_to_restart: 'warning',
    upgrading: 'warning',
    success: 'success',
    failed: 'danger',
    rolled_back: 'info',
  }
  return map[status] || 'info'
}

function formatDate(date?: string) {
  if (!date) return '—'
  return new Date(date).toLocaleString()
}

onMounted(() => {
  load()
  loadUpgradeLogs()
})
</script>

<template>
  <div class="autoupdate-view">
    <div class="page-header">
      <h1>🚀 {{ t('ops.autoupdate.title') }}</h1>
      <el-button type="primary" @click="showCreateDialog = true">
        + {{ t('ops.autoupdate.createRelease') }}
      </el-button>
    </div>

    <!-- Releases Table -->
    <el-card class="main-card" shadow="never">
      <template #header>
        <span>{{ t('ops.autoupdate.releases') }}</span>
      </template>
      <el-table v-loading="loading" :data="releases">
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="150" />
        <el-table-column prop="channel" :label="t('ops.autoupdate.channelLabel')" width="100">
          <template #default="{ row }">
            <el-tag :type="channelType(row.channel)" size="small">
              {{ channelLabel(row.channel) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.autoupdate.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="rollout_percentage" :label="t('ops.autoupdate.rolloutPercentage')" width="120">
          <template #default="{ row }">
            <el-progress :percentage="row.rollout_percentage" :stroke-width="8" />
          </template>
        </el-table-column>
        <el-table-column prop="published_at" :label="t('ops.autoupdate.publishedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.published_at) }}</template>
        </el-table-column>
        <el-table-column prop="release_notes" :label="t('ops.autoupdate.releaseNotes')" min-width="200" show-overflow-tooltip />
        <el-table-column :label="t('common.actions')" width="280" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'draft'"
              type="success"
              size="small"
              @click="openPublishDialog(row)"
            >
              {{ t('ops.autoupdate.publish') }}
            </el-button>
            <el-button
              v-if="row.status === 'published'"
              type="warning"
              size="small"
              @click="handleRollback(row)"
            >
              {{ t('ops.autoupdate.rollback') }}
            </el-button>
            <el-button type="primary" size="small" @click="viewLogs(row)">
              {{ t('ops.autoupdate.viewLogs') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Upgrade Logs -->
    <el-card class="logs-card" shadow="never">
      <template #header>
        <div class="card-header">
          <span>{{ t('ops.autoupdate.upgradeLogs') }}</span>
          <el-button v-if="selectedReleaseVersion" size="small" @click="selectedReleaseVersion = null; loadUpgradeLogs()">
            {{ t('ops.autoupdate.showAll') }}
          </el-button>
        </div>
      </template>
      <el-table :data="upgradeLogs" size="small">
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="200" />
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="120" />
        <el-table-column prop="status" :label="t('common.status')" width="120">
          <template #default="{ row }">
            <el-tag :type="logStatusType(row.status)" size="small">
              {{ row.status || '—' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="started_at" :label="t('ops.autoupdate.startedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.started_at) }}</template>
        </el-table-column>
        <el-table-column prop="completed_at" :label="t('ops.autoupdate.completedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.completed_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('ops.autoupdate.errorMessage')" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">{{ row.error_message || row.error || '—' }}</template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Create Release Dialog -->
    <el-dialog
      v-model="showCreateDialog"
      :title="t('ops.autoupdate.createReleaseTitle')"
      width="600px"
    >
      <el-form :model="createForm" label-width="140px">
        <el-form-item :label="t('ops.autoupdate.version')" required>
          <el-input v-model="createForm.version" placeholder="v1.2.3" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.buildSeq')" required>
          <el-input-number v-model="createForm.build_seq" :min="1" style="width: 100%" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.channelLabel')" required>
          <el-radio-group v-model="createForm.channel">
            <el-radio label="stable">{{ t('ops.autoupdate.channel.stable') }}</el-radio>
            <el-radio label="beta">{{ t('ops.autoupdate.channel.beta') }}</el-radio>
            <el-radio label="canary">{{ t('ops.autoupdate.channel.alpha') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.title')" required>
          <el-input v-model="createForm.title" placeholder="Release v1.2.3" />
        </el-form-item>
        <el-form-item :label="t('common.description')">
          <el-input v-model="createForm.description" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.imageTag')" required>
          <el-input v-model="createForm.image_tag" placeholder="registry.kxpms.cn/llm-gateway-go:v1.2.3" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.imageDigest')">
          <el-input v-model="createForm.image_digest" placeholder="sha256:..." />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.changelog')">
          <el-input v-model="createForm.changelog" type="textarea" :rows="4" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreateDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreate">
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Publish Release Dialog -->
    <el-dialog
      v-model="showPublishDialog"
      :title="t('ops.autoupdate.publishReleaseTitle')"
      width="500px"
    >
      <el-form :model="publishForm" label-width="140px">
        <el-form-item :label="t('ops.autoupdate.rolloutPercentage')" required>
          <el-slider
            v-model="publishForm.rolloutPercentage"
            :min="0"
            :max="100"
            :step="10"
            :marks="{ 0: '0%', 10: '10%', 50: '50%', 100: '100%' }"
            show-stops
          />
        </el-form-item>
        <el-alert
          :title="t('ops.autoupdate.gradualRolloutInfo')"
          type="info"
          :closable="false"
          style="margin-top: 12px"
        />
      </el-form>
      <template #footer>
        <el-button @click="showPublishDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handlePublish">
          {{ t('ops.autoupdate.publish') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.autoupdate-view {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.page-header h1 {
  font-size: 24px;
  margin: 0;
}

.main-card {
  margin-bottom: 20px;
}

.logs-card {
  margin-top: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
</style>
