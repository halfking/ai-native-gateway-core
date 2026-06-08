<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  listModels, listTags, patchModelTags, resetModelTags,
  listModelFamilies, createModel, getModel, updateModel,
  createModelAlias, createModelAliasesBulk, updateModelAlias, discoverModels, getModelDiscoveryStatus,
  getProviders,
  type ModelCanonical, type ModelDetail, type ModelFamily, type TagNamespaceGroup,
  type DiscoverModelsResult, type ModelDiscoveryRun, type Provider,
} from '../api'
import TagEditor from '../components/TagEditor.vue'
import ActiveFilterChips from '../components/ActiveFilterChips.vue'
import { useFilterChips } from '../composables/useFilterChips'
import { useDynamicNamespaceFilters } from '../composables/useDynamicNamespaceFilters'

type ModelStatus = 'active' | 'disabled' | 'deprecated' | 'hidden'

const models = ref<ModelCanonical[]>([])
const families = ref<ModelFamily[]>([])
const providers = ref<Provider[]>([])
const namespaces = ref<TagNamespaceGroup[]>([])
const loading = ref(false)
const error = ref('')
const search = ref('')
const activeTags = ref<string[]>([])
const statusFilter = ref('')
const editingId = ref<number | null>(null)
const editTags = ref<string[]>([])
const detail = ref<ModelDetail | null>(null)
const detailLoading = ref(false)
const editInfo = ref({ display_name: '', family: '', modality: 'text', context_window: '', parameters_b: '', notes: '', status: 'active' as ModelStatus, disabled_reason: '' })
const newAlias = ref({ raw_name: '', surface: '', quantization: '', notes: '' })
const bulkAliasText = ref('')
const bulkAliasProfiles = ref('')
const creating = ref(false)
const discovering = ref(false)
const discoverResult = ref<DiscoverModelsResult | null>(null)
const discoverRun = ref<ModelDiscoveryRun | null>(null)
const discoverMessage = ref('')
const showCreateModal = ref(false)
const createForm = ref({ canonical_name: '', display_name: '', family: '', modality: 'text', context_window: '', parameters_b: '', aliases: '', notes: '' })
const showNamespaceFilters = ref(false)

// 新增：厂商和模型选择
const selectedVendor = ref('')
const selectedCanonical = ref('')

const statuses: ModelStatus[] = ['active', 'disabled', 'deprecated', 'hidden']
const modalities = ['text', 'vision', 'audio', 'multimodal', 'embedding']
const singleSelectNamespaces = new Set(['family', 'generation', 'modality', 'series', 'variant', 'version'])

function matchesVendor(model: ModelCanonical, vendor: string): boolean {
  if (!vendor) return true
  const family = families.value.find((item) => item.id === model.family)
  return family?.vendor === vendor
}

function matchesSearch(model: ModelCanonical, query: string): boolean {
  const q = query.toLowerCase().trim()
  if (!q) return true
  return (
    model.canonical_name.toLowerCase().includes(q) ||
    (model.display_name ?? '').toLowerCase().includes(q) ||
    (model.family ?? '').toLowerCase().includes(q) ||
    model.tags.some((tag) => tag.toLowerCase().includes(q))
  )
}

const {
  filterItems: filterModels,
  filtered,
  namespaceOptions,
  tagNamespace,
  toggleTag: toggleNamespaceTag,
} = useDynamicNamespaceFilters<ModelCanonical>({
  items: models,
  namespaceGroups: namespaces,
  activeTags,
  search,
  vendor: selectedVendor,
  getTags: (model) => model.tags,
  matchesSearch,
  matchesVendor,
  singleSelectNamespaces,
})

// 计算厂商列表
const vendors = computed(() => {
  const vendorSet = new Set<string>()
  const base = filterModels(models.value, { search: search.value, tags: activeTags.value })
  base.forEach((model) => {
    const family = families.value.find((item) => item.id === model.family)
    if (family?.vendor) vendorSet.add(family.vendor)
  })
  if (selectedVendor.value) vendorSet.add(selectedVendor.value)
  return Array.from(vendorSet).sort()
})

const familyOptions = computed(() => {
  const base = filterModels(models.value, { vendor: selectedVendor.value, search: search.value })
  const counts = new Map<string, number>()
  base.forEach((model) => {
    if (!model.family) return
    counts.set(model.family, (counts.get(model.family) ?? 0) + 1)
  })
  return families.value
    .filter((family) => counts.has(family.id) || activeTags.value.includes(`family:${family.id}`))
    .map((family) => ({ ...family, count: counts.get(family.id) ?? 0 }))
    .sort((a, b) => (b.count - a.count) || a.id.localeCompare(b.id))
})

const selectedFamily = computed(() => {
  const tag = activeTags.value.find((item) => item.startsWith('family:'))
  return tag ? tag.slice('family:'.length) : ''
})

