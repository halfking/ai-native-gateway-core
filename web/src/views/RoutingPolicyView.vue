<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import { ref, onMounted, computed } from 'vue'
import {
  getPolicy, patchPolicy, getFeatured, patchFeatured,
  getScoringWeights, updateScoringWeights,
  type RoutingPolicy, type ScoringWeights,
} from '../api'
import { useActionMessage } from '../composables/useActionMessage'
import { ElAlert } from 'element-plus'
import ModelPicker from '../components/ModelPicker.vue'

const { t } = useI18n()


const policy   = ref<RoutingPolicy | null>(null)
const draft    = ref<Partial<RoutingPolicy>>({})
const featuredArray = ref<string[]>([])
const loading  = ref(false)
const saving   = ref(false)
// 审计 R3#10：操作反馈条统一走 useActionMessage。
const { message, error, notifySuccess, notifyError, clear: clearActionMsg } = useActionMessage()

const weights   = ref<ScoringWeights>({
  price: 10,
  session_load: 5,
  failure_penalty: 20,
  default_price_cny: 5.0,
  default_price_usd: 5.0,
})
const weightsDraft = ref<ScoringWeights>({ ...weights.value })

const FIELDS = computed<{ key: keyof RoutingPolicy; label: string; min?: number; max?: number; step?: number }[]>(() => [
  { key: 'algorithm_version',         label: t('routing.algorithm_version'), min: 1, max: 2, step: 1 },
  { key: 'retry_per_credential',      label: t('routing.retry_per_credential'),              min: 0, max: 5, step: 1 },
  { key: 'tier_fallback_max',         label: t('routing.tier_fallback_max'),          min: 1, max: 20, step: 1 },
  { key: 'slot_soft_limit_ratio',     label: t('routing.slot_soft_limit_ratio'),              min: 0.1, max: 5,  step: 0.1 },
  { key: 'slot_hard_limit_ratio',     label: t('routing.slot_hard_limit_ratio'),              min: 0.1, max: 5,  step: 0.1 },
  { key: 'slot_wait_max_ms',          label: t('routing.slot_wait_max_ms'),            min: 0, max: 5000, step: 10 },
  { key: 'circuit_open_seconds',      label: t('routing.circuit_open_seconds'),              min: 1, max: 3600, step: 1 },
  { key: 'circuit_failure_threshold', label: t('routing.circuit_failure_threshold'),        min: 1, max: 50, step: 1 },
  { key: 'circuit_max_open_seconds',  label: t('routing.circuit_max_open_seconds'),              min: 1, max: 86400, step: 1 },
])

const formulaPreview = computed(() => {
  const w = weightsDraft.value
  return t('routing.policy.formulaPrefix', {
    price: w.price,
    session_load: w.session_load,
    failure_penalty: w.failure_penalty,
  })
})

// 表头：3 个标量键（**不能用数组型语言包键** —— vue-i18n 9 的 t() 不支持数组消息，
// 会把键路径字符串原样返回 ⇒ v-for 会逐字符渲染键名）。
const policyTableHeaders = computed(() => [
  t('routing.policy.table.param'),
  t('routing.policy.table.current'),
  t('routing.policy.table.next'),
])

async function load() {
  loading.value = true
  try {
    policy.value = await getPolicy()
    draft.value  = { ...policy.value }
    const f = await getFeatured()
    featuredArray.value = (f.featured_models || []).slice()
    const w = await getScoringWeights()
    weights.value = w
    weightsDraft.value = { ...w }
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'routing.loadFailed' }))
  } finally {
    loading.value = false
  }
}

const dirtyKeys = computed<string[]>(() => {
  if (!policy.value) return []
  const out: string[] = []
  for (const f of FIELDS.value) {
    const a = (policy.value as any)[f.key]
    const b = (draft.value  as any)[f.key]
    if (String(a) !== String(b)) out.push(String(f.key))
  }
  return out
})

const weightsDirty = computed(() => {
  return JSON.stringify(weights.value) !== JSON.stringify(weightsDraft.value)
})

async function savePolicy() {
  if (!dirtyKeys.value.length) {
    notifySuccess(t('routing.noChanges'))
    return
  }
  saving.value = true
  clearActionMsg()
  try {
    const patch: Record<string, unknown> = { actor: 'admin' }
    for (const k of dirtyKeys.value) patch[k] = (draft.value as any)[k]
    const updated = await patchPolicy(patch as Partial<RoutingPolicy>)
    policy.value = updated
    draft.value  = { ...updated }
    notifySuccess(t('routing.updatedPolicy'))
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'routing.saveFailed' }))
  } finally {
    saving.value = false
  }
}

async function saveWeights() {
  saving.value = true
  clearActionMsg()
  try {
    await updateScoringWeights(weightsDraft.value)
    weights.value = { ...weightsDraft.value }
    notifySuccess(t('routing.updatedWeights'))
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'routing.saveFailed' }))
  } finally {
    saving.value = false
  }
}

