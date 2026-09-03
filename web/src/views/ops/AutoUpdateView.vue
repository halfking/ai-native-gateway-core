<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { fmtDateTime24h } from '../../i18n/useFormat'
import {
  getReleases,
  createRelease,
  publishRelease,
  unpublishRelease,
  createGrayRelease,
  updateGrayPhase,
  getRolloutStatus,
  pauseGrayRelease,
  resumeGrayRelease,
  rollbackRelease,
  getUpgradeLogs,
  type Release,
  type GrayReleaseRule,
  type RolloutStatus,
  type UpgradeLog,
} from '../../api/ops'

import { useEnumLabel } from '../../composables/useEnumLabel'
import { confirmDialog } from '../../composables/useConfirmDialog'

const { t } = useI18n()
const enumLabel = useEnumLabel()
const channelLabel = (value?: string | null) => enumLabel('ops.autoupdate.channel', value)
const logStatusLabel = (value?: string | null) => enumLabel('ops.autoupdate.logStatus', value)

const releases = ref<Release[]>([])
const upgradeLogs = ref<UpgradeLog[]>([])
const loading = ref(false)
const logsLoading = ref(false)
const logsPage = ref(1)
const logsPageSize = ref(20)
const logsTotal = ref(0)
const page = ref(1)
const pageSize = ref(20)

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

// Rollout gate dialog state
const showRolloutDialog = ref(false)
const rolloutLoading = ref(false)
const rolloutStatus = ref<RolloutStatus | null>(null)
const rolloutVersion = ref('')

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
  logsLoading.value = true
  try {
    const res = await getUpgradeLogs({
      offset: (logsPage.value - 1) * logsPageSize.value,
      limit: logsPageSize.value,
    })
    upgradeLogs.value = res.items || []
    logsTotal.value = res.total || 0
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.loadLogsFailed'))
    console.error(error)
  } finally {
    logsLoading.value = false
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
  if (!(await confirmDialog(
    t('ops.autoupdate.publishConfirm', { version: release.version }),
    { title: t('common.confirm'), type: 'info' },
  ))) return
  try {
    await publishRelease(release.version)
    ElMessage.success(t('ops.autoupdate.publishSuccess'))
    await load()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.publishFailed'))
    console.error(error)
  }
}

async function handleUnpublish(release: Release) {
  if (!(await confirmDialog(
    t('ops.autoupdate.unpublishConfirm', { version: release.version }),
    { title: t('common.warning') },
  ))) return
  try {
    await unpublishRelease(release.version)
    ElMessage.success(t('ops.autoupdate.unpublishSuccess'))
    await load()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.unpublishFailed'))
    console.error(error)
  }
}

function openGrayDialog(release: Release) {
  grayForm.value = { version: release.version, phase: 'canary', percent: 10 }
  showGrayDialog.value = true
}

