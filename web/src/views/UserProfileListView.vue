<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getUserProfileList, type UserProfileSummary } from '../api/admin'
import { sortByName } from '../utils/sortByName'
import {
  emptyDegradation,
  isDegraded,
  mergeDegradation,
  type DegradationPayload,
  type DegradationState,
} from '../composables/useDegradationMarker'


// 2026-09-13 P5：补齐模板使用的 el-* 组件注册（修复运行时 resolve 失败）
import { ElButton, ElCard, ElInput, ElPagination, ElTable, ElTableColumn } from 'element-plus'
const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const users = ref<UserProfileSummary[]>([])
const total = ref(0)
const search = ref('')
const currentPage = ref(1)
const pageSize = 20

// 2026-10-03：两处此前都在骗用户。
//
// ① `catch { users.value = [] }` —— 无参 catch 连 console 都不记。请求失败时
//    页面渲染成「没有用户」，既没有 console 错误也没有任何提示。
//    本任务的原始要求之一就是「逐页检查 console 错误并修正」；静默吞掉
//    比报错更糟 —— 它让问题不可诊断。
// ② 后端在 schema 落后时返回 200 + 空列表（session_owners 是可选聚合表，
//    本地库未建）。修复前这与「真的没有用户」在页面上完全同形。
const loadError = ref('')
const degraded = ref<DegradationState>(emptyDegradation())

// 2026-10-03：首列就是 owner_user，之前完全按后端返回顺序。
// ⚠ 限制要说清：本页是**服务端分页**（limit/offset 传给后端），所以这里排的是
// 「当前这一页」而不是全量。跨页的全局有序要后端加 order_by 才能保证 ——
// 页内有序 ≠ 全局有序，别把前者当成后者写进任何文档。
const sortedUsers = computed(() => sortByName(users.value))

onMounted(() => void load())

async function load() {
  loading.value = true
  loadError.value = ''
  degraded.value = emptyDegradation()
  try {
    const res = await getUserProfileList({
      limit: pageSize,
      offset: (currentPage.value - 1) * pageSize,
      search: search.value || undefined,
    })
    users.value = res.users || []
    total.value = res.total
    if (isDegraded(res)) {
      degraded.value = mergeDegradation(
        degraded.value,
        t('sessions.userProfile.title'),
        res as unknown as DegradationPayload,
      )
    }
  } catch (e) {
    // 记 console（可诊断）+ 记 error（用户可见），两者都不能少。
    console.error('user profile list load failed:', e)
    users.value = []
    total.value = 0
    loadError.value = e instanceof Error ? e.message : String(e)
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

      <!--
        降级 vs 失败 vs 空，三者必须可区分。
        修复前三种情况渲染出完全相同的页面：一个空表格 + 「没有用户」。

        ⚠️ degraded 时**刻意不显示**下面的「没有用户」：服务端没算出来，
        说「没有用户」是把「不知道」讲成「知道」。这是本页此前最有害的一处。
      -->
      <div v-if="degraded.active" class="alert alert-warning" role="status">
        <strong>{{ t('dataLifecycle.usageCost.degraded.title') }}</strong>
        {{ t('dataLifecycle.usageCost.degraded.hint') }}
        <ul class="degraded-reasons">
          <li v-for="r in degraded.reasons" :key="r">{{ r }}</li>
        </ul>
      </div>

      <div v-if="loadError" class="alert alert-danger">{{ loadError }}</div>

      <el-table v-loading="loading" :data="sortedUsers" stripe>
        <el-table-column :label="t('sessions.userProfile.ownerUser')" prop="owner_user" min-width="160" />
        <el-table-column :label="t('sessions.userProfile.sessionCount')" prop="session_count" width="100" align="right" />
        <el-table-column :label="t('sessions.userProfile.requestCount')" prop="total_requests" width="100" align="right" />
        <el-table-column :label="t('sessions.userProfile.totalCost')" prop="total_cost_usd" width="120" align="right">
          <template #default="scope">
            ${{ (scope?.row?.total_cost_usd ?? 0).toFixed(4) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.avgCostPerSession')" prop="avg_cost_per_session" width="120" align="right">
          <template #default="scope">
            ${{ (scope?.row?.avg_cost_per_session ?? 0).toFixed(4) }}
          </template>
        </el-table-column>
        <el-table-column :label="t('sessions.userProfile.endUserCount')" prop="end_user_count" width="110" align="right" />
        <el-table-column :label="t('sessions.userProfile.firstSeenAt')" prop="first_seen_at" width="170" />
        <el-table-column :label="t('sessions.userProfile.lastSeenAt')" prop="last_seen_at" width="170" />
        <el-table-column :label="t('sessions.userProfile.actionDetail')" width="100" fixed="right">
          <template #default="scope">
            <el-button size="small" type="primary" link @click="goProfile(scope?.row?.owner_user)">
              {{ t('sessions.userProfile.actionDetail') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <!--
        「没有用户」只在**确实拿到了一份成功的、非降级的空列表**时显示。
        降级/失败时都不能说「没有用户」—— 那是把「不知道」说成「知道」。
      -->
      <div
        v-if="!loading && users.length === 0 && !degraded.active && !loadError"
        class="empty"
      >{{ t('sessions.userProfile.empty') }}</div>

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
