<template>
  <div class="pricing-management">
    <div class="pm-header">
      <h2>Pricing Management</h2>
      <div class="pm-actions">
        <button class="btn btn-sm" @click="fetchData" :disabled="loading">
          {{ loading ? 'Loading...' : 'Refresh' }}
        </button>
        <button class="btn btn-sm" @click="exportCsv">Export CSV</button>
        <button class="btn btn-sm btn-primary" @click="showImport = true">Import CSV</button>
        <button class="btn btn-sm btn-success" @click="autoInherit">Auto Inherit</button>
      </div>
    </div>

    <div class="pm-summary" v-if="summary">
      <div class="stat-card">
        <div class="stat-val">{{ summary.total_offers }}</div>
        <div class="stat-label">Total Offers</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.priced_in }}</div>
        <div class="stat-label">Priced (Input)</div>
      </div>
      <div class="stat-card">
        <div class="stat-val">{{ summary.priced_out }}</div>
        <div class="stat-label">Priced (Output)</div>
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
        <div class="stat-label">Free</div>
      </div>
    </div>

    <!-- Filter Bar -->
    <div class="pm-filters">
      <div class="filter-group">
        <input v-model="filters.search" placeholder="Search models..." class="filter-input" @input="onFilterChange" />
      </div>
      <div class="filter-group">
        <select v-model="filters.provider_id" @change="onFilterChange" class="filter-select">
          <option value="">All Providers</option>
          <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.display_name }}</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.billing_mode" @change="onFilterChange" class="filter-select">
          <option value="">All Billing</option>
          <option value="per_token">Per Token</option>
          <option value="per_request">Per Request</option>
          <option value="monthly">Monthly</option>
          <option value="free">Free</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.currency" @change="onFilterChange" class="filter-select">
          <option value="">All Currency</option>
          <option value="CNY">CNY</option>
          <option value="USD">USD</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.pricing_status" @change="onFilterChange" class="filter-select">
          <option value="">All Pricing</option>
          <option value="priced">Priced</option>
          <option value="unpriced">Unpriced</option>
          <option value="free">Free</option>
        </select>
      </div>
      <div class="filter-group">
        <select v-model="filters.availability" @change="onFilterChange" class="filter-select">
          <option value="">All Status</option>
          <option value="true">Available</option>
          <option value="false">Unavailable</option>
        </select>
      </div>
      <button class="btn btn-sm" @click="clearFilters">Clear</button>
    </div>

    <!-- View Tabs -->
    <div class="pm-tabs">
      <button :class="['tab', { active: viewMode === 'tree' }]" @click="viewMode = 'tree'">Tree View</button>
      <button :class="['tab', { active: viewMode === 'table' }]" @click="viewMode = 'table'">Table View</button>
    </div>

    <!-- Tree View -->
    <div v-if="viewMode === 'tree'" class="pm-body">
      <div class="pm-tree">
        <div v-for="fam in filteredFamilies" :key="fam.canonical_name" class="tree-family">
          <div class="tree-family-header" @click="toggle(fam.canonical_name)">
            <span class="arrow" :class="{ open: expanded[fam.canonical_name] }">&#9654;</span>
            <span class="family-name">{{ fam.canonical_name }}</span>
            <span class="family-meta">{{ fam.family }} | {{ fam.offers.length }} offers</span>
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
              <span class="offer-price free" v-else-if="offer.billing_mode === 'free'">FREE</span>
              <span class="offer-price pending" v-else>pending</span>
              <span class="offer-window" v-if="offer.window_requests">
                W:{{ offer.window_success_rate ? (offer.window_success_rate * 100).toFixed(0) + '%' : '-' }}
              </span>
            </div>
          </div>
        </div>
      </div>

      <div class="pm-detail" v-if="selectedOffer">
        <h3>{{ selectedOffer.raw_model_name }}</h3>
        <p class="detail-sub">{{ selectedOffer.provider_name }} / {{ selectedOffer.credential_label }}</p>

        <!-- Routing Params -->
        <div class="detail-section">
          <h4>Routing Parameters</h4>
          <div class="params-grid">
            <div class="param">
              <label>Tier</label>
              <span class="param-val">{{ selectedOffer.routing_tier }}</span>
            </div>
            <div class="param">
              <label>Weight</label>
              <span class="param-val">{{ selectedOffer.weight }}</span>
            </div>
            <div class="param">
              <label>Available</label>
              <span class="param-val" :class="{ ok: selectedOffer.available }">{{ selectedOffer.available ? 'Yes' : 'No' }}</span>
            </div>
            <div class="param">
              <label>Success Rate</label>
              <span class="param-val">{{ selectedOffer.success_rate != null ? (selectedOffer.success_rate * 100).toFixed(1) + '%' : '-' }}</span>
            </div>
            <div class="param">
              <label>P95 Latency</label>
              <span class="param-val">{{ selectedOffer.p95_latency_ms ? selectedOffer.p95_latency_ms + 'ms' : '-' }}</span>
            </div>
            <div class="param">
              <label>Last Seen</label>
              <span class="param-val">{{ selectedOffer.last_seen_at ? new Date(selectedOffer.last_seen_at).toLocaleString() : '-' }}</span>
            </div>
          </div>
        </div>

        <!-- Window Stats -->
        <div class="detail-section" v-if="selectedOffer.window_requests">
          <h4>Window Stats (10min)</h4>
          <div class="params-grid">
            <div class="param">
              <label>Requests</label>
              <span class="param-val">{{ selectedOffer.window_requests }}</span>
            </div>
            <div class="param">
              <label>Success Rate</label>
              <span class="param-val">{{ selectedOffer.window_success_rate ? (selectedOffer.window_success_rate * 100).toFixed(1) + '%' : '-' }}</span>
            </div>
            <div class="param">
              <label>P95 Latency</label>
              <span class="param-val">{{ selectedOffer.window_latency_p95_ms ? selectedOffer.window_latency_p95_ms.toFixed(0) + 'ms' : '-' }}</span>
            </div>
          </div>
        </div>

        <!-- Pricing Form -->
        <div class="detail-section">
          <h4>Pricing</h4>
          <div class="form-group">
            <label>Input Price (per 1M tokens)</label>
            <input v-model.number="editForm.unit_price_in_per_1m" type="number" step="0.001" />
          </div>
          <div class="form-group">
            <label>Output Price (per 1M tokens)</label>
            <input v-model.number="editForm.unit_price_out_per_1m" type="number" step="0.001" />
          </div>
          <div class="form-group">
            <label>Cache Read Price (per 1M)</label>
            <input v-model.number="editForm.cache_read_price_per_1m" type="number" step="0.001" />
          </div>
          <div class="form-group">
            <label>Cache Write Price (per 1M)</label>
            <input v-model.number="editForm.cache_write_price_per_1m" type="number" step="0.001" />
          </div>
          <div class="form-group">
            <label>Currency</label>
            <select v-model="editForm.currency">
              <option value="CNY">CNY</option>
              <option value="USD">USD</option>
            </select>
          </div>
          <div class="form-group">
            <label>Billing Mode</label>
            <select v-model="editForm.billing_mode">
              <option value="per_token">Per Token</option>
              <option value="per_request">Per Request</option>
              <option value="monthly">Monthly</option>
              <option value="free">Free</option>
            </select>
          </div>
        </div>

        <div class="detail-balance" v-if="selectedOffer.balance_usd != null">
          <strong>Balance:</strong> {{ selectedOffer.balance_usd }} {{ selectedOffer.balance_currency || 'USD' }}
          <span v-if="selectedOffer.pool_group"> | Pool: {{ selectedOffer.pool_group }}</span>
        </div>

        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="saveOffer" :disabled="saving">
            {{ saving ? 'Saving...' : 'Save' }}
          </button>
          <button class="btn btn-sm" @click="copyPrice">Copy Price</button>
          <button class="btn btn-sm" @click="pastePrice" :disabled="!clipboard || !selectedOffer">Paste</button>
          <span v-if="saveMsg" class="save-msg" :class="{ ok: saveOk }">{{ saveMsg }}</span>
        </div>
      </div>
      <div class="pm-detail empty" v-else>
        <p>Select an offer from the tree to edit pricing.</p>
      </div>
    </div>

    <!-- Table View -->
    <div v-if="viewMode === 'table'" class="pm-table-container">
      <table class="pm-table">
        <thead>
          <tr>
            <th><input type="checkbox" @change="toggleSelectAll" :checked="allSelected" /></th>
            <th @click="sortTable('canonical_name')" class="sortable">Model {{ sortIcon('canonical_name') }}</th>
            <th @click="sortTable('provider_name')" class="sortable">Provider {{ sortIcon('provider_name') }}</th>
            <th>Credential</th>
            <th @click="sortTable('unit_price_in_per_1m')" class="sortable">In Price {{ sortIcon('unit_price_in_per_1m') }}</th>
            <th @click="sortTable('unit_price_out_per_1m')" class="sortable">Out Price {{ sortIcon('unit_price_out_per_1m') }}</th>
            <th @click="sortTable('currency')" class="sortable">Currency {{ sortIcon('currency') }}</th>
            <th @click="sortTable('billing_mode')" class="sortable">Billing {{ sortIcon('billing_mode') }}</th>
            <th @click="sortTable('routing_tier')" class="sortable">Tier {{ sortIcon('routing_tier') }}</th>
            <th @click="sortTable('weight')" class="sortable">Weight {{ sortIcon('weight') }}</th>
            <th @click="sortTable('available')" class="sortable">Avail {{ sortIcon('available') }}</th>
            <th @click="sortTable('success_rate')" class="sortable">Rate {{ sortIcon('success_rate') }}</th>
            <th @click="sortTable('p95_latency_ms')" class="sortable">P95 {{ sortIcon('p95_latency_ms') }}</th>
            <th>Window</th>
            <th>Actions</th>
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
              <button class="btn btn-xs" @click="editFromTable(item)">Edit</button>
              <button class="btn btn-xs" @click="copyPriceFromTable(item)">Copy</button>
            </td>
          </tr>
        </tbody>
      </table>
      <div class="pm-pagination">
        <button class="btn btn-sm" :disabled="tablePage <= 1" @click="tablePage--; fetchTable()">Prev</button>
        <span>Page {{ tablePage }} of {{ Math.ceil(tableTotal / tablePageSize) }}</span>
        <button class="btn btn-sm" :disabled="tablePage >= Math.ceil(tableTotal / tablePageSize)" @click="tablePage++; fetchTable()">Next</button>
        <select v-model.number="tablePageSize" @change="tablePage = 1; fetchTable()" class="page-size-select">
          <option :value="25">25</option>
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
        </select>
      </div>
      <div class="pm-bulk-actions" v-if="selectedRows.size > 0">
        <span>{{ selectedRows.size }} selected</span>
        <button class="btn btn-sm" @click="pasteToSelected">Paste Price to Selected</button>
      </div>
    </div>

    <!-- Import Modal -->
    <div v-if="showImport" class="modal-overlay" @click.self="showImport = false">
      <div class="modal">
        <h3>Import Pricing CSV</h3>
        <p>CSV columns: offer_id, unit_price_in_per_1m, unit_price_out_per_1m, currency, billing_mode</p>
        <input type="file" accept=".csv" @change="onFileChange" />
        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="importCsv" :disabled="!importFile || importing">
            {{ importing ? 'Importing...' : 'Import' }}
          </button>
          <button class="btn btn-sm" @click="showImport = false">Cancel</button>
          <span v-if="importMsg">{{ importMsg }}</span>
        </div>
      </div>
    </div>

    <!-- Auto Inherit Modal -->
    <div v-if="showInheritPreview" class="modal-overlay" @click.self="showInheritPreview = false">
      <div class="modal">
        <h3>Auto Inherit Pricing</h3>
        <p>{{ inheritPreview.would_inherit }} offers will inherit pricing from same provider+model.</p>
        <div class="inherit-details" v-if="inheritPreview.details">
          <div v-for="d in inheritPreview.details.slice(0, 20)" :key="d.target_offer_id" class="inherit-row">
            Offer #{{ d.target_offer_id }} ← Offer #{{ d.source_offer_id }}
          </div>
          <p v-if="inheritPreview.details.length > 20">... and {{ inheritPreview.details.length - 20 }} more</p>
        </div>
        <div class="form-actions">
          <button class="btn btn-primary btn-sm" @click="confirmInherit" :disabled="inheriting">
            {{ inheriting ? 'Inheriting...' : 'Confirm Inherit' }}
          </button>
          <button class="btn btn-sm" @click="showInheritPreview = false">Cancel</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { store } from '../store'

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

