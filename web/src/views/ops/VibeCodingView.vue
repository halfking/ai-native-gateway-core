<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { fmtDateTime24h } from '../../i18n/useFormat'
import {
  getVibeCodingProjects,
  createVibeCodingProject,
  getVibeCodingSessions,
  createVibeCodingSession,
  getCodeReviews,
  type VibeCodingProject,
  type VibeCodingSession,
  type CodeReview,
  type CodeIssue,
} from '../../api/ops'

import { useEnumLabel } from '../../composables/useEnumLabel'
import { useWindowClass } from '../../composables/useWindowClass'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField, CardTone } from '../../components/ui/CardList.vue'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElBadge, ElButton, ElCard, ElDescriptions, ElDescriptionsItem, ElDivider, ElForm, ElFormItem, ElInput, ElTable, ElTableColumn, ElTag } from 'element-plus'
// 2026-09-13：弹层壳由 el-dialog 收敛到 ui/AppModal（EP 表单/表格体保留）
import AppModal from '../../components/ui/AppModal.vue'
const { t } = useI18n()
const { isCompact } = useWindowClass()
const enumLabel = useEnumLabel()
const vibeStatusLabel = (value?: string | null) => enumLabel('ops.vibecoding.status', value)

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
  taskType: '',
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

function reviewIssues(review: CodeReview): CodeIssue[] {
  return review.review_result?.issues || []
}

function reviewSuggestions(review: CodeReview): string[] {
  return review.review_result?.suggestions || []
}

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
    taskType: '',
  }
  showSessionDialog.value = true
}

