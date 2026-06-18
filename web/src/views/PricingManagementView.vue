<template>
  <div class="pricing-management">
    <div class="pm-header">
      <h2>成本价格</h2>
      <div class="pm-actions">
        <button class="btn btn-sm" @click="fetchData" :disabled="loading">
          {{ loading ? '加载中…' : '刷新' }}
        </button>
        <button class="btn btn-sm" @click="exportCsv">导出 CSV</button>
        <button v-if="!readOnly" class="btn btn-sm btn-primary" @click="showImport = true">导入 CSV</button>
        <button v-if="!readOnly" class="btn btn-sm btn-success" @click="autoInherit">自动继承</button>
      </div>
    </div>

    <div v-if="readOnly" class="alert alert-info" style="margin-bottom:12px">
      📖 您是租户管理员，当前为只读模式。上游成本价格仅供查看，不能修改或导入。
    </div>

    <div class="pm-summary" v-if="summary">
      <div class="stat-card">
        <div class="stat-val">{{ summary.total_offers }}</div>
        <div class="stat-label">总 Offer</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.priced_in }}</div>
        <div class="stat-label">已定价（输入）</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.priced_out }}</div>
        <div class="stat-label">已定价（输出）</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.cny_offers }}</div>
        <div class="stat-label">CNY</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.usd_offers }}</div>
        <div class="stat-label">USD</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.free_offers }}</div>
        <div class="stat-label">免费</div>
      </div>
      <div class="stat-card" :class="coverageClass">
        <div class="stat-val">{{ coveragePct }}%</div>
        <div class="stat-label">覆盖率 ({{ summary.canonical_covered }}/{{ summary.total_canonical }})</div>
      </div>
    </div>

    <!-- Coverage by credential -->
    <div v-if="coverageByCred.length" class="pm-coverage">
      <h3>按凭据覆盖率</h3>
      <div class="cov-row" v-for="row in coverageByCred" :key="row.credential_id">
        <div class="cov-name">{{ row.credential_name }}</div>
        <div class="cov-bar">
          <div class="cov-fill" :style="{ width: row.pct + '%', background: row.color }"></div>
        </div>
        <div class="cov-stats">
          {{ row.priced }}/{{ row.total }} ({{ row.pct }}%)
          <span v-if="row.token_plan > 0" class="cov-tag cov-tag-tp">+{{ row.token_plan }} token_plan</span>
        </div>
      </div>
    </div>

    <!-- Filter Bar -->
    <div class="pm-filters">
      <div class="filter-group">
        <ModelPicker
          v-model="filters.search"
          placeholder="选择模型筛选…"
          title="成本价格 · 模型筛选"
          @update:model-value="onFilterChange"
        />
      </div>
      <div class="filter-group">
        <select v-model="filters.provider_id" @change="onFilterChange" class="filter-select">
          <option value="">全部供应商</option>
          <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.display_name }}</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.billing_mode" @change="onFilterChange" class="filter-select">
          <option value="">全部计费</option>
          <option value="token">Token（每百万）</option>
          <option value="token_plan">Token 套餐（小米/火山）</option>
          <option value="code_plan">Code 套餐</option>
          <option value="per_token">按 Token（旧）</option>
          <option value="per_request">按次</option>
          <option value="monthly">包月</option>
          <option value="free">免费</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.currency" @change="onFilterChange" class="filter-select">
          <option value="">全部币种</option>
          <option value="CNY">CNY</option>
          <option value="USD">USD</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.pricing_status" @change="onFilterChange" class="filter-select">
          <option value="">全部定价</option>
          <option value="priced">已定价</option>
          <option value="unpriced">未定价</option>
          <option value="free">免费</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.availability" @change="onFilterChange" class="filter-select">
          <option value="">全部状态</option>
          <option value="true">可用</option>
          <option value="false">不可用</option>
        </select>
      </div>
      <button class="btn btn-sm" @click="clearFilters">清空</button>
    </div>

    <!-- View Tabs -->
    <div class="pm-tabs">
      <button :class="['tab', { active: viewMode === 'tree' }]" @click="viewMode = 'tree'">树形视图</button>
      <button :class="['tab', { active: viewMode === 'table' }]" @click="viewMode = 'table'">表格视图</button>
    </div>

    <!-- Tree View -->
    <div v-if="viewMode === 'tree'" class="pm-body">
      <div class="pm-tree">
        <div v-for="fam in filteredFamilies" :key="fam.canonical_name" class="tree-family">
          <div class="tree-family-header" @click="toggle(fam.canonical_name)">
            <span class="arrow" :class="{ open: expanded[fam.canonical_name] }">&#9654;</span>
            <span class="family-name">{{ fam.canonical_name }}</span>
            <span class="family-meta">{{ fam.family }} | {{ fam.offers.length }} 个 offer</span>
          </div>
          <div v-if="expanded[fam.canonical_name]" class="tree-offers">
            <div
              v-for="offer in fam.offers"
              :key="offer.offer_id"
              class="tree-offer"
              :class="{ selected: selectedOffer?.offer_id === offer.offer_id, 'has-price': offer.unit_price_in_per_1m != null }"
              @click="selectOffer(offer)"
            >
              <span class="offer-provider">{{ offer.provider_name || offer.catalog_code || '?' }}</span>
              <span class="offer-cred">{{ offer.credential_label }}</span>
              <span class="offer-tier" :class="'tier-' + offer.routing_tier">T{{ offer.routing_tier }}</span>
              <span class="offer-avail" :class="{ ok: offer.available }">{{ offer.available ? '✓' : '✗' }}</span>
              <span class="offer-price" v-if="offer.unit_price_in_per_1m != null">
                {{ offer.unit_price_in_per_1m }}/{{ offer.unit_price_out_per_1m }} {{ offer.currency }}
              </span>
              <span class="offer-price free" v-else-if="offer.billing_mode === 'free'">免费</span>
              <span class="offer-price pending" v-else>待定价</span>
              <span class="offer-window" v-if="offer.window_requests">
                W:{{ offer.window_success_rate ? (offer.window_success_rate * 100).toFixed(0) + '%' : '-' }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Table View -->
    <div v-if="viewMode === 'table'" class="pm-table-container">
      <table class="pm-table">
        <thead>
          <tr>
            <th><input type="checkbox" @change="toggleSelectAll" :checked="allSelected" /></th>
            <th @click="sortTable('canonical_name')" class="sortable">模型 {{ sortIcon('canonical_name') }}</th>
            <th @click="sortTable('provider_name')" class="sortable">供应商 {{ sortIcon('provider_name') }}</th>
            <th>凭据</th>
            <th @click="sortTable('unit_price_in_per_1m')" class="sortable">输入价 {{ sortIcon('unit_price_in_per_1m') }}</th>
            <th @click="sortTable('unit_price_out_per_1m')" class="sortable">输出价 {{ sortIcon('unit_price_out_per_1m') }}</th>
            <th @click="sortTable('currency')" class="sortable">币种 {{ sortIcon('currency') }}</th>
            <th @click="sortTable('billing_mode')" class="sortable">计费 {{ sortIcon('billing_mode') }}</th>
            <th @click="sortTable('routing_tier')" class="sortable">Tier {{ sortIcon('routing_tier') }}</th>
            <th @click="sortTable('weight')" class="sortable">权重 {{ sortIcon('weight') }}</th>
            <th @click="sortTable('available')" class="sortable">可用 {{ sortIcon('available') }}</th>
            <th @click="sortTable('success_rate')" class="sortable">成功率 {{ sortIcon('success_rate') }}</th>
            <th @click="sortTable('p95_latency_ms')" class="sortable">P95 {{ sortIcon('p95_latency_ms') }}</th>
            <th>窗口</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in tableItems" :key="item.offer_id" :class="{ selected: selectedRows.has(item.offer_id) }">
            <td><input type="checkbox" :checked="selectedRows.has(item.offer_id)" @change="toggleRow(item.offer_id)" /></td>
            <td class="model-name">{{ item.canonical_name || item.raw_model_name }}</td>
            <td>{{ item.provider_name }}</td>
            <td>{{ item.credential_label }}</td>
            <td class="price">{{ item.unit_price_in_per_1m != null ? item.unit_price_in_per_1m : '-' }}</td>
            <td class="price">{{ item.unit_price_out_per_1m != null ? item.unit_price_out_per_1m : '-' }}</td>
            <td>{{ item.currency || '-' }}</td>
            <td>{{ item.billing_mode || '-' }}</td>
            <td><span class="tier-badge" :class="'tier-' + item.routing_tier">{{ item.routing_tier }}</span></td>
            <td>{{ item.weight }}</td>
            <td><span :class="{ ok: item.available }">{{ item.available ? '✓' : '✗' }}</span></td>
            <td>{{ item.success_rate != null ? (item.success_rate * 100).toFixed(0) + '%' : '-' }}</td>
            <td>{{ item.p95_latency_ms ? item.p95_latency_ms + 'ms' : '-' }}</td>
            <td>{{ item.window_requests ? `W:${item.window_success_rate ? (item.window_success_rate * 100).toFixed(0) + '%' : '-'}` : '-' }}</td>
            <td>
              <button class="btn btn-xs" @click="editFromTable(item)">编辑</button>
              <button class="btn btn-xs" @click="copyPriceFromTable(item)">复制</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div class="pm-pagination">
        <button class="btn btn-sm" :disabled="tablePage <= 1" @click="tablePage--; fetchTable()">上一页</button>
        <span>第 {{ tablePage }} / {{ Math.ceil(tableTotal / tablePageSize) }} 页</span>
        <button class="btn btn-sm" :disabled="tablePage >= Math.ceil(tableTotal / tablePageSize)" @click="tablePage++; fetchTable()">下一页</button>
        <select v-model.number="tablePageSize" @change="tablePage = 1; fetchTable()" class="page-size-select">
          <option :value="25">25</option>
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
        </select>
      </div>
      <div class="pm-bulk-actions" v-if="selectedRows.size > 0">
        <span>已选 {{ selectedRows.size }} 项</span>
        <button class="btn btn-sm" @click="pasteToSelected">粘贴价格到所选</button>
      </div>
    </div>

    <!-- Import Modal -->
    <div v-if="showImport" class="modal-overlay" @click.self="showImport = false">
      <div class="modal" @click.stop>
        <h3>导入定价 CSV</h3>
        <p>CSV 列：offer_id, unit_price_in_per_1m, unit_price_out_per_1m, currency, billing_mode</p>
        <input type="file" accept=".csv" @change="onFileChange" />
        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="importCsv" :disabled="!importFile || importing">
            {{ importing ? '导入中…' : '导入' }}
          </button>
          <button class="btn btn-sm" @click="showImport = false">取消</button>
          <span v-if="importMsg">{{ importMsg }}</span>
        </div>
      </div>
    </div>

    <!-- Edit Drawer (tree + table) -->
    <Teleport to="body">
      <div v-if="selectedOffer" class="drawer-backdrop" @click="closeDetail">
        <div class="drawer-panel card drawer-panel-wide" @click.stop>
          <div class="drawer-header">
            <div>
              <h3 class="drawer-title">{{ selectedOffer.raw_model_name }}</h3>
              <p class="detail-sub">{{ selectedOffer.provider_name }} / {{ selectedOffer.credential_label }}</p>
            </div>
            <button class="btn btn-ghost btn-sm" @click="closeDetail">✕ 关闭</button>
          </div>

          <div class="drawer-body">
            <!-- Routing Params -->
            <div class="detail-section">
              <h4>路由参数</h4>
              <div class="params-grid">
                <div class="param">
                  <label>Tier</label>
                  <span class="param-val">{{ selectedOffer.routing_tier }}</span>
                </div>
                <div class="param">
                  <label>权重</label>
                  <span class="param-val">{{ selectedOffer.weight }}</span>
                </div>
                <div class="param">
                  <label>可用</label>
                  <span class="param-val" :class="{ ok: selectedOffer.available }">{{ selectedOffer.available ? '是' : '否' }}</span>
                </div>
                <div class="param">
                  <label>成功率</label>
                  <span class="param-val">{{ selectedOffer.success_rate != null ? (selectedOffer.success_rate * 100).toFixed(1) + '%' : '-' }}</span>
                </div>
                <div class="param">
                  <label>P95 延迟</label>
                  <span class="param-val">{{ selectedOffer.p95_latency_ms ? selectedOffer.p95_latency_ms + 'ms' : '-' }}</span>
                </div>
                <div class="param">
                  <label>最近活跃</label>
                  <span class="param-val">{{ selectedOffer.last_seen_at ? new Date(selectedOffer.last_seen_at).toLocaleString() : '-' }}</span>
                </div>
              </div>
            </div>

            <!-- Window Stats -->
            <div class="detail-section" v-if="selectedOffer.window_requests">
              <h4>窗口统计（10 分钟）</h4>
              <div class="params-grid">
                <div class="param">
                  <label>请求数</label>
                  <span class="param-val">{{ selectedOffer.window_requests }}</span>
                </div>
                <div class="param">
                  <label>成功率</label>
                  <span class="param-val">{{ selectedOffer.window_success_rate ? (selectedOffer.window_success_rate * 100).toFixed(1) + '%' : '-' }}</span>
                </div>
                <div class="param">
                  <label>P95 延迟</label>
                  <span class="param-val">{{ selectedOffer.window_latency_p95_ms ? selectedOffer.window_latency_p95_ms.toFixed(0) + 'ms' : '-' }}</span>
                </div>
              </div>
            </div>

            <!-- Pricing Form -->
            <div class="detail-section">
              <h4>定价</h4>
              <div class="form-group">
                <label>输入价（每百万 Token）</label>
                <input v-model.number="editForm.unit_price_in_per_1m" type="number" step="0.001" />
              </div>
              <div class="form-group">
                <label>输出价（每百万 Token）</label>
                <input v-model.number="editForm.unit_price_out_per_1m" type="number" step="0.001" />
              </div>
              <div class="form-group">
                <label>缓存读价（每百万）</label>
                <input v-model.number="editForm.cache_read_price_per_1m" type="number" step="0.001" />
              </div>
              <div class="form-group">
                <label>缓存写价（每百万）</label>
                <input v-model.number="editForm.cache_write_price_per_1m" type="number" step="0.001" />
              </div>
              <div class="form-group">
                <label>币种</label>
                <select v-model="editForm.currency">
                  <option value="CNY">CNY</option>
                  <option value="USD">USD</option>
                </select>
              </div>
              <div class="form-group">
                <label>计费模式</label>
                <select v-model="editForm.billing_mode">
                  <option value="per_token">按 Token</option>
                  <option value="per_request">按次</option>
                  <option value="monthly">包月</option>
                  <option value="free">免费</option>
                </select>
              </div>
            </div>

            <div class="detail-balance" v-if="selectedOffer.balance_usd != null">
              <strong>余额：</strong> {{ selectedOffer.balance_usd }} {{ selectedOffer.balance_currency || 'USD' }}
              <span v-if="selectedOffer.pool_group"> | 资源池：{{ selectedOffer.pool_group }}</span>
            </div>

            <div class="form-actions">
              <button class="btn btn-primary btn-sm" @click="saveOffer" :disabled="saving">
                {{ saving ? '保存中…' : '保存' }}
              </button>
              <button class="btn btn-sm" @click="copyPrice">复制价格</button>
              <button class="btn btn-sm" @click="pastePrice" :disabled="!clipboard">粘贴</button>
              <span v-if="saveMsg" class="save-msg" :class="{ ok: saveOk }">{{ saveMsg }}</span>
            </div>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Auto Inherit Modal -->
    <div v-if="showInheritPreview" class="modal-overlay" @click.self="showInheritPreview = false">
      <div class="modal" @click.stop>
        <h3>自动继承定价</h3>
        <p>{{ inheritPreview.would_inherit }} 个 offer 将从同供应商+模型继承定价。</p>
        <div class="inherit-details" v-if="inheritPreview.details">
          <div v-for="d in inheritPreview.details.slice(0, 20)" :key="d.target_offer_id" class="inherit-row">
            Offer #{{ d.target_offer_id }} ← Offer #{{ d.source_offer_id }}
          </div>
          <p v-if="inheritPreview.details.length > 20">… 另有 {{ inheritPreview.details.length - 20 }} 项</p>
        </div>
        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="confirmInherit" :disabled="inheriting">
            {{ inheriting ? '继承中…' : '确认继承' }}
          </button>
          <button class="btn btn-sm" @click="showInheritPreview = false">取消</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { store, isReadOnlyMode, authBearer } from '../store'
import ModelPicker from '../components/ModelPicker.vue'

const readOnly = computed(() => isReadOnlyMode())

const API = '/api/pricing'

interface Offer {
  offer_id: number
  raw_model_name: string
  unit_price_in_per_1m: number | null
  unit_price_out_per_1m: number | null
  cache_read_price_per_1m: number | null
  cache_write_price_per_1m: number | null
  currency: string | null
  billing_mode: string | null
  pricing_source: string | null
  pricing_updated_at: string | null
  available: boolean
  routing_tier: number
  weight: number
  last_seen_at: string | null
  success_rate: number | null
  p95_latency_ms: number | null
  credential_id: number
  credential_label: string
  credential_status: string
  balance_usd: number | null
  balance_currency: string | null
  pool_group: string | null
  provider_id: number
  provider_name: string
  catalog_code: string
  catalog_display_name: string
  window_success_rate: number | null
  window_latency_p95_ms: number | null
  window_requests: number | null
}

interface Family {
  canonical_id: number | null
  canonical_name: string
  family: string | null
  modality: string | null
  offers: Offer[]
}

interface Provider {
  id: number
  display_name: string
}

const loading = ref(false)
const saving = ref(false)
const families = ref<Family[]>([])
const summary = ref<any>(null)
const providers = ref<Provider[]>([])
const expanded = ref<Record<string, boolean>>({})
const selectedOffer = ref<Offer | null>(null)
const editForm = ref<any>({})
const saveMsg = ref('')
const saveOk = ref(false)
const showImport = ref(false)
const importFile = ref<File | null>(null)
const importing = ref(false)
const importMsg = ref('')
const viewMode = ref<'tree' | 'table'>('tree')
const clipboard = ref<Offer | null>(null)

// Table view state
const tableItems = ref<Offer[]>([])
const tableTotal = ref(0)
const tablePage = ref(1)
const tablePageSize = ref(50)
const tableSortBy = ref('canonical_name')
const tableSortDir = ref('asc')
const selectedRows = ref<Set<number>>(new Set())

// Filter state
const filters = ref({
  search: '',
  provider_id: '',
  billing_mode: '',
  currency: '',
  pricing_status: '',
  availability: '',
})

// Auto inherit state
const showInheritPreview = ref(false)
const inheritPreview = ref<any>({})
const inheriting = ref(false)

// Coverage state (2026-06-12)
const coverageByCred = ref<Array<{
  credential_id: number
  credential_name: string
  priced: number
  total: number
  pct: number
  token_plan: number
  color: string
}>>([])

const coveragePct = computed(() => {
  if (!summary.value || !summary.value.total_canonical) return 0
  return Math.round((summary.value.canonical_covered / summary.value.total_canonical) * 100)
})

const coverageClass = computed(() => {
  if (coveragePct.value >= 90) return 'stat-card-good'
  if (coveragePct.value >= 50) return 'stat-card-warn'
  return 'stat-card-bad'
})

// Helper to lookup credential label by id (uses already-fetched providers list)
function c_label(credId: number): string {
  const o: any = (tableItems.value as any[]).find((x: any) => x.credential_id === credId)
  if (o && (o.credential_label || o.credential_name)) return o.credential_label || o.credential_name
  return `cred-${credId}`
}

function buildCoverageByCred(offers: Offer[]) {
  const byCred = new Map<number, { name: string; total: number; priced: number; token_plan: number }>()
  for (const o of offers) {
    const credName = (o as any).credential_label || (o as any).credential_name || c_label(o.credential_id)
    const e = byCred.get(o.credential_id) || { name: credName, total: 0, priced: 0, token_plan: 0 }
    e.total += 1
    if (o.unit_price_in_per_1m && Number(o.unit_price_in_per_1m) > 0) e.priced += 1
    if (o.billing_mode === 'token_plan') e.token_plan += 1
    byCred.set(o.credential_id, e)
  }
  const arr: any[] = []
  for (const [id, e] of byCred) {
    const pct = e.total > 0 ? Math.round((e.priced / e.total) * 100) : 0
    let color = '#ef4444'  // red
    if (pct >= 90) color = '#10b981'  // green
    else if (pct >= 50) color = '#f59e0b'  // amber
    arr.push({
      credential_id: id,
      credential_name: e.name,
      priced: e.priced,
      total: e.total,
      pct,
      token_plan: e.token_plan,
      color,
    })
  }
  arr.sort((a, b) => b.pct - a.pct)
  coverageByCred.value = arr
}

const authHeaders = () => ({
  'Authorization': `Bearer ${authBearer()}`,
  'Content-Type': 'application/json',
})

const filteredFamilies = computed(() => {
  if (!filters.value.search) return families.value
  const q = filters.value.search.toLowerCase()
  return families.value.filter(f =>
    f.canonical_name.toLowerCase().includes(q) ||
    f.family?.toLowerCase().includes(q) ||
    f.offers.some(o => o.provider_name?.toLowerCase().includes(q))
  )
})

const allSelected = computed(() => {
  return tableItems.value.length > 0 && tableItems.value.every(item => selectedRows.value.has(item.offer_id))
})

function buildFilterParams() {
  const params = new URLSearchParams()
  if (filters.value.search) params.set('search', filters.value.search)
  if (filters.value.provider_id) params.set('provider_id', filters.value.provider_id)
  if (filters.value.billing_mode) params.set('billing_mode', filters.value.billing_mode)
  if (filters.value.currency) params.set('currency', filters.value.currency)
  if (filters.value.pricing_status) params.set('pricing_status', filters.value.pricing_status)
  if (filters.value.availability) params.set('availability', filters.value.availability)
  return params.toString()
}

async function fetchProviders() {
  try {
    const res = await fetch('/api/providers', { headers: authHeaders() })
    providers.value = await res.json()
  } catch (e) {
    console.error('Failed to fetch providers', e)
  }
}

async function fetchTree() {
  loading.value = true
  try {
    const params = buildFilterParams()
    const [treeRes, sumRes] = await Promise.all([
      fetch(`${API}/tree?${params}`, { headers: authHeaders() }),
      fetch(`${API}/summary`, { headers: authHeaders() }),
    ])
    const treeData = await treeRes.json()
    families.value = treeData.families || []
    summary.value = await sumRes.json()
    // Flatten tree leaves to compute coverage
    // Family shape: { canonical_id, canonical_name, family, modality, offers: Offer[] }
    const flat: any[] = []
    for (const f of families.value) {
      for (const o of (f.offers || [])) {
        flat.push({
          ...o,
          credential_id: o.credential_id,
          credential_label: o.credential_label || c_label(o.credential_id),
        })
      }
    }
    if (flat.length) buildCoverageByCred(flat as any)
  } catch (e) {
    console.error('Failed to fetch pricing tree', e)
  } finally {
    loading.value = false
  }
}

async function fetchTable() {
  loading.value = true
  try {
    const params = new URLSearchParams(buildFilterParams())
    params.set('page', tablePage.value.toString())
    params.set('page_size', tablePageSize.value.toString())
    params.set('sort_by', tableSortBy.value)
    params.set('sort_dir', tableSortDir.value)
    const res = await fetch(`${API}/table?${params}`, { headers: authHeaders() })
    const data = await res.json()
    tableItems.value = data.items || []
    tableTotal.value = data.total || 0
    buildCoverageByCred(tableItems.value)
  } catch (e) {
    console.error('Failed to fetch table', e)
  } finally {
    loading.value = false
  }
}

async function fetchData() {
  await fetchProviders()
  if (viewMode.value === 'tree') {
    await fetchTree()
  } else {
    await fetchTable()
  }
}

function onFilterChange() {
  if (viewMode.value === 'tree') {
    fetchTree()
  } else {
    tablePage.value = 1
    fetchTable()
  }
}

function clearFilters() {
  filters.value = {
    search: '',
    provider_id: '',
    billing_mode: '',
    currency: '',
    pricing_status: '',
    availability: '',
  }
  onFilterChange()
}

function toggle(name: string) {
  expanded.value[name] = !expanded.value[name]
}

function selectOffer(offer: Offer) {
  selectedOffer.value = offer
  editForm.value = {
    unit_price_in_per_1m: offer.unit_price_in_per_1m,
    unit_price_out_per_1m: offer.unit_price_out_per_1m,
    cache_read_price_per_1m: offer.cache_read_price_per_1m,
    cache_write_price_per_1m: offer.cache_write_price_per_1m,
    currency: offer.currency || 'USD',
    billing_mode: offer.billing_mode || 'per_token',
  }
  saveMsg.value = ''
}

function closeDetail() {
  selectedOffer.value = null
  saveMsg.value = ''
}

async function saveOffer() {
  if (!selectedOffer.value) return
  saving.value = true
  saveMsg.value = ''
  try {
    const res = await fetch(`${API}/bulk-update`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({
        updates: [{ offer_id: selectedOffer.value.offer_id, ...editForm.value }],
      }),
    })
    const data = await res.json()
    if (data.updated > 0) {
      saveMsg.value = '已保存'
      saveOk.value = true
      await fetchData()
    } else {
      saveMsg.value = '无变更'
      saveOk.value = false
    }
  } catch (e) {
    saveMsg.value = '保存失败'
    saveOk.value = false
  } finally {
    saving.value = false
  }
}

