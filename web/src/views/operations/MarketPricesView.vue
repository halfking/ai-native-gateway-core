<script setup lang="ts">
// MarketPricesView.vue — 市场价格采集面（定稿 M3-T1，2026-11-13 A-17 收口）。
// 两块读面：① 平台注册表（958：区域/币种裁定权威 + A-5 我方平台排除，
// M1-T3 的 GET/PUT 管理）；② 同步任务与台账（940 源/落/拒/冲突 对账）。
// 空态文案必须给「为什么空 + 下一步」（设计定稿 §4.4 空状态门）。
// 红线锚点：本页只读对账 + 元数据管理，无任何价格写路径。
import { useI18n } from 'vue-i18n'
import { ref, computed, onMounted } from 'vue'
import {
  getAdminMarketPlatforms,
  putAdminMarketPlatform,
  type MarketPlatformRow,
} from '../../api/market'
import { getAdminMaasMarketPriceSyncStatus, type MarketPriceSyncRun } from '../../api/maas'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import { useActionMessage } from '../../composables/useActionMessage'

const { t } = useI18n()
const { message: actionMsg, error: actionErr, notifySuccess, notifyError, clear: clearActionMsg } = useActionMessage()

const loading = ref(false)
const platforms = ref<MarketPlatformRow[]>([])
const platformsAvailable = ref(true)
const syncRuns = ref<MarketPriceSyncRun[]>([])

