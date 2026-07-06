<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getUserProfileList, type UserProfileSummary } from '../api/admin'

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const users = ref<UserProfileSummary[]>([])
const total = ref(0)
const search = ref('')
const currentPage = ref(1)
const pageSize = 20

onMounted(() => void load())

async function load() {
  loading.value = true
  try {
    const res = await getUserProfileList({
      limit: pageSize,
      offset: (currentPage.value - 1) * pageSize,
      search: search.value || undefined,
    })
    users.value = res.users || []
    total.value = res.total
  } catch {
    users.value = []
  } finally {
    loading.value = false
  }
}

function goProfile(owner: string) {
  router.push(`/admin/session-analytics/users/${owner}`)
}

function onSearch() {
  currentPage.value = 1
  void load()
}
</script>

<template>
  <div class="user-profile-list">
    <div class="page-header">
      <h2>{{ t('sessions.userProfile.title') }}</h2>
    </div>

    <el-card>
      <template #header>
        <div class="card-header-toolbar">
          <el-input
            v-model="search"
            :placeholder="t('sessions.userProfileSearchPlaceholder')"
            style="width: 280px"
            clearable
            @clear="onSearch"
            @keyup.enter="onSearch"
          />
        </div>
      </template>

      <el-table v-loading="loading" :data="users" stripe>
        <el-table-column :label="t('sessions.userProfile.ownerUser')" prop="owner_user" min-width="160" />
        <el-table-column :label="t('sessions.userProfile.sessionCount')" prop="session_count" width="100" align="right" />
        <el-table-column :label="t('sessions.userProfile.requestCount')" prop="total_requests" width="100" align="right" />
        <el-table-column :label="t('sessions.userProfile.totalCost')" prop="total_cost_usd" width="120" align="right">
          <template #default="{ row }">
            ${{ row.total_cost_usd.toFixed(4) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.avgCostPerSession')" prop="avg_cost_per_session" width="120" align="right">
          <template #default="{ row }">
            ${{ row.avg_cost_per_session.toFixed(4) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.endUserCount')" prop="end_user_count" width="110" align="right" />
        <el-table-column :label="t('sessions.userProfile.firstSeenAt')" prop="first_seen_at" width="170" />
        <el-table-column :label="t('sessions.userProfile.lastSeenAt')" prop="last_seen_at" width="170" />
        <el-table-column :label="t('sessions.userProfile.actionDetail')" width="100" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="primary" link @click="goProfile(row.owner_user)">
              {{ t('sessions.userProfile.actionDetail') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-if="!loading && users.length === 0" class="empty">{{ t('sessions.userProfile.empty') }}</div>

      <div v-if="total > pageSize" class="pagination-wrap">
        <el-pagination
          v-model:current-page="currentPage"
          :page-size="pageSize"
          :total="total"
          layout="prev, pager, next"
          @current-change="load"
        />
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.user-profile-list { padding: 20px; }
.page-header { margin-bottom: 16px; }
.card-header-toolbar { display: flex; align-items: center; gap: 12px; }
.pagination-wrap { margin-top: 16px; display: flex; justify-content: center; }
</style>
