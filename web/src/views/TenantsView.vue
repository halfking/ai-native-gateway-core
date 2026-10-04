<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '../utils/datetime'
import { sortByName } from '../utils/sortByName'
import { localeRef } from '../i18n'
import { computed, ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getTenantsAdmin, TENANT_STATUSES, TENANT_STATUS_COLORS } from '../api'
import type { Tenant } from '../api'
import TenantCreateDialog from './TenantCreateDialog.vue'
import FeeCostCell from '../components/FeeCostCell.vue'
import { isPlatformOpsView } from '../store'
import { useTenantStatusLabel } from '../composables/useTenantStatusLabel'
// 2026-09-13 P3：页头收敛到 ui/PageHeader；表格容器收敛到 ui/DataTable（方案 §4.5.2/§4.5.5）
import PageHeader from '../components/ui/PageHeader.vue'
// 2026-10-04 H6：桌面端保留 DataTable 包裹；compact 端由 ResponsiveDataView 提供卡片形态。
//   桌面表格结构**逐字未改**（桌面零回归红线），只换外层容器。
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t } = useI18n()
const { tenantStatusLabel } = useTenantStatusLabel()

const router = useRouter()
const tenants = ref<Tenant[]>([])
const loading = ref(false)
const error = ref('')
const filterStatus = ref<string>('')
const showCreate = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    // 2026-10-03：按名称排序（老板要求的查找性排序）。原先直接用后端返回顺序，
    // 实际是按 tenant code 排的（acme/chenb/debug/…），而列表首列显示的是
    // 租户名 —— 看到什么顺序就找不到什么，得先在脑子里把名字翻译成 code。
    const rows = await getTenantsAdmin(filterStatus.value || undefined)
    tenants.value = sortByName(rows)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('tenants.list.loadFailed')
  } finally {
    loading.value = false
  }
}

function statusColor(s: string) {
  return TENANT_STATUS_COLORS[s] || 'badge-gray'
}

function statusLabel(s: string) {
  return tenantStatusLabel(s)
}

function fmtTime(s: string) {
  if (!s) return '-'
  return formatDateTime(s, { locale: localeRef.value })
}

function fmtNum(n?: number) {
  if (n == null) return '-'
  return n.toLocaleString()
}

const showCost = isPlatformOpsView()

/**
 * 桌面表格的最小列宽。沿用改造前 `DataTable min-width="760px"` 的同一数值。
 * 必须在脚本里声明是因为模板插槽与 prop 都要用，且它**只有一个真源**。
 */
const TABLE_MIN_WIDTH = '760px'

function goDetail(t: Tenant) {
  router.push(`/tenants/${t.code}`)
}

// ── compact 卡片形态的字段描述 ────────────────────────────────────────────
// 与桌面表格读**同一份** `tenants` 数组，只是换了呈现。切换形态不重新打接口。
//
// 每个字段都带 `format`：`CardList` 默认只做 String(row[key])，
// 没有格式化钩子就渲染不出千分位、本地化日期与状态译名。
const cardFields = computed<CardField[]>(() => [
  {
    key: 'status',
    label: t('tenants.list.colStatus'),
    type: 'badge',
    format: (v) => (v == null ? null : statusLabel(String(v))),
  },
  {
    key: 'user_count',
    label: t('tenants.list.colUsers'),
    type: 'metric',
    align: 'end',
    format: (v) => fmtNum(v as number | undefined),
  },
  {
    key: 'api_key_count',
    label: t('tenants.list.colKeys'),
    type: 'metric',
    align: 'end',
    format: (v) => fmtNum(v as number | undefined),
  },
  {
    key: 'total_requests',
    label: t('tenants.list.colRequests'),
    type: 'metric',
    align: 'end',
    format: (v) => fmtNum(v as number | undefined),
  },
  {
    key: 'contact_email',
    label: t('tenants.list.colContact'),
    format: (v) => (v == null ? null : String(v)),
  },
  {
    key: 'created_at',
    label: t('tenants.list.colCreated'),
    format: (v) => fmtTime(v == null ? '' : String(v)),
  },
])

/** 卡片副标题用 code：它是进入详情后要用的标识，比状态更适合当第二识别物。 */
const cardSubtitleKeys = ['code']

/** 整卡可点 = 进入详情，与表格行点击同一意图。 */
function onCardClick(tenant: Tenant) {
  goDetail(tenant)
}

onMounted(load)
</script>

