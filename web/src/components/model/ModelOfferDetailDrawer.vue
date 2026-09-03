<script setup lang="ts">
import { reactive, watch, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { ModelOffer } from '../../api/providers'
import {
  updateModelOffer, toggleModelOfferState, getModelOfferSuggestions,
  type ModelOfferSuggestion,
} from '../../api/providers'
import { updateModel } from '../../api/models'
import ModelIdentityChip from './ModelIdentityChip.vue'
import ModelOfferExtrasPanel from './ModelOfferExtrasPanel.vue'
import { useCredentialLabels } from '../../composables/useCredentialLabels'
import {
  BILLING_MODES,
  PRICE_FIELDS,
  normalizePrice,
  validatePricing,
  type PriceField,
} from '../../utils/modelOfferPricing'

const props = withDefaults(defineProps<{
  providerId: number
  offer: ModelOffer | null
  siblingOffers?: ModelOffer[]
  canEdit?: boolean
}>(), { canEdit: true })

const emit = defineEmits<{ close: []; updated: [ModelOffer]; iqTested: [] }>()
const router = useRouter()
// credentialDisplayName resolves credential id → human label; the composable
// keeps a Map<id,label> refreshed via loadCredentialLabels(), and falls back
// to "凭据 #ID" if the label is missing.
const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()

const saving = ref(false)
const saveErr = ref('')
const toggling = ref(false)
const suggest = ref<ModelOfferSuggestion | null>(null)
const initialContextWindow = ref<number | null>(null)
const initialPrices = reactive({
  unit_price_in_per_1m: null as number | null,
  unit_price_out_per_1m: null as number | null,
  cache_read_price_per_1m: null as number | null,
  cache_write_price_per_1m: null as number | null,
  billing_mode: null as string | null,
})

const BILLING_MODE_OPTIONS = BILLING_MODES.map(value => ({ value, label: value }))

const draft = reactive({
  standardized_name: '',
  canonical_id: null as number | null,
  outbound_model_name: '',
  context_window: null as number | '' | null,
  unit_price_in_per_1m: null as number | '' | null,
  unit_price_out_per_1m: null as number | '' | null,
  cache_read_price_per_1m: null as number | '' | null,
  cache_write_price_per_1m: null as number | '' | null,
  billing_mode: 'per_token',
  modality: 'text',
  thinking_supported: false,
  thinking_dialect: '',
})

watch(() => props.offer, (o) => {
  if (!o) return
  // Refresh the label cache so credentialDisplayName() resolves to the latest
  // label when the drawer opens against a freshly edited offer.
  void loadCredentialLabels()
  draft.standardized_name = o.standardized_name ?? ''
  draft.canonical_id = o.canonical_id
  draft.outbound_model_name = o.outbound_model_name ?? ''
  draft.context_window = o.context_window_override ?? null
  initialContextWindow.value = o.context_window_override ?? null
  draft.unit_price_in_per_1m = o.unit_price_in_per_1m ?? null
  draft.unit_price_out_per_1m = o.unit_price_out_per_1m ?? null
  draft.cache_read_price_per_1m = o.cache_read_price_per_1m ?? null
  draft.cache_write_price_per_1m = o.cache_write_price_per_1m ?? null
  draft.billing_mode = o.billing_mode || 'per_token'
  initialPrices.unit_price_in_per_1m = o.unit_price_in_per_1m ?? null
  initialPrices.unit_price_out_per_1m = o.unit_price_out_per_1m ?? null
  initialPrices.cache_read_price_per_1m = o.cache_read_price_per_1m ?? null
  initialPrices.cache_write_price_per_1m = o.cache_write_price_per_1m ?? null
  initialPrices.billing_mode = o.billing_mode ?? null
  draft.modality = o.modality || 'text'
  const caps = o.reasoning_caps as { supported?: boolean; dialect?: string } | null | undefined
  draft.thinking_supported = !!caps?.supported
  draft.thinking_dialect = caps?.dialect || ''
  saveErr.value = ''
  loadSuggest()
}, { immediate: true })

async function loadSuggest() {
  if (!props.offer) return
  try {
    suggest.value = await getModelOfferSuggestions(props.providerId, props.offer.id)
  } catch {
    suggest.value = null
  }
}

function applyRuleBased() {
  if (!suggest.value?.rule_based) return
  draft.standardized_name = suggest.value.rule_based
  const match = suggest.value.canonical_options.find(
    c => (c.canonical_name || '').toLowerCase() === draft.standardized_name.toLowerCase(),
  )
  draft.canonical_id = match ? match.id : null
}

function priceChanged(field: PriceField): boolean {
  return normalizePrice(draft[field]) !== initialPrices[field]
}

function validatePrices(): string | null {
  return validatePricing({
    unit_price_in_per_1m: draft.unit_price_in_per_1m,
    unit_price_out_per_1m: draft.unit_price_out_per_1m,
    cache_read_price_per_1m: draft.cache_read_price_per_1m,
    cache_write_price_per_1m: draft.cache_write_price_per_1m,
  }, draft.billing_mode)
}

async function saveNode() {
  if (!props.canEdit || !props.offer) return
  const validationError = validatePrices()
  if (validationError) {
    saveErr.value = validationError
    return
  }
  saving.value = true
  saveErr.value = ''
  try {
    const body: Parameters<typeof updateModelOffer>[2] = {
      standardized_name: draft.standardized_name.trim() || null,
      canonical_id: draft.canonical_id,
      outbound_model_name: draft.outbound_model_name.trim(),
    }
    const rawCw = draft.context_window
    if (rawCw !== initialContextWindow.value) {
      body.context_window =
        rawCw == null || rawCw === '' || (typeof rawCw === 'number' && rawCw <= 0) ? 0 : Number(rawCw)
    }
    for (const field of PRICE_FIELDS) {
      if (priceChanged(field)) {
        const value = normalizePrice(draft[field])
        if (value != null) body[field] = value
      }
    }
    const billingMode = draft.billing_mode || null
    if (billingMode !== initialPrices.billing_mode) body.billing_mode = billingMode

    const updated = await updateModelOffer(props.providerId, props.offer.id, body)
    initialContextWindow.value = updated.context_window_override ?? null
    initialPrices.unit_price_in_per_1m = updated.unit_price_in_per_1m ?? null
    initialPrices.unit_price_out_per_1m = updated.unit_price_out_per_1m ?? null
    initialPrices.cache_read_price_per_1m = updated.cache_read_price_per_1m ?? null
    initialPrices.cache_write_price_per_1m = updated.cache_write_price_per_1m ?? null
    initialPrices.billing_mode = updated.billing_mode ?? null
    draft.unit_price_in_per_1m = updated.unit_price_in_per_1m ?? null
    draft.unit_price_out_per_1m = updated.unit_price_out_per_1m ?? null
    draft.cache_read_price_per_1m = updated.cache_read_price_per_1m ?? null
    draft.cache_write_price_per_1m = updated.cache_write_price_per_1m ?? null
    draft.billing_mode = updated.billing_mode || 'per_token'
    emit('updated', {
      ...props.offer,
      standardized_name: updated.standardized_name,
      canonical_id: updated.canonical_id,
      outbound_model_name: updated.outbound_model_name ?? '',
      context_window: updated.context_window,
      context_window_override: updated.context_window_override,
      unit_price_in_per_1m: updated.unit_price_in_per_1m,
      unit_price_out_per_1m: updated.unit_price_out_per_1m,
      cache_read_price_per_1m: updated.cache_read_price_per_1m,
      cache_write_price_per_1m: updated.cache_write_price_per_1m,
      billing_mode: updated.billing_mode,
    })
  } catch (e: unknown) {
    saveErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function saveCanonical() {
  if (!props.canEdit || !props.offer?.canonical_id) {
    saveErr.value = '未关联标准模型，请先选择 canonical'
    return
  }
  saving.value = true
  saveErr.value = ''
  try {
    const reasoning_caps = draft.thinking_supported
      ? { supported: true, dialect: draft.thinking_dialect || undefined }
      : { supported: false }
    await updateModel(props.offer.canonical_id, {
      modality: draft.modality,
      reasoning_caps,
    })
    emit('updated', {
      ...props.offer,
      modality: draft.modality,
      reasoning_caps,
    })
  } catch (e: unknown) {
    saveErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    saving.value = false
  }
}

async function toggleAvail() {
  if (!props.canEdit || !props.offer) return
  toggling.value = true
  try {
    const res = await toggleModelOfferState(props.providerId, props.offer.id, {
      available: !props.offer.available,
    })
    emit('updated', { ...props.offer, available: res.available })
  } catch (e: unknown) {
    saveErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    toggling.value = false
  }
}

function goCanonical() {
  const name = props.offer?.canonical_name || props.offer?.standardized_name
  if (name) router.push({ path: '/models', query: { q: name } })
}
</script>

<template>
  <div v-if="offer" class="drawer-backdrop" @click="emit('close')">
    <div class="drawer-panel card drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <div>
          <h3 style="margin:0"><code>{{ offer.raw_model_name }}</code></h3>
          <div class="drawer-sub">#{{ offer.id }} · {{ credentialDisplayName(offer.credential_id) }} {{ offer.credential_label }}</div>
          <ModelIdentityChip
            style="margin-top:8px"
            :canonical-name="offer.canonical_name || offer.standardized_name"
            :outbound-model="offer.outbound_model_name"
            :raw-model="offer.raw_model_name"
            @click-canonical="goCanonical"
          />
        </div>
        <button type="button" class="btn btn-ghost btn-sm" @click="emit('close')">关闭</button>
      </div>

      <div class="drawer-body dual">
        <section class="pane">
          <h4>标准信息 <span v-if="offer.canonical_id" class="badge inherit">继承/可改</span></h4>
          <label class="field-label">标准名</label>
          <div class="cell-sub"><code>{{ offer.canonical_name || offer.standardized_name || '—' }}</code></div>
          <label class="field-label">状态</label>
          <div class="cell-sub">{{ offer.canonical_status || '—' }}</div>
          <label class="field-label">模态</label>
          <select v-model="draft.modality" class="field-input" :disabled="!canEdit">
            <option value="text">text</option>
            <option value="vision">vision</option>
            <option value="audio">audio</option>
            <option value="video">video</option>
            <option value="multimodal">multimodal</option>
            <option value="embedding">embedding</option>
          </select>
          <label class="field-label">思考能力</label>
          <label class="manual-toggle">
            <input v-model="draft.thinking_supported" type="checkbox" :disabled="!canEdit" />
            <span>支持 thinking / reasoning</span>
          </label>
          <input
            v-if="draft.thinking_supported"
            v-model="draft.thinking_dialect"
            class="field-input"
            :disabled="!canEdit"
            placeholder="dialect: openai / anthropic / glm …"
          />
          <div class="btn-row" style="margin-top:10px">
            <button class="btn btn-sm btn-primary" :disabled="!canEdit || saving || !offer.canonical_id" @click="saveCanonical">
              保存标准能力
            </button>
          </div>
        </section>

        <section class="pane">
          <h4>本节点
            <span v-if="offer.admin_protected" class="badge protect">手工保护</span>
            <span v-if="offer.context_window_override != null" class="badge override">context 覆盖</span>
          </h4>
          <div class="avail-row">
            <span class="avail-badge" :class="offer.available ? 'on' : 'off'">
              {{ offer.available ? '可用' : '不可用' }}
            </span>
            <button class="btn btn-sm" :disabled="!canEdit || toggling" @click="toggleAvail">
              {{ offer.available ? '停用' : '启用' }}
            </button>
          </div>
          <label class="field-label">标准化名</label>
          <input v-model="draft.standardized_name" class="field-input" :disabled="!canEdit" />
          <div v-if="suggest?.rule_based" class="cell-sub" style="margin-top:4px">
            规则建议
            <button type="button" class="btn btn-sm btn-ghost" @click="applyRuleBased">{{ suggest.rule_based }}</button>
          </div>
          <label class="field-label">关联 canonical</label>
          <select
            class="field-input"
            :disabled="!canEdit"
            :value="draft.canonical_id ?? ''"
            @change="(e: Event) => {
              const v = (e.target as HTMLSelectElement).value
              draft.canonical_id = v === '' ? null : Number(v)
            }"
          >
            <option value="">—</option>
            <option
              v-for="c in (suggest?.canonical_options ?? [])"
              :key="c.id"
              :value="c.id"
            >{{ c.canonical_name }}</option>
          </select>
          <label class="field-label">出站名 (outbound)</label>
          <input v-model="draft.outbound_model_name" class="field-input" :disabled="!canEdit" :placeholder="offer.raw_model_name" />
          <label class="field-label">上下文窗口覆盖</label>
          <input v-model.number="draft.context_window" type="number" min="0" class="field-input" :disabled="!canEdit" placeholder="空=继承标准" />
          <div class="cell-sub">
            生效:
            <code>{{ offer.context_window ?? '—' }}</code>
            <template v-if="offer.context_window_override != null">（本节点 {{ offer.context_window_override }}）</template>
          </div>
          <div class="price-grid">
            <div>
              <label class="field-label">输入价格 / 1M tokens</label>
              <input v-model.number="draft.unit_price_in_per_1m" type="number" min="0" step="0.001" class="field-input" :disabled="!canEdit" placeholder="保持现值" />
            </div>
            <div>
              <label class="field-label">输出价格 / 1M tokens</label>
              <input v-model.number="draft.unit_price_out_per_1m" type="number" min="0" step="0.001" class="field-input" :disabled="!canEdit" placeholder="保持现值" />
            </div>
            <div>
              <label class="field-label">缓存读取价格 / 1M tokens</label>
              <input v-model.number="draft.cache_read_price_per_1m" type="number" min="0" step="0.001" class="field-input" :disabled="!canEdit" placeholder="保持现值" />
            </div>
            <div>
              <label class="field-label">缓存写入价格 / 1M tokens</label>
              <input v-model.number="draft.cache_write_price_per_1m" type="number" min="0" step="0.001" class="field-input" :disabled="!canEdit" placeholder="保持现值" />
            </div>
          </div>
          <label class="field-label">计费模式</label>
            <select v-model="draft.billing_mode" class="field-input" :disabled="!canEdit">
              <option v-for="mode in BILLING_MODE_OPTIONS" :key="mode.value" :value="mode.value">{{ mode.label }}</option>
            </select>
          <div class="cell-sub">价格留空表示保持现值；显式 0 表示免费。</div>
          <div class="btn-row" style="margin-top:10px">
            <button class="btn btn-sm btn-primary" :disabled="!canEdit || saving" @click="saveNode">保存节点</button>
          </div>
        </section>
      </div>

      <div class="drawer-body" style="padding-top:0">
        <ModelOfferExtrasPanel
          :provider-id="providerId"
          :offer="offer"
          :sibling-offers="siblingOffers ?? []"
          :can-edit="canEdit"
          @iq-tested="emit('iqTested')"
        />
      </div>
      <div v-if="saveErr" class="drawer-footer cell-sub cell-sub--danger">{{ saveErr }}</div>
    </div>
  </div>
</template>

<style scoped>
.dual { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
@media (max-width: 900px) { .dual { grid-template-columns: 1fr; } }
.pane h4 { margin: 0 0 10px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.badge { font-size: 10px; padding: 2px 6px; border-radius: 4px; background: color-mix(in srgb, var(--accent) 15%, transparent); }
.badge.protect { background: color-mix(in srgb, var(--warning-dark) 20%, transparent); }
.badge.override { background: color-mix(in srgb, var(--accent) 20%, transparent); }
.avail-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.avail-badge { font-size: 12px; padding: 2px 8px; border-radius: 999px; }
.avail-badge.on { background: color-mix(in srgb, var(--success) 20%, transparent); }
.avail-badge.off { background: color-mix(in srgb, var(--danger) 20%, transparent); }
.field-label { display: block; margin-top: 8px; font-size: 12px; color: var(--muted); }
.field-input { width: 100%; margin-top: 4px; }
.price-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
@media (max-width: 520px) { .price-grid { grid-template-columns: 1fr; } }
</style>
