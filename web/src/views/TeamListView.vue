<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Edit, Delete, View } from '@element-plus/icons-vue'
import { listTeams, deleteTeam, type Team } from '../api-teams'
import { surfaceError } from '../i18n/errorSurface'
import TeamFormModal from './TeamFormModal.vue'

const { t } = useI18n()
const router = useRouter()

const teams = ref<Team[]>([])
const loading = ref(false)
const showCreateDialog = ref(false)

onMounted(() => {
  fetchTeams()
})

async function fetchTeams() {
  loading.value = true
  try {
    const response = await listTeams()
    teams.value = response.teams
  } catch (err) {
    surfaceError(err, { key: 'teams.errors.loadFailed' })
  } finally {
    loading.value = false
  }
}

function handleCreate() {
  showCreateDialog.value = true
}

function handleView(teamId: number) {
  // 详情页路由（router.ts 的 /admin/teams/:id）。路径字面量与 router 定义一致，
  // navCoverage 的「视图内可达」判据按字面量识别 ⇒ 该深链页不算孤儿路由。
  router.push(`/admin/teams/${teamId}`)
}

function handleEdit(team: Team) {
  // TODO: Open edit dialog
  ElMessage.info('Edit functionality - to be implemented')
}

async function handleDelete(team: Team) {
  try {
    await ElMessageBox.confirm(
      t('teams.confirmDelete', { name: team.display_name || team.name }),
      t('common.warning'),
      {
        confirmButtonText: t('common.confirm'),
        cancelButtonText: t('common.cancel'),
        type: 'warning',
      }
    )
    
    await deleteTeam(team.id)
    ElMessage.success(t('teams.deleteSuccess'))
    await fetchTeams()
  } catch (err) {
    if (err !== 'cancel') {
      surfaceError(err, { key: 'teams.errors.deleteFailed' })
    }
  }
}

function onTeamCreated() {
  showCreateDialog.value = false
  fetchTeams()
}
</script>

<template>
  <div class="teams-view">
    <div class="header">
      <h2>{{ t('teams.title') }}</h2>
      <el-button type="primary" :icon="Plus" @click="handleCreate">
        {{ t('teams.createTeam') }}
      </el-button>
    </div>

    <el-table
      v-loading="loading"
      :data="teams"
      style="width: 100%"
      :empty-text="t('teams.noTeams')"
    >
      <el-table-column prop="name" :label="t('teams.name')" width="200" />
      <el-table-column prop="display_name" :label="t('teams.displayName')" />
      <el-table-column prop="description" :label="t('teams.description')" />
      <el-table-column prop="tenant_id" :label="t('teams.tenant')" width="150" />
      <el-table-column :label="t('common.actions')" width="200" fixed="right">
        <template #default="{ row }">
          <el-button size="small" :icon="View" @click="handleView(row.id)">
            {{ t('common.view') }}
          </el-button>
          <el-button size="small" :icon="Edit" @click="handleEdit(row)" />
          <el-button size="small" type="danger" :icon="Delete" @click="handleDelete(row)" />
        </template>
      </el-table-column>
    </el-table>

    <!-- ★ 2026-11-23（12h 修订审计轮）：把已实现但未接线的 TeamFormModal 接进来。
         原先此处是一个内联占位 el-dialog（注释写着「TeamFormModal will be
         implemented here」），「新建团队」按钮只关弹窗、**从不调后端** ⇒
         团队列表页是一个「点了没反应」的半成品。后端契约
         （admin/team_handlers.go 的 POST /api/admin/teams）已就位，
         TeamFormModal.vue 也已实现完整 ⇒ 此处改为真实组件接线。
         组件的 success 事件回填列表（onTeamCreated 原本就在，只是没人触发）。 -->
    <TeamFormModal v-model="showCreateDialog" mode="create" @success="onTeamCreated" />
  </div>
</template>

<style scoped>
.teams-view {
  padding: 20px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

h2 {
  margin: 0;
}
</style>
