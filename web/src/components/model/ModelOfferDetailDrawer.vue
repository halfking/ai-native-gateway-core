<script setup lang="ts">
import { reactive, watch, ref } from 'vue'
import type { ModelOffer } from '../../api/providers'
import {
  updateModelOffer, toggleModelOfferState, getModelOfferSuggestions,
  type ModelOfferSuggestion,
} from '../../api/providers'
import { updateModel } from '../../api/models'
import ModelIdentityChip from './ModelIdentityChip.vue'

const props = defineProps<{
  providerId: number
  offer: ModelOffer | null
}>()

const emit = defineEmits<{ close: []; updated: [ModelOffer] }>()

const saving = ref(false)
const saveErr = ref('')
const toggling = ref(false)
const suggest = ref<ModelOfferSuggestion | null>(null)

const draft = reactive({
  standardized_name: '',
  canonical_id: null as number | null,
  outbound_model_name: '',
  context_window: '' as number | '' | null,
  modality: 'text',
  thinking_supported: false,
  thinking_dialect: '',
})

watch(() => props.offer, (o) => {
  if (!o) return
  draft.standardized_name = o.standardized_name ?? ''
  draft.canonical_id = o.canonical_id
  draft.outbound_model_name = o.outbound_model_name ?? ''
  draft.context_window = o.context_window_override ?? o.context_window ?? ''
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

async function saveNode() {
  if (!props.offer) return
  saving.value = true
  saveErr.value = ''
  try {
    const body: Parameters<typeof updateModelOffer>[2] = {
      standardized_name: draft.standardized_name.trim() || null,
      canonical_id: draft.canonical_id,
      outbound_model_name: draft.outbound_model_name.trim(),
    }
    const cw = draft.context_window
    if (cw === '' || cw == null) body.context_window = 0
    else body.context_window = Number(cw)
    const updated = await updateModelOffer(props.providerId, props.offer.id, body)
    emit('updated', {
      ...props.offer,
      standardized_name: updated.standardized_name,
      canonical_id: updated.canonical_id,
      outbound_model_name: updated.outbound_model_name ?? '',
      context_window: updated.context_window,
      context_window_override: updated.context_window_override,
    })
  } catch (e: any) {
    saveErr.value = e?.message || String(e)
  } finally {
    saving.value = false
  }
}

async function saveCanonical() {
  if (!props.offer?.canonical_id) {
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
    } as any)
    emit('updated', {
      ...props.offer,
      modality: draft.modality,
      reasoning_caps,
    })
  } catch (e: any) {
    saveErr.value = e?.message || String(e)
  } finally {
    saving.value = false
  }
}

async function toggleAvail() {
  if (!props.offer) return
  toggling.value = true
  try {
    const res = await toggleModelOfferState(props.providerId, props.offer.id, {
      available: !props.offer.available,
    })
    emit('updated', { ...props.offer, available: res.available })
  } catch (e: any) {
    saveErr.value = e?.message || String(e)
  } finally {
    toggling.value = false
  }
}
</script>

<template>
  <div v-if="offer" class="drawer-backdrop" @click="emit('close')">
    <div class="drawer-panel card drawer-panel-wide" @click.stop>
      <div class="drawer-header">
        <div>
          <h3 style="margin:0"><code>{{ offer.raw_model_name }}</code></h3>
          <div class="drawer-sub">#{{ offer.id }} · 凭据 #{{ offer.credential_id }} {{ offer.credential_label }}</div>
          <ModelIdentityChip
            style="margin-top:8px"
            :canonical-name="offer.canonical_name || offer.standardized_name"
            :outbound-model="offer.outbound_model_name"
            :raw-model="offer.raw_model_name"
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
          <select v-model="draft.modality" class="field-input">
            <option value="text">text</option>
            <option value="vision">vision</option>
            <option value="audio">audio</option>
            <option value="video">video</option>
            <option value="multimodal">multimodal</option>
            <option value="embedding">embedding</option>
          </select>
          <label class="field-label">思考能力</label>
          <label class="manual-toggle">
            <input v-model="draft.thinking_supported" type="checkbox" />
            <span>支持 thinking / reasoning</span>
          </label>
          <input
            v-if="draft.thinking_supported"
            v-model="draft.thinking_dialect"
            class="field-input"
            placeholder="dialect: openai / anthropic / glm …"
          />
          <div class="btn-row" style="margin-top:10px">
            <button class="btn btn-sm btn-primary" :disabled="saving || !offer.canonical_id" @click="saveCanonical">
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
            <button class="btn btn-sm" :disabled="toggling" @click="toggleAvail">
              {{ offer.available ? '停用' : '启用' }}
            </button>
          </div>
          <label class="field-label">标准化名</label>
          <input v-model="draft.standardized_name" class="field-input" />
          <label class="field-label">关联 canonical</label>
          <select
            class="field-input"
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
          <input v-model="draft.outbound_model_name" class="field-input" :placeholder="offer.raw_model_name" />
          <label class="field-label">上下文窗口覆盖</label>
          <input v-model.number="draft.context_window" type="number" min="0" class="field-input" placeholder="空=继承标准" />
          <div class="cell-sub">
            生效:
            <code>{{ offer.context_window ?? '—' }}</code>
            <template v-if="offer.context_window_override != null">（本节点 {{ offer.context_window_override }}）</template>
          </div>
          <div class="btn-row" style="margin-top:10px">
            <button class="btn btn-sm btn-primary" :disabled="saving" @click="saveNode">保存节点</button>
          </div>
        </section>
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
.badge.protect { background: color-mix(in srgb, #c97800 20%, transparent); }
.badge.override { background: color-mix(in srgb, #3b82f6 20%, transparent); }
.avail-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.avail-badge { font-size: 12px; padding: 2px 8px; border-radius: 999px; }
.avail-badge.on { background: color-mix(in srgb, #16a34a 20%, transparent); }
.avail-badge.off { background: color-mix(in srgb, #dc2626 20%, transparent); }
.field-label { display: block; margin-top: 8px; font-size: 12px; color: var(--muted); }
.field-input { width: 100%; margin-top: 4px; }
</style>
