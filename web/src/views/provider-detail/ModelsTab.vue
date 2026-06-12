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

const routable = ref<{
  total_bindings: number
  routable_bindings: number
  unavailable_bindings: number
  unavailable_breakdown: Record<string, number>
  routable_ratio: number
} | null>(null)
const routableLoading = ref(false)

const selected = ref<ModelOffer | null>(null)

interface EditDraft {
  standardized_name: string
  canonical_id: number | null
  saving: boolean
  toggling: boolean
  loadingSuggest: boolean
  suggest: ModelOfferSuggestion | null
  suggestErr: string
  saveErr: string
}
const draft = reactive<Partial<EditDraft>>({})

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

function sourceLabel(v?: string | null) {
  if (v === 'auto') return '自动'
  if (v === 'manual') return '手动'
  return '从未'
}

function timeText(v?: string | null) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { hour12: false })
}

function resetDraft(o: ModelOffer) {
  draft.standardized_name = o.standardized_name ?? ''
  draft.canonical_id = o.canonical_id ?? null
  draft.saving = false
  draft.toggling = false
  draft.loadingSuggest = false
  draft.suggest = null
  draft.suggestErr = ''
  draft.saveErr = ''
}

async function openDrawer(o: ModelOffer) {
  selected.value = o
  resetDraft(o)
  draft.loadingSuggest = true
  try {
    draft.suggest = await getModelOfferSuggestions(props.providerId, o.id)
  } catch (e: unknown) {
    draft.suggestErr = e instanceof Error ? e.message : '加载推荐失败'
  } finally {
    draft.loadingSuggest = false
  }
}

function closeDrawer() {
  selected.value = null
}

function applyRuleBased() {
  if (!draft.suggest) return
  draft.standardized_name = draft.suggest.rule_based || draft.standardized_name || ''
  const match = draft.suggest.canonical_options.find(
    c => (c.canonical_name || '').toLowerCase() === (draft.standardized_name || '').toLowerCase()
  )
  draft.canonical_id = match ? match.id : null
}

function applyCanonical(canonicalId: number | null) {
  draft.canonical_id = canonicalId
  if (canonicalId != null && draft.suggest) {
    const match = draft.suggest.canonical_options.find(c => c.id === canonicalId)
    if (match) draft.standardized_name = match.canonical_name
  }
}

async function saveEdit() {
  const o = selected.value
  if (!o) return
  draft.saving = true
  draft.saveErr = ''
  try {
    const updated = await updateModelOffer(props.providerId, o.id, {
      standardized_name: (draft.standardized_name ?? '').trim() || null,
      canonical_id: draft.canonical_id ?? null,
    })
    const idx = offers.value.findIndex(x => x.id === o.id)
    if (idx >= 0) {
      offers.value[idx] = {
        ...offers.value[idx],
        standardized_name: updated.standardized_name ?? '',
        canonical_id: updated.canonical_id,
      }
      selected.value = offers.value[idx]
    }
  } catch (e: unknown) {
    draft.saveErr = e instanceof Error ? e.message : '保存失败'
  } finally {
    draft.saving = false
  }
}

