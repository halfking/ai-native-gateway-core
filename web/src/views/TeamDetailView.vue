<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Edit, Plus, Delete, ArrowLeft } from '@element-plus/icons-vue'
import {
  getTeam,
  getTeamBudget,
  getTeamBudgetStats,
  addTeamMember,
  removeTeamMember,
  type Team,
  type TeamBudget,
  type TeamBudgetStats,
  type TeamMember,
  getBudgetUsagePercentage,
} from '../api-teams'
import { surfaceError } from '../i18n/errorSurface'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const teamId = computed(() => parseInt(route.params.id as string, 10))

const team = ref<Team | null>(null)
const budget = ref<TeamBudget | null>(null)
const budgetStats = ref<TeamBudgetStats | null>(null)
const members = ref<TeamMember[]>([])
const loading = ref(false)

const budgetUsagePercent = computed(() => {
  if (!budgetStats.value) return 0
  return getBudgetUsagePercentage(budgetStats.value)
})

const budgetStatus = computed(() => {
  const percent = budgetUsagePercent.value
  if (percent >= 100) return 'exception'
  if (percent >= 80) return 'warning'
  return 'success'
})

onMounted(() => {
  fetchTeamData()
})

async function fetchTeamData() {
  loading.value = true
  try {
    // Fetch team details
    team.value = await getTeam(teamId.value)
    
    // Fetch budget data
    try {
      budget.value = await getTeamBudget(teamId.value)
      if (budget.value) {
        budgetStats.value = await getTeamBudgetStats(teamId.value)
      }
    } catch (err) {
      // Budget may not be configured, that's okay
      console.warn('Budget not configured for team', err)
    }

    // TODO: Fetch members
    // members.value = await listTeamMembers(teamId.value)
  } catch (err) {
    surfaceError(err, { key: 'teams.errors.loadFailed' })
  } finally {
    loading.value = false
  }
}

function handleBack() {
  // 回列表页：用 vue-router 的**命名路由**（路径只在 router.ts 里定义一次，
  // 与 error-playbook 的 list/detail 配对写法一致）。
  router.push({ name: 'team-list' })
}

function handleEdit() {
  // TODO: Open edit dialog
  ElMessage.info('Edit functionality - to be implemented')
}

function handleAddMember() {
  // TODO: Open add member dialog
  ElMessage.info('Add member functionality - to be implemented')
}

function handleEditBudget() {
  // TODO: Open budget configuration dialog
  ElMessage.info('Edit budget functionality - to be implemented')
}

async function handleRemoveMember(member: TeamMember) {
  try {
    await ElMessageBox.confirm(
      t('teams.confirmRemoveMember'),
      t('common.warning'),
      {
        confirmButtonText: t('common.confirm'),
        cancelButtonText: t('common.cancel'),
        type: 'warning',
      }
    )
    
    await removeTeamMember({
      team_id: teamId.value,
      user_id: member.user_id,
    })
    
    ElMessage.success(t('teams.removeMemberSuccess'))
    await fetchTeamData()
  } catch (err) {
    if (err !== 'cancel') {
      surfaceError(err, { key: 'teams.errors.removeMemberFailed' })
    }
  }
}
</script>

<template>
  <div v-loading="loading" class="team-detail-view">
    <div class="header">
      <el-button :icon="ArrowLeft" @click="handleBack">
        {{ t('common.back') }}
      </el-button>
      <el-button type="primary" :icon="Edit" @click="handleEdit">
        {{ t('teams.editTeam') }}
      </el-button>
    </div>

    <el-card v-if="team" class="team-info">
      <template #header>
        <div class="card-header">
          <span>{{ team.display_name || team.name }}</span>
        </div>
      </template>
      <el-descriptions :column="2" border>
        <el-descriptions-item :label="t('teams.name')">
          {{ team.name }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('teams.displayName')">
          {{ team.display_name || '-' }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('teams.tenant')">
          {{ team.tenant_id }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('teams.ownerId')">
          {{ team.owner_id }}
        </el-descriptions-item>
        <el-descriptions-item :label="t('teams.description')" :span="2">
          {{ team.description || '-' }}
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-card class="members-card">
      <template #header>
        <div class="card-header">
          <span>{{ t('teams.members') }} ({{ members.length }})</span>
          <el-button size="small" type="primary" :icon="Plus" @click="handleAddMember">
            {{ t('teams.addMember') }}
          </el-button>
        </div>
      </template>
      <el-table :data="members" :empty-text="t('teams.noMembers')">
        <el-table-column prop="user_id" :label="t('teams.userId')" width="100" />
        <el-table-column prop="role" :label="t('teams.role')" width="120">
          <template #default="{ row }">
            <el-tag :type="row.role === 'owner' ? 'danger' : row.role === 'admin' ? 'warning' : 'info'">
              {{ t(`teams.roles.${row.role}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="joined_at" :label="t('teams.joinedAt')" />
        <el-table-column :label="t('common.actions')" width="100">
          <template #default="{ row }">
            <el-button
              v-if="row.role !== 'owner'"
              size="small"
              type="danger"
              :icon="Delete"
              @click="handleRemoveMember(row)"
            >
              {{ t('common.remove') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card v-if="budget" class="budget-card">
      <template #header>
        <div class="card-header">
          <span>{{ t('teams.budget') }}</span>
          <el-button size="small" :icon="Edit" @click="handleEditBudget">
            {{ t('teams.editBudget') }}
          </el-button>
        </div>
      </template>
      <div class="budget-content">
        <el-descriptions :column="2" border>
          <el-descriptions-item :label="t('teams.monthlyLimit')">
            {{ budget.monthly_limit_credits.toLocaleString() }} {{ t('teams.credits') }}
          </el-descriptions-item>
          <el-descriptions-item :label="t('teams.enforcementMode')">
            <el-tag :type="budget.enforcement_mode === 'hard' ? 'danger' : 'warning'">
              {{ t(`teams.enforcement.${budget.enforcement_mode}`) }}
            </el-tag>
          </el-descriptions-item>
        </el-descriptions>

        <div v-if="budgetStats" class="budget-stats">
          <h4>{{ t('teams.currentUsage') }}</h4>
          <p>
            {{ budgetStats.spent_credits.toLocaleString() }} / 
            {{ budgetStats.monthly_limit_credits.toLocaleString() }} {{ t('teams.credits') }}
            ({{ budgetUsagePercent.toFixed(1) }}%)
          </p>
          <el-progress
            :percentage="Math.min(100, budgetUsagePercent)"
            :status="budgetStatus"
          />
          <p v-if="budgetStats.over" class="over-budget-warning">
            {{ t('teams.budgetExceeded') }}
          </p>
        </div>
      </div>
    </el-card>
    <el-card v-else class="budget-card">
      <template #header>
        <div class="card-header">
          <span>{{ t('teams.budget') }}</span>
          <el-button size="small" type="primary" :icon="Plus" @click="handleEditBudget">
            {{ t('teams.configureBudget') }}
          </el-button>
        </div>
      </template>
      <el-empty :description="t('teams.noBudgetConfigured')" />
    </el-card>
  </div>
</template>

<style scoped>
.team-detail-view {
  padding: 20px;
}

.header {
  display: flex;
  justify-content: space-between;
  margin-bottom: 20px;
}

.team-info,
.members-card,
.budget-card {
  margin-bottom: 20px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.budget-stats {
  margin-top: 20px;
}

.budget-stats h4 {
  margin-bottom: 10px;
}

.over-budget-warning {
  color: #f56c6c;
  font-weight: bold;
  margin-top: 10px;
}
</style>