const authHeaders = () => {
  const key = store.apiKey
  return { 'Authorization': `Bearer ${key}`, 'Content-Type': 'application/json' }
}

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
      saveMsg.value = 'Saved!'
      saveOk.value = true
      await fetchData()
    } else {
      saveMsg.value = 'No changes'
      saveOk.value = false
    }
  } catch (e) {
    saveMsg.value = 'Error'
    saveOk.value = false
  } finally {
    saving.value = false
  }
}

function copyPrice() {
  if (selectedOffer.value) {
    clipboard.value = { ...selectedOffer.value }
    saveMsg.value = 'Price copied!'
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
  saveMsg.value = 'Price pasted! Click Save to apply.'
  saveOk.value = true
}

function copyPriceFromTable(item: Offer) {
  clipboard.value = { ...item }
  saveMsg.value = 'Price copied!'
  saveOk.value = true
}

function editFromTable(item: Offer) {
  selectOffer(item)
  viewMode.value = 'tree'
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
    saveMsg.value = `Updated ${data.updated} offers`
    saveOk.value = true
    selectedRows.value.clear()
    await fetchTable()
  } catch (e) {
    saveMsg.value = 'Error'
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
    saveMsg.value = `Inherited ${data.inherited} offers`
    saveOk.value = true
    await fetchData()
  } catch (e) {
    saveMsg.value = 'Error inheriting'
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
      headers: { 'Authorization': `Bearer ${store.apiKey}`, 'Content-Type': 'application/json' },
      body: fd,
    })
    const data = await res.json()
    importMsg.value = `Updated ${data.updated} offers`
    showImport.value = false
    await fetchData()
  } catch (e) {
    importMsg.value = 'Error'
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
.stat-val { font-size: 24px; font-weight: 700; color: #89b4fa; }
.stat-label { font-size: 12px; color: #888; margin-top: 4px; }

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
.pm-body { display: flex; gap: 20px; min-height: 60vh; }
.pm-tree { width: 55%; overflow-y: auto; background: #1e1e2e; border: 1px solid #333; border-radius: 8px; padding: 12px; }
.pm-detail { width: 45%; background: #1e1e2e; border: 1px solid #333; border-radius: 8px; padding: 20px; overflow-y: auto; }
.pm-detail.empty { display: flex; align-items: center; justify-content: center; color: #666; }
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
.detail-sub { color: #888; margin-bottom: 16px; }
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
.modal-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 1000; }
.modal { background: #1e1e2e; border: 1px solid #444; border-radius: 8px; padding: 24px; width: 480px; max-height: 80vh; overflow-y: auto; }
.modal h3 { margin-top: 0; color: #fff; }
.inherit-details { max-height: 300px; overflow-y: auto; margin: 12px 0; }
.inherit-row { padding: 4px 0; font-size: 13px; color: #888; }
</style>
