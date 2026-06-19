<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  listModels, listTags, patchModelTags, resetModelTags,
  listModelFamilies, createModel, getModel, updateModel,
  createModelAlias, createModelAliasesBulk, updateModelAlias, discoverModels, getModelDiscoveryStatus,
  getProviders, getFeatured, patchFeatured, getFeaturedModelsDynamic,
  type ModelCanonical, type ModelDetail, type ModelFamily, type TagNamespaceGroup,
  type DiscoverModelsResult, type ModelDiscoveryRun, type Provider,
  type FeaturedModel,
} from '../api'
import ActiveFilterChips from '../components/ActiveFilterChips.vue'
import CatalogPanel from '../components/CatalogPanel.vue'
import ModelPicker from '../components/ModelPicker.vue'
import { useFilterChips } from '../composables/useFilterChips'
import ModelCatalogFilterBar from '../components/ModelCatalogFilterBar.vue'
import { useDynamicNamespaceFilters } from '../composables/useDynamicNamespaceFilters'
import { isReadOnlyMode, isPlatformOpsView } from '../store'
import { normalizeTags, resolveVendor, matchesModelCatalogSearch } from '../utils/modelCatalog'

type PageTab = 'canonical' | 'catalog'

const route = useRoute()
const router = useRouter()
const isPlatformOps = computed(() => isPlatformOpsView())
const activeTab = ref<PageTab>(isPlatformOps.value ? 'canonical' : 'catalog')

function tabFromQuery(q: unknown): PageTab | null {
  if (q === 'canonical' || q === 'models') return 'canonical'
  if (q === 'catalog') return 'catalog'
  return null
}

function setTab(tab: PageTab) {
  if (tab === 'canonical' && !isPlatformOps.value) return
  activeTab.value = tab
  const nextQuery = { ...route.query }
  if (tab === 'canonical') delete nextQuery.tab
  else nextQuery.tab = tab
  router.replace({ path: '/models', query: nextQuery })
}

function syncTabFromRoute() {
  const fromQuery = tabFromQuery(route.query.tab)
  if (fromQuery === 'canonical' && !isPlatformOps.value) {
    router.replace({ path: '/models', query: { tab: 'catalog' } })
    activeTab.value = 'catalog'
    return
  }
  if (fromQuery) {
    activeTab.value = fromQuery
    return
  }
  activeTab.value = isPlatformOps.value ? 'canonical' : 'catalog'
}

watch(() => route.query.tab, syncTabFromRoute)

const readOnly = computed(() => isReadOnlyMode())

type ModelStatus = 'active' | 'disabled' | 'deprecated' | 'hidden'

const models = ref<ModelCanonical[]>([])
const families = ref<ModelFamily[]>([])
const providers = ref<Provider[]>([])
const namespaces = ref<TagNamespaceGroup[]>([])
const loading = ref(false)
const error = ref('')
const pickedModel = ref('')
const textSearch = ref('')
const searchStub = ref('')
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
const showDiscoveryCard = ref(false)
const showCreateModal = ref(false)
const showFeaturedDrawer = ref(false)
const featuredArray = ref<string[]>([])
const featuredLoading = ref(false)
const featuredSaving = ref(false)
const featuredError = ref('')
const featuredMessage = ref('')
const featuredRecommendPreview = ref<FeaturedModel[]>([])
const featuredRecommendLoading = ref(false)
const featuredRecommendMessage = ref('')
const createForm = ref({ canonical_name: '', display_name: '', family: '', modality: 'text', context_window: '', parameters_b: '', aliases: '', notes: '' })
const showNamespaceFilters = ref(false)

// 新增：厂商和模型选择
const selectedVendor = ref('')
const selectedCanonical = ref('')

const statuses: ModelStatus[] = ['active', 'disabled', 'deprecated', 'hidden']
const modalities = ['text', 'vision', 'audio', 'multimodal', 'embedding']
const singleSelectNamespaces = new Set(['family', 'generation', 'modality', 'series', 'variant', 'version'])

const modelStatusOptions = [
  { value: 'active', label: 'active' },
  { value: 'disabled', label: 'disabled' },
  { value: 'deprecated', label: 'deprecated' },
  { value: 'hidden', label: 'hidden' },
]

function modelVendorName(model: ModelCanonical): string {
  return resolveVendor(
    model.canonical_name,
    model.family,
    model.vendor ?? families.value.find((f) => f.id === model.family)?.vendor,
  )
}

function matchesVendor(model: ModelCanonical, vendor: string): boolean {
  if (!vendor) return true
  return modelVendorName(model) === vendor
}

