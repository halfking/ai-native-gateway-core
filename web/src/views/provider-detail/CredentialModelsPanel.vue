<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import {
  getCredentialModels, createCredentialModel, clearCredentialModels, refreshCredentialModels,
  type ModelOffer, type CredentialModelCreateBody,
} from '../../api/providers'
import ModelOfferDetailDrawer from '../../components/model/ModelOfferDetailDrawer.vue'
import ModelIdentityChip from '../../components/model/ModelIdentityChip.vue'

const props = defineProps<{
  providerId: number
  credentialId: number
}>()

const offers = ref<ModelOffer[]>([])
const loading = ref(false)
const busy = ref(false)
const msg = ref('')
const msgKind = ref<'ok' | 'err' | ''>('')
const selected = ref<ModelOffer | null>(null)
const showAdd = ref(false)
const includeProtected = ref(false)

const addForm = ref<CredentialModelCreateBody>({
  raw_model_name: '',
  standardized_name: '',
  available: true,
  modality: 'text',
  context_window: null,
  reasoning_caps: { supported: false },
})

const empty = computed(() => !loading.value && offers.value.length === 0)

async function load() {
  loading.value = true
  msg.value = ''
  try {
    offers.value = await getCredentialModels(props.providerId, props.credentialId)
  } catch (e: any) {
    msg.value = e?.message || String(e)
    msgKind.value = 'err'
  } finally {
    loading.value = false
  }
}

watch(() => [props.providerId, props.credentialId], () => { load() }, { immediate: true })

async function onRefreshDb() {
  await load()
  msg.value = `已刷新，共 ${offers.value.length} 个模型`
  msgKind.value = 'ok'
}

async function onFetchUpstream() {
  busy.value = true
  msg.value = '正在从上游拉取…'
  msgKind.value = ''
  try {
    const res = await refreshCredentialModels(props.providerId, props.credentialId)
    await load()
    msg.value = `拉取完成：更新 ${res.models_upserted}，失败 ${res.models_failed}，保护 ${res.skipped_protected}`
    msgKind.value = res.models_failed && !res.models_upserted ? 'err' : 'ok'
  } catch (e: any) {
    msg.value = e?.message || String(e)
    msgKind.value = 'err'
  } finally {
    busy.value = false
  }
}

async function onClear() {
  const tip = includeProtected.value
    ? '确定清空该凭据全部模型（含手工保护）？'
    : '确定清空该凭据非保护模型？（手工保护将保留）'
  if (!confirm(tip)) return
  busy.value = true
  try {
    const res = await clearCredentialModels(props.providerId, props.credentialId, includeProtected.value)
    await load()
    msg.value = `已删除 ${res.deleted} 条` + (res.protected_kept ? `，保留保护 ${res.protected_kept}` : '')
    msgKind.value = 'ok'
  } catch (e: any) {
    msg.value = e?.message || String(e)
    msgKind.value = 'err'
  } finally {
    busy.value = false
  }
}

async function onAdd() {
  if (!addForm.value.raw_model_name.trim()) {
    msg.value = '请填写上游原名'
    msgKind.value = 'err'
    return
  }
  busy.value = true
  try {
    const body = { ...addForm.value }
    if (body.reasoning_caps && (body.reasoning_caps as any).supported) {
      /* keep */
    } else {
      body.reasoning_caps = { supported: false }
    }
    await createCredentialModel(props.providerId, props.credentialId, body)
    showAdd.value = false
    addForm.value = {
      raw_model_name: '',
      standardized_name: '',
      available: true,
      modality: 'text',
      context_window: null,
      reasoning_caps: { supported: false },
    }
    await load()
    msg.value = '已手工加入模型'
    msgKind.value = 'ok'
  } catch (e: any) {
    msg.value = e?.message || String(e)
    msgKind.value = 'err'
  } finally {
    busy.value = false
  }
}

function onUpdated(o: ModelOffer) {
  const i = offers.value.findIndex(x => x.id === o.id)
  if (i >= 0) offers.value[i] = { ...offers.value[i], ...o }
  // After a delete-then-update race the row may be gone from the offers
  // list. Avoid spreading `undefined` into `selected`; just clear it.
  const base = i >= 0 ? offers.value[i] : null
  selected.value = base ? { ...base, ...o } : null
}

function thinkingLabel(o: ModelOffer) {
  const caps = o.reasoning_caps as { supported?: boolean; dialect?: string } | null | undefined
  if (!caps?.supported) return '—'
  return caps.dialect ? `是 (${caps.dialect})` : '是'
}
</script>

