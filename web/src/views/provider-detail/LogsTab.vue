<script setup lang="ts">
import ModelIdentityChip from '../../components/model/ModelIdentityChip.vue'
import { computed, ref, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getProviderLogs, getProviderCredentials, type ProviderLogEntry, type ProviderCredential } from '../../api'
import ModelPicker from '../../components/ModelPicker.vue'
import { useFormat } from '../../i18n/useFormat'
// 2026-10-04 H6 第二条切片：呈现形态与加载方式是**两个独立维度**（规范 03 §1）。
//   桌面 → 表格 + 页码（既有逻辑逐字保留）
//   compact → 卡片 + 连续加载
import { useWindowClass } from '../../composables/useWindowClass'
import { createHyperPages } from '../../lib/shell/hyper/hyperPages'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../../components/ui/HyperLoadMore.vue'
import type { CardField } from '../../components/ui/CardList.vue'

const props = defineProps<{ providerId: number }>()
const router = useRouter()
const { t: td } = useI18n()
const pl = (k: string, params?: Record<string, unknown>): string => td(`providerDetail.logs.${k}` as never, params as never)
/** 模型身份三段术语的真源在 ModelIdentityChip 的词条里，这里复用而不是另写一份。 */
const mi = (k: string): string => td(`models.modelIdentity.${k}` as never) as string
const { fmtDateTime, fmtNumber } = useFormat()
const { isCompact } = useWindowClass()

const PAGE_SIZE = 50

const logs = ref<ProviderLogEntry[]>([])
const credentials = ref<ProviderCredential[]>([])
// 凭据下拉是**另一个**数据源，失败要单独讲（理由见 loadCredentials 的注释）
const credentialsError = ref('')
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const error = ref('')
const modelFilter = ref('')
const credentialId = ref<number | ''>('')
const successFilter = ref<'all' | 'true' | 'false'>('all')
const errorKindFilter = ref('')
const hours = ref(24)

function goCanonical(name?: string | null) {
  const q = (name || '').trim()
  if (q) router.push({ path: '/models', query: { q } })
}

function timeRange() {
  const end = new Date()
  const start = new Date(end.getTime() - hours.value * 3600 * 1000)
  return { from_ts: start.toISOString(), to_ts: end.toISOString() }
}

/**
 * 筛选条件的**唯一真源**。两条加载路径都从这里取参数 ——
 * 各写一份的话，改一个筛选器漏改另一条路径，症状是
 * 「桌面筛了，compact 没筛」这类幽灵 bug。
 */
function filterBody() {
  const range = timeRange()
  return {
    model: modelFilter.value.trim() || undefined,
    credential_id: credentialId.value === '' ? undefined : Number(credentialId.value),
    success: successFilter.value === 'all' ? undefined : successFilter.value === 'true',
    error_kind: errorKindFilter.value.trim() || undefined,
    from_ts: range.from_ts,
    to_ts: range.to_ts,
  }
}

/**
 * 稳定行 id。`request_id` 可为 null，此时用字段组合兜底。
 * **不能用数组下标** —— 连续加载的第 2 页下标 0 是另一行，
 * 去重会把它当成与第 1 页的第 0 行相同（或反之），表现为「行随机消失」。
 */
function logRowKey(l: ProviderLogEntry): string {
  if (l.request_id) return l.request_id
  return [l.ts, l.client_model, l.credential_id, l.prompt_tokens, l.completion_tokens, l.latency_ms].join('|')
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const resp = await getProviderLogs(props.providerId, {
      ...filterBody(),
      page: page.value,
      page_size: PAGE_SIZE,
    })
    logs.value = resp.items
    total.value = resp.total
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pl('loadFailed')
  } finally {
    loading.value = false
  }
}

