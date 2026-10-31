<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { useTopbarAction } from '@/components/shell/topbarActions'
import {
  createCanonicalAlias,
  fetchAvailableModels,
  fetchCanonicalModel,
  fetchCanonicalModels,
  fetchFeaturedModels,
  updateCanonicalAlias,
  updateCanonicalTags,
  saveFeaturedModels,
  updateCanonicalModel,
  type CanonicalModel,
  type CanonicalModelDetail,
  type ModelStatus,
} from '@/api/models'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'
import AppSheet from '@/components/common/AppSheet.vue'
import AppStateView from '@/components/common/AppStateView.vue'
import AppIcon from '@/components/common/AppIcon.vue'

useHyperPage({ title: () => t('models.title') })
const auth = useAuthStore()
const canManage = computed(() => auth.role === 'super_admin')
const models = ref<CanonicalModel[]>([])
const featured = ref<string[]>([])
const featuredDraft = ref<string[]>([])
const query = ref('')
const tab = ref<'all' | 'featured'>('all')
const loading = ref(true)
const savingFeatured = ref(false)
const loadingDetail = ref(false)
const savingDetail = ref(false)
const error = ref('')
const detailError = ref('')
const detail = ref<CanonicalModelDetail | null>(null)
const detailOpen = computed({
  get: () => detail.value !== null,
  set: (open: boolean) => { if (!open) detail.value = null },
})
const form = ref({ display_name: '', family: '', modality: 'text', context_window: '', parameters_b: '', status: 'active' as ModelStatus, disabled_reason: '', notes: '', reasoning_caps: '', multimodal_caps: '', tags: '' })
const aliasForm = ref({ raw_name: '', surface: '', quantization: '', notes: '' })
const aliasSaving = ref(false)
const aliasForms = ref<Record<number, { raw_name: string; surface: string; quantization: string; status: ModelStatus; notes: string }>>({})

useTopbarAction({
  key: 'refresh',
  label: () => t('common.refresh'),
  icon: 'refresh',
  disabled: () => loading.value,
  onClick: load,
})

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return models.value.filter((model) => {
    if (tab.value === 'featured' && !featured.value.includes(model.canonical_name)) return false
    if (!q) return true
    return [model.canonical_name, model.display_name ?? '', model.family ?? '', model.vendor ?? '', ...model.tags].some((v) => v.toLowerCase().includes(q))
  })
})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [catalog, available] = await Promise.all([fetchCanonicalModels(), fetchAvailableModels()])
    models.value = catalog.items
    const fallback = available.families.flatMap((family) => family.versions.filter((version) => version.featured).map((version) => version.canonical_name))
    if (canManage.value) {
      try {
        const configured = await fetchFeaturedModels()
        featured.value = configured.featured_models
      } catch {
        // Keep the catalog's feature markers visible if the write-protected policy API is unavailable.
        featured.value = fallback
      }
    } else {
      featured.value = fallback
    }
    featuredDraft.value = [...featured.value]
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('models.loadFailed')
  } finally {
    loading.value = false
  }
}

function toggleFeatured(name: string): void {
  featuredDraft.value = featuredDraft.value.includes(name)
    ? featuredDraft.value.filter((item) => item !== name)
    : [...featuredDraft.value, name]
}

async function saveFeatured(): Promise<void> {
  if (!canManage.value || savingFeatured.value) return
  savingFeatured.value = true
  error.value = ''
  try {
    const next = [...new Set(featuredDraft.value)]
    await saveFeaturedModels(next)
    featured.value = next
    featuredDraft.value = [...featured.value]
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : t('models.saveFailed')
  } finally {
    savingFeatured.value = false
  }
}