async function load() {
  loading.value = true
  try {
    const [reg, sync] = await Promise.all([
      getAdminMarketPlatforms(),
      getAdminMaasMarketPriceSyncStatus(10).catch(() => ({ items: [] })),
    ])
    platforms.value = reg.items || []
    platformsAvailable.value = true
    syncRuns.value = sync.items || []
  } catch {
    platformsAvailable.value = false
    notifyError(t('marketPrices.loadFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- 注册表编辑（M1-T3 PUT：部分更新，未提交字段保持现值） ----
const showEditModal = ref(false)
const saving = ref(false)
const editForm = ref<{ platformId: string; platform_name: string; region: string; native_currency: string; is_ours: boolean; enabled: boolean; notes: string }>(emptyEdit())

function emptyEdit() {
  return { platformId: '', platform_name: '', region: 'overseas', native_currency: 'USD', is_ours: false, enabled: true, notes: '' }
}

function openEdit(p: MarketPlatformRow) {
  clearActionMsg()
  editForm.value = {
    platformId: p.platform_id,
    platform_name: p.platform_name,
    region: p.region,
    native_currency: p.native_currency,
    is_ours: p.is_ours,
    enabled: p.enabled,
    notes: p.notes,
  }
  showEditModal.value = true
}

async function saveEdit() {
  if (!editForm.value.platformId) return
  saving.value = true
  try {
    await putAdminMarketPlatform(editForm.value.platformId, {
      platform_name: editForm.value.platform_name,
      region: editForm.value.region,
      native_currency: editForm.value.native_currency,
      is_ours: editForm.value.is_ours,
      enabled: editForm.value.enabled,
      notes: editForm.value.notes,
    })
    notifySuccess(t('marketPrices.saveOk'))
    showEditModal.value = false
    await load()
  } catch {
    notifyError(t('marketPrices.saveFailed'))
  } finally {
    saving.value = false
  }
}

function regionBadge(r: string) {
  return r === 'overseas' ? 'badge-blue' : 'badge-green'
}

function fmtWhen(s: string) {
  return s ? s.replace('T', ' ').replace('Z', ' UTC') : '—'
}

// 同步台账冲突计数（conflicts 是对象映射）。
function conflictCount(d: { conflicts?: Record<string, unknown> | null }) {
  if (!d || !d.conflicts) return 0
  return Object.keys(d.conflicts).length
}

const platformFields = computed<CardField[]>(() => [
  { key: 'platform_id', label: t('marketPrices.colPlatformId') },
  { key: 'platform_name', label: t('marketPrices.colName') },
  { key: 'region', label: t('marketPrices.colRegion') },
  { key: 'native_currency', label: t('marketPrices.colCurrency') },
  { key: 'is_ours', label: t('marketPrices.colIsOurs') },
  { key: 'enabled', label: t('marketPrices.colEnabled') },
])
</script>

<template>
  <div class="page">
    <h2 class="page-title">{{ t('marketPrices.title') }}</h2>
    <p class="page-sub">{{ t('marketPrices.subtitle') }}</p>

    <!-- ① 平台注册表（958） -->
    <div class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('marketPrices.registryTitle') }}</h3>
        <span class="badge badge-gray">{{ platforms.length }}</span>
      </div>
      <div v-if="!platformsAvailable" class="app-empty-state">
        <p>{{ t('marketPrices.registryUnavailableWhy') }}</p>
        <p class="empty-next">{{ t('marketPrices.registryUnavailableNext') }}</p>
      </div>
      <ResponsiveDataView v-else :rows="platforms" title-key="platform_id" :title-format="row => String(row.platform_name || row.platform_id)" :fields="platformFields" :loading="loading" :empty="!platforms.length">
        <template #table>
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('marketPrices.colPlatformId') }}</th>
                <th>{{ t('marketPrices.colName') }}</th>
                <th>{{ t('marketPrices.colRegion') }}</th>
                <th>{{ t('marketPrices.colCurrency') }}</th>
                <th>{{ t('marketPrices.colIsOurs') }}</th>
                <th>{{ t('marketPrices.colEnabled') }}</th>
                <th>{{ t('marketPrices.colNotes') }}</th>
                <th>{{ t('marketPrices.colUpdatedAt') }}</th>
                <th>{{ t('marketPrices.colActions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in platforms" :key="p.platform_id">
                <td><code class="mono-sm">{{ p.platform_id }}</code></td>
                <td>{{ p.platform_name }}</td>
                <td><span class="badge" :class="regionBadge(p.region)">{{ p.region }}</span></td>
                <td class="num">{{ p.native_currency }}</td>
                <td><span v-if="p.is_ours" class="badge badge-yellow">{{ t('marketPrices.isOursYes') }}</span><span v-else>—</span></td>
                <td><span class="badge" :class="p.enabled ? 'badge-blue' : 'badge-gray'">{{ p.enabled ? t('marketPrices.enabled') : t('marketPrices.disabled') }}</span></td>
                <td class="notes-cell">{{ p.notes || '—' }}</td>
                <td class="mono-sm">{{ fmtWhen(p.updated_at) }}</td>
                <td class="actions"><button class="btn btn-ghost btn-sm" @click="openEdit(p)">{{ t('marketPrices.edit') }}</button></td>
              </tr>
            </tbody>
          </table>
        </template>
        <template #actions="{ row }">
          <button class="btn btn-ghost market-card-action" @click="openEdit(row)">{{ t('marketPrices.edit') }}</button>
        </template>
        <template #empty>
          <p>{{ t('marketPrices.registryEmptyWhy') }}</p>
          <p class="empty-next">{{ t('marketPrices.registryEmptyNext') }}</p>
        </template>
      </ResponsiveDataView>
      <p v-if="platformsAvailable && platforms.length" class="hint-line">{{ t('marketPrices.registryHint') }}</p>
    </div>

    <!-- ② 同步台账（940） -->
    <div class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('marketPrices.syncTitle') }}</h3>
        <span class="badge badge-gray">{{ syncRuns.length }}</span>
      </div>
      <div v-if="!syncRuns.length" class="app-empty-state">
        <p>{{ t('marketPrices.syncEmptyWhy') }}</p>
        <p class="empty-next">{{ t('marketPrices.syncEmptyNext') }}</p>
      </div>
      <table v-else class="data-table">
        <thead>
          <tr>
            <th>{{ t('marketPrices.colRunDate') }}</th>
            <th>{{ t('marketPrices.colSourcePlatforms') }}</th>
            <th>{{ t('marketPrices.colSourceRows') }}</th>
            <th>{{ t('marketPrices.colLanded') }}</th>
            <th>{{ t('marketPrices.colRejected') }}</th>
            <th>{{ t('marketPrices.colConflicts') }}</th>
            <th>{{ t('marketPrices.colCreatedAt') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="s in syncRuns" :key="s.id">
            <td>{{ s.run_date }}</td>
            <td class="num">{{ s.source_platforms }}</td>
            <td class="num">{{ s.source_rows }}</td>
            <td class="num">{{ s.landed_rows }}</td>
            <td class="num" :class="{ 'num-warn': s.rejected_rows > 0 }">{{ s.rejected_rows }}</td>
            <td class="num" :class="{ 'num-warn': conflictCount(s) > 0 }">{{ conflictCount(s) }}</td>
            <td class="mono-sm">{{ fmtWhen(s.created_at) }}</td>
          </tr>
        </tbody>
      </table>
      <p v-if="syncRuns.length" class="hint-line">{{ t('marketPrices.syncHint') }}</p>
    </div>

    <p v-if="actionMsg" class="action-message success">{{ actionMsg }}</p>
    <p v-if="actionErr" class="action-message error">{{ actionErr }}</p>

    <!-- 编辑弹窗 -->
    <div v-if="showEditModal" class="modal-backdrop" @click.self="showEditModal = false">
      <div class="modal market-edit-modal">
        <h3 class="section-title">{{ t('marketPrices.editTitle') }} — {{ editForm.platformId }}</h3>
        <label class="form-row">
          <span>{{ t('marketPrices.colName') }}</span>
          <input v-model="editForm.platform_name" type="text" />
        </label>
        <label class="form-row">
          <span>{{ t('marketPrices.colRegion') }}</span>
          <select v-model="editForm.region">
            <option value="overseas">overseas</option>
            <option value="domestic">domestic</option>
          </select>
        </label>
        <label class="form-row">
          <span>{{ t('marketPrices.colCurrency') }}</span>
          <select v-model="editForm.native_currency">
            <option value="USD">USD</option>
            <option value="CNY">CNY</option>
          </select>
        </label>
        <p class="hint-line">{{ t('marketPrices.pairHint') }}</p>
        <label class="form-row form-check">
          <input v-model="editForm.is_ours" type="checkbox" />
          <span>{{ t('marketPrices.colIsOurs') }}</span>
        </label>
        <label class="form-row form-check">
          <input v-model="editForm.enabled" type="checkbox" />
          <span>{{ t('marketPrices.colEnabled') }}</span>
        </label>
        <label class="form-row">
          <span>{{ t('marketPrices.colNotes') }}</span>
          <textarea v-model="editForm.notes" rows="2"></textarea>
        </label>
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="showEditModal = false">{{ t('marketPrices.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveEdit">{{ t('marketPrices.save') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.market-edit-modal { min-width: 320px; max-width: 460px; }
.form-row { display: flex; align-items: center; gap: 8px; margin: 8px 0; }
.form-row > span { flex: 0 0 110px; font-size: 13px; color: var(--text-secondary, #666); }
.form-row input[type="text"], .form-row select, .form-row textarea { flex: 1; }
.form-check input { flex: 0 0 auto; }
.form-check > span { flex: 1; }
.notes-cell { max-width: 220px; font-size: 12px; color: var(--text-secondary, #666); }
.hint-line { font-size: 12px; color: var(--text-secondary, #666); margin: 6px 0 0; }
.empty-next { font-size: 12px; color: var(--text-secondary, #888); }
.num-warn { color: #c0392b; font-weight: 600; }
.action-message { font-size: 13px; margin-top: 8px; }
.action-message.error { color: #c0392b; }
.action-message.success { color: #1e8e3e; }
</style>