// ── compact 连续加载 ──────────────────────────────────────────────────────
// 独立于上面的页码状态机：两者不共享 ref、不互相写。
// 见规范 13 §1「布局模式与数据加载方式是两个独立维度」。
const continuous = createHyperPages<ProviderLogEntry>({
  pageSize: PAGE_SIZE,
  rowKey: logRowKey,
  // API 字段是 items，`createHyperPages` 约定是 rows —— 在这里映射，
  // 不要为了对齐而改 api 层的返回形状（那会影响全部调用方）。
  fetchPage: async (p) => {
    const resp = await getProviderLogs(props.providerId, { ...filterBody(), page: p, page_size: PAGE_SIZE })
    return { rows: resp.items, total: resp.total }
  },
})

/** 实际展示的行：按档位二选一。 */
const rows = computed<ProviderLogEntry[]>(() => (isCompact.value ? continuous.rows.value : logs.value))

/** 尾部控件要显示的状态。桌面为 paged，不参与连续加载语义。 */
const continuousState = computed(() => continuous.state.value)

async function loadCredentials() {
  credentialsError.value = ''
  try {
    credentials.value = await getProviderCredentials(props.providerId)
  } catch (e) {
    // 2026-10-03：原来 `catch { credentials.value = [] }`。凭据下拉失败后
    // 只剩「全部凭据」，用户按某个凭据筛出零条日志，结论会是
    // 「这个凭据没有调用记录」——而真相是「凭据没加载出来」。
    // 与 RequestLogsView.loadKeys 同一形状；那处已有 filter-error 横幅，这里同理。
    // ⚠️ 不能复用 `error`：那个是主列表的错误，混在一起会让人以为日志列表也挂了。
    credentials.value = []
    console.error('Failed to load provider credentials:', e)
    credentialsError.value = pl('credentialsLoadFailed')
  }
}

/**
 * 触发一次查询。两条路径都要处理，且**顺序不能反**：
 * 先 `invalidate()`（作废所有在途结果）再 `loadFirst()`，
 * 否则旧请求可能在新请求之后落地。
 */
function runQuery() {
  if (isCompact.value) {
    continuous.invalidate()
    void continuous.loadFirst()
    return
  }
  page.value = 1
  void load()
}

function resetFilters() {
  modelFilter.value = ''
  credentialId.value = ''
  successFilter.value = 'all'
  errorKindFilter.value = ''
  hours.value = 24
  runQuery()
}

function search() {
  runQuery()
}

function credLabel(l: ProviderLogEntry) {
  if (l.credential_label) return l.credential_label
  return l.credential_id != null ? `#${l.credential_id}` : '—'
}

function fmtTs(ts: string | null) { return ts ? fmtDateTime(ts) : '—' }
function token(v: number | null | undefined) { return v == null ? '—' : fmtNumber(v) }
function fmtCost(v: number | null) { return v != null ? '$' + Number(v).toFixed(4) : null }
function fmtLatency(v: number | null) { return v != null ? v + 'ms' : null }

const totalPages = computed(() => Math.ceil(total.value / PAGE_SIZE))
const showPager = computed(() => !isCompact.value && total.value > PAGE_SIZE)

/**
 * 「模型」列表头 = chip 内部那三段（客户端 / 标准 / 出站）。
 * 原先是模板里的硬编码中文 `… / 标准 / 出站`（HEAD 遗留），
 * 现与 chip 共用 `models.modelIdentity`，避免表头与行内标签各说各话。
 * 分隔符用「 / 」：它是对称标点，RTL 语种同样成立，不进词条。
 */
const identityHeader = computed(() => [mi('client'), mi('canonical'), mi('outbound')].join(' / '))

