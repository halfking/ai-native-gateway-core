<script setup lang="ts">
// TaskProfileView.vue — 任务档案面板（v2 规划 P0③，2026-09-24；2026-10-02 整合轮收编）。
//
// taskprofile 插件模块的运营门面：档案注册表 + 修正统计 + 分层建议 + apply
// （写 task_type_tier_config）+ overlay reload。原独立页 /routing-v2/task-profile
// 已 redirect 到 /routing-v2/auto-ops?tab=profiles；原四张大卡（版本/schema/
// 类型数/修正数）收敛为一行信息 chips，导出/导入闭环与标注工作台面板不重复
// （工作台工具栏保留 CSV 出入口，本面板按窗口期导出）。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getTaskProfile,
  getTaskTypeCorrectionStats,
  applyTierConfig,
  reloadTaskProfile,
  exportCorrections,
  type TaskProfileView as TaskProfileViewModel,
  type CorrectionStatsResponse,
} from '../api/taskProfile'
import DataTable from '../components/ui/DataTable.vue'
import { exportFile } from '../utils/exportFile'

const { t } = useI18n()

const profile = ref<TaskProfileViewModel | null>(null)
const stats = ref<CorrectionStatsResponse | null>(null)
const loading = ref(false)
const error = ref('')
const notice = ref('')
const sinceDays = ref(30)
const applying = ref(false)
const reloading = ref(false)
const exporting = ref(false)

type ProfileRow = TaskProfileViewModel['profiles'][number] & {
  rate: number
  total: number
  corrected: number
  suggestTier: string
  suggestSource: string
}

const rows = computed<ProfileRow[]>(() => {
  if (!profile.value) return []
  return profile.value.profiles.map((p) => {
    const cs = stats.value?.stats?.[p.task_type]
    const sug = stats.value?.suggestions?.[p.task_type]
    return {
      ...p,
      total: cs?.total ?? 0,
      rate: cs?.correction_rate ?? 0,
      corrected: cs?.corrected ?? 0,
      suggestTier: sug?.tier ?? p.suggestion?.tier ?? p.preferred_tier,
      suggestSource: sug?.tier_source ?? p.suggestion?.tier_source ?? '',
    }
  }).sort((a, b) => b.rate - a.rate || a.task_type.localeCompare(b.task_type))
})

// 修正总量按窗口内 corrected 计数求和（原实现只数"修正率>0 的类型数"，
// 语义与标签不符——整合轮修正为真实修正条数）。
const totalCorrected = computed(() =>
  rows.value.reduce((acc, r) => acc + r.corrected, 0)
)

// 建议层 ≠ 当前层的类型数：提示有多少档位变更待人工裁决。
const pendingSuggestions = computed(() =>
  rows.value.filter((r) => r.suggestTier !== r.preferred_tier).length
)

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const [p, s] = await Promise.all([
      getTaskProfile(),
      getTaskTypeCorrectionStats(sinceDays.value),
    ])
    profile.value = p
    stats.value = s
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('taskProfile.loadFailed')
  } finally {
    loading.value = false
  }
}

async function apply() {
  if (!window.confirm(t('taskProfile.action.applyConfirm'))) return
  applying.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await applyTierConfig([])
    notice.value = r.applied.length === 0
      ? t('taskProfile.action.applyNone')
      : t('taskProfile.action.applyDone', {
          types: r.applied.map((a) => `${a.task_type}→${a.preferred_tier}`).join(', '),
        })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    applying.value = false
  }
}

async function reload() {
  if (!window.confirm(t('taskProfile.action.reloadConfirm'))) return
  reloading.value = true
  error.value = ''
  notice.value = ''
  try {
    const r = await reloadTaskProfile()
    notice.value = t('taskProfile.action.reloadDone', { version: r.registry_version })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    reloading.value = false
  }
}

async function exportCsv() {
  exporting.value = true
  error.value = ''
  try {
    const { filename, content } = await exportCorrections(sinceDays.value)
    const blob = new Blob([content], { type: 'text/csv; charset=utf-8' })
    // 2026-10-05（UI规范 19 §3.1）：走 exportFile 降级链（分享面→壳桥→blob）。
    await exportFile({ filename, blob })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    exporting.value = false
  }
}