function copyPrice() {
  if (selectedOffer.value) {
    clipboard.value = { ...selectedOffer.value }
    saveMsg.value = '价格已复制'
    saveOk.value = true
  }
}

function pastePrice() {
  if (!clipboard.value || !selectedOffer.value) return
  editForm.value = {
    unit_price_in_per_1m: clipboard.value.unit_price_in_per_1m,
    unit_price_out_per_1m: clipboard.value.unit_price_out_per_1m,
    cache_read_price_per_1m: clipboard.value.cache_read_price_per_1m,
    cache_write_price_per_1m: clipboard.value.cache_write_price_per_1m,
    currency: clipboard.value.currency || 'USD',
    billing_mode: clipboard.value.billing_mode || 'per_token',
  }
  saveMsg.value = '已粘贴，请点击保存生效'
  saveOk.value = true
}

function copyPriceFromTable(item: Offer) {
  clipboard.value = { ...item }
  saveMsg.value = '价格已复制'
  saveOk.value = true
}

function editFromTable(item: Offer) {
  selectOffer(item)
}

async function pasteToSelected() {
  if (!clipboard.value || selectedRows.value.size === 0) return
  saving.value = true
  try {
    const updates = Array.from(selectedRows.value).map(offer_id => ({
      offer_id,
      unit_price_in_per_1m: clipboard.value!.unit_price_in_per_1m,
      unit_price_out_per_1m: clipboard.value!.unit_price_out_per_1m,
      cache_read_price_per_1m: clipboard.value!.cache_read_price_per_1m,
      cache_write_price_per_1m: clipboard.value!.cache_write_price_per_1m,
      currency: clipboard.value!.currency,
      billing_mode: clipboard.value!.billing_mode,
    }))
    const res = await fetch(`${API}/bulk-update`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ updates }),
    })
    const data = await res.json()
    saveMsg.value = `已更新 ${data.updated} 个 offer`
    saveOk.value = true
    selectedRows.value.clear()
    await fetchTable()
  } catch (e) {
    saveMsg.value = '批量更新失败'
    saveOk.value = false
  } finally {
    saving.value = false
  }
}