// ── compact 卡片字段 ──────────────────────────────────────────────────────
const cardFields = computed<CardField[]>(() => [
  {
    key: 'success',
    label: pl('table.result'),
    type: 'badge',
    format: (v) => (v == null ? null : v ? pl('resultOk') : pl('resultFail')),
  },
  {
    key: 'client_model',
    label: pl('table.clientModel'),
    format: (v) => (v == null ? null : String(v)),
  },
  { key: 'credential_id', label: pl('table.credential'), format: (v) => credLabel({ credential_id: v as number | null } as ProviderLogEntry) },
  { key: 'error_kind', label: pl('table.errorKind') },
  {
    key: 'prompt_tokens',
    label: pl('table.tokens'),
    type: 'metric',
    align: 'end',
    format: (v, row) => `${token(v as number | null)} / ${token(row.completion_tokens as number | null)}`,
  },
  { key: 'cost_usd', label: pl('table.cost'), type: 'metric', align: 'end', format: (v) => fmtCost(v as number | null) },
  { key: 'latency_ms', label: pl('table.latency'), type: 'metric', align: 'end', format: (v) => fmtLatency(v as number | null) },
  { key: 'ts', label: pl('table.time'), format: (v) => fmtTs(v == null ? null : String(v)) },
])

/**
 * 卡头：时间必须本地化。
 * `titleKey` 本身不给格式化钩子，卡头会直接渲染 `ts` 的原始值 ——
 * 那是一条裸 ISO 串 `2026-10-05T01:15:30.000Z`。2026-10-05 H6 第四条切片补上
 * `CardList.titleFormat` 后一并修掉（见 CardList 同名 prop 的说明）。
 *
 * 形参用 `Record<string, unknown>`：组件契约是那个形状，写成 `ProviderLogEntry`
 * 会被逆变检查拒掉。
 */
function cardTitle(row: Record<string, unknown>): string {
  return fmtTs(row.ts == null ? null : String(row.ts))
}

onMounted(() => { loadCredentials(); search() })
watch(() => props.providerId, () => { loadCredentials(); resetFilters() })
</script>

