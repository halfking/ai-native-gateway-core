<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getVibeCodingProjects,
  createVibeCodingProject,
  getVibeCodingSessions,
  createVibeCodingSession,
  getCodeReviews,
  type VibeCodingProject,
  type VibeCodingSession,
  type CodeReview,
} from '../../api/ops'

const { t } = useI18n()

const projects = ref<VibeCodingProject[]>([])
const sessions = ref<VibeCodingSession[]>([])
const reviews = ref<CodeReview[]>([])
const loading = ref(false)

// Filter state
const selectedProjectId = ref<number | null>(null)
const selectedSessionId = ref<number | null>(null)

// Project dialog state
const showProjectDialog = ref(false)
const projectForm = ref({
  name: '',
  language: '',
  framework: '',
})

// Session dialog state
const showSessionDialog = ref(false)
const sessionForm = ref({
  projectId: 0,
  sessionName: '',
})

// Review detail dialog
const showReviewDialog = ref(false)
const selectedReview = ref<CodeReview | null>(null)

const filteredSessions = computed(() => {
  if (!selectedProjectId.value) return sessions.value
  return sessions.value.filter((s) => s.project_id === selectedProjectId.value)
})

const filteredReviews = computed(() => {
  if (!selectedSessionId.value) return reviews.value
  return reviews.value.filter((r) => r.session_id === selectedSessionId.value)
})

