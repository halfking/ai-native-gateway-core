<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getReleases,
  createRelease,
  publishRelease,
  unpublishRelease,
  createGrayRelease,
  updateGrayPhase,
  rollbackRelease,
  getUpgradeLogs,
  type Release,
  type GrayReleaseRule,
  type UpgradeLog,
} from '../../api/ops'

const { t } = useI18n()

const releases = ref<Release[]>([])
const upgradeLogs = ref<UpgradeLog[]>([])
const loading = ref(false)

// Create dialog state
const showCreateDialog = ref(false)
const createForm = ref({
  version: '',
  build_seq: 0,
  channel: 'stable' as 'stable' | 'beta' | 'canary',
  title: '',
  image_tag: '',
  created_by: '',
  description: '',
  changelog: '',
  image_digest: '',
  min_version: '',
  mandatory: false,
})

// Gray release dialog state
const showGrayDialog = ref(false)
const grayForm = ref({
  version: '',
  phase: 'canary' as string,
  percent: 10,
})

// Rollback dialog state
const showRollbackDialog = ref(false)
const rollbackTarget = ref('')

async function load() {
  loading.value = true
  try {
    const res = await getReleases()
    releases.value = res.items || []
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function loadUpgradeLogs() {
  try {
    upgradeLogs.value = await getUpgradeLogs()
  } catch (error) {
    console.error('Failed to load upgrade logs:', error)
  }
}

async function handleCreate() {
  if (!createForm.value.version || !createForm.value.title || !createForm.value.image_tag || !createForm.value.created_by) {
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
      image_tag: createForm.value.image_tag,
      created_by: createForm.value.created_by,
      description: createForm.value.description || undefined,
      changelog: createForm.value.changelog || undefined,
      image_digest: createForm.value.image_digest || undefined,
      min_version: createForm.value.min_version || undefined,
      mandatory: createForm.value.mandatory,
    })
    ElMessage.success(t('ops.autoupdate.createSuccess'))
    showCreateDialog.value = false
    createForm.value = {
      version: '', build_seq: 0, channel: 'stable', title: '',
      image_tag: '', created_by: '', description: '', changelog: '',
      image_digest: '', min_version: '', mandatory: false,
    }
    await load()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.createFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handlePublish(release: Release) {
  try {
    await ElMessageBox.confirm(
      t('ops.autoupdate.publishConfirm', { version: release.version }),
      t('common.confirm'),
      { type: 'info' }
    )
    await publishRelease(release.version)
    ElMessage.success(t('ops.autoupdate.publishSuccess'))
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.autoupdate.publishFailed'))
      console.error(error)
    }
  }
}

async function handleUnpublish(release: Release) {
  try {
    await ElMessageBox.confirm(
      t('ops.autoupdate.unpublishConfirm', { version: release.version }),
      t('common.warning'),
      { type: 'warning' }
    )
    await unpublishRelease(release.version)
    ElMessage.success(t('ops.autoupdate.unpublishSuccess'))
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.autoupdate.unpublishFailed'))
      console.error(error)
    }
  }
}

function openGrayDialog(release: Release) {
  grayForm.value = { version: release.version, phase: 'canary', percent: 10 }
  showGrayDialog.value = true
}