<template>
  <div>
    <div class="compact-filter-bar">
      <span class="cf-hint" :title="pl('filterHintTitle')">{{ pl('filterTitle') }}</span>
      <select v-model.number="hours" class="cf-select cf-hours" :title="pl('hoursLabel')" @change="search">
        <option :value="1">{{ pl('hours1') }}</option>
        <option :value="6">{{ pl('hours6') }}</option>
        <option :value="24">{{ pl('hours24') }}</option>
        <option :value="168">{{ pl('hours168') }}</option>
      </select>
      <div class="cf-grow" style="min-width:200px">
        <ModelPicker
          v-model="modelFilter"
          :placeholder="pl('modelPlaceholder')"
          :title="pl('modelPickerTitle')"
          @update:model-value="search"
        />
      </div>
      <select v-model="credentialId" class="cf-select cf-cred" :title="pl('credentialTitle')" @change="search">
        <option value="">{{ pl('credentialAll') }}</option>
        <option v-for="c in credentials" :key="c.id" :value="c.id">
          #{{ c.id }} {{ c.label || '—' }}
        </option>
      </select>
      <select v-model="successFilter" class="cf-select cf-status" :title="pl('credentialTitle')" @change="search">
        <option value="all">{{ pl('resultAll') }}</option>
        <option value="true">{{ pl('resultOk') }}</option>
        <option value="false">{{ pl('resultFail') }}</option>
      </select>
      <input
        v-model="errorKindFilter"
        class="cf-input cf-medium"
        :placeholder="pl('errorKindPlaceholder')"
        @keyup.enter="search"
      />
      <button class="btn btn-primary btn-sm" @click="search" :disabled="loading">{{ loading ? pl('searchLoading') : pl('search') }}</button>
      <button class="btn btn-ghost btn-sm" @click="resetFilters" :disabled="loading">{{ pl('reset') }}</button>
      <span class="cf-meta">{{ pl('total', { n: total }) }}</span>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <!-- 凭据下拉的范围未知：与上面的列表错误分开说，
         否则「日志列表正常 + 凭据下拉空」会被读成「这个凭据没有记录」。 -->
    <div v-if="credentialsError" class="alert alert-warning" role="status">{{ credentialsError }}</div>

    <!--
      页码条：仅桌面。compact 走连续加载，由底部 HyperLoadMore 承担，
      两条路径**不同时出现** —— 屏幕上不该有两个「加载更多」语义。
    -->
    <div v-if="showPager" class="pager">
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="page--; load()">{{ pl('pagerPrev') }}</button>
      <span class="cf-meta">{{ page }} / {{ totalPages }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="page++; load()">{{ pl('pagerNext') }}</button>
    </div>

    <!--
      2026-10-04 H6：双模板 + 双加载方式。
      · 桌面 → 表格（表头/表体/ModelIdentityChip/徽章逐字未改）+ 页码
      · compact → 卡片 + 连续加载 + 底部 sentinel
      两态由容器统一裁定，桌面表格密度不受影响。
    -->
    <ResponsiveDataView
      :rows="rows"
      title-key="ts"
      :title-format="cardTitle"
      :fields="cardFields"
      table-min-width="900px"
      :loading="loading && !isCompact"
      :empty="rows.length === 0 && !loading"
      :empty-text="pl('empty')"
    >
      <template #table>
    <div class="card" style="overflow-x:auto">
      <table v-if="logs.length" class="data-table logs-table">
        <thead>
          <tr>
            <th>{{ pl('table.time') }}</th>
            <th>{{ pl('table.credential') }}</th>
            <th>{{ identityHeader }}</th>
            <th>{{ pl('table.result') }}</th>
            <th>{{ pl('table.errorKind') }}</th>
            <th>{{ pl('table.tokens') }}</th>
            <th>{{ pl('table.cost') }}</th>
            <th>{{ pl('table.latency') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(l, i) in logs" :key="l.request_id || i">
            <td>{{ fmtTs(l.ts) }}</td>
            <td class="cell-muted" :title="l.credential_id != null ? pl('credentialTitleAttr', { id: l.credential_id }) : ''">{{ credLabel(l) }}</td>
            <td>
              <ModelIdentityChip
                compact
                :client-model="l.client_model"
                :canonical-name="l.canonical_name || l.canonical_model"
                :outbound-model="l.outbound_model || l.provider_model"
                @click-canonical="goCanonical(l.canonical_name || l.canonical_model)"
              />
            </td>
            <td>
              <span class="badge" :class="l.success ? 'badge-green' : 'badge-red'">{{ l.success ? pl('resultOk') : pl('resultFail') }}</span>
            </td>
            <td class="cell-muted">{{ l.error_kind || '—' }}</td>
            <td>{{ token(l.prompt_tokens) }} / {{ token(l.completion_tokens) }}</td>
            <td>{{ l.cost_usd != null ? '$' + Number(l.cost_usd).toFixed(4) : '—' }}</td>
            <td>{{ l.latency_ms != null ? l.latency_ms + 'ms' : '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && logs.length === 0" class="empty-hint">{{ pl('empty') }}</div>
    </div>
      </template>
    </ResponsiveDataView>

    <!-- 连续加载尾部：仅 compact -->
    <HyperLoadMore
      v-if="isCompact"
      :state="continuousState"
      :has-more="continuous.hasMore.value"
      :loaded-count="continuous.loadedCount.value"
      @load-more="continuous.loadNext()"
      @retry="continuous.retry()"
    />

    <div v-if="showPager" class="pager">
      <button class="btn btn-ghost btn-sm" :disabled="page <= 1" @click="page--; load()">{{ pl('pagerPrev') }}</button>
      <span class="cf-meta">{{ page }} / {{ totalPages }}</span>
      <button class="btn btn-ghost btn-sm" :disabled="page >= totalPages" @click="page++; load()">{{ pl('pagerNext') }}</button>
    </div>
  </div>
</template>

<style scoped>
.logs-table {
  width: 100%;
  font-size: 12px;
}
.cell-muted {
  color: var(--muted);
}
.empty-hint {
  color: var(--muted);
  text-align: center;
  padding: 24px;
  font-size: 13px;
}
.pager {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-top: 12px;
}
</style>