async function openModel(model: CanonicalModel): Promise<void> {
  detail.value = null
  detailError.value = ''
  loadingDetail.value = true
  aliasForm.value = { raw_name: '', surface: '', quantization: '', notes: '' }
  try {
    const loaded = await fetchCanonicalModel(model.id)
    detail.value = loaded
    form.value = {
      display_name: loaded.display_name ?? '',
      family: loaded.family ?? '',
      modality: loaded.modality || 'text',
      context_window: loaded.context_window == null ? '' : String(loaded.context_window),
      parameters_b: loaded.parameters_b == null ? '' : String(loaded.parameters_b),
      status: loaded.status,
      disabled_reason: loaded.disabled_reason ?? '',
      notes: loaded.notes ?? '',
      reasoning_caps: loaded.reasoning_caps ? JSON.stringify(loaded.reasoning_caps, null, 2) : '',
      multimodal_caps: (loaded.multimodal_caps ?? []).join(', '),
      tags: loaded.tags.join(', '),
    }
    aliasForms.value = Object.fromEntries(loaded.aliases.map((alias) => [alias.id, {
      raw_name: alias.raw_name, surface: alias.surface ?? '', quantization: alias.quantization ?? '', status: alias.status, notes: alias.notes ?? '',
    }]))
  } catch (cause) {
    detailError.value = cause instanceof Error ? cause.message : t('models.loadDetailFailed')
  } finally {
    loadingDetail.value = false
  }
}

function numberOrNull(value: string): number | null | undefined {
  if (!value.trim()) return null
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : undefined
}

async function saveModel(): Promise<void> {
  if (!detail.value || !canManage.value || savingDetail.value) return
  const contextWindow = numberOrNull(form.value.context_window)
  const parameters = numberOrNull(form.value.parameters_b)
  if (contextWindow === undefined || parameters === undefined) {
    detailError.value = t('models.invalidNumber')
    return
  }
  let reasoningCaps: Record<string, unknown> | null = null
  if (form.value.reasoning_caps.trim()) {
    try {
      const parsed: unknown = JSON.parse(form.value.reasoning_caps)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('expected object')
      reasoningCaps = parsed as Record<string, unknown>
    } catch {
      detailError.value = t('models.invalidReasoningCaps')
      return
    }
  }
  savingDetail.value = true
  detailError.value = ''
  try {
    let updated = await updateCanonicalModel(detail.value.id, {
      display_name: form.value.display_name.trim() || null,
      family: form.value.family.trim() || null,
      modality: form.value.modality,
      context_window: contextWindow,
      parameters_b: parameters,
      status: form.value.status,
      disabled_reason: form.value.disabled_reason.trim() || null,
      notes: form.value.notes.trim() || null,
      reasoning_caps: reasoningCaps,
      multimodal_caps: form.value.multimodal_caps.split(',').map((item) => item.trim()).filter(Boolean),
    })
    const tags = [...new Set(form.value.tags.split(',').map((item) => item.trim()).filter(Boolean))]
    if (tags.join('\u0000') !== detail.value.tags.join('\u0000')) updated = await updateCanonicalTags(detail.value.id, tags)
    Object.assign(detail.value, updated)
    const row = models.value.find((item) => item.id === updated.id)
    if (row) Object.assign(row, updated)
  } catch (cause) {
    detailError.value = cause instanceof Error ? cause.message : t('models.saveFailed')
  } finally {
    savingDetail.value = false
  }
}

async function addAlias(): Promise<void> {
  if (!detail.value || !canManage.value || aliasSaving.value || !aliasForm.value.raw_name.trim()) return
  aliasSaving.value = true
  detailError.value = ''
  try {
    await createCanonicalAlias(detail.value.id, {
      raw_name: aliasForm.value.raw_name.trim(),
      surface: aliasForm.value.surface.trim() || null,
      quantization: aliasForm.value.quantization.trim() || null,
      notes: aliasForm.value.notes.trim() || null,
    })
    detail.value = await fetchCanonicalModel(detail.value.id)
    aliasForm.value = { raw_name: '', surface: '', quantization: '', notes: '' }
  } catch (cause) {
    detailError.value = cause instanceof Error ? cause.message : t('models.saveFailed')
  } finally {
    aliasSaving.value = false
  }
}