function matchesSearch(model: ModelCanonical, _query: string): boolean {
  return matchesModelCatalogSearch(
    model.canonical_name,
    model.display_name ?? '',
    modelVendorName(model),
    pickedModel.value,
    textSearch.value,
    [model.family ?? '', ...normalizeTags(model.tags)],
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
  search: searchStub,
  vendor: selectedVendor,
  getTags: (model) => normalizeTags(model.tags),
  getFamily: (model) => model.family ?? null,
  matchesSearch,
  matchesVendor,
  singleSelectNamespaces,
})

// 计算厂商列表
const vendors = computed(() => {
  const vendorSet = new Set<string>()
  const base = filterModels(models.value, { tags: activeTags.value })
  base.forEach((model) => vendorSet.add(modelVendorName(model)))
  if (selectedVendor.value) vendorSet.add(selectedVendor.value)
  return Array.from(vendorSet).sort((a, b) => a.localeCompare(b, 'zh-CN'))
})

const familyOptions = computed(() => {
  const base = filterModels(models.value, { vendor: selectedVendor.value })
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
  pickedModel.value = ''
  textSearch.value = ''
  if (shouldReload) loadModels()
}

function onStatusFilterChange(status: string) {
  statusFilter.value = status
  loadModels()
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
  pickedModel.value = ''
  textSearch.value = ''
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
  pickedModel.value.trim() || textSearch.value.trim() ? {
    key: `search:${pickedModel.value}:${textSearch.value}`,
    label: `模型: ${pickedModel.value || textSearch.value.trim()}`,
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
    showDiscoveryCard.value = true
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
      refreshDiscoveryCardVisibility()
      return
    }
    await new Promise((resolve) => window.setTimeout(resolve, 3000))
  }
}

function discoveryHasStats(result: DiscoverModelsResult | null | undefined): boolean {
  if (!result) return false
  return (
    (result.credentials_scanned ?? 0) > 0 ||
    (result.models_seen ?? 0) > 0 ||
    (result.offers_upserted ?? 0) > 0 ||
    (result.credentials_failed ?? 0) > 0
  )
}

function refreshDiscoveryCardVisibility() {
  if (discovering.value) {
    showDiscoveryCard.value = true
    return
  }
  const run = discoverRun.value
  const result = discoverResult.value
  if (discoveryHasStats(result)) {
    showDiscoveryCard.value = true
    return
  }
  if (run && (run.status === 'succeeded' || run.status === 'failed')) {
    showDiscoveryCard.value = true
    return
  }
  if (run?.status === 'running' && run.trigger === 'manual') {
    showDiscoveryCard.value = true
    return
  }
  showDiscoveryCard.value = false
}

async function loadDiscoveryStatus() {
  try {
    const status = await getModelDiscoveryStatus()
    if (status.running?.trigger === 'manual') {
      discoverRun.value = status.running
      if (status.running.summary) discoverResult.value = status.running.summary
      discovering.value = true
      discoverMessage.value = '扫描正在后台运行'
      showDiscoveryCard.value = true
      await pollDiscovery(status.running.id)
      return
    }
    discoverRun.value = status.latest
    if (status.latest?.summary) discoverResult.value = status.latest.summary
    if (status.running?.trigger === 'scheduled') {
      discoverMessage.value = ''
    }
    refreshDiscoveryCardVisibility()
  } catch (e) { /* ignore */ }
}

async function openFeaturedDrawer() {
  showFeaturedDrawer.value = true
  featuredError.value = ''
  featuredMessage.value = ''
  featuredRecommendPreview.value = []
  featuredRecommendMessage.value = ''
  try {
    featuredLoading.value = true
    const r = await getFeatured()
    featuredArray.value = (r.featured_models || []).slice()
  } catch (e: unknown) {
    featuredError.value = e instanceof Error ? e.message : '加载特色模型失败'
    featuredArray.value = []
  } finally {
    featuredLoading.value = false
  }
}

function closeFeaturedDrawer() {
  showFeaturedDrawer.value = false
  featuredError.value = ''
  featuredMessage.value = ''
  featuredRecommendPreview.value = []
  featuredRecommendMessage.value = ''
}

async function saveFeatured() {
  featuredSaving.value = true
  featuredError.value = ''
  featuredMessage.value = ''
  try {
    const list = featuredArray.value.map((s) => s.trim()).filter(Boolean)
    const r = await patchFeatured(list)
    featuredArray.value = (r.featured_models || []).slice()
    featuredMessage.value = `特色模型已更新（${list.length}）`
  } catch (e: unknown) {
    featuredError.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    featuredSaving.value = false
  }
}

