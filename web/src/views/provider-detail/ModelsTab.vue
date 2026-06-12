<script setup lang="ts">
import { ref, reactive } from 'vue'
import {
  getProviderModels,
  toggleModelOfferState,
  getModelOfferSuggestions,
  updateModelOffer,
  getRoutableSummary,
  type ModelOffer,
  type ModelOfferSuggestion,
} from '../../api'

const props = defineProps<{ providerId: number }>()

const offers = ref<ModelOffer[]>([])
const loading = ref(false)
const error = ref('')

// 900-series: routable summary from VIEW
const routable = ref<{
  total_bindings: number
  routable_bindings: number
  unavailable_bindings: number
  unavailable_breakdown: Record<string, number>
  routable_ratio: number
} | null>(null)
const routableLoading = ref(false)

// Per-row edit state keyed by offer.id
interface EditDraft {
  standardized_name: string
  canonical_id: number | null
  saving: boolean
  loadingSuggest: boolean
  suggest: ModelOfferSuggestion | null
  suggestErr: string
  saveErr: string
}
const editingId = ref<number | null>(null)
const draft = reactive<Record<number, EditDraft>>({})

async function load() {
  loading.value = true
  error.value = ''
  try {
    offers.value = await getProviderModels(props.providerId)
    routableLoading.value = true
    try {
      routable.value = await getRoutableSummary(props.providerId)
    } catch {
      routable.value = null
    } finally {
      routableLoading.value = false
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function toggle(offer: ModelOffer) {
  try {
    await toggleModelOfferState(props.providerId, offer.id, { available: !offer.available })
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  }
}

function sourceLabel(v?: string | null) {
  if (v === 'auto') return '自动'
  if (v === 'manual') return '手动'
  return '从未'
}

function timeText(v?: string | null) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { hour12: false })
}

function ensureDraft(o: ModelOffer): EditDraft {
  if (!draft[o.id]) {
    draft[o.id] = reactive({
      standardized_name: o.standardized_name ?? '',
      canonical_id: o.canonical_id ?? null,
      saving: false,
      loadingSuggest: false,
      suggest: null,
      suggestErr: '',
      saveErr: '',
    }) as EditDraft
  }
  return draft[o.id]
}

async function startEdit(o: ModelOffer) {
  editingId.value = o.id
  const d = ensureDraft(o)
  d.standardized_name = o.standardized_name ?? ''
  d.canonical_id = o.canonical_id ?? null
  d.saveErr = ''
  d.suggestErr = ''
  d.suggest = null
  d.loadingSuggest = true
  try {
    d.suggest = await getModelOfferSuggestions(props.providerId, o.id)
  } catch (e: unknown) {
    d.suggestErr = e instanceof Error ? e.message : '加载推荐失败'
  } finally {
    d.loadingSuggest = false
  }
}

function cancelEdit(o: ModelOffer) {
  if (editingId.value === o.id) editingId.value = null
  delete draft[o.id]
}

function applyRuleBased(o: ModelOffer) {
  const d = ensureDraft(o)
  if (!d.suggest) return
  d.standardized_name = d.suggest.rule_based || d.standardized_name
  // If the rule-based value matches a DB canonical, link it automatically.
  const match = d.suggest.canonical_options.find(
    c => (c.canonical_name || '').toLowerCase() === (d.standardized_name || '').toLowerCase()
  )
  d.canonical_id = match ? match.id : null
}

function applyCanonical(o: ModelOffer, canonicalId: number | null) {
  const d = ensureDraft(o)
  d.canonical_id = canonicalId
  if (canonicalId != null && d.suggest) {
    const match = d.suggest.canonical_options.find(c => c.id === canonicalId)
    if (match) d.standardized_name = match.canonical_name
  }
}

async function saveEdit(o: ModelOffer) {
  const d = ensureDraft(o)
  d.saving = true
  d.saveErr = ''
  try {
    const updated = await updateModelOffer(props.providerId, o.id, {
      standardized_name: d.standardized_name.trim() || null,
      canonical_id: d.canonical_id,
    })
    // Reflect saved values into the local list without a full reload
    const idx = offers.value.findIndex(x => x.id === o.id)
    if (idx >= 0) {
      offers.value[idx] = {
        ...offers.value[idx],
        standardized_name: updated.standardized_name ?? '',
        canonical_id: updated.canonical_id,
      }
    }
    editingId.value = null
    delete draft[o.id]
  } catch (e: unknown) {
    d.saveErr = e instanceof Error ? e.message : '保存失败'
  } finally {
    d.saving = false
  }
}

load()
</script>

<template>
  <div>
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px">
      <h4 style="margin:0">模型清单 ({{ offers.length }})</h4>
      <button class="btn btn-sm" @click="load" :disabled="loading">{{ loading ? '加载中…' : '刷新' }}</button>
    </div>

    <!-- 900-series: routable binding summary -->
    <div v-if="routable" class="card" style="margin-bottom:12px;background:rgba(99,102,241,0.04)">
      <h5 style="margin:0 0 8px 0">可路由性摘要 (v_routable_credential_models)</h5>
      <div class="metric-grid" style="grid-template-columns:repeat(4,1fr);gap:8px">
        <div class="metric">
          <b>{{ routable.routable_bindings }} / {{ routable.total_bindings }}</b>
          <span>可路由 (routable_ratio: {{ (routable.routable_ratio * 100).toFixed(0) }}%)</span>
        </div>
        <div class="metric">
          <b>{{ routable.unavailable_bindings }}</b>
          <span>不可路由</span>
        </div>
        <div class="metric" v-if="Object.keys(routable.unavailable_breakdown).length > 0">
          <b>细分</b>
          <div style="font-size:10px;text-align:left;max-height:80px;overflow-y:auto">
            <div v-for="(count, code) in routable.unavailable_breakdown" :key="code">
              <code>{{ code }}</code>: {{ count }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div class="card" style="overflow-x:auto">
      <table class="data-table" style="width:100%;font-size:12px">
        <thead>
          <tr>
            <th>原始模型名</th>
            <th style="min-width:340px">标准化名</th>
            <th>关联凭据</th>
            <th>可用</th>
            <th>来源</th>
            <th>赋值时间</th>
            <th>延迟 P95</th>
            <th>成功率</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td colspan="8">加载中…</td></tr>
          <tr v-else-if="!offers.length"><td colspan="8">暂无模型</td></tr>
          <template v-for="o in offers" :key="o.id">
            <tr>
              <td><code>{{ o.raw_model_name }}</code></td>
              <td>
                <template v-if="editingId === o.id">
                  <div style="display:flex;flex-direction:column;gap:6px">
                    <input
                      v-model="draft[o.id].standardized_name"
                      class="form-input"
                      placeholder="标准化模型名"
                      style="width:100%"
                    />
                    <div v-if="draft[o.id].saveErr" class="alert alert-danger" style="margin:0;padding:4px 8px">{{ draft[o.id].saveErr }}</div>
                    <div class="suggest-block">
                      <div class="suggest-row">
                        <span class="suggest-label">规则推荐</span>
                        <span v-if="draft[o.id].loadingSuggest" class="suggest-loading">计算中…</span>
                        <button
                          v-else-if="draft[o.id].suggest && draft[o.id].suggest?.rule_based"
                          type="button"
                          class="suggest-chip"
                          :title="`基于规则 standardize_name(${o.raw_model_name})`"
                          @click="applyRuleBased(o)"
                        >{{ draft[o.id].suggest?.rule_based }}</button>
                        <span v-else class="suggest-empty">—</span>
                      </div>
                      <div class="suggest-row">
                        <span class="suggest-label">从已认可标准化名中选择</span>
                        <select
                          :value="draft[o.id].canonical_id ?? ''"
                          @change="(ev) => applyCanonical(o, (ev.target as HTMLSelectElement).value === '' ? null : Number((ev.target as HTMLSelectElement).value))"
                          class="form-input"
                          style="flex:1;min-width:0"
                        >
                          <option value="">— 不关联 canonical —</option>
                          <option
                            v-for="c in (draft[o.id].suggest?.canonical_options ?? [])"
                            :key="c.id"
                            :value="c.id"
                          >{{ c.canonical_name }}<span v-if="c.display_name && c.display_name !== c.canonical_name"> · {{ c.display_name }}</span></option>
                        </select>
                      </div>
                      <div v-if="draft[o.id].suggestErr" class="suggest-err">{{ draft[o.id].suggestErr }}</div>
                    </div>
                  </div>
                </template>
                <template v-else>
                  <div class="name-cell">
                    <code v-if="o.standardized_name">{{ o.standardized_name }}</code>
                    <span v-else class="name-empty">—</span>
                    <button
                      type="button"
                      class="icon-btn"
                      title="编辑标准化名"
                      @click="startEdit(o)"
                    >✎</button>
                  </div>
                </template>
                <div v-if="editingId === o.id" class="edit-actions">
                  <button class="btn btn-primary btn-sm" :disabled="draft[o.id].saving" @click="saveEdit(o)">
                    {{ draft[o.id].saving ? '保存中…' : '保存' }}
                  </button>
                  <button class="btn btn-ghost btn-sm" :disabled="draft[o.id].saving" @click="cancelEdit(o)">取消</button>
                </div>
              </td>
              <td>#{{ o.credential_id }} {{ o.credential_label }}</td>
              <td>
                <button
                  type="button"
                  class="avail-toggle"
                  :class="o.available ? 'on' : 'off'"
                  :title="o.available ? '点击禁用' : '点击启用'"
                  @click="toggle(o)"
                >
                  {{ o.available ? '可用' : '不可用' }}
                </button>
              </td>
              <td><span class="badge" :class="o.availability_source === 'auto' ? 'badge-amber' : o.availability_source === 'manual' ? 'badge-blue' : ''">{{ sourceLabel(o.availability_source) }}</span></td>
              <td>{{ timeText(o.unavailable_at) }}</td>
              <td>{{ o.p95_latency_ms != null ? o.p95_latency_ms + 'ms' : '—' }}</td>
              <td>{{ o.success_rate != null ? (o.success_rate * 100).toFixed(1) + '%' : '—' }}</td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.form-input {
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  color: var(--text, #e6edf3);
  border-radius: 4px;
  padding: 4px 8px;
  font-size: 12px;
  font-family: inherit;
}
.suggest-block {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 6px 8px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 4px;
}
.suggest-row {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
}
.suggest-label {
  color: var(--muted);
  white-space: nowrap;
  min-width: 130px;
}
.suggest-chip {
  border: 1px solid var(--accent, #6366f1);
  background: rgba(99,102,241,0.12);
  color: var(--text, #e6edf3);
  border-radius: 999px;
  padding: 2px 10px;
  font-size: 12px;
  font-family: monospace;
  cursor: pointer;
}
.suggest-chip:hover {
  background: var(--accent, #6366f1);
  color: #fff;
}
.suggest-loading,
.suggest-empty,
.suggest-err {
  color: var(--muted);
  font-size: 11px;
}
.suggest-err {
  color: var(--danger, #f85149);
}
.name-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.name-empty {
  color: var(--muted);
}
.icon-btn {
  border: none;
  background: transparent;
  color: var(--muted);
  cursor: pointer;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 13px;
  line-height: 1;
}
.icon-btn:hover {
  color: var(--accent, #6366f1);
  background: rgba(99, 102, 241, 0.1);
}
.edit-actions {
  display: flex;
  gap: 6px;
  margin-top: 6px;
}
.avail-toggle {
  border: 1px solid transparent;
  border-radius: 999px;
  padding: 2px 10px;
  font-size: 11px;
  cursor: pointer;
  font-family: inherit;
}
.avail-toggle.on {
  background: rgba(34, 197, 94, 0.15);
  color: #22c55e;
  border-color: rgba(34, 197, 94, 0.35);
}
.avail-toggle.off {
  background: rgba(239, 68, 68, 0.12);
  color: #ef4444;
  border-color: rgba(239, 68, 68, 0.35);
}
.avail-toggle:hover {
  filter: brightness(1.1);
}
</style>