async function handleCreateSession() {
  if (!sessionForm.value.taskType) {
    ElMessage.warning(t('ops.vibecoding.fillRequired'))
    return
  }

  loading.value = true
  try {
    await createVibeCodingSession(sessionForm.value.projectId, sessionForm.value.taskType)
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

/**
 * ── H6 第十六条切片（2026-10-06）：3 张 el-table 接 compact 卡片形态 ─────────────
 *
 * 三张表都是**单请求、无分页、无定时器**：一次 `Promise.all` 取回 projects/sessions/reviews
 * 三份数据，级联筛选（项目 → 会话）纯粹是本地 `computed` ⇒ 只改呈现形态，不引入连续加载。
 *
 * ## `title-key` 统一 `id`，脸各不相同 —— `task_type` 与 `file_path` 都不唯一
 *
 * `CardList` 的 `:key` 取 `titleKey` 的**原始值**。三张表都带 `id: number` ⇒ 键取 `id`，
 * 卡头另用 `titleFormat` 出脸。**不能图省事把 `title-key` 写成 `task_type`**：
 * 同一项目下多个会话可以共用一个 `task_type`，`file_path` 也会在同一会话里重复
 * ⇒ 撞键会让 Vue 复用错行，症状是「卡片内容串行」。这与切片十三第 4 张表的
 * 复合身份是同一个坑的两面（那边要**拼**派生键，这边**已有**主键可用）。
 *
 * ## 两处 tone 映射都是**从桌面函数派生的**，不是另写一张表
 *
 * 桌面 `statusType` 出 2 档 Element 类型（success/info），`getScoreColor` 出 3 档
 * （success/warning/danger）⇒ 分别映射成 2 档 / 3 档卡片 tone，**判读线共用**：
 * 桌面把 `statusType` 改一档，卡片这层跟着变，不会两套标准各说各话。
 *
 * ## 三张表**都不传 `:loading`** —— 与桌面「保留旧行」一致，且三张表口径统一
 *
 * 桌面 projects 表自带 `v-loading="loading"`，但那是**覆盖层**：`el-table` 仍在 DOM 里、
 * 旧行照常渲染。而 `ResponsiveDataView` 的 `loading` 分支是 `v-if="loading"` / `v-else` 才轮到
 * 表与卡 ⇒ 传 `:loading` 会让**桌面在每次刷新时把整张 el-table 换成 spinner**，
 * 那是桌面 DOM 变更（首次加载时 Element 的 "No Data" 也会整个消失）。
 * sessions/reviews 桌面**根本没有** loading 指示 ⇒ 三张表统一不传，
 * 页面级的延迟反馈继续由 `v-loading` 承担。门禁断「三张都不出现 `:loading`」。
 *
 * ## 三态：compact 的空态只能来自容器，桌面留给 Element
 *
 * 三张 el-table 桌面**都没有自己的空态行**，0 行时是 Element 自带的 "No Data"
 * ⇒ `:empty` **必须带 `isCompact` 前置**（否则桌面会把 Element 的空态换成我们的
 * `EmptyState`，那是桌面变更）。文案复用既有的 `hyper.list.empty` ⇒ **0 新增 i18n 键**。
 * sessions/reviews 的谓词用**筛选后**的数组（`filteredSessions` / `filteredReviews`），
 * 不用原始数组 —— 否则「选了项目但该项目下没有会话」时会空态缺席。
 *
 * ## 计数两列（issues / suggestions）：行对象上**没有**这两个键
 *
 * 桌面的这两列是纯插槽（无 `prop`），从 `review_result` 里数出来。
 * 卡片仍用列名作 `key`（`CardList` 只用它取标签与身份），值由 `format(_, row)` 算。
 * `el-badge` 的 `showZero` 在 Element Plus 2.14.3 **默认 `true`**（已实测）⇒
 * 桌面 0 个问题照样出「success 徽章 0」，卡片出 `good` 色调的 `0` ⇒ 两边都在，
 * 不是口径差异。若哪天有人给桌面加 `show-zero` 之外的隐藏逻辑，这里要跟着复核。
 *
 * ## 一处**已知且有界**的口径差异（不是漏）
 *
 * `score` 缺值时桌面 el-table 渲染**空白**，卡片出 `—`。
 * `CardList` 没有「空白」这个状态（`null`/`undefined`/`''` 一律出 `—`），
 * 而 `—` 就是本仓统一的「无值」记号。**卡片不能渲染空白**，改桌面又越过红线。
 * 与切片十五的 `avg_health` 同源。
 */

/** 状态标签色 → 卡片 tone。判读线从 `statusType` 派生，不另写映射表。 */
function statusTone(status: unknown): CardTone {
  return statusType(status == null ? '' : String(status)) === 'success' ? 'good' : 'neutral'
}

/** 评分色 → 卡片 tone。三档一一对应，判读线从 `getScoreColor` 派生。 */
function scoreTone(score: unknown): CardTone {
  const color = getScoreColor(Number(score))
  if (color === 'success') return 'good'
  if (color === 'warning') return 'warn'
  return 'danger'
}

const projectTitle = (row: Record<string, unknown>) => (row.name == null ? undefined : String(row.name))
const sessionTitle = (row: Record<string, unknown>) => (row.task_type == null ? undefined : String(row.task_type))
const reviewTitle = (row: Record<string, unknown>) => (row.file_path == null ? undefined : String(row.file_path))

/** 项目表卡片字段。6 列里除 `name`（卡头）与操作列外的那 4 个。 */
const projectFields = computed<CardField[]>(() => [
  { key: 'language', label: t('ops.vibecoding.language') },
  { key: 'framework', label: t('ops.vibecoding.framework') },
  {
    key: 'status',
    label: t('common.table.status'),
    type: 'badge',
    format: (v) => vibeStatusLabel(v == null ? null : String(v)),
    tone: (row) => statusTone(row.status),
  },
  { key: 'created_at', label: t('common.createdAt'), format: (v) => fmtDateTime24h(v == null ? null : String(v)) },
])

/** 会话表卡片字段。6 列里除 `task_type`（卡头）与操作列外的那 4 个。 */
const sessionFields = computed<CardField[]>(() => [
  { key: 'project_id', label: t('ops.vibecoding.projectId'), type: 'metric', align: 'end' },
  {
    key: 'status',
    label: t('common.table.status'),
    type: 'badge',
    format: (v) => vibeStatusLabel(v == null ? null : String(v)),
    tone: (row) => statusTone(row.status),
  },
  { key: 'created_at', label: t('ops.vibecoding.startedAt'), format: (v) => fmtDateTime24h(v == null ? null : String(v)) },
  {
    key: 'completed_at',
    label: t('ops.vibecoding.endedAt'),
    // 桌面这一列是显式三元 `completed_at ? fmt : '—'`，卡片照抄同一口径。
    format: (v) => (v ? fmtDateTime24h(String(v)) : undefined),
  },
])

/** 评审表卡片字段。7 列里除 `file_path`（卡头）与操作列外的那 5 个。 */
const reviewFields = computed<CardField[]>(() => [
  { key: 'language', label: t('ops.vibecoding.language') },
  {
    key: 'score',
    label: t('ops.vibecoding.score'),
    type: 'badge',
    align: 'end',
    format: (v) => (v == null ? undefined : String(v)),
    tone: (row) => scoreTone(row.score),
  },
  {
    key: 'issues',
    label: t('ops.vibecoding.issues'),
    type: 'metric',
    align: 'end',
    format: (_v, row) => String(reviewIssues(row as unknown as CodeReview).length),
    tone: (row) => (reviewIssues(row as unknown as CodeReview).length > 0 ? 'danger' : 'good'),
  },
  {
    key: 'suggestions',
    label: t('ops.vibecoding.suggestions'),
    type: 'metric',
    align: 'end',
    format: (_v, row) => String(reviewSuggestions(row as unknown as CodeReview).length),
  },
  { key: 'created_at', label: t('ops.vibecoding.reviewedAt'), format: (v) => fmtDateTime24h(v == null ? null : String(v)) },
])

onMounted(load)
</script>

<template>
  <div class="vibecoding-view">
    <div class="page-header">
      <h1>{{ t('ops.vibecoding.title') }}</h1>
      <el-button type="primary" @click="showProjectDialog = true">
        + {{ t('ops.vibecoding.createProject') }}
      </el-button>
    </div>

    <!-- Projects -->
    <el-card class="section-card" shadow="never" data-testid="vc-projects">
      <template #header>
        <span>{{ t('ops.vibecoding.projects') }}</span>
      </template>
      <ResponsiveDataView
        :rows="projects"
        title-key="id"
        :title-format="projectTitle"
        :fields="projectFields"
        :empty="isCompact && projects.length === 0"
        :empty-text="t('hyper.list.empty')"
      >
        <template #table>
      <el-table v-loading="loading" :data="projects">
        <el-table-column prop="name" :label="t('ops.vibecoding.projectName')" width="200" />
        <el-table-column prop="language" :label="t('ops.vibecoding.language')" width="120" />
        <el-table-column prop="framework" :label="t('ops.vibecoding.framework')" width="150" />
        <el-table-column prop="status" :label="t('common.table.status')" width="100">
          <template #default="scope">
            <el-tag :type="statusType(scope?.row?.status)" size="small">
              {{ vibeStatusLabel(scope?.row?.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('common.createdAt')" width="160">
          <template #default="scope">{{ fmtDateTime24h(scope?.row?.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="200" fixed="right">
          <template #default="scope">
            <el-button type="primary" size="small" @click="openSessionDialog(scope?.row as VibeCodingProject)">
              {{ t('ops.vibecoding.newSession') }}
            </el-button>
            <el-button size="small" @click="selectedProjectId = scope?.row?.id">
              {{ t('ops.vibecoding.viewSessions') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
        </template>
        <template #actions="{ row }">
          <el-button type="primary" size="small" @click="openSessionDialog(row as VibeCodingProject)">
            {{ t('ops.vibecoding.newSession') }}
          </el-button>
          <el-button size="small" @click="selectedProjectId = row.id">
            {{ t('ops.vibecoding.viewSessions') }}
          </el-button>
        </template>
      </ResponsiveDataView>
    </el-card>

    <!-- Sessions -->
    <el-card class="section-card" shadow="never" data-testid="vc-sessions">
      <template #header>
        <div class="card-header">
          <span>{{ t('ops.vibecoding.sessions') }}</span>
          <el-button v-if="selectedProjectId" size="small" @click="selectedProjectId = null">
            {{ t('ops.vibecoding.showAll') }}
          </el-button>
        </div>
      </template>
      <ResponsiveDataView
        :rows="filteredSessions"
        title-key="id"
        :title-format="sessionTitle"
        :fields="sessionFields"
        :empty="isCompact && filteredSessions.length === 0"
        :empty-text="t('hyper.list.empty')"
      >
        <template #table>
      <el-table :data="filteredSessions" size="small">
        <el-table-column prop="task_type" :label="t('ops.vibecoding.sessionName')" width="200" />
        <el-table-column prop="project_id" :label="t('ops.vibecoding.projectId')" width="100" />
        <el-table-column prop="status" :label="t('common.table.status')" width="100">
          <template #default="scope">
            <el-tag :type="statusType(scope?.row?.status)" size="small">
              {{ vibeStatusLabel(scope?.row?.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('ops.vibecoding.startedAt')" width="160">
          <template #default="scope">{{ fmtDateTime24h(scope?.row?.created_at) }}</template>
        </el-table-column>
        <el-table-column prop="completed_at" :label="t('ops.vibecoding.endedAt')" width="160">
          <template #default="scope">{{ scope?.row?.completed_at ? fmtDateTime24h(scope?.row?.completed_at) : '—' }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="140" fixed="right">
          <template #default="scope">
            <el-button size="small" @click="selectedSessionId = scope?.row?.id">
              {{ t('ops.vibecoding.viewReviews') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
        </template>
        <template #actions="{ row }">
          <el-button size="small" @click="selectedSessionId = row.id">
            {{ t('ops.vibecoding.viewReviews') }}
          </el-button>
        </template>
      </ResponsiveDataView>
    </el-card>

    <!-- Code Reviews -->
    <el-card class="section-card" shadow="never" data-testid="vc-reviews">
      <template #header>
        <div class="card-header">
          <span>{{ t('ops.vibecoding.codeReviews') }}</span>
          <el-button v-if="selectedSessionId" size="small" @click="selectedSessionId = null">
            {{ t('ops.vibecoding.showAll') }}
          </el-button>
        </div>
      </template>
      <ResponsiveDataView
        :rows="filteredReviews"
        title-key="id"
        :title-format="reviewTitle"
        :fields="reviewFields"
        :empty="isCompact && filteredReviews.length === 0"
        :empty-text="t('hyper.list.empty')"
      >
        <template #table>
      <el-table :data="filteredReviews" size="small">
        <el-table-column prop="language" :label="t('ops.vibecoding.language')" width="100" />
        <el-table-column prop="file_path" :label="t('ops.vibecoding.filePath')" min-width="250" show-overflow-tooltip />
        <el-table-column prop="score" :label="t('ops.vibecoding.score')" width="100">
          <template #default="scope">
            <el-tag :type="getScoreColor(scope?.row?.score)" size="small">
              {{ scope?.row?.score }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.vibecoding.issues')" width="80">
          <template #default="scope">
            <el-badge :value="reviewIssues(scope?.row as CodeReview).length" :type="reviewIssues(scope?.row as CodeReview).length > 0 ? 'danger' : 'success'" />
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.vibecoding.suggestions')" width="80">
          <template #default="scope">
            <el-badge :value="reviewSuggestions(scope?.row as CodeReview).length" type="info" />
          </template>
        </el-table-column>
        <el-table-column prop="created_at" :label="t('ops.vibecoding.reviewedAt')" width="160">
          <template #default="scope">{{ fmtDateTime24h(scope?.row?.created_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="100" fixed="right">
          <template #default="scope">
            <el-button size="small" @click="viewReviewDetail(scope?.row as CodeReview)">
              {{ t('common.detail') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
        </template>
        <template #actions="{ row }">
          <el-button size="small" @click="viewReviewDetail(row as unknown as CodeReview)">
            {{ t('common.detail') }}
          </el-button>
        </template>
      </ResponsiveDataView>
    </el-card>

    <!-- Create Project Dialog -->
    <AppModal
      v-model="showProjectDialog"
      :title="t('ops.vibecoding.createProjectTitle')"
      size="sm"
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
    </AppModal>

    <!-- Create Session Dialog -->
    <AppModal
      v-model="showSessionDialog"
      :title="t('ops.vibecoding.createSessionTitle')"
      size="sm"
    >
      <el-form :model="sessionForm" label-width="120px">
        <el-form-item :label="t('ops.vibecoding.taskType')" required>
          <el-input v-model="sessionForm.taskType" :placeholder="t('ops.vibecoding.taskTypePlaceholder')" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showSessionDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleCreateSession">
          {{ t('common.create') }}
        </el-button>
      </template>
    </AppModal>

    <!-- Review Detail Dialog -->
    <AppModal
      v-model="showReviewDialog"
      :title="t('ops.vibecoding.reviewDetail')"
      size="lg"
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
              {{ selectedReview.score }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.vibecoding.reviewedAt')">
            {{ fmtDateTime24h(selectedReview.created_at) }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.vibecoding.summary')" :span="2">
            {{ selectedReview.review_result?.summary }}
          </el-descriptions-item>
        </el-descriptions>

        <el-divider />

        <h4>{{ t('ops.vibecoding.issues') }} ({{ reviewIssues(selectedReview).length }})</h4>
        <el-table :data="reviewIssues(selectedReview)" size="small" style="margin-bottom: 20px">
          <el-table-column prop="line" :label="t('ops.vibecoding.line')" width="80" />
          <el-table-column prop="severity" :label="t('ops.vibecoding.severity')" width="100">
            <template #default="scope">
              <el-tag :type="severityType(scope?.row?.severity)" size="small">
                {{ scope?.row?.severity }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="message" :label="t('ops.vibecoding.message')" min-width="200" />
          <el-table-column prop="category" :label="t('ops.vibecoding.category')" width="120" />
        </el-table>

        <h4>{{ t('ops.vibecoding.suggestions') }} ({{ reviewSuggestions(selectedReview).length }})</h4>
        <el-table :data="(reviewSuggestions(selectedReview) as unknown as Record<string, unknown>[])" size="small">
          <el-table-column type="index" :label="'#'" width="50" />
          <el-table-column prop="" :label="t('ops.vibecoding.message')" min-width="300" show-overflow-tooltip>
            <template #default="scope">{{ scope?.row }}</template>
          </el-table-column>
        </el-table>
      </div>
      <template #footer>
        <el-button @click="showReviewDialog = false">{{ t('common.close') }}</el-button>
      </template>
    </AppModal>
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