async function previewRecommendedFeatured() {
  featuredRecommendLoading.value = true
  featuredRecommendMessage.value = ''
  try {
    const r = await getFeaturedModelsDynamic()
    const list: FeaturedModel[] = (r.models ?? []).filter((m) => m.name)
    featuredRecommendPreview.value = list
    if (list.length === 0) {
      featuredRecommendMessage.value = '无可推荐模型：最近 7 天无成功请求，或 featured_models 与 popular 均空'
    } else {
      const tagged = list.map((m) => `${m.name}${m.count > 0 ? ` (${m.count})` : ''}`)
      featuredRecommendMessage.value = `已加载 ${list.length} 个推荐：${tagged.slice(0, 5).join('、')}${list.length > 5 ? '…' : ''}`
    }
  } catch (e: unknown) {
    featuredRecommendMessage.value = e instanceof Error ? e.message : '加载推荐失败'
    featuredRecommendPreview.value = []
  } finally {
    featuredRecommendLoading.value = false
  }
}

function adoptRecommendedFeatured() {
  if (featuredRecommendPreview.value.length === 0) return
  const recommended = featuredRecommendPreview.value.map((m) => m.name)
  const merged = Array.from(new Set([...featuredArray.value, ...recommended]))
  featuredArray.value = merged
  featuredRecommendMessage.value = `已将 ${recommended.length} 个推荐合并到下方（总 ${merged.length}），点击「保存特色模型」生效`
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
  syncTabFromRoute()
  await reloadAll()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h2>模型与目录</h2>
      <div v-if="activeTab === 'canonical'" style="display:flex;gap:8px;align-items:center">
        <span class="badge badge-gray">{{ filtered.length }} 个模型</span>
        <button v-if="!readOnly" class="btn btn-ghost btn-sm" @click="openFeaturedDrawer">
          ★ 特色模型
        </button>
        <button v-if="!readOnly" class="btn btn-primary btn-sm" @click="showCreateModal = true">
          新增模型
        </button>
        <button v-if="!readOnly" class="btn btn-ghost btn-sm" :disabled="discovering" @click="runDiscovery">
          {{ discovering ? '扫描中…' : '扫描供应商模型' }}
        </button>
      </div>
    </div>

    <div class="tab-bar" style="margin-bottom:16px">
      <button
        v-if="isPlatformOps"
        type="button"
        class="tab-btn"
        :class="{ active: activeTab === 'canonical' }"
        @click="setTab('canonical')"
      >
        规范模型 <span class="tab-count">{{ filtered.length }}</span>
      </button>
      <button
        type="button"
        class="tab-btn"
        :class="{ active: activeTab === 'catalog' }"
        @click="setTab('catalog')"
      >
        提供商目录
      </button>
    </div>

    <CatalogPanel v-if="activeTab === 'catalog'" />

    <template v-else>
    <div v-if="readOnly" class="alert alert-info" style="margin-bottom:12px">
      📖 您是租户管理员，当前为只读模式。模型目录仅供查看，不能创建、编辑或删除模型。
    </div>

    <!-- 发现任务状态 -->
    <div v-if="showDiscoveryCard" class="card discovery-card">
      <div class="card-header"><h3>发现任务</h3></div>
      <div class="card-body">
        <div v-if="discoverRun" class="summary-row">
          <span
            class="badge"
            :class="discoverRun.status === 'succeeded' ? 'badge-green' : discoverRun.status === 'failed' ? 'badge-red' : (discovering ? 'badge-blue' : 'badge-gray')"
          >{{ discovering ? discoverRun.status : (discoverRun.status === 'running' ? '空闲' : discoverRun.status) }}</span>
          <span class="badge badge-gray">{{ discoverRun.trigger }}</span>
          <span class="muted small">#{{ discoverRun.id }} · {{ discoverMessage || (discovering ? '扫描进行中' : '最近一次扫描') }}</span>
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
          <button v-if="activeTags.length || statusFilter || selectedVendor || pickedModel.trim() || textSearch.trim()" class="btn btn-ghost btn-sm" @click="clearFilters">清空</button>
        </div>
      </div>
      <div class="card-body">
        <ModelCatalogFilterBar
          v-model:picked-model="pickedModel"
          v-model:filter-vendor="selectedVendor"
          v-model:extra-filter="statusFilter"
          v-model:text-search="textSearch"
          :vendor-options="vendors"
          :count="filtered.length"
          picker-title="模型管理 · 标准模型筛选"
          picker-placeholder="选择标准模型…"
          status-label="全部状态"
          :status-options="modelStatusOptions"
          show-text-search
          text-search-placeholder="family / 标签…"
          :show-clear="false"
          @status-change="onStatusFilterChange"
        />

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
              <td>{{ modelVendorName(m) }}</td>
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

    <!-- 特色模型抽屉 -->
    <div v-if="showFeaturedDrawer" class="drawer-backdrop" @click="closeFeaturedDrawer">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <h3>★ 特色模型 (Featured)</h3>
          <button class="btn btn-ghost btn-sm" @click="closeFeaturedDrawer">关闭</button>
        </div>
        <div class="drawer-body">
          <p class="muted small" style="margin-top:0">
            路由 v2 在「仅特色」筛选、ClientConfig 默认模型集等场景使用此列表。已存 <code>routing_policy.featured_models</code>，仅 <code>default</code> 租户生效。
          </p>

          <div v-if="featuredError" class="alert alert-error">{{ featuredError }}</div>
          <div v-if="featuredMessage" class="alert alert-success">{{ featuredMessage }}</div>

          <div v-if="featuredLoading" class="muted">加载中…</div>
          <template v-else>
            <div class="featured-recommend-bar">
              <button class="btn btn-sm btn-ghost" :disabled="featuredRecommendLoading" @click="previewRecommendedFeatured">
                {{ featuredRecommendLoading ? '加载中…' : '⚡ 自动推荐（7d 热门 + 当前策略）' }}
              </button>
              <button
                v-if="featuredRecommendPreview.length"
                class="btn btn-sm btn-primary"
                @click="adoptRecommendedFeatured"
              >采用推荐到选择器</button>
              <span v-if="featuredRecommendMessage" class="featured-recommend-msg">{{ featuredRecommendMessage }}</span>
            </div>

            <div class="form-group">
              <label>特色模型列表（多选）</label>
              <ModelPicker
                v-model="featuredArray"
                mode="multi"
                placeholder="选择特色模型…"
                title="特色模型（多选）"
              />
            </div>

            <div class="drawer-actions">
              <button class="btn btn-primary" @click="saveFeatured" :disabled="featuredSaving">
                {{ featuredSaving ? '保存中…' : '保存特色模型' }}
              </button>
              <button class="btn btn-ghost" @click="closeFeaturedDrawer">关闭</button>
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 模型详情弹层 -->
    <div v-if="detail" class="drawer-backdrop" @click="detail = null">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <h3>{{ detail.canonical_name }}</h3>
          <button class="btn btn-ghost btn-sm" @click="detail = null">关闭</button>
        </div>
        <div class="drawer-body">
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
      <div class="modal-content create-modal" @click.stop>
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
    </template>
  </div>
</template>

<style scoped>
.tab-bar { display: flex; gap: 8px; flex-wrap: wrap; }
.tab-btn {
  border: 1px solid var(--border);
  background: rgba(255,255,255,.02);
  border-radius: 999px;
  padding: 6px 14px;
  font-size: 13px;
  cursor: pointer;
  color: var(--muted);
}
.tab-btn:hover:not(.active) { color: var(--text); }
.tab-btn.active {
  border-color: var(--primary, #6366f1);
  color: var(--text);
  font-weight: 600;
  outline: 2px solid color-mix(in srgb, var(--primary, #6366f1) 24%, transparent);
}
.tab-count {
  font-size: 11px;
  opacity: .75;
  margin-left: 2px;
}

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
  background: rgba(139,148,158,.15);
  border-radius: 999px;
  padding: 4px 9px;
  font-size: 12px;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text);
}
.family-chip code {
  color: var(--text-muted, var(--muted));
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

.drawer-actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}

.drawer-backdrop {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  z-index: 1000;
}

.drawer-panel {
  position: fixed;
  top: 0;
  right: 0;
  bottom: 0;
  width: 680px;
  max-width: 90vw;
  border-radius: 0;
  overflow-y: auto;
  z-index: 1001;
}

.drawer-panel-wide {
  width: 860px;
}

.drawer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 20px 24px;
  border-bottom: 1px solid var(--border);
}

.drawer-header h3 {
  margin: 0;
  font-size: 18px;
}

.drawer-body {
  padding: 24px;
}

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

.create-modal {
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
  .create-modal { margin: 20px; }
  .drawer-panel { width: 95vw; }
  .drawer-panel-wide { width: 95vw; }
  .filter-card .card-header { align-items: flex-start; }
  .filter-heading { width: 100%; }
  .filter-header-actions { width: 100%; justify-content: flex-start; }
  .filter-row { flex-direction: column; }
  .namespace-panel { grid-template-columns: 1fr; max-height: 280px; }
}
</style>