async function saveAlias(aliasId: number): Promise<void> {
  if (!detail.value || !canManage.value || aliasSaving.value) return
  const alias = aliasForms.value[aliasId]
  if (!alias?.raw_name.trim()) return
  aliasSaving.value = true
  detailError.value = ''
  try {
    await updateCanonicalAlias(detail.value.id, aliasId, {
      raw_name: alias.raw_name.trim(), surface: alias.surface.trim() || null,
      quantization: alias.quantization.trim() || null, status: alias.status, notes: alias.notes.trim() || null,
    })
    detail.value = await fetchCanonicalModel(detail.value.id)
    aliasForms.value = Object.fromEntries(detail.value.aliases.map((item) => [item.id, {
      raw_name: item.raw_name, surface: item.surface ?? '', quantization: item.quantization ?? '', status: item.status, notes: item.notes ?? '',
    }]))
  } catch (cause) {
    detailError.value = cause instanceof Error ? cause.message : t('models.saveFailed')
  } finally {
    aliasSaving.value = false
  }
}

onMounted(() => { void load() })
</script>

<template>
  <div class="view-root model-catalog">
    <div class="model-catalog__toolbar">
      <div class="model-catalog__tabs" role="tablist" :aria-label="t('models.title')">
        <button type="button" role="tab" :aria-selected="tab === 'all'" :class="{ active: tab === 'all' }" @click="tab = 'all'">{{ t('models.allModels') }}</button>
        <button type="button" role="tab" :aria-selected="tab === 'featured'" :class="{ active: tab === 'featured' }" @click="tab = 'featured'">★ {{ t('models.featured') }} <span>{{ featured.length }}</span></button>
      </div>
    </div>
    <label class="model-catalog__search"><span>{{ t('common.search') }}</span><input v-model="query" type="search" :placeholder="t('models.searchPlaceholder')" /></label>
    <p v-if="error" class="model-catalog__error" role="alert">{{ error }}</p>
    <AppStateView v-if="loading" :loading="true" />
    <template v-else>
      <section v-if="tab === 'featured'" class="model-catalog__featured">
        <div><strong class="model-catalog__featured-title"><AppIcon name="star" :size="14" filled />{{ t('models.featured') }}</strong><p>{{ t('models.featuredDescription') }}</p></div>
        <button v-if="canManage" type="button" class="btn btn--primary" :disabled="savingFeatured || featuredDraft.length === featured.length && featuredDraft.every((x) => featured.includes(x))" @click="saveFeatured">{{ savingFeatured ? t('common.loading') : t('models.saveFeatured') }}</button>
      </section>
      <p v-if="!filtered.length" class="model-catalog__empty">{{ tab === 'featured' ? t('models.noFeatured') : t('common.empty') }}</p>
      <ul v-else class="model-catalog__list">
        <li v-for="model in filtered" :key="model.id" class="model-catalog__card">
          <button type="button" class="model-catalog__open" @click="openModel(model)">
            <span class="model-catalog__identity"><strong>{{ model.display_name || model.canonical_name }}</strong><code>{{ model.canonical_name }}</code></span>
            <span class="model-catalog__meta"><span class="badge badge--muted">{{ model.vendor || model.family || t('common.unknown') }}</span><span class="badge" :class="model.status === 'active' ? 'badge--success' : 'badge--muted'">{{ model.status }}</span><span v-if="featured.includes(model.canonical_name)" class="badge badge--info"><AppIcon name="star" :size="11" filled />{{ t('models.featured') }}</span></span>
            <span class="model-catalog__stats">{{ model.modality }} · {{ model.context_window == null ? '—' : `${model.context_window.toLocaleString()} ${t('models.tokenUnit')}` }} · {{ model.offer_count ?? 0 }} {{ t('models.providers') }}</span>
          </button>
          <button v-if="canManage" type="button" class="model-catalog__star" :class="{ 'is-on': featuredDraft.includes(model.canonical_name) }" :aria-pressed="featuredDraft.includes(model.canonical_name)" :aria-label="t(featuredDraft.includes(model.canonical_name) ? 'models.removeFeatured' : 'models.addFeatured')" @click="toggleFeatured(model.canonical_name)"><AppIcon name="star" :size="24" :filled="featuredDraft.includes(model.canonical_name)" /></button>
        </li>
      </ul>
      <p v-if="canManage && tab === 'all'" class="model-catalog__hint">{{ t('models.featuredChangesHint') }}</p>
    </template>

    <AppSheet v-model="detailOpen" presentation="sheet" :title="detail?.canonical_name ?? t('models.detail')">
      <AppStateView v-if="loadingDetail" :loading="true" />
      <p v-if="detailError" class="model-catalog__error" role="alert">{{ detailError }}</p>
      <template v-if="detail && !loadingDetail">
        <p class="model-catalog__readonly"><span>{{ t('models.canonicalName') }}</span><code>{{ detail.canonical_name }}</code></p>
        <fieldset class="model-catalog__form" :disabled="!canManage || savingDetail">
          <label><span>{{ t('models.displayName') }}</span><input v-model="form.display_name" /></label>
          <label><span>{{ t('models.family') }}</span><input v-model="form.family" /></label>
          <label><span>{{ t('models.status') }}</span><select v-model="form.status"><option v-for="status in (['active','disabled','deprecated','hidden'] as ModelStatus[])" :key="status" :value="status">{{ status }}</option></select></label>
          <label><span>{{ t('models.modality') }}</span><select v-model="form.modality"><option v-for="item in ['text','vision','audio','multimodal','embedding']" :key="item" :value="item">{{ item }}</option></select></label>
          <label><span>{{ t('models.contextWindowLabel') }}</span><input v-model="form.context_window" inputmode="numeric" type="number" min="0" /></label>
          <label><span>{{ t('models.parametersLabel') }}</span><input v-model="form.parameters_b" inputmode="decimal" type="number" min="0" step="any" /></label>
          <label><span>{{ t('models.disabledReason') }}</span><input v-model="form.disabled_reason" /></label>
          <label><span>{{ t('models.notes') }}</span><textarea v-model="form.notes" rows="3" /></label>
          <label><span>{{ t('models.reasoningCaps') }}</span><textarea v-model="form.reasoning_caps" rows="4" spellcheck="false" /></label>
          <label><span>{{ t('models.multimodalCaps') }}</span><input v-model="form.multimodal_caps" /></label>
          <label><span>{{ t('models.tags') }}</span><input v-model="form.tags" /></label>
        </fieldset>
        <button v-if="canManage" type="button" class="btn btn--primary model-catalog__save" :disabled="savingDetail" @click="saveModel">{{ savingDetail ? t('common.loading') : t('common.save') }}</button>
        <section class="model-catalog__aliases">
          <h3>{{ t('models.aliases') }} <span>{{ detail.aliases.length }}</span></h3>
          <p v-if="!detail.aliases.length" class="model-catalog__hint">{{ t('models.noAliases') }}</p>
          <div v-for="alias in detail.aliases" :key="alias.id" class="model-catalog__alias-editor">
            <template v-if="canManage && aliasForms[alias.id]">
              <label><span>{{ t('models.rawAlias') }}</span><input v-model="aliasForms[alias.id]!.raw_name" /></label>
              <label><span>{{ t('models.surface') }}</span><input v-model="aliasForms[alias.id]!.surface" /></label>
              <label><span>{{ t('models.quantization') }}</span><input v-model="aliasForms[alias.id]!.quantization" /></label>
              <label><span>{{ t('models.status') }}</span><select v-model="aliasForms[alias.id]!.status"><option v-for="status in (['active','disabled','deprecated','hidden'] as ModelStatus[])" :key="status" :value="status">{{ status }}</option></select></label>
              <label><span>{{ t('models.notes') }}</span><input v-model="aliasForms[alias.id]!.notes" /></label>
              <button type="button" class="btn" :disabled="aliasSaving" @click="saveAlias(alias.id)">{{ aliasSaving ? t('common.loading') : t('common.save') }}</button>
            </template>
            <div v-else class="model-catalog__alias"><code>{{ alias.raw_name }}</code><span>{{ alias.surface || alias.quantization || '—' }}</span><span class="badge badge--muted">{{ alias.status }}</span></div>
          </div>
          <div v-if="detail.offers.length" class="model-catalog__offers">
            <h4>{{ t('models.providerOffers') }}</h4>
            <article v-for="offer in detail.offers" :key="`${offer.provider_id}-${offer.credential_id}-${offer.raw_model_name}`">
              <div><strong>{{ offer.provider_name }}</strong><small>{{ offer.credential_label }} · {{ offer.raw_model_name }}</small></div>
              <span class="badge" :class="offer.available ? 'badge--success' : 'badge--danger'">{{ offer.available ? t('providers.available') : t('providers.unavailable') }}</span>
              <small>{{ offer.p95_latency_ms == null ? '—' : `${offer.p95_latency_ms}ms` }} · {{ offer.success_rate == null ? '—' : `${(offer.success_rate * 100).toFixed(1)}%` }}</small>
            </article>
          </div>
          <div v-if="canManage" class="model-catalog__alias-form">
            <label><span>{{ t('models.rawAlias') }}</span><input v-model="aliasForm.raw_name" /></label>
            <label><span>{{ t('models.surface') }}</span><input v-model="aliasForm.surface" /></label>
            <label><span>{{ t('models.quantization') }}</span><input v-model="aliasForm.quantization" /></label>
            <button type="button" class="btn" :disabled="aliasSaving || !aliasForm.raw_name.trim()" @click="addAlias">{{ aliasSaving ? t('common.loading') : t('models.addAlias') }}</button>
          </div>
        </section>
      </template>
    </AppSheet>
  </div>