async function toggleAvailable() {
  const o = selected.value
  if (!o) return
  draft.toggling = true
  try {
    await toggleModelOfferState(props.providerId, o.id, { available: !o.available })
    await load()
    const refreshed = offers.value.find(x => x.id === o.id)
    if (refreshed) selected.value = refreshed
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '操作失败'
  } finally {
    draft.toggling = false
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
      <table class="data-table model-table">
        <thead>
          <tr>
            <th>原始模型名</th>
            <th>标准化名</th>
            <th>关联凭据</th>
            <th>可用</th>
            <th>来源</th>
            <th>延迟 P95</th>
            <th>成功率</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td colspan="7">加载中…</td></tr>
          <tr v-else-if="!offers.length"><td colspan="7">暂无模型</td></tr>
          <tr
            v-for="o in offers"
            :key="o.id"
            class="model-row"
            tabindex="0"
            @click="openDrawer(o)"
            @keydown.enter="openDrawer(o)"
          >
            <td><code>{{ o.raw_model_name }}</code></td>
            <td>
              <code v-if="o.standardized_name">{{ o.standardized_name }}</code>
              <span v-else class="cell-muted">—</span>
            </td>
            <td>#{{ o.credential_id }} {{ o.credential_label }}</td>
            <td>
              <span class="avail-badge" :class="o.available ? 'on' : 'off'">
                {{ o.available ? '可用' : '不可用' }}
              </span>
            </td>
            <td>
              <span class="badge" :class="o.availability_source === 'auto' ? 'badge-amber' : o.availability_source === 'manual' ? 'badge-blue' : ''">
                {{ sourceLabel(o.availability_source) }}
              </span>
            </td>
            <td>{{ o.p95_latency_ms != null ? o.p95_latency_ms + 'ms' : '—' }}</td>
            <td>{{ o.success_rate != null ? (o.success_rate * 100).toFixed(1) + '%' : '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Model detail drawer -->
    <div v-if="selected" class="drawer-backdrop" @click="closeDrawer">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <div>
            <h3 style="margin:0"><code>{{ selected.raw_model_name }}</code></h3>
            <div class="drawer-sub">offer #{{ selected.id }} · 凭据 #{{ selected.credential_id }} {{ selected.credential_label }}</div>
          </div>
          <button type="button" class="btn btn-ghost btn-sm" @click="closeDrawer">关闭</button>
        </div>

        <div class="drawer-body">
          <div class="drawer-section">
            <div class="drawer-section-title">可用状态</div>
            <div class="avail-row">
              <span class="avail-badge lg" :class="selected.available ? 'on' : 'off'">
                {{ selected.available ? '可用' : '不可用' }}
              </span>
              <span class="cell-muted">{{ sourceLabel(selected.availability_source) }}</span>
              <span v-if="selected.unavailable_at" class="cell-muted">{{ timeText(selected.unavailable_at) }}</span>
            </div>
            <button
              class="btn btn-sm"
              :disabled="draft.toggling"
              style="margin-top:10px"
              @click="toggleAvailable"
            >
              {{ draft.toggling ? '处理中…' : (selected.available ? '禁用此绑定' : '启用此绑定') }}
            </button>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">标准化名</div>
            <input
              v-model="draft.standardized_name"
              class="field-input"
              placeholder="标准化模型名"
            />
            <div v-if="draft.saveErr" class="alert alert-danger" style="margin:8px 0;padding:6px 10px">{{ draft.saveErr }}</div>
            <div class="suggest-block">
              <div class="suggest-row">
                <span class="suggest-label">规则推荐</span>
                <span v-if="draft.loadingSuggest" class="suggest-loading">计算中…</span>
                <button
                  v-else-if="draft.suggest?.rule_based"
                  type="button"
                  class="suggest-chip"
                  @click="applyRuleBased"
                >{{ draft.suggest?.rule_based }}</button>
                <span v-else class="suggest-empty">—</span>
              </div>
              <div class="suggest-row">
                <span class="suggest-label">已认可标准化名</span>
                <select
                  :value="draft.canonical_id ?? ''"
                  class="field-input"
                  style="margin:0"
                  @change="(ev) => applyCanonical((ev.target as HTMLSelectElement).value === '' ? null : Number((ev.target as HTMLSelectElement).value))"
                >
                  <option value="">— 不关联 canonical —</option>
                  <option
                    v-for="c in (draft.suggest?.canonical_options ?? [])"
                    :key="c.id"
                    :value="c.id"
                  >{{ c.canonical_name }}</option>
                </select>
              </div>
              <div v-if="draft.suggestErr" class="suggest-err">{{ draft.suggestErr }}</div>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">指标</div>
            <div class="metric-row">
              <span>P95 延迟</span>
              <b>{{ selected.p95_latency_ms != null ? selected.p95_latency_ms + 'ms' : '—' }}</b>
            </div>
            <div class="metric-row">
              <span>成功率</span>
              <b>{{ selected.success_rate != null ? (selected.success_rate * 100).toFixed(1) + '%' : '—' }}</b>
            </div>
          </div>
        </div>

        <div class="drawer-footer">
          <div class="btn-row btn-row--end">
            <button class="btn btn-ghost" @click="closeDrawer">取消</button>
            <button class="btn btn-primary" :disabled="draft.saving" @click="saveEdit">
              {{ draft.saving ? '保存中…' : '保存标准化名' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-table {
  width: 100%;
  font-size: 12px;
}
.model-row {
  cursor: pointer;
}
.model-row:hover td {
  background: rgba(99, 102, 241, 0.06);
}
.model-row:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.cell-muted {
  color: var(--muted);
}
.avail-badge {
  display: inline-block;
  border-radius: 999px;
  padding: 2px 10px;
  font-size: 11px;
}
.avail-badge.on {
  background: rgba(34, 197, 94, 0.15);
  color: #22c55e;
}
.avail-badge.off {
  background: rgba(239, 68, 68, 0.12);
  color: #ef4444;
}
.avail-badge.lg {
  font-size: 13px;
  padding: 4px 14px;
}
.drawer-sub {
  font-size: 12px;
  color: var(--muted);
  margin-top: 4px;
}
.drawer-body {
  flex: 1;
  overflow-y: auto;
}
.drawer-footer {
  margin-top: auto;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}
.avail-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.field-input {
  width: 100%;
  padding: 8px 10px;
  font-size: 13px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
}
.field-input:focus {
  border-color: var(--accent);
  outline: none;
}
.suggest-block {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px;
  margin-top: 10px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}
.suggest-row {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
}
.suggest-label {
  color: var(--muted);
  white-space: nowrap;
  min-width: 110px;
}
.suggest-chip {
  border: 1px solid var(--accent, #6366f1);
  background: rgba(99,102,241,0.12);
  color: var(--text, #e6edf3);
  border-radius: 999px;
  padding: 4px 12px;
  font-size: 12px;
  font-family: monospace;
  cursor: pointer;
}
.suggest-chip:hover {
  background: var(--accent, #6366f1);
  color: #fff;
}
.suggest-loading,
.suggest-empty {
  color: var(--muted);
  font-size: 11px;
}
.suggest-err {
  color: var(--danger, #f85149);
  font-size: 11px;
}
.metric-row {
  display: flex;
  justify-content: space-between;
  padding: 6px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--border);
}
.btn-row {
  display: flex;
  gap: 8px;
}
.btn-row--end {
  justify-content: flex-end;
}
</style>