async function saveFeatured() {
  saving.value = true
  clearActionMsg()
  try {
    const list = featuredArray.value.map(s => s.trim()).filter(Boolean)
    await patchFeatured(list)
    notifySuccess(t('routing.policy.updatedFeatured', { n: list.length }))
    await load()
  } catch (e: unknown) {
    notifyError(surfaceError(e, { key: 'routing.saveFailed' }))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div class="page-header-left">
        <router-link to="/routing-v2" class="back-link">{{ t('routing.policy.backLink') }}</router-link>
        <h2>{{ t('routing.policy.title') }}</h2>
      </div>
      <button class="btn btn-ghost" @click="load" :disabled="loading">{{ t('routing.shared.refresh') }}</button>
    </div>
    <p style="color:var(--muted);margin-bottom:16px">
      {{ t('routing.policy.description') }}
    </p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="message" class="alert alert-success">{{ message }}</div>
    <div v-if="loading" class="empty">{{ t('routing.policy.loading') }}</div>

    <div v-if="!loading && policy" class="card" style="margin-bottom:16px">
      <h3 style="margin-top:0">{{ t('routing.policy.globalTitle') }}</h3>
      <table>
        <thead>
          <tr>
            <th v-for="(h, i) in policyTableHeaders" :key="i" :style="i === 0 ? 'width:40%' : null">{{ h }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="f in FIELDS" :key="String(f.key)">
            <td>{{ f.label }} <code style="font-size:10px;color:var(--muted)">{{ String(f.key) }}</code></td>
            <td><code>{{ (policy as any)[f.key] }}</code></td>
            <td>
              <input
                type="number"
                v-model.number="(draft as any)[f.key]"
                :min="f.min"
                :max="f.max"
                :step="f.step"
                style="width:140px"
              />
            </td>
          </tr>
        </tbody>
      </table>
      <div style="margin-top:12px;display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" @click="savePolicy" :disabled="saving || !dirtyKeys.length">
          {{ saving ? t('routing.policy.saving') : t('routing.policy.savePolicy') }}
        </button>
        <span v-if="dirtyKeys.length" style="color:var(--muted);font-size:12px">
          {{ t('routing.policy.dirtyHint', { n: dirtyKeys.length, keys: dirtyKeys.join(', ') }) }}
        </span>
      </div>
    </div>

    <div v-if="!loading" class="card" style="margin-bottom:16px">
      <h3 style="margin-top:0">{{ t('routing.policy.weightsTitle') }}</h3>
      <p style="color:var(--muted);font-size:12px;margin-bottom:12px">
        {{ t('routing.policy.formulaLabel') }}<strong>{{ t('routing.policy.freeHighlight') }}</strong>
      </p>
      <el-alert type="info" show-icon :closable="false" :title="t('routing.policy.weightsDisplayOnlyHint')" style="margin-bottom:12px" />
      <div style="background:var(--bg-subtle);border:1px solid var(--border);padding:12px;border-radius:6px;margin-bottom:16px;font-family:monospace;font-size:13px;color:var(--text)">
        {{ formulaPreview }}
      </div>
      <div class="weights-grid">
        <div class="weight-item">
          <label>{{ t('routing.policy.weights.price') }}</label>
          <input type="number" v-model.number="weightsDraft.price" min="0" max="100" step="1" />
          <span class="cell-muted">{{ t('routing.policy.weights.priceHint') }}</span>
        </div>
        <div class="weight-item">
          <label>{{ t('routing.policy.weights.sessionLoad') }}</label>
          <input type="number" v-model.number="weightsDraft.session_load" min="0" max="100" step="1" />
          <span class="cell-muted">{{ t('routing.policy.weights.sessionLoadHint') }}</span>
        </div>
        <div class="weight-item">
          <label>{{ t('routing.policy.weights.errorPenalty') }}</label>
          <input type="number" v-model.number="weightsDraft.failure_penalty" min="0" max="100" step="1" />
          <span class="cell-muted">{{ t('routing.policy.weights.errorPenaltyHint') }}</span>
        </div>
        <div class="weight-item">
          <label>{{ t('routing.policy.weights.defaultPriceCny') }}</label>
          <input type="number" v-model.number="weightsDraft.default_price_cny" min="0.01" max="100" step="0.1" />
          <span class="cell-muted">{{ t('routing.policy.weights.defaultPriceHint') }}</span>
        </div>
        <div class="weight-item">
          <label>{{ t('routing.policy.weights.defaultPriceUsd') }}</label>
          <input type="number" v-model.number="weightsDraft.default_price_usd" min="0.01" max="100" step="0.1" />
          <span class="cell-muted">{{ t('routing.policy.weights.defaultPriceHint') }}</span>
        </div>
      </div>
      <div style="margin-top:12px;display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" @click="saveWeights" :disabled="saving || !weightsDirty">
          {{ saving ? t('routing.policy.saving') : t('routing.policy.savingWeights') }}
        </button>
        <span v-if="weightsDirty" style="color:var(--muted);font-size:12px">
          {{ t('routing.policy.weightsDirty') }}
        </span>
      </div>
    </div>

    <div v-if="!loading" class="card">
      <h3 style="margin-top:0">{{ t('routing.policy.featuredTitle') }}</h3>
      <p style="color:var(--muted);font-size:12px;margin-bottom:8px">
        {{ t('routing.policy.featuredDescription') }}
      </p>
      <ModelPicker
        v-model="featuredArray"
        mode="multi"
        :placeholder="t('routing.policy.featuredPlaceholder')"
        :title="t('routing.policy.featuredMultiTitle')"
      />
      <div style="margin-top:8px">
        <button class="btn btn-primary" @click="saveFeatured" :disabled="saving">
          {{ saving ? t('routing.policy.saving') : t('routing.policy.saveFeatured') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-header-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.back-link {
  font-size: 11px;
  color: var(--muted);
  text-decoration: none;
}
.back-link:hover { color: var(--accent-h); }
.weights-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 16px;
}
.weight-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.weight-item label {
  font-weight: 600;
  font-size: 13px;
}
.weight-item input {
  width: 100%;
  padding: 6px 10px;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 14px;
}
.cell-muted {
  color: var(--muted);
  font-size: 11px;
}

.featured-recommend-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
  padding: 8px 10px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.featured-recommend-msg {
  font-size: 11px;
  color: var(--muted);
  flex: 1;
  min-width: 200px;
}
</style>