function toggleSelectAll(e: Event) {
  const checked = (e.target as HTMLInputElement).checked
  if (checked) {
    tableItems.value.forEach(item => selectedRows.value.add(item.offer_id))
  } else {
    selectedRows.value.clear()
  }
}

function toggleRow(offerId: number) {
  if (selectedRows.value.has(offerId)) {
    selectedRows.value.delete(offerId)
  } else {
    selectedRows.value.add(offerId)
  }
}

function sortTable(column: string) {
  if (tableSortBy.value === column) {
    tableSortDir.value = tableSortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    tableSortBy.value = column
    tableSortDir.value = 'asc'
  }
  fetchTable()
}

function sortIcon(column: string) {
  if (tableSortBy.value !== column) return ''
  return tableSortDir.value === 'asc' ? '↑' : '↓'
}

async function autoInherit() {
  try {
    const res = await fetch(`${API}/auto-inherit`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ dry_run: true }),
    })
    inheritPreview.value = await res.json()
    showInheritPreview.value = true
  } catch (e) {
    console.error('Failed to preview auto-inherit', e)
  }
}

async function confirmInherit() {
  inheriting.value = true
  try {
    const res = await fetch(`${API}/auto-inherit`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ dry_run: false }),
    })
    const data = await res.json()
    showInheritPreview.value = false
    saveMsg.value = `已继承 ${data.inherited} 个 offer`
    saveOk.value = true
    await fetchData()
  } catch (e) {
    saveMsg.value = '自动继承失败'
    saveOk.value = false
  } finally {
    inheriting.value = false
  }
}