</template>

<style scoped>
/* ⚠️ 2026-10-13 真机实测（同 UserUsageView 的缺陷，同一根因）：
   根容器只有 `padding-bottom`，**缺横向内边距** ⇒「搜索」标签与模型卡片
   直接贴屏幕边缘。像素实测（`.maestro/device/measure-insets.py`）：
   本页左 1.0pt / 右 1.0pt，而正常页是 15.5~16pt。
   ⚠️ 同一批测量里其余页面全是 15.5~16pt ⇒ 影响面**不是全站**，逐页量、逐页改。
   ⚠️ 别改成 `.page` 类 —— 本视图不是滚动宿主（滚动宿主是 HyperList）。
   ⚠️ 只补横向；`padding-bottom` 原来就有。
   ⚠️ 2026-10-28 订正：上面「滚动宿主是 HyperList」对本页**从未成立** —— 目录是
   一次性全量加载（无分页 API），从未接 HyperList，根容器也不带 overflow ⇒ 壳层
   `.hyper-app__main`（纯布局容器，overflow:hidden，07 §1 一个轴一个主宿主）把首屏
   以下全部剪裁。真机实测（vivo V2436A / llm.itestu.cn /m/models）CDP 读数：内容
   scrollHeight 421,636px、链上**零滚动宿主** ⇒「模型目录无法滑动」。修法已上移到
   全局兜底：shared.css 的 `.hyper-app__main > *`（2026-10-28 审计 88 个视图同病，
   逐页补不现实）；本页根类因此不再自带 overflow，避免同一元素两处声明。 */