<template>
  <div class="tenants-page">
    <PageHeader :title="t('tenants.list.title')">
      <template #actions>
        <button class="btn btn-primary" @click="showCreate = true">{{ t('tenants.list.createBtn') }}</button>
      </template>
    </PageHeader>

    <div v-if="error" class="alert alert-danger" style="margin-bottom:12px">{{ error }}</div>

    <div class="filters">
      <label>{{ t('tenants.list.statusLabel') }}:</label>
      <select v-model="filterStatus" @change="load">
        <option value="">{{ t('tenants.list.allStatuses') }}</option>
        <option v-for="s in TENANT_STATUSES" :key="s" :value="s">{{ statusLabel(s) }}</option>
      </select>
    </div>

    <div v-if="loading" class="loading">{{ t('tenants.list.loading') }}</div>

    <!--
      2026-10-04 H6：双模板容器。
      · compact  → 自动出卡片（切换钮不渲染，见 03 §2.1）
      · 桌面     → 出下面的表格，**表头/表体/行样式逐字未改**（桌面零回归红线）
      · 三态（loading/empty）由容器统一裁定，不会出现「表格有骨架、卡片空白」

      ★ 唯一一处桌面可见变化，如实记录：空态由「表格内的一行居中文字」
        换成了共享的 `<EmptyState>`（同文案、同 40px padding、同 muted 色，
        差别仅是显式 13px 字号）。这是「两套空态体系并存」的历史欠账，
        本次顺带收敛一处；**不是**表格密度或列宽的变化。
    -->
    <ResponsiveDataView
      v-else
      :rows="tenants"
      title-key="name"
      :subtitle-keys="cardSubtitleKeys"
      :fields="cardFields"
      :table-min-width="TABLE_MIN_WIDTH"
      :clickable="true"
      :clickable-label="t('tenants.list.colName')"
      :empty="tenants.length === 0"
      :empty-text="t('tenants.list.empty')"
      @row-click="onCardClick"
    >
      <template #table>
    <table class="table tenants-table" style="width:100%">
      <thead>
        <tr>
          <th>{{ t('tenants.list.colName') }}</th>
          <th>{{ t('tenants.list.colCode') }}</th>
          <th>{{ t('tenants.list.colStatus') }}</th>
          <th>{{ t('tenants.list.colUsers') }}</th>
          <th>{{ t('tenants.list.colKeys') }}</th>
          <th>{{ t('tenants.list.colCost7d') }}</th>
          <th>{{ t('tenants.list.colRequests') }}</th>
          <th>{{ t('tenants.list.colContact') }}</th>
          <th>{{ t('tenants.list.colCreated') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="tenant in tenants"
          :key="tenant.code"
          class="tenant-row"
          tabindex="0"
          @click="goDetail(tenant)"
          @keydown.enter="goDetail(tenant)"
        >
          <td><strong>{{ tenant.name }}</strong></td>
          <td><code>{{ tenant.code }}</code></td>
          <td><span class="badge" :class="statusColor(tenant.status)">{{ statusLabel(tenant.status) }}</span></td>
          <td>{{ fmtNum(tenant.user_count) }}</td>
          <td>{{ fmtNum(tenant.api_key_count) }}</td>
          <td>
            <FeeCostCell
              :credits="tenant.credits_7d"
              :cost-usd="tenant.cost_7d_usd"
              :show-cost="showCost"
            />
          </td>
          <td>{{ fmtNum(tenant.total_requests) }}</td>
          <td>{{ tenant.contact_email || '-' }}</td>
          <td class="mono">{{ fmtTime(tenant.created_at) }}</td>
        </tr>
      </tbody>
    </table>
      </template>
    </ResponsiveDataView>

    <TenantCreateDialog v-if="showCreate" @close="showCreate = false" @created="load" />
  </div>
</template>

<style scoped>
.filters {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}
.filters label { font-size: 13px; color: var(--muted); }
.filters select {
  padding: 4px 8px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
  width: auto;
  max-width: 200px;
}
.tenants-table .tenant-row {
  cursor: pointer;
}
.tenants-table .tenant-row:hover {
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.tenants-table .tenant-row:focus-visible {
  outline: 2px solid var(--accent-h);
  outline-offset: -2px;
}
.badge-purple { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent-h); }
.badge-blue { background: var(--info-bg); color: var(--accent); }
.badge-red { background: var(--danger-bg); color: var(--danger); }
.badge-green { background: var(--success-bg); color: var(--success); }
.badge-yellow { background: var(--warning-bg); color: var(--warning); }
.badge-gray { background: var(--neutral-bg); color: var(--muted); }
.mono { font-family: 'SF Mono', 'Fira Code', monospace; font-size: 12px; }
.loading {
  text-align: center;
  padding: 40px;
  color: var(--muted);
}
</style>