async function load() {
  loading.value = true
  try {
    const [projectsData, sessionsData, reviewsData] = await Promise.all([
      getVibeCodingProjects(),
      getVibeCodingSessions(),
      getCodeReviews(),
    ])
    projects.value = projectsData
    sessions.value = sessionsData
    reviews.value = reviewsData
  } catch (error) {
    ElMessage.error(t('ops.vibecoding.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleCreateProject() {
  if (!projectForm.value.name || !projectForm.value.language) {
    ElMessage.warning(t('ops.vibecoding.fillRequired'))
    return
  }

  loading.value = true
  try {
    await createVibeCodingProject(projectForm.value)
    ElMessage.success(t('ops.vibecoding.createProjectSuccess'))
    showProjectDialog.value = false
    projectForm.value = { name: '', language: '', framework: '' }
    await load()
  } catch (error) {
    ElMessage.error(t('ops.vibecoding.createProjectFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function openSessionDialog(project: VibeCodingProject) {
  sessionForm.value = {
    projectId: project.id,
    sessionName: '',
  }
  showSessionDialog.value = true
}

async function handleCreateSession() {
  if (!sessionForm.value.sessionName) {
    ElMessage.warning(t('ops.vibecoding.fillRequired'))
    return
  }

  loading.value = true
  try {
    await createVibeCodingSession(sessionForm.value.projectId, sessionForm.value.sessionName)
    ElMessage.success(t('ops.vibecoding.createSessionSuccess'))
    showSessionDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(t('ops.vibecoding.createSessionFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function viewReviewDetail(review: CodeReview) {
  selectedReview.value = review
  showReviewDialog.value = true
}

function statusType(status: string) {
  const map: Record<string, 'success' | 'info'> = {
    active: 'success',
    archived: 'info',
    completed: 'info',
  }
  return map[status] || 'info'
}

function getScoreColor(score: number) {
  if (score >= 80) return 'success'
  if (score >= 60) return 'warning'
  return 'danger'
}

function severityType(severity: string) {
  const map: Record<string, 'danger' | 'warning' | 'info'> = {
    error: 'danger',
    warning: 'warning',
    info: 'info',
  }
  return map[severity] || 'info'
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}

function formatDuration(seconds: number) {
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

onMounted(load)
</script>

<template>
  <div class="vibecoding-view">
    <div class="page-header">
      <h1>💻 {{ t('ops.vibecoding.title') }}</h1>
      <el-button type="primary" @click="showProjectDialog = true">
        + {{ t('ops.vibecoding.createProject') }}
      </el-button>
    </div>

    <!-- Projects -->
    <el-card class="section-card" shadow="never">
      <template #header>
        <span>{{ t('ops.vibecoding.projects') }}</span>
      </template>
      <el-table v-loading="loading" :data="projects">
        <el-table-column prop="name" :label="t('ops.vibecoding.projectName')" width="200" />
        <el-table-column prop="language" :label="t('ops.vibecoding.language')" width="120" />
        <el-table-column prop="framework" :label="t('ops.vibecoding.framework')" width="150" />
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.vibecoding.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('common.createdAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="200" fixed="right">
          <template #default="{ row }">
            <el-button type="primary" size="small" @click="openSessionDialog(row)">
              {{ t('ops.vibecoding.newSession') }}
            </el-button>
            <el-button size="small" @click="selectedProjectId = row.id">
              {{ t('ops.vibecoding.viewSessions') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Sessions -->
    <el-card class="section-card" shadow="never">
      <template #header>
        <div class="card-header">
          <span>{{ t('ops.vibecoding.sessions') }}</span>
          <el-button v-if="selectedProjectId" size="small" @click="selectedProjectId = null">
            {{ t('ops.vibecoding.showAll') }}
          </el-button>
        </div>
      </template>
      <el-table :data="filteredSessions" size="small">
        <el-table-column prop="session_name" :label="t('ops.vibecoding.sessionName')" width="200" />
        <el-table-column prop="project_id" :label="t('ops.vibecoding.projectId')" width="100" />
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.vibecoding.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="duration_seconds" :label="t('ops.vibecoding.duration')" width="100">
          <template #default="{ row }">{{ formatDuration(row.duration_seconds) }}</template>
        </el-table-column>
        <el-table-column prop="started_at" :label="t('ops.vibecoding.startedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.started_at) }}</template>
        </el-table-column>
        <el-table-column prop="ended_at" :label="t('ops.vibecoding.endedAt')" width="160">
          <template #default="{ row }">{{ row.ended_at ? formatDate(row.ended_at) : '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="140" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click="selectedSessionId = row.id">
              {{ t('ops.vibecoding.viewReviews') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Code Reviews -->
    <el-card class="section-card" shadow="never">
      <template #header>
        <div class="card-header">
          <span>{{ t('ops.vibecoding.codeReviews') }}</span>
          <el-button v-if="selectedSessionId" size="small" @click="selectedSessionId = null">
            {{ t('ops.vibecoding.showAll') }}
          </el-button>
        </div>
      </template>
      <el-table :data="filteredReviews" size="small">
        <el-table-column prop="language" :label="t('ops.vibecoding.language')" width="100" />
        <el-table-column prop="file_path" :label="t('ops.vibecoding.filePath')" min-width="250" show-overflow-tooltip />
        <el-table-column prop="score" :label="t('ops.vibecoding.score')" width="120">
          <template #default="{ row }">
            <el-tag :type="getScoreColor(row.score)" size="small">
              {{ row.score }}/100
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.vibecoding.issues')" width="100">
          <template #default="{ row }">
            <el-badge :value="row.issues.length" :type="row.issues.length > 0 ? 'danger' : 'success'" />
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.vibecoding.suggestions')" width="100">
          <template #default="{ row }">
            <el-badge :value="row.suggestions.length" type="info" />
          </template>
        </el-table-column>
        <el-table-column prop="reviewed_at" :label="t('ops.vibecoding.reviewedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.reviewed_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="100" fixed="right">
          <template #default="{ row }">
            <el-button size="small" @click="viewReviewDetail(row)">
              {{ t('common.detail') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Create Project Dialog -->
    <el-dialog
      v-model="showProjectDialog"
      :title="t('ops.vibecoding.createProjectTitle')"
      width="500px"
    >
      <el-form :model="projectForm" label-width="120px">
        <el-form-item :label="t('ops.vibecoding.projectName')" required>
          <el-input v-model="projectForm.name" :placeholder="t('ops.vibecoding.projectNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.vibecoding.language')" required>
          <el-input v-model="projectForm.language" :placeholder="t('ops.vibecoding.languagePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.vibecoding.framework')">
          <el-input v-model="projectForm.framework" :placeholder="t('ops.vibecoding.frameworkPlaceholder')" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showProjectDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreateProject">
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Create Session Dialog -->
    <el-dialog
      v-model="showSessionDialog"
      :title="t('ops.vibecoding.createSessionTitle')"
      width="500px"
    >
      <el-form :model="sessionForm" label-width="120px">
        <el-form-item :label="t('ops.vibecoding.sessionName')" required>
          <el-input v-model="sessionForm.sessionName" :placeholder="t('ops.vibecoding.sessionNamePlaceholder')" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showSessionDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreateSession">
          {{ t('common.create') }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Review Detail Dialog -->
    <el-dialog
      v-model="showReviewDialog"
      :title="t('ops.vibecoding.reviewDetail')"
      width="800px"
    >
      <div v-if="selectedReview">
        <el-descriptions :column="2" border>
          <el-descriptions-item :label="t('ops.vibecoding.filePath')">
            {{ selectedReview.file_path }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.vibecoding.language')">
            {{ selectedReview.language }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.vibecoding.score')">
            <el-tag :type="getScoreColor(selectedReview.score)">
              {{ selectedReview.score }}/100
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.vibecoding.reviewedAt')">
            {{ formatDate(selectedReview.reviewed_at) }}
          </el-descriptions-item>
        </el-descriptions>

        <el-divider />

        <h4>{{ t('ops.vibecoding.issues') }}</h4>
        <el-table :data="selectedReview.issues" size="small" style="margin-bottom: 20px">
          <el-table-column prop="line" :label="t('ops.vibecoding.line')" width="80" />
          <el-table-column prop="severity" :label="t('ops.vibecoding.severity')" width="100">
            <template #default="{ row }">
              <el-tag :type="severityType(row.severity)" size="small">
                {{ row.severity }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="message" :label="t('ops.vibecoding.message')" min-width="200" />
          <el-table-column prop="code" :label="t('ops.vibecoding.code')" width="120" />
        </el-table>

        <h4>{{ t('ops.vibecoding.suggestions') }}</h4>
        <el-table :data="selectedReview.suggestions" size="small">
          <el-table-column prop="line" :label="t('ops.vibecoding.line')" width="80" />
          <el-table-column prop="message" :label="t('ops.vibecoding.message')" min-width="200" />
          <el-table-column prop="suggested_code" :label="t('ops.vibecoding.suggestedCode')" min-width="200" show-overflow-tooltip />
        </el-table>
      </div>
      <template #footer>
        <el-button @click="showReviewDialog = false">{{ t('common.close') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.vibecoding-view {
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

.section-card {
  margin-bottom: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

h4 {
  margin: 16px 0 12px 0;
  font-size: 14px;
  font-weight: 600;
}
</style>