function rateClass(rate: number): string {
  if (rate >= 0.3) return 'rate-high'
  if (rate >= 0.1) return 'rate-mid'
  return 'rate-low'
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <div class="panel-toolbar">
      <div v-if="profile" class="info-chips">
        <span class="info-chip" :title="t('taskProfile.registry.version')">
          {{ t('taskProfile.registry.version') }} <code class="mono">{{ profile.registry_version }}</code>
        </span>
        <span class="info-chip">
          {{ t('taskProfile.registry.types') }} <strong>{{ profile.profiles.length }}</strong>
        </span>
        <span class="info-chip">
          {{ t('taskProfile.registry.correctedTotal') }} <strong>{{ totalCorrected }}</strong>
        </span>
        <span v-if="pendingSuggestions > 0" class="info-chip info-chip--warn">
          {{ t('taskProfile.registry.pendingSuggestions') }} <strong>{{ pendingSuggestions }}</strong>
        </span>
      </div>
      <div class="toolbar-actions">
        <label class="inline-label">
          {{ t('taskProfile.action.days') }}
          <select v-model.number="sinceDays" class="select-sm" @change="load">
            <option :value="7">7</option>
            <option :value="14">14</option>
            <option :value="30">30</option>
            <option :value="90">90</option>
          </select>
        </label>
        <button class="btn btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('taskProfile.refreshing') : t('taskProfile.refresh') }}
        </button>
        <button class="btn btn-sm" :disabled="reloading" @click="reload">
          {{ reloading ? t('taskProfile.status.reloading') : t('taskProfile.action.reload') }}
        </button>
        <button class="btn btn-sm" :disabled="exporting" @click="exportCsv">
          {{ exporting ? t('taskProfile.status.exporting') : t('taskProfile.action.exportCsv') }}
        </button>
        <button class="btn btn-primary btn-sm" :disabled="applying" @click="apply">
          {{ applying ? t('taskProfile.status.applying') : t('taskProfile.action.apply') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
    <div v-if="notice" class="alert alert-success" role="status">{{ notice }}</div>

    <DataTable :loading="loading" :empty="!loading && rows.length === 0" :empty-text="t('taskProfile.noData')" min-width="960px">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('taskProfile.table.taskType') }}</th>
            <th>{{ t('taskProfile.table.description') }}</th>
            <th>{{ t('taskProfile.table.tier') }}</th>
            <th>{{ t('taskProfile.table.fallbacks') }}</th>
            <th class="col-num">{{ t('taskProfile.table.minConf') }}</th>
            <th class="col-num">{{ t('taskProfile.table.total') }}</th>
            <th class="col-num">{{ t('taskProfile.table.rate') }}</th>
            <th>{{ t('taskProfile.table.suggestion') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.task_type">
            <td class="mono">{{ r.task_type }}</td>
            <td class="desc-cell">{{ r.description }}</td>
            <td><span class="tier-badge" :class="`tier-${r.preferred_tier}`">{{ r.preferred_tier }}</span></td>
            <td class="mono dim">{{ r.fallback_tiers.join(' → ') || '—' }}</td>
            <td class="mono col-num">{{ r.min_confidence.toFixed(2) }}</td>
            <td class="mono col-num">{{ r.total }}</td>
            <td class="col-num">
              <span :class="rateClass(r.rate)">{{ (r.rate * 100).toFixed(1) }}%</span>
            </td>
            <td>
              <span
                v-if="r.suggestTier !== r.preferred_tier"
                class="tier-badge tier-changed"
                :title="r.suggestSource"
              >{{ r.suggestTier }} ↑</span>
              <span v-else class="dim">{{ r.suggestTier }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </DataTable>
  </div>
</template>

<style scoped>
/* 嵌入面板：宿主 AutoRoutingOpsView 提供外层 padding 与纵向间距 */
.panel-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 0.75rem;
  flex-wrap: wrap;
  margin-bottom: 0.75rem;
}

.info-chips {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.info-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.2rem 0.6rem;
  border-radius: 999px;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  font-size: 0.78rem;
  color: var(--text-muted);
}

.info-chip strong {
  color: var(--text);
}

.info-chip--warn {
  background: var(--warning-bg);
  border-color: var(--warning);
  color: var(--warning-strong);
}

.info-chip--warn strong {
  color: var(--warning-strong);
}

.toolbar-actions {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.inline-label {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.select-sm {
  padding: 0.2rem 0.4rem;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--card);
  color: var(--text);
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}

.data-table th,
.data-table td {
  padding: 0.5rem 0.6rem;
  border-bottom: 1px solid var(--border);
  text-align: left;
  vertical-align: top;
}

.data-table th {
  font-weight: 600;
  color: var(--text-muted);
  white-space: nowrap;
}

.data-table tbody tr:hover {
  background: var(--bg-hover);
}

.col-num {
  text-align: right;
}

.mono {
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  white-space: nowrap;
}

.dim {
  color: var(--text-muted);
}

.desc-cell {
  min-width: 14rem;
  max-width: 26rem;
}

.tier-badge {
  display: inline-block;
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  border: 1px solid var(--border);
}

.tier-tier-a {
  background: var(--danger-bg);
  border-color: var(--danger);
}

.tier-tier-b {
  background: var(--info-bg);
  border-color: var(--accent);
}

.tier-tier-c {
  background: var(--success-bg);
  border-color: var(--success);
}

.tier-changed {
  background: var(--warning-bg);
  border-color: var(--warning);
  cursor: help;
}

.rate-high {
  color: var(--danger);
  font-weight: 600;
}

.rate-mid {
  color: var(--warning);
}

.rate-low {
  color: var(--text-muted);
}
</style>