async function handleCreateGray() {
  loading.value = true
  try {
    try {
      await createGrayRelease(grayForm.value.version, {
        phase: grayForm.value.phase,
        percent: grayForm.value.percent,
      })
    } catch {
      await updateGrayPhase(grayForm.value.version, {
        phase: grayForm.value.phase,
        percent: grayForm.value.percent,
      })
    }
    ElMessage.success(t('ops.autoupdate.grayCreateSuccess'))
    showGrayDialog.value = false
  } catch (error: unknown) {
    const err = error as { response?: { data?: { reason?: string } }; message?: string }
    const reason = err?.response?.data?.reason
    ElMessage.error(reason || t('ops.autoupdate.grayCreateFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function openRolloutDialog(release: Release) {
  rolloutVersion.value = release.version
  showRolloutDialog.value = true
  rolloutLoading.value = true
  try {
    rolloutStatus.value = await getRolloutStatus(release.version)
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.rolloutLoadFailed'))
    console.error(error)
    showRolloutDialog.value = false
  } finally {
    rolloutLoading.value = false
  }
}

async function refreshRolloutStatus() {
  if (!rolloutVersion.value) return
  rolloutLoading.value = true
  try {
    rolloutStatus.value = await getRolloutStatus(rolloutVersion.value)
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.rolloutLoadFailed'))
    console.error(error)
  } finally {
    rolloutLoading.value = false
  }
}

async function handlePauseRollout() {
  if (!rolloutVersion.value) return
  rolloutLoading.value = true
  try {
    await pauseGrayRelease(rolloutVersion.value)
    ElMessage.success(t('ops.autoupdate.rolloutPauseSuccess'))
    await refreshRolloutStatus()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.rolloutActionFailed'))
    console.error(error)
  } finally {
    rolloutLoading.value = false
  }
}

async function handleResumeRollout() {
  if (!rolloutVersion.value) return
  rolloutLoading.value = true
  try {
    await resumeGrayRelease(rolloutVersion.value)
    ElMessage.success(t('ops.autoupdate.rolloutResumeSuccess'))
    await refreshRolloutStatus()
  } catch (error: unknown) {
    const err = error as { response?: { data?: { reason?: string } } }
    ElMessage.error(err?.response?.data?.reason || t('ops.autoupdate.rolloutActionFailed'))
    console.error(error)
  } finally {
    rolloutLoading.value = false
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

  if (!(await confirmDialog(
    t('ops.autoupdate.rollbackConfirm', { version: rollbackTarget.value }),
    { title: t('common.warning') },
  ))) return
  try {
    await rollbackRelease(rollbackTarget.value)
    ElMessage.success(t('ops.autoupdate.rollbackSuccess'))
    showRollbackDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(t('ops.autoupdate.rollbackFailed'))
    console.error(error)
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

onMounted(() => {
  load()
  loadUpgradeLogs()
})
</script>

<template>
  <div class="autoupdate-view">
    <div class="page-header">
      <h1>{{ t('ops.autoupdate.title') }}</h1>
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
      <el-table v-loading="loading" :data="releases.slice((page - 1) * pageSize, page * pageSize)">
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="120" />
        <el-table-column prop="build_seq" :label="t('ops.autoupdate.buildSeq')" width="80" />
        <el-table-column prop="channel" :label="t('ops.autoupdate.channelLabel')" width="90">
          <template #default="scope">
            <el-tag :type="channelType(scope?.row?.channel)" size="small">
              {{ channelLabel(scope?.row?.channel) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="title" :label="t('ops.autoupdate.releaseTitle')" min-width="150" />
        <el-table-column prop="image_tag" :label="t('ops.autoupdate.imageTag')" width="180" show-overflow-tooltip />
        <el-table-column prop="created_by" :label="t('ops.autoupdate.createdBy')" width="120" />
        <el-table-column prop="mandatory" :label="t('ops.autoupdate.mandatory')" width="80">
          <template #default="scope">
            <el-tag :type="scope?.row?.mandatory ? 'danger' : 'info'" size="small">
              {{ scope?.row?.mandatory ? t('common.yes') : t('common.no') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="published_at" :label="t('ops.autoupdate.publishedAt')" width="160">
          <template #default="scope">{{ fmtDateTime24h(scope?.row?.published_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="340" fixed="right">
          <template #default="scope">
            <el-button
              v-if="!scope?.row?.published_at"
              type="success"
              size="small"
              @click="handlePublish(scope?.row)"
            >
              {{ t('ops.autoupdate.publish') }}
            </el-button>
            <el-button
              v-if="scope?.row?.published_at"
              type="warning"
              size="small"
              @click="handleUnpublish(scope?.row)"
            >
              {{ t('ops.autoupdate.unpublish') }}
            </el-button>
            <el-button size="small" @click="openGrayDialog(scope?.row)">
              {{ t('ops.autoupdate.gray') }}
            </el-button>
            <el-button
              v-if="scope?.row?.published_at"
              size="small"
              type="info"
              @click="openRolloutDialog(scope?.row)"
            >
              {{ t('ops.autoupdate.rolloutGate') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="releases.length > pageSize" class="pagination-wrap">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="releases.length"
          :page-sizes="[10, 20, 50]"
          layout="sizes, prev, pager, next"
        />
      </div>
    </el-card>

    <!-- Upgrade Logs -->
    <el-card class="logs-card" shadow="never">
      <template #header>
        <span>{{ t('ops.autoupdate.upgradeLogs') }}</span>
      </template>
      <el-table v-loading="logsLoading" :data="upgradeLogs" size="small">
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="200" />
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="120" />
        <el-table-column prop="status" :label="t('common.table.status')" width="120">
          <template #default="scope">
            <el-tag :type="logStatusType(scope?.row?.status)" size="small">
              {{ logStatusLabel(scope?.row?.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="started_at" :label="t('ops.autoupdate.startedAt')" width="160">
          <template #default="scope">{{ fmtDateTime24h(scope?.row?.started_at) }}</template>
        </el-table-column>
        <el-table-column prop="completed_at" :label="t('ops.autoupdate.completedAt')" width="160">
          <template #default="scope">{{ fmtDateTime24h(scope?.row?.completed_at) }}</template>
        </el-table-column>
        <el-table-column prop="error" :label="t('ops.autoupdate.errorMessage')" min-width="200" show-overflow-tooltip />
        <el-table-column prop="retry_count" :label="t('ops.autoupdate.retryCount')" width="90" />
      </el-table>
      <div v-if="logsTotal > logsPageSize" class="pagination-wrap">
        <el-pagination
          v-model:current-page="logsPage"
          v-model:page-size="logsPageSize"
          :total="logsTotal"
          :page-sizes="[10, 20, 50]"
          layout="sizes, prev, pager, next"
          @current-change="loadUpgradeLogs"
          @size-change="loadUpgradeLogs"
        />
      </div>
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
            <el-radio label="stable">{{ t('ops.autoupdate.channel.stable') }}</el-radio>
            <el-radio label="beta">{{ t('ops.autoupdate.channel.beta') }}</el-radio>
            <el-radio label="canary">{{ t('ops.autoupdate.channel.canary') }}</el-radio>
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
            <el-option :label="t('ops.autoupdate.grayPhaseCanary')" value="canary" />
            <el-option :label="t('ops.autoupdate.grayPhaseBatch1')" value="batch_1" />
            <el-option :label="t('ops.autoupdate.grayPhaseBatch2')" value="batch_2" />
            <el-option :label="t('ops.autoupdate.grayPhaseBatch3')" value="batch_3" />
            <el-option :label="t('ops.autoupdate.grayPhaseFull')" value="full" />
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

    <!-- Rollout Gate Dialog -->
    <el-dialog
      v-model="showRolloutDialog"
      :title="t('ops.autoupdate.rolloutTitle', { version: rolloutVersion })"
      width="560px"
    >
      <div v-loading="rolloutLoading">
        <template v-if="rolloutStatus">
          <el-descriptions :column="2" border size="small" class="rollout-desc">
            <el-descriptions-item :label="t('ops.autoupdate.rolloutRuleStatus')">
              {{ rolloutStatus.rule_status || '-' }}
            </el-descriptions-item>
            <el-descriptions-item :label="t('ops.autoupdate.rolloutGateAllowed')">
              <el-tag :type="rolloutStatus.gate.allowed ? 'success' : 'danger'" size="small">
                {{ rolloutStatus.gate.allowed ? t('common.yes') : t('common.no') }}
              </el-tag>
            </el-descriptions-item>
            <el-descriptions-item :label="t('ops.autoupdate.rolloutSuccessRate')">
              {{ rolloutStatus.gate.stats.success_rate_pct }}%
            </el-descriptions-item>
            <el-descriptions-item :label="t('ops.autoupdate.rolloutRollbackRate')">
              {{ rolloutStatus.gate.stats.rollback_rate_pct }}%
            </el-descriptions-item>
            <el-descriptions-item :label="t('ops.autoupdate.rolloutSamples')" :span="2">
              {{ rolloutStatus.gate.stats.total }}
              ({{ t('ops.autoupdate.rolloutSuccessCount') }}: {{ rolloutStatus.gate.stats.success_count }},
              {{ t('ops.autoupdate.rolloutFailedCount') }}: {{ rolloutStatus.gate.stats.failed_count }},
              {{ t('ops.autoupdate.rolloutRolledBackCount') }}: {{ rolloutStatus.gate.stats.rolled_back_count }})
            </el-descriptions-item>
          </el-descriptions>
          <el-alert
            v-if="rolloutStatus.gate.reason"
            :title="rolloutStatus.gate.reason"
            :type="rolloutStatus.gate.allowed ? 'info' : 'warning'"
            :closable="false"
            show-icon
            class="rollout-reason"
          />
        </template>
      </div>
      <template #footer>
        <el-button @click="showRolloutDialog = false">{{ t('common.close') }}</el-button>
        <el-button :loading="rolloutLoading" @click="refreshRolloutStatus">{{ t('common.refresh') }}</el-button>
        <el-button type="warning" :loading="rolloutLoading" @click="handlePauseRollout">
          {{ t('ops.autoupdate.rolloutPause') }}
        </el-button>
        <el-button type="success" :loading="rolloutLoading" @click="handleResumeRollout">
          {{ t('ops.autoupdate.rolloutResume') }}
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

.pagination-wrap {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.rollout-desc {
  margin-bottom: 12px;
}

.rollout-reason {
  margin-top: 12px;
}
</style>