async function handleCreateGray() {
  loading.value = true
  try {
    await createGrayRelease(grayForm.value.version, {
      phase: grayForm.value.phase,
      percent: grayForm.value.percent,
    })
    ElMessage.success(t('ops.autoupdate.grayCreateSuccess'))
    showGrayDialog.value = false
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.grayCreateFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function openRollbackDialog() {
  rollbackTarget.value = ''
  showRollbackDialog.value = true
}

async function handleRollback() {
  if (!rollbackTarget.value) {
    ElMessage.warning(t('ops.autoupdate.fillRequired'))
    return
  }

  try {
    await ElMessageBox.confirm(
      t('ops.autoupdate.rollbackConfirm', { version: rollbackTarget.value }),
      t('common.warning'),
      { type: 'warning' }
    )
    await rollbackRelease(rollbackTarget.value)
    ElMessage.success(t('ops.autoupdate.rollbackSuccess'))
    showRollbackDialog.value = false
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.autoupdate.rollbackFailed'))
      console.error(error)
    }
  }
}

function channelType(channel: string) {
  const map: Record<string, 'success' | 'warning' | 'info'> = {
    stable: 'success',
    beta: 'warning',
    canary: 'info',
  }
  return map[channel] || 'info'
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
      <div class="header-actions">
        <el-button @click="openRollbackDialog">
          {{ t('ops.autoupdate.rollback') }}
        </el-button>
        <el-button type="primary" @click="showCreateDialog = true">
          + {{ t('ops.autoupdate.createRelease') }}
        </el-button>
      </div>
    </div>

    <!-- Releases Table -->
    <el-card class="main-card" shadow="never">
      <template #header>
        <span>{{ t('ops.autoupdate.releases') }}</span>
      </template>
      <el-table v-loading="loading" :data="releases">
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="120" />
        <el-table-column prop="build_seq" :label="t('ops.autoupdate.buildSeq')" width="80" />
        <el-table-column prop="channel" :label="t('ops.autoupdate.channelLabel')" width="90">
          <template #default="{ row }">
            <el-tag :type="channelType(row.channel)" size="small">
              {{ row.channel }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="title" :label="t('ops.autoupdate.releaseTitle')" min-width="150" />
        <el-table-column prop="image_tag" :label="t('ops.autoupdate.imageTag')" width="180" show-overflow-tooltip />
        <el-table-column prop="created_by" :label="t('ops.autoupdate.createdBy')" width="120" />
        <el-table-column prop="mandatory" :label="t('ops.autoupdate.mandatory')" width="80">
          <template #default="{ row }">
            <el-tag :type="row.mandatory ? 'danger' : 'info'" size="small">
              {{ row.mandatory ? t('common.yes') : t('common.no') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="published_at" :label="t('ops.autoupdate.publishedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.published_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="260" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="!row.published_at"
              type="success"
              size="small"
              @click="handlePublish(row)"
            >
              {{ t('ops.autoupdate.publish') }}
            </el-button>
            <el-button
              v-if="row.published_at"
              type="warning"
              size="small"
              @click="handleUnpublish(row)"
            >
              {{ t('ops.autoupdate.unpublish') }}
            </el-button>
            <el-button size="small" @click="openGrayDialog(row)">
              {{ t('ops.autoupdate.gray') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Upgrade Logs -->
    <el-card class="logs-card" shadow="never">
      <template #header>
        <span>{{ t('ops.autoupdate.upgradeLogs') }}</span>
      </template>
      <el-table :data="upgradeLogs" size="small">
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="200" />
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="120" />
        <el-table-column prop="status" :label="t('common.status')" width="120">
          <template #default="{ row }">
            <el-tag :type="logStatusType(row.status)" size="small">
              {{ row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="started_at" :label="t('ops.autoupdate.startedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.started_at) }}</template>
        </el-table-column>
        <el-table-column prop="completed_at" :label="t('ops.autoupdate.completedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.completed_at) }}</template>
        </el-table-column>
        <el-table-column prop="error" :label="t('ops.autoupdate.errorMessage')" min-width="200" show-overflow-tooltip />
        <el-table-column prop="retry_count" :label="t('ops.autoupdate.retryCount')" width="90" />
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
          <el-input v-model="createForm.version" :placeholder="t('ops.autoupdate.versionPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.buildSeq')" required>
          <el-input-number v-model="createForm.build_seq" :min="1" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.channelLabel')" required>
          <el-radio-group v-model="createForm.channel">
            <el-radio label="stable">Stable</el-radio>
            <el-radio label="beta">Beta</el-radio>
            <el-radio label="canary">Canary</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.releaseTitle')" required>
          <el-input v-model="createForm.title" :placeholder="t('ops.autoupdate.releaseTitlePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.imageTag')" required>
          <el-input v-model="createForm.image_tag" :placeholder="t('ops.autoupdate.imageTagPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.createdBy')" required>
          <el-input v-model="createForm.created_by" :placeholder="t('ops.autoupdate.createdByPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.description')">
          <el-input v-model="createForm.description" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.changelog')">
          <el-input v-model="createForm.changelog" type="textarea" :rows="3" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.imageDigest')">
          <el-input v-model="createForm.image_digest" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.minVersion')">
          <el-input v-model="createForm.min_version" />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.mandatory')">
          <el-switch v-model="createForm.mandatory" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreateDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreate">
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Gray Release Dialog -->
    <el-dialog
      v-model="showGrayDialog"
      :title="t('ops.autoupdate.grayTitle')"
      width="500px"
    >
      <el-form :model="grayForm" label-width="140px">
        <el-form-item :label="t('ops.autoupdate.version')">
          <el-input :model-value="grayForm.version" disabled />
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.grayPhase')" required>
          <el-select v-model="grayForm.phase" style="width: 100%">
            <el-option label="Canary (金丝雀)" value="canary" />
            <el-option label="Batch 1 (第一批)" value="batch_1" />
            <el-option label="Batch 2 (第二批)" value="batch_2" />
            <el-option label="Batch 3 (第三批)" value="batch_3" />
            <el-option label="Full (全量)" value="full" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.autoupdate.rolloutPercentage')" required>
          <el-slider
            v-model="grayForm.percent"
            :min="0"
            :max="100"
            :step="5"
            show-stops
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showGrayDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreateGray">
          {{ t('ops.autoupdate.grayCreate') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Rollback Dialog -->
    <el-dialog
      v-model="showRollbackDialog"
      :title="t('ops.autoupdate.rollbackTitle')"
      width="500px"
    >
      <el-form label-width="140px">
        <el-form-item :label="t('ops.autoupdate.targetVersion')" required>
          <el-input v-model="rollbackTarget" :placeholder="t('ops.autoupdate.targetVersionPlaceholder')" />
        </el-form-item>
        <el-alert
          :title="t('ops.autoupdate.rollbackWarning')"
          type="warning"
          :closable="false"
          show-icon
        />
      </el-form>
      <template #footer>
        <el-button @click="showRollbackDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="danger" :loading="loading" @click="handleRollback">
          {{ t('ops.autoupdate.rollback') }}
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

.header-actions {
  display: flex;
  gap: 8px;
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