function setFamilyFilter(familyId: string) {
  const familyTag = `family:${familyId}`
  if (activeTags.value.includes(familyTag)) {
    activeTags.value = activeTags.value.filter((tag) => tag !== familyTag)
    return
  }
  activeTags.value = [
    ...activeTags.value.filter((tag) => !tag.startsWith('family:')),
    familyTag,
  ]
}

// 根据选择的规范模型获取详情和供应商信息
const selectedModelDetail = ref<ModelDetail | null>(null)
const loadingDetail = ref(false)

// 获取供应商信息
async function loadProviders() {
  try {
    providers.value = await getProviders()
  } catch (e) { /* ignore */ }
}

async function loadTags() {
  try {
    const r = await listTags()
    namespaces.value = r.namespaces
  } catch (e) { /* ignore */ }
}

async function loadFamilies() {
  try {
    const r = await listModelFamilies()
    families.value = r.items
  } catch (e) { /* ignore */ }
}

async function loadModels() {
  loading.value = true
  error.value = ''
  try {
    const r = await listModels({ status: statusFilter.value || undefined })
    models.value = r.items
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function reloadAll() {
  await Promise.all([loadTags(), loadFamilies(), loadModels(), loadProviders()])
}

function toggleTag(t: string) {
  toggleNamespaceTag(t)
}

function clearFilters() {
  const shouldReload = Boolean(statusFilter.value)
  activeTags.value = []
  statusFilter.value = ''
  selectedVendor.value = ''
  selectedCanonical.value = ''
  selectedModelDetail.value = null
  search.value = ''
  if (shouldReload) loadModels()
}

function removeTag(tag: string) {
  if (!activeTags.value.includes(tag)) return
  activeTags.value = activeTags.value.filter((item) => item !== tag)
}

function clearStatusFilter() {
  if (!statusFilter.value) return
  statusFilter.value = ''
  loadModels()
}

function clearVendorFilter() {
  selectedVendor.value = ''
}

function clearSearchFilter() {
  search.value = ''
}

const activeFilterChips = useFilterChips(() => [
  statusFilter.value ? {
    key: `status:${statusFilter.value}`,
    label: `状态: ${statusFilter.value}`,
    onRemove: clearStatusFilter,
    className: 'badge-gray',
  } : null,
  selectedVendor.value ? {
    key: `vendor:${selectedVendor.value}`,
    label: `厂商: ${selectedVendor.value}`,
    onRemove: clearVendorFilter,
    className: 'badge-gray',
  } : null,
  search.value.trim() ? {
    key: `search:${search.value.trim()}`,
    label: `搜索: ${search.value.trim()}`,
    onRemove: clearSearchFilter,
    className: 'badge-gray',
  } : null,
  ...activeTags.value.map((tag) => ({
    key: `tag:${tag}`,
    label: tag,
    onRemove: () => removeTag(tag),
    className: tagBadgeClass(tag),
  })),
])

const namespaceTagCount = computed(() =>
  namespaceOptions.value.reduce((total, group) => total + group.tags.length, 0)
)

function beginEditTags(m: ModelCanonical) {
  editingId.value = m.id
  editTags.value = [...m.tags]
}

async function saveTags(m: ModelCanonical) {
  try {
    const updated = await patchModelTags(m.id, editTags.value)
    Object.assign(m, updated)
    if (detail.value?.id === m.id) Object.assign(detail.value, updated)
    editingId.value = null
    await loadTags()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  }
}

async function doReset(m: ModelCanonical) {
  try {
    const updated = await resetModelTags(m.id)
    Object.assign(m, updated)
    if (detail.value?.id === m.id) Object.assign(detail.value, updated)
    if (editingId.value === m.id) editTags.value = [...updated.tags]
    await loadTags()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '重置失败'
  }
}

async function openDetail(m: ModelCanonical) {
  detailLoading.value = true
  error.value = ''
  selectedCanonical.value = m.canonical_name
  try {
    detail.value = await getModel(m.id)
    selectedModelDetail.value = detail.value
    editInfo.value = {
      display_name: detail.value.display_name || detail.value.canonical_name,
      family: detail.value.family || '',
      modality: detail.value.modality,
      context_window: detail.value.context_window == null ? '' : String(detail.value.context_window),
      parameters_b: detail.value.parameters_b == null ? '' : String(detail.value.parameters_b),
      notes: detail.value.notes || '',
      status: detail.value.status,
      disabled_reason: detail.value.disabled_reason || '',
    }
    newAlias.value = { raw_name: '', surface: '', quantization: '', notes: '' }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载详情失败'
  } finally {
    detailLoading.value = false
  }
}

async function saveInfo() {
  if (!detail.value) return
  const updated = await updateModel(detail.value.id, {
    display_name: editInfo.value.display_name || null,
    family: editInfo.value.family || null,
    modality: editInfo.value.modality,
    context_window: editInfo.value.context_window ? Number(editInfo.value.context_window) : null,
    parameters_b: editInfo.value.parameters_b ? Number(editInfo.value.parameters_b) : null,
    notes: editInfo.value.notes || null,
    status: editInfo.value.status,
    disabled_reason: editInfo.value.disabled_reason || null,
  })
  Object.assign(detail.value, updated)
  const row = models.value.find((m) => m.id === updated.id)
  if (row) Object.assign(row, updated)
  await Promise.all([loadFamilies(), loadTags()])
}

async function toggleModelStatus(m: ModelCanonical) {
  const next: ModelStatus = m.status === 'active' ? 'disabled' : 'active'
  const updated = await updateModel(m.id, {
    status: next,
    disabled_reason: next === 'disabled' ? 'manual disable from admin UI' : null,
  })
  Object.assign(m, updated)
  if (detail.value?.id === m.id) Object.assign(detail.value, updated)
  await loadTags()
}

async function bulkImportAliases() {
  if (!detail.value || !bulkAliasText.value.trim()) return
  const raw_names = bulkAliasText.value.split('\n').map((x) => x.trim()).filter(Boolean)
  const profiles = bulkAliasProfiles.value
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)
  await createModelAliasesBulk(detail.value.id, {
    raw_names,
    client_profiles: profiles.length ? profiles : null,
    notes: 'agent terminal bulk import',
  })
  bulkAliasText.value = ''
  await openDetail(models.value.find((m) => m.id === detail.value!.id)!)
  await loadModels()
}

async function addAlias() {
  if (!detail.value || !newAlias.value.raw_name.trim()) return
  const created = await createModelAlias(detail.value.id, {
    raw_name: newAlias.value.raw_name.trim(),
    surface: newAlias.value.surface || null,
    quantization: newAlias.value.quantization || null,
    notes: newAlias.value.notes || null,
  })
  detail.value.aliases = [...detail.value.aliases, created]
  newAlias.value = { raw_name: '', surface: '', quantization: '', notes: '' }
  await loadModels()
}

async function setAliasStatus(aliasId: number, status: ModelStatus) {
  if (!detail.value) return
  const updated = await updateModelAlias(detail.value.id, aliasId, { status })
  const idx = detail.value.aliases.findIndex((a) => a.id === aliasId)
  if (idx >= 0) detail.value.aliases[idx] = updated
  await loadModels()
}

async function submitCreate() {
  creating.value = true
  error.value = ''
  try {
    const aliases = createForm.value.aliases.split('\n').map((x) => x.trim()).filter(Boolean)
    const created = await createModel({
      canonical_name: createForm.value.canonical_name.trim(),
      display_name: createForm.value.display_name || null,
      family: createForm.value.family || null,
      modality: createForm.value.modality,
      context_window: createForm.value.context_window ? Number(createForm.value.context_window) : null,
      parameters_b: createForm.value.parameters_b ? Number(createForm.value.parameters_b) : null,
      aliases,
      notes: createForm.value.notes || null,
    })
    createForm.value = { canonical_name: '', display_name: '', family: '', modality: 'text', context_window: '', parameters_b: '', aliases: '', notes: '' }
    showCreateModal.value = false
    await reloadAll()
    const row = models.value.find((m) => m.id === created.id)
    if (row) await openDetail(row)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '新增失败'
  } finally {
    creating.value = false
  }
}

async function runDiscovery() {
  discovering.value = true
  discoverResult.value = null
  discoverMessage.value = ''
  error.value = ''
  try {
    const started = await discoverModels({ use_manifest_fallback: true, force: true })
    discoverRun.value = started.run
    discoverMessage.value = started.reason === 'already_running' ? '已有扫描正在运行，继续等待结果' : '扫描任务已启动'
    await pollDiscovery(started.run.id)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '发现模型失败'
  } finally {
    discovering.value = false
  }
}

async function pollDiscovery(runId?: number) {
  for (;;) {
    const status = await getModelDiscoveryStatus()
    discoverRun.value = status.running || status.latest
    const latest = status.latest
    if (latest?.summary) discoverResult.value = latest.summary
    if (!status.running || (runId && latest?.id === runId && latest.status !== 'running')) {
      if (latest?.status === 'failed') error.value = latest.error || '扫描失败'
      if (latest?.status === 'succeeded') {
        discoverMessage.value = '扫描完成'
        await reloadAll()
        window.dispatchEvent(new CustomEvent('llm-gateway:models-updated'))
      }
      return
    }
    await new Promise((resolve) => window.setTimeout(resolve, 3000))
  }
}

async function loadDiscoveryStatus() {
  try {
    const status = await getModelDiscoveryStatus()
    discoverRun.value = status.running || status.latest
    if (status.latest?.summary) discoverResult.value = status.latest.summary
    if (status.running) {
      discovering.value = true
      discoverMessage.value = '扫描正在后台运行'
      await pollDiscovery(status.running.id)
    }
  } catch (e) { /* ignore */ }
}

function tagBadgeClass(tag: string): string {
  if (tag.startsWith('cap:')) return 'badge-blue'
  if (tag.startsWith('family:')) return 'badge-green'
  if (tag.startsWith('series:')) return 'badge-green'
  if (tag.startsWith('generation:')) return 'badge-blue'
  if (tag.startsWith('variant:')) return 'badge-purple'
  if (tag.startsWith('modality:')) return 'badge-yellow'
  if (tag.startsWith('user:')) return 'badge-gray'
  return 'badge-gray'
}

function statusBadgeClass(status: string): string {
  if (status === 'active') return 'badge-green'
  if (status === 'disabled') return 'badge-red'
  if (status === 'deprecated') return 'badge-yellow'
  return 'badge-gray'
}

function healthBadgeClass(status?: string): string {
  if (status === 'healthy') return 'badge-green'
  if (status === 'warning') return 'badge-yellow'
  if (status === 'unreachable') return 'badge-red'
  return 'badge-gray'
}

function healthLabel(status?: string): string {
  if (status === 'healthy') return '正常'
  if (status === 'warning') return '警示'
  if (status === 'unreachable') return '不可达'
  return '未探测'
}

onMounted(async () => {
  await reloadAll()
  await loadDiscoveryStatus()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>模型管理</h2>
      <div style="display:flex;gap:8px;align-items:center">
        <span class="badge badge-gray">{{ filtered.length }} 个模型</span>
        <button class="btn btn-primary btn-sm" @click="showCreateModal = true">
          新增模型
        </button>
        <button class="btn btn-ghost btn-sm" :disabled="discovering" @click="runDiscovery">
          {{ discovering ? '扫描中…' : '扫描供应商模型' }}
        </button>
      </div>
    </div>

    <!-- 发现任务状态 -->
    <div v-if="discoverRun || discoverResult" class="card discovery-card">
      <div class="card-header"><h3>发现任务</h3></div>
      <div class="card-body">
        <div v-if="discoverRun" class="summary-row">
          <span class="badge" :class="discoverRun.status === 'succeeded' ? 'badge-green' : discoverRun.status === 'failed' ? 'badge-red' : 'badge-blue'">{{ discoverRun.status }}</span>
          <span class="badge badge-gray">{{ discoverRun.trigger }}</span>
          <span class="muted small">#{{ discoverRun.id }} · {{ discoverMessage || '最近一次扫描' }}</span>
        </div>
        <div class="summary-row">
          <span class="badge badge-blue">凭据 {{ discoverResult?.credentials_succeeded ?? 0 }}/{{ discoverResult?.credentials_scanned ?? 0 }}</span>
          <span class="badge badge-green">模型 {{ discoverResult?.models_seen ?? 0 }}</span>
          <span class="badge badge-purple">offers {{ discoverResult?.offers_upserted ?? 0 }}</span>
          <span v-if="discoverResult?.credentials_failed" class="badge badge-yellow">失败 {{ discoverResult.credentials_failed }}</span>
        </div>
        <div class="summary-row" v-if="discoverResult">
          <span class="badge badge-green">正常 {{ discoverResult.healthy_credentials ?? 0 }}</span>
          <span class="badge badge-yellow">警示 {{ discoverResult.warning_credentials ?? 0 }}</span>
          <span class="badge badge-red">不可达 {{ discoverResult.unreachable_credentials ?? 0 }}</span>
        </div>
      </div>
    </div>

    <!-- 筛选区域 -->
    <div class="card filter-card" style="margin-bottom:12px">
      <div class="card-header">
        <div class="filter-heading">
          <h3>筛选</h3>
          <div v-if="namespaceOptions.length" class="filter-summary-row">
            <span class="muted small">标签维度 {{ namespaceOptions.length }} · 候选 {{ namespaceTagCount }}</span>
            <span v-if="activeTags.length" class="muted small">已选标签 {{ activeTags.length }}</span>
          </div>
        </div>
        <div class="filter-header-actions">
          <button
            v-if="namespaceOptions.length"
            class="btn btn-ghost btn-sm"
            @click="showNamespaceFilters = !showNamespaceFilters"
          >
            {{ showNamespaceFilters ? '收起高级筛选' : '展开高级筛选' }}
          </button>
          <button v-if="activeTags.length || statusFilter || selectedVendor || search.trim()" class="btn btn-ghost btn-sm" @click="clearFilters">清空</button>
        </div>
      </div>
      <div class="card-body">
        <div class="filter-row">
          <select v-model="statusFilter" class="input" style="max-width:180px" @change="loadModels">
            <option value="">全部状态</option>
            <option v-for="s in statuses" :key="s" :value="s">{{ s }}</option>
          </select>

          <select v-model="selectedVendor" class="input" style="max-width:200px">
            <option value="">全部厂商</option>
            <option v-for="v in vendors" :key="v" :value="v">{{ v }}</option>
          </select>

          <input v-model="search" class="input" placeholder="搜索名称 / family / 标签" style="max-width:280px" />
        </div>

        <div v-if="familyOptions.length" class="family-quick-row">
          <span class="muted small">Family 快捷筛选</span>
          <button
            v-for="family in familyOptions.slice(0, 18)"
            :key="family.id"
            type="button"
            class="family-chip"
            :class="{ active: selectedFamily === family.id }"
            :title="`${family.vendor || '未知厂商'} · ${family.id}`"
            @click="setFamilyFilter(family.id)"
          >
            <span>{{ family.vendor || family.display_name }}</span>
            <code>{{ family.id }}</code>
            <span class="cnt">{{ family.count }}</span>
          </button>
        </div>

        <ActiveFilterChips :chips="activeFilterChips" />

        <div v-if="showNamespaceFilters && namespaceOptions.length" class="namespace-panel">
          <div v-for="g in namespaceOptions" :key="g.namespace" class="ns-block">
            <div class="ns-label-row">
              <div class="ns-label">{{ g.namespace }}</div>
              <span class="badge badge-gray">{{ g.tags.length }}</span>
            </div>
            <div class="tag-list">
              <button
                v-for="t in g.tags"
                :key="t.tag"
                type="button"
                class="tag-chip"
                :class="{ active: activeTags.includes(t.tag), disabled: t.disabled, [tagBadgeClass(t.tag)]: true }"
                @click="toggleTag(t.tag)"
                :disabled="t.disabled"
                :title="t.disabled ? '当前其他条件下无可匹配结果' : `${t.count} 个模型`"
              >
                {{ t.tag }} <span class="cnt">{{ t.count }}</span>
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 模型列表 -->
    <div class="card" style="margin-top:12px">
      <div class="card-header">
        <h3>模型清单</h3>
      </div>
      <div class="card-body">
        <div v-if="error" class="alert alert-error">{{ error }}</div>
        <div v-if="loading" class="muted">加载中…</div>
        <table v-else class="table">
          <thead>
            <tr>
              <th>规范名</th>
              <th>显示名</th>
              <th>厂商</th>
              <th>family</th>
              <th>状态</th>
              <th>modality</th>
              <th>ctx</th>
              <th>aliases/offers</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in filtered" :key="m.id" :class="{ mutedRow: m.status !== 'active' }">
              <td>
                <code>{{ m.canonical_name }}</code>
              </td>
              <td>{{ m.display_name || '-' }}</td>
              <td>{{ families.find(f => f.id === m.family)?.vendor || '-' }}</td>
              <td>{{ m.family || '-' }}</td>
              <td><span class="badge" :class="statusBadgeClass(m.status)">{{ m.status }}</span></td>
              <td>{{ m.modality }}</td>
              <td>{{ m.context_window ?? '-' }}</td>
              <td>{{ m.alias_count ?? 0 }} / {{ m.offer_count ?? 0 }}</td>
              <td style="white-space:nowrap">
                <button class="btn btn-primary btn-sm" @click="openDetail(m)">查看详情</button>
                <button class="btn btn-ghost btn-sm" @click="toggleModelStatus(m)">{{ m.status === 'active' ? '禁用' : '启用' }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 模型详情弹层 -->
    <div v-if="detail" class="modal-overlay" @click.self="detail = null">
      <div class="modal-content detail-modal">
        <div class="modal-header">
          <h3>{{ detail.canonical_name }}</h3>
          <button class="btn btn-ghost btn-sm" @click="detail = null">关闭</button>
        </div>
        <div class="modal-body">
          <div v-if="detailLoading" class="muted">加载中…</div>

          <!-- 基础信息 -->
          <div class="section">
            <h4>基础信息</h4>
            <div class="form-grid">
              <div class="form-group">
                <label>显示名</label>
                <input v-model="editInfo.display_name" class="input" placeholder="显示名" />
              </div>
              <div class="form-group">
                <label>厂商/Family</label>
                <select v-model="editInfo.family" class="input">
                  <option value="">无 family</option>
                  <option v-for="f in families" :key="f.id" :value="f.id">{{ f.vendor ? f.vendor + ' - ' : '' }}{{ f.display_name }} · {{ f.id }}</option>
                </select>
              </div>
              <div class="form-group">
                <label>状态</label>
                <select v-model="editInfo.status" class="input">
                  <option v-for="s in statuses" :key="s" :value="s">{{ s }}</option>
                </select>
              </div>
              <div class="form-group">
                <label>模态</label>
                <select v-model="editInfo.modality" class="input">
                  <option v-for="m in modalities" :key="m" :value="m">{{ m }}</option>
                </select>
              </div>
              <div class="form-group">
                <label>Context Window</label>
                <input v-model="editInfo.context_window" class="input" placeholder="如 128000" />
                <span class="help-text">模型支持的上下文窗口大小（token 数）</span>
              </div>
              <div class="form-group">
                <label>参数量 (B)</label>
                <input v-model="editInfo.parameters_b" class="input" placeholder="如 70" />
                <span class="help-text">模型参数量，单位为 B（Billion），如 7 表示 7B</span>
              </div>
              <div class="form-group span-2">
                <label>禁用/弃用原因</label>
                <input v-model="editInfo.disabled_reason" class="input" placeholder="禁用/弃用原因" />
              </div>
              <div class="form-group span-2">
                <label>备注</label>
                <textarea v-model="editInfo.notes" class="input" rows="2" placeholder="备注" />
              </div>
              <div class="form-group">
                <button class="btn btn-primary" @click="saveInfo">保存基础信息</button>
              </div>
            </div>
          </div>

          <!-- Aliases 部分 -->
          <div class="section">
            <h4>供应商模型名称 (Aliases)</h4>
            <p class="help-text">Aliases 是供应商实际使用的模型名称。一个标准模型可以有多个别名，用于映射不同供应商或客户端使用的不同名称。</p>

            <div class="alias-add">
              <div class="form-group">
                <label>Raw Model Name</label>
                <input v-model="newAlias.raw_name" class="input" placeholder="供应商实际模型名" />
                <span class="help-text">供应商返回的原始模型名称，如 gpt-4o-2024-08-06</span>
              </div>
              <div class="form-group">
                <label>Surface Name</label>
                <input v-model="newAlias.surface" class="input" placeholder="客户端显示名称" />
                <span class="help-text">客户端看到的友好名称，可选</span>
              </div>
              <div class="form-group">
                <label>Quantization</label>
                <input v-model="newAlias.quantization" class="input" placeholder="如 fp16, int4" />
                <span class="help-text">量化方式，可选</span>
              </div>
              <div class="form-group">
                <label>备注</label>
                <input v-model="newAlias.notes" class="input" placeholder="备注" />
              </div>
              <div class="form-group" style="align-self:end">
                <button class="btn btn-primary btn-sm" @click="addAlias">新增 alias</button>
              </div>
            </div>

            <div style="margin:12px 0">
              <div style="font-size:12px;color:var(--muted);margin-bottom:6px">批量导入（每行一个供应商模型名）</div>
              <textarea v-model="bulkAliasText" class="input" rows="4" placeholder="gpt-4o&#10;claude-sonnet-4&#10;gemini-pro" />
              <input v-model="bulkAliasProfiles" class="input" style="margin-top:6px" placeholder="client profiles 逗号分隔，如 cursor,roocode" />
              <button class="btn btn-ghost btn-sm" style="margin-top:6px" @click="bulkImportAliases">批量导入 alias</button>
            </div>

            <table class="table alias-table">
              <thead><tr><th>Raw Name</th><th>Surface</th><th>Quant</th><th>状态</th><th>备注</th><th>操作</th></tr></thead>
              <tbody>
                <tr v-for="a in detail.aliases" :key="a.id" :class="{ mutedRow: a.status !== 'active' }">
                  <td><code>{{ a.raw_name }}</code></td>
                  <td>{{ a.surface || '-' }}</td>
                  <td>{{ a.quantization || '-' }}</td>
                  <td><span class="badge" :class="statusBadgeClass(a.status)">{{ a.status }}</span></td>
                  <td>{{ a.notes || '-' }}</td>
                  <td>
                    <button class="btn btn-ghost btn-sm" @click="setAliasStatus(a.id, a.status === 'active' ? 'disabled' : 'active')">
                      {{ a.status === 'active' ? '禁用' : '启用' }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 供应商信息 -->
          <div class="section">
            <h4>供应商模型与凭据信息</h4>
            <p class="help-text">显示该标准模型对应的所有供应商模型和凭据信息，包括价格、成功率、延迟等。</p>

            <div v-if="detail.offers && detail.offers.length > 0">
              <table class="table offers-table">
                <thead>
                  <tr>
                    <th>供应商</th>
                    <th>凭据</th>
                    <th>原始模型名</th>
                    <th>输入价格</th>
                    <th>输出价格</th>
                    <th>成功率</th>
                    <th>P95延迟</th>
                    <th>健康状态</th>
                    <th>状态</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="offer in detail.offers" :key="`${offer.provider_id}-${offer.credential_id}-${offer.raw_model_name}`">
                    <td>
                      <div class="provider-cell">
                        <span class="provider-name">{{ offer.provider_name }}</span>
                        <span class="badge badge-gray">{{ offer.catalog_code }}</span>
                      </div>
                    </td>
                    <td>
                      <div class="credential-cell">
                        <span>{{ offer.credential_label || `#${offer.credential_id}` }}</span>
                        <span v-if="offer.concurrency_limit" class="muted small">并发: {{ offer.concurrency_limit }}</span>
                      </div>
                    </td>
                    <td><code>{{ offer.raw_model_name }}</code></td>
                    <td v-if="offer.standardized_name"><code style="color:var(--accent)">{{ offer.standardized_name }}</code></td>
                    <td>{{ offer.input_price ? `¥${offer.input_price}/M` : '-' }}</td>
                    <td>{{ offer.output_price ? `¥${offer.output_price}/M` : '-' }}</td>
                    <td>
                      <span v-if="offer.success_rate !== null" :class="offer.success_rate >= 0.95 ? 'text-green' : offer.success_rate >= 0.8 ? 'text-yellow' : 'text-red'">
                        {{ (offer.success_rate * 100).toFixed(1) }}%
                      </span>
                      <span v-else>-</span>
                    </td>
                    <td>{{ offer.p95_latency_ms ? `${offer.p95_latency_ms}ms` : '-' }}</td>
                    <td><span class="badge" :class="healthBadgeClass(offer.health_status)">{{ healthLabel(offer.health_status) }}</span></td>
                    <td>
                      <span class="badge" :class="offer.available ? 'badge-green' : 'badge-red'">
                        {{ offer.available ? '可用' : '不可用' }}
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-else class="muted">
              暂无供应商模型信息。请先运行模型扫描或手动添加模型别名。
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 新增模型弹层 -->
    <div v-if="showCreateModal" class="modal-overlay" @click.self="showCreateModal = false">
      <div class="modal-content create-modal">
        <div class="modal-header">
          <h3>新增模型</h3>
          <button class="btn btn-ghost btn-sm" @click="showCreateModal = false">关闭</button>
        </div>
        <div class="modal-body">
          <div class="form-grid">
            <div class="form-group">
              <label>Canonical Name <span class="required">*</span></label>
              <input v-model="createForm.canonical_name" class="input" placeholder="gpt-4o, claude-sonnet-4" />
              <span class="help-text">模型的标准名称，用于内部标识和路由。使用小写和连字符，如 gpt-4o, claude-sonnet-4</span>
            </div>
            <div class="form-group">
              <label>显示名称</label>
              <input v-model="createForm.display_name" class="input" placeholder="GPT-4o, Claude Sonnet 4" />
              <span class="help-text">在界面上显示的友好名称，可选</span>
            </div>
            <div class="form-group">
              <label>厂商/Family</label>
              <select v-model="createForm.family" class="input">
                <option value="">选择 family</option>
                <option v-for="f in families" :key="f.id" :value="f.id">{{ f.vendor ? f.vendor + ' - ' : '' }}{{ f.display_name }} · {{ f.id }}</option>
              </select>
              <span class="help-text">模型所属的厂商或系列</span>
            </div>
            <div class="form-group">
              <label>模态</label>
              <select v-model="createForm.modality" class="input">
                <option v-for="m in modalities" :key="m" :value="m">{{ m }}</option>
              </select>
              <span class="help-text">模型支持的输入输出类型</span>
            </div>
            <div class="form-group">
              <label>Context Window</label>
              <input v-model="createForm.context_window" class="input" placeholder="128000" />
              <span class="help-text">模型支持的上下文窗口大小（token 数）。例如：GPT-4o 为 128000，Claude 3.5 Sonnet 为 200000</span>
            </div>
            <div class="form-group">
              <label>参数量 (B)</label>
              <input v-model="createForm.parameters_b" class="input" placeholder="70" />
              <span class="help-text">模型参数量，单位为 B（Billion，十亿）。例如：7 表示 7B 参数，70 表示 70B 参数。开源模型通常有明确参数量，闭源模型可留空</span>
            </div>
            <div class="form-group span-2">
              <label>Aliases（供应商模型名称）</label>
              <textarea v-model="createForm.aliases" class="input" rows="3" placeholder="gpt-4o-2024-08-06&#10;gpt-4o-latest&#10;openai/gpt-4o" />
              <span class="help-text">供应商实际使用的模型名称，每行一个。例如：标准名 gpt-4o 的别名可以是 gpt-4o-2024-08-06、gpt-4o-latest 等</span>
            </div>
            <div class="form-group span-2">
              <label>备注</label>
              <textarea v-model="createForm.notes" class="input" rows="2" placeholder="备注信息" />
            </div>
          </div>
          <div class="modal-footer">
            <button class="btn btn-ghost" @click="showCreateModal = false">取消</button>
            <button class="btn btn-primary" :disabled="creating || !createForm.canonical_name" @click="submitCreate">
              {{ creating ? '创建中...' : '创建模型' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.filter-row {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  align-items: center;
}

.filter-card {
  padding: 12px 14px;
}

.filter-card .card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}

.filter-card .card-header h3 {
  margin: 0;
  font-size: 15px;
}

.filter-heading {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.filter-card .card-body {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.filter-header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.filter-summary-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  align-items: center;
}

.family-quick-row {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
  padding-top: 4px;
}

.family-chip {
  border: 1px solid var(--border);
  background: rgba(255,255,255,.02);
  border-radius: 999px;
  padding: 4px 9px;
  font-size: 12px;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.family-chip.active {
  border-color: var(--primary, #6366f1);
  outline: 2px solid color-mix(in srgb, var(--primary, #6366f1) 24%, transparent);
}

.family-chip code {
  font-size: 11px;
}

.family-chip .cnt {
  color: var(--text-muted);
  font-size: 10px;
}

.namespace-panel {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--border);
  max-height: 320px;
  overflow: auto;
  padding-right: 4px;
}

.ns-block {
  margin-bottom: 0;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 10px;
  background: rgba(255,255,255,.02);
}

.ns-label-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}

.ns-label { font-size: 11px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; }
.tag-list { display: flex; flex-wrap: wrap; gap: 4px; }
.tag-chip {
  border: 1px solid var(--border); background: var(--bg);
  border-radius: 12px; padding: 2px 8px; font-size: 12px; cursor: pointer;
  display: inline-flex; align-items: center; gap: 4px;
}
.tag-chip.active { outline: 2px solid var(--primary, #6366f1); }
.tag-chip.disabled { opacity: .38; cursor: not-allowed; }
.tag-chip:disabled { opacity: .38; cursor: not-allowed; }
.tag-chip .cnt { color: var(--text-muted); font-size: 10px; }
.muted { color: var(--text-muted); }
.small { font-size: 11px; margin-top: 3px; }
.mutedRow { opacity: .62; }
.discovery-card { margin-bottom: 12px; }
.summary-row { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }

/* 弹层样式 */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding: 40px 20px;
  z-index: 1000;
  overflow-y: auto;
}

.modal-content {
  background: var(--bg);
  border-radius: 12px;
  width: 100%;
  max-width: 900px;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 20px 24px;
  border-bottom: 1px solid var(--border);
}

.modal-header h3 {
  margin: 0;
  font-size: 18px;
}

.modal-body {
  padding: 24px;
}

.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}

/* 表单样式 */
.form-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.form-group label {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}

.form-group .required {
  color: #ef4444;
}

.form-group .help-text {
  font-size: 11px;
  color: var(--text-muted);
  line-height: 1.4;
}

.span-2 {
  grid-column: span 2;
}

.section {
  margin-bottom: 24px;
  padding-bottom: 24px;
  border-bottom: 1px solid var(--border);
}

.section:last-child {
  border-bottom: none;
  margin-bottom: 0;
  padding-bottom: 0;
}

.section h4 {
  margin: 0 0 12px 0;
  font-size: 15px;
  font-weight: 600;
}

/* Alias 添加样式 */
.alias-add {
  display: grid;
  grid-template-columns: repeat(4, 1fr) auto;
  gap: 12px;
  align-items: start;
  margin: 12px 0;
}

.alias-table {
  margin-top: 12px;
}

/* 供应商网格 */
.provider-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 12px;
}

.provider-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px;
}

.provider-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.provider-name {
  font-weight: 600;
  font-size: 14px;
}

.provider-info {
  display: flex;
  gap: 8px;
  align-items: center;
}

/* 供应商表格样式 */
.offers-table {
  margin-top: 12px;
  font-size: 13px;
}

.offers-table th {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  text-transform: uppercase;
}

.provider-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.credential-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.text-green { color: #166534; }
.text-yellow { color: #92400e; }
.text-red { color: #991b1b; }

/* Badge 样式 */
.badge-red { background: #fee2e2; color: #991b1b; }
.badge-green { background: #dcfce7; color: #166534; }
.badge-blue { background: #dbeafe; color: #1e40af; }
.badge-yellow { background: #fef3c7; color: #92400e; }
.badge-purple { background: #ede9fe; color: #5b21b6; }
.badge-gray { background: #f3f4f6; color: #374151; }

@media (max-width: 900px) {
  .form-grid, .alias-add { grid-template-columns: 1fr; }
  .span-2 { grid-column: span 1; }
  .modal-content { margin: 20px; }
  .filter-card .card-header { align-items: flex-start; }
  .filter-heading { width: 100%; }
  .filter-header-actions { width: 100%; justify-content: flex-start; }
  .filter-row { flex-direction: column; }
  .namespace-panel { grid-template-columns: 1fr; max-height: 280px; }
}
</style>