async function exportCsv() {
  const res = await fetch(`${API}/export`, { headers: authHeaders() })
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'pricing_export.csv'
  a.click()
  URL.revokeObjectURL(url)
}

function onFileChange(e: Event) {
  const input = e.target as HTMLInputElement
  importFile.value = input.files?.[0] || null
}

async function importCsv() {
  if (!importFile.value) return
  importing.value = true
  importMsg.value = ''
  try {
    const fd = new FormData()
    fd.append('file', importFile.value)
    const res = await fetch(`${API}/import`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${authBearer()}`, 'Content-Type': 'application/json' },
      body: fd,
    })
    const data = await res.json()
    importMsg.value = `已更新 ${data.updated} 个 offer`
    showImport.value = false
    await fetchData()
  } catch (e) {
    importMsg.value = '导入失败'
  } finally {
    importing.value = false
  }
}

watch(viewMode, () => {
  if (viewMode.value === 'table' && tableItems.value.length === 0) {
    fetchTable()
  }
})

onMounted(fetchData)
</script>

<style scoped>
.pricing-management { padding: 20px; color: #e0e0e0; }
.pm-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.pm-header h2 { margin: 0; color: #fff; }
.pm-actions { display: flex; gap: 8px; }
.pm-summary { display: flex; gap: 12px; margin-bottom: 16px; flex-wrap: wrap; }
.stat-card { background: #1e1e2e; border: 1px solid #333; border-radius: 8px; padding: 12px 20px; min-width: 100px; text-align: center; }
.stat-card-good { border-color: #10b981; }
.stat-card-warn { border-color: #f59e0b; }
.stat-card-bad { border-color: #ef4444; }
.stat-val { font-size: 24px; font-weight: 700; color: #89b4fa; }
.stat-label { font-size: 12px; color: #888; margin-top: 4px; }

/* Coverage by credential */
.pm-coverage { background: #1e1e2e; border: 1px solid #333; border-radius: 8px; padding: 16px; margin-bottom: 16px; }
.pm-coverage h3 { margin: 0 0 12px 0; font-size: 14px; color: #fff; }
.cov-row { display: grid; grid-template-columns: 200px 1fr 200px; gap: 12px; align-items: center; margin-bottom: 6px; font-size: 12px; }
.cov-name { color: #cdd6f4; font-weight: 500; }
.cov-bar { background: #2a2a3e; height: 16px; border-radius: 4px; overflow: hidden; border: 1px solid #444; }
.cov-fill { height: 100%; transition: width 0.3s; }
.cov-stats { color: #888; text-align: right; }
.cov-tag { display: inline-block; margin-left: 6px; padding: 1px 6px; border-radius: 3px; font-size: 10px; font-weight: 600; }
.cov-tag-tp { background: #45475a; color: #f9e2af; }

/* Filters */
.pm-filters { display: flex; gap: 8px; margin-bottom: 16px; flex-wrap: wrap; align-items: center; }
.filter-group { flex: 1; min-width: 150px; }
.filter-input, .filter-select { width: 100%; padding: 6px 10px; background: #2a2a3e; border: 1px solid #444; border-radius: 4px; color: #e0e0e0; font-size: 13px; }
.filter-select { cursor: pointer; }

/* Tabs */
.pm-tabs { display: flex; gap: 0; margin-bottom: 16px; border-bottom: 1px solid #333; }
.tab { padding: 8px 16px; background: none; border: none; color: #888; cursor: pointer; font-size: 14px; border-bottom: 2px solid transparent; }
.tab.active { color: #89b4fa; border-bottom-color: #89b4fa; }
.tab:hover { color: #e0e0e0; }

/* Tree View */
.pm-body { min-height: 60vh; }
.pm-tree { width: 100%; overflow-y: auto; background: #1e1e2e; border: 1px solid #333; border-radius: 8px; padding: 12px; max-height: calc(100vh - 320px); }
.drawer-title { margin: 0; color: #fff; font-size: 16px; }
.drawer-body { flex: 1; overflow-y: auto; }
.tree-family { margin-bottom: 4px; }
.tree-family-header { cursor: pointer; padding: 6px 8px; border-radius: 4px; display: flex; align-items: center; gap: 8px; }
.tree-family-header:hover { background: #2a2a3e; }
.arrow { font-size: 10px; transition: transform 0.2s; }
.arrow.open { transform: rotate(90deg); }
.family-name { font-weight: 600; color: #cba6f7; }
.family-meta { font-size: 11px; color: #666; }
.tree-offers { margin-left: 20px; }
.tree-offer { padding: 4px 8px; cursor: pointer; border-radius: 4px; display: flex; align-items: center; gap: 8px; font-size: 13px; }
.tree-offer:hover { background: #2a2a3e; }
.tree-offer.selected { background: #3a3a5e; border-left: 3px solid #89b4fa; }
.offer-provider { color: #a6e3a1; min-width: 80px; }
.offer-cred { color: #888; min-width: 60px; }
.offer-tier { font-size: 11px; padding: 1px 4px; border-radius: 3px; background: #333; }
.offer-tier.tier-1 { background: #a6e3a1; color: #1e1e2e; }
.offer-tier.tier-2 { background: #89b4fa; color: #1e1e2e; }
.offer-tier.tier-3 { background: #f9e2af; color: #1e1e2e; }
.offer-avail { font-size: 12px; }
.offer-avail.ok { color: #a6e3a1; }
.offer-price { color: #f9e2af; }
.offer-price.free { color: #a6e3a1; font-weight: 600; }
.offer-price.pending { color: #666; font-style: italic; }
.offer-window { font-size: 11px; color: #89dceb; }

/* Detail Panel */
.detail-sub { color: #888; margin: 4px 0 0; font-size: 13px; }
.detail-section { margin-bottom: 16px; padding-bottom: 16px; border-bottom: 1px solid #333; }
.detail-section h4 { margin: 0 0 8px 0; color: #cba6f7; font-size: 14px; }
.params-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.param { background: #2a2a3e; padding: 8px; border-radius: 4px; }
.param label { display: block; font-size: 11px; color: #888; margin-bottom: 2px; }
.param-val { font-size: 14px; font-weight: 600; color: #e0e0e0; }
.param-val.ok { color: #a6e3a1; }
.form-group { margin-bottom: 12px; }
.form-group label { display: block; font-size: 12px; color: #888; margin-bottom: 4px; }
.form-group input, .form-group select { width: 100%; padding: 6px 10px; background: #2a2a3e; border: 1px solid #444; border-radius: 4px; color: #e0e0e0; }
.detail-balance { margin: 12px 0; padding: 8px; background: #2a2a3e; border-radius: 4px; font-size: 13px; }
.form-actions { margin-top: 16px; display: flex; gap: 8px; align-items: center; }
.save-msg { font-size: 13px; }
.save-msg.ok { color: #a6e3a1; }

/* Table View */
.pm-table-container { background: #1e1e2e; border: 1px solid #333; border-radius: 8px; overflow: hidden; }
.pm-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.pm-table th { background: #2a2a3e; padding: 8px 12px; text-align: left; color: #888; font-weight: 600; border-bottom: 1px solid #333; white-space: nowrap; }
.pm-table th.sortable { cursor: pointer; }
.pm-table th.sortable:hover { color: #89b4fa; }
.pm-table td { padding: 6px 12px; border-bottom: 1px solid #2a2a3e; }
.pm-table tr:hover { background: #2a2a3e; }
.pm-table tr.selected { background: #3a3a5e; }
.pm-table .model-name { color: #cba6f7; font-weight: 600; }
.pm-table .price { color: #f9e2af; }
.tier-badge { font-size: 11px; padding: 1px 4px; border-radius: 3px; }
.tier-badge.tier-1 { background: #a6e3a1; color: #1e1e2e; }
.tier-badge.tier-2 { background: #89b4fa; color: #1e1e2e; }
.tier-badge.tier-3 { background: #f9e2af; color: #1e1e2e; }
.pm-pagination { display: flex; gap: 8px; align-items: center; padding: 12px; justify-content: center; }
.page-size-select { padding: 4px 8px; background: #2a2a3e; border: 1px solid #444; border-radius: 4px; color: #e0e0e0; }
.pm-bulk-actions { display: flex; gap: 8px; align-items: center; padding: 12px; background: #2a2a3e; }

/* Buttons */
.btn { padding: 6px 14px; border: 1px solid #444; border-radius: 4px; background: #2a2a3e; color: #e0e0e0; cursor: pointer; font-size: 13px; }
.btn:hover { background: #3a3a5e; }
.btn-primary { background: #89b4fa; color: #1e1e2e; border-color: #89b4fa; }
.btn-primary:hover { background: #74c7ec; }
.btn-success { background: #a6e3a1; color: #1e1e2e; border-color: #a6e3a1; }
.btn-success:hover { background: #94e2d5; }
.btn-sm { padding: 4px 10px; font-size: 12px; }
.btn-xs { padding: 2px 6px; font-size: 11px; }

/* Modals */

.modal h3 { margin-top: 0; color: #fff; }
.inherit-details { max-height: 300px; overflow-y: auto; margin: 12px 0; }
.inherit-row { padding: 4px 0; font-size: 13px; color: #888; }
</style>