<template>
  <div class="cred-models">
    <div class="toolbar">
      <button class="btn btn-sm" :disabled="loading || busy" @click="onRefreshDb">刷新</button>
      <button class="btn btn-sm btn-primary" :disabled="busy" @click="onFetchUpstream">从上游拉取</button>
      <button class="btn btn-sm" :disabled="busy" @click="showAdd = true">手工加入</button>
      <button class="btn btn-sm btn-danger-outline" :disabled="busy || empty" @click="onClear">清空</button>
      <label class="protect-opt">
        <input v-model="includeProtected" type="checkbox" />
        清空含手工保护
      </label>
    </div>
    <div v-if="msg" class="banner" :class="msgKind">{{ msg }}</div>

    <div v-if="empty" class="empty">
      <p>该凭据下还没有模型。</p>
      <div class="btn-row">
        <button class="btn btn-primary" :disabled="busy" @click="onFetchUpstream">从上游拉取</button>
        <button class="btn" @click="showAdd = true">手工加入</button>
      </div>
      <p class="hint">若上游拉取失败，请检查 API Key、discovery 策略或 catalog manifest。</p>
    </div>

    <table v-else class="data-table">
      <thead>
        <tr>
          <th>模型身份</th>
          <th>可用</th>
          <th>来源</th>
          <th>模态</th>
          <th>思考</th>
          <th>Context</th>
          <th>P95</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="loading"><td colspan="7">加载中…</td></tr>
        <tr
          v-for="o in offers"
          :key="o.id"
          class="row-click"
          tabindex="0"
          @click="selected = o"
          @keydown.enter="selected = o"
        >
          <td>
            <ModelIdentityChip
              compact
              :canonical-name="o.canonical_name || o.standardized_name"
              :outbound-model="o.outbound_model_name"
              :raw-model="o.raw_model_name"
            />
            <span v-if="o.admin_protected" class="tag">保护</span>
          </td>
          <td>{{ o.available ? '是' : '否' }}</td>
          <td>{{ o.source || o.availability_source || '—' }}</td>
          <td>{{ o.modality || '—' }}</td>
          <td>{{ thinkingLabel(o) }}</td>
          <td>
            <code v-if="o.context_window != null">{{ o.context_window }}</code>
            <span v-else>—</span>
            <span v-if="o.context_window_override != null" class="tag">覆盖</span>
          </td>
          <td>{{ o.p95_latency_ms != null ? o.p95_latency_ms + 'ms' : '—' }}</td>
        </tr>
      </tbody>
    </table>

    <ModelOfferDetailDrawer
      v-if="selected"
      :provider-id="providerId"
      :offer="selected"
      :sibling-offers="offers"
      @close="selected = null"
      @updated="onUpdated"
    />

    <div v-if="showAdd" class="modal-overlay" @click.self="showAdd = false">
      <div class="modal" @click.stop>
        <h3>手工加入模型</h3>
        <div class="form-group">
          <label>上游原名 *</label>
          <input v-model="addForm.raw_model_name" placeholder="供应商返回的模型名" />
        </div>
        <div class="form-group">
          <label>标准名</label>
          <input v-model="addForm.standardized_name" placeholder="默认从原名规范化" />
        </div>
        <div class="form-group">
          <label>出站名</label>
          <input v-model="addForm.outbound_model_name" placeholder="可选，如 endpoint id" />
        </div>
        <div class="form-group" style="display:grid;grid-template-columns:1fr 1fr;gap:8px">
          <div>
            <label>模态</label>
            <select v-model="addForm.modality">
              <option value="text">text</option>
              <option value="vision">vision</option>
              <option value="multimodal">multimodal</option>
              <option value="embedding">embedding</option>
            </select>
          </div>
          <div>
            <label>上下文</label>
            <input v-model.number="addForm.context_window" type="number" min="0" placeholder="可选覆盖" />
          </div>
        </div>
        <label class="manual-toggle">
          <input
            type="checkbox"
            :checked="!!(addForm.reasoning_caps as any)?.supported"
            @change="(e: Event) => {
              const on = (e.target as HTMLInputElement).checked
              addForm.reasoning_caps = on ? { supported: true, dialect: 'openai' } : { supported: false }
            }"
          />
          <span>支持思考能力</span>
        </label>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
          <button class="btn btn-ghost" @click="showAdd = false">取消</button>
          <button class="btn btn-primary" :disabled="busy" @click="onAdd">加入</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 10px; }
.protect-opt { font-size: 12px; color: var(--muted); display: inline-flex; gap: 4px; align-items: center; }
.banner { margin-bottom: 8px; font-size: 12px; padding: 6px 10px; border-radius: 6px; }
.banner.ok { background: color-mix(in srgb, #16a34a 12%, transparent); }
.banner.err { background: color-mix(in srgb, #dc2626 12%, transparent); }
.empty { padding: 24px 8px; text-align: center; color: var(--muted); }
.empty .hint { font-size: 12px; margin-top: 8px; }
.row-click { cursor: pointer; }
.row-click:hover { background: color-mix(in srgb, var(--accent) 6%, transparent); }
.tag { margin-left: 6px; font-size: 10px; padding: 1px 5px; border-radius: 4px; background: color-mix(in srgb, #c97800 18%, transparent); }
</style>