.model-catalog {
  display: grid;
  gap: 12px;
  padding-inline: var(--app-space-content-x);
  padding-bottom: var(--app-safe-bottom);
}
.model-catalog__toolbar, .model-catalog__featured, .model-catalog__identity, .model-catalog__meta, .model-catalog__alias { display: flex; align-items: center; justify-content: space-between; gap: 10px; min-width: 0; }
.model-catalog__tabs { display: flex; gap: 6px; overflow-x: auto; }
.model-catalog__tabs button { min-height: 48px; padding: 0 14px; border: 1px solid var(--app-border); border-radius: 999px; background: var(--app-surface); color: var(--app-text); white-space: nowrap; }
.model-catalog__tabs button.active { border-color: var(--app-primary); color: var(--app-primary); }
.model-catalog__tabs span, .model-catalog__aliases h3 span { color: var(--app-text-muted); }
.model-catalog__search, .model-catalog__form label, .model-catalog__alias-form label { display: grid; gap: 6px; color: var(--app-text-muted); font-size: 12px; }
.model-catalog input, .model-catalog select, .model-catalog textarea { width: 100%; min-height: 48px; padding: 10px 12px; border: 1px solid var(--app-border); border-radius: 10px; background: var(--app-surface); color: var(--app-text); font: inherit; }
.model-catalog__featured, .model-catalog__card, .model-catalog__readonly, .model-catalog__aliases { padding: 14px; border: 1px solid var(--app-border); border-radius: 12px; background: var(--app-surface); }
.model-catalog__featured p, .model-catalog__hint { margin: 5px 0 0; color: var(--app-text-muted); font-size: 12px; }
.model-catalog__list { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
.model-catalog__card { display: flex; align-items: center; gap: 8px; }
.model-catalog__open { display: grid; flex: 1; min-width: 0; gap: 8px; padding: 0; border: 0; background: transparent; color: inherit; text-align: left; }
.model-catalog__identity { align-items: flex-start; flex-direction: column; }
.model-catalog__identity strong { overflow-wrap: anywhere; }
.model-catalog code { overflow-wrap: anywhere; font-size: 12px; }
.model-catalog__meta { justify-content: flex-start; flex-wrap: wrap; }
.model-catalog__stats, .model-catalog__readonly { color: var(--app-text-muted); font-size: 12px; }
/* ★ 2026-10-27 第 27 增量：★ 精选标记原来用 Unicode 字形 `★` / `☆`
 *   ⇒ craft-floor 硬禁「Unicode 字形顶替图标系统」⇒ ⇒ 换成画出来的星标。
 *   ★ 实心/空心两态由 `filled` 属性切 `fill`（★ 一枚路径 ⇒ 一个概念一份画法）；
 *     桌面端同概念用 element-plus `<StarFilled />`（ModelPicker / RoutingOverviewView）
 *     ⇒ ★ 两端画法一致。 */
.model-catalog__star { width: 48px; min-width: 48px; height: 48px; border: 0; background: transparent; color: var(--app-primary); display: inline-flex; align-items: center; justify-content: center; }
.model-catalog__featured-title { display: inline-flex; align-items: center; gap: 6px; }
.model-catalog__form { display: grid; gap: 12px; margin: 16px 0; padding: 0; border: 0; }
.model-catalog__form:disabled { opacity: .7; }
.model-catalog__save { width: 100%; min-height: 48px; }
.model-catalog__aliases { display: grid; gap: 10px; margin-top: 16px; }
.model-catalog__aliases h3 { margin: 0; font-size: 14px; }
.model-catalog__alias { justify-content: flex-start; flex-wrap: wrap; min-height: 48px; border-top: 1px solid var(--app-border); }
.model-catalog__alias-editor, .model-catalog__offers { display: grid; gap: 10px; padding: 12px 0; border-top: 1px solid var(--app-border); }
.model-catalog__alias-editor label { display: grid; gap: 6px; color: var(--app-text-muted); font-size: 12px; }
.model-catalog__offers h4 { margin: 0; font-size: 14px; }
.model-catalog__offers article { display: grid; grid-template-columns: 1fr auto; gap: 5px 10px; padding: 10px; border: 1px solid var(--app-border); border-radius: 10px; }
.model-catalog__offers article div { display: grid; gap: 4px; min-width: 0; }
.model-catalog__offers article small { color: var(--app-text-muted); overflow-wrap: anywhere; }
.model-catalog__alias-form { display: grid; gap: 10px; padding-top: 10px; border-top: 1px solid var(--app-border); }
.model-catalog__error { color: var(--app-danger); overflow-wrap: anywhere; }
.model-catalog__empty { text-align: center; color: var(--app-text-muted); }
@media (min-width: 700px) { .model-catalog__list { grid-template-columns: repeat(2,minmax(0,1fr)); } .model-catalog__form { grid-template-columns: repeat(2,minmax(0,1fr)); } }
</style>
