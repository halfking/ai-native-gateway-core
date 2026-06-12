<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import {
  getPolicy, patchPolicy, getFeatured, patchFeatured,
  getScoringWeights, updateScoringWeights,
  type RoutingPolicy, type ScoringWeights,
} from '../api'
import ModelPicker from '../components/ModelPicker.vue'

const policy   = ref<RoutingPolicy | null>(null)
const draft    = ref<Partial<RoutingPolicy>>({})
const featuredArray = ref<string[]>([])
const loading  = ref(false)
const saving   = ref(false)
const error    = ref('')
const message  = ref('')

const weights   = ref<ScoringWeights>({
  price: 10,
  session_load: 5,
  failure_penalty: 20,
  default_price_cny: 5.0,
  default_price_usd: 5.0,
})
const weightsDraft = ref<ScoringWeights>({ ...weights.value })

const FIELDS: { key: keyof RoutingPolicy; label: string; min?: number; max?: number; step?: number }[] = [
  { key: 'algorithm_version',         label: '算法版本 (1=旧, 2=v2 分层)', min: 1, max: 2, step: 1 },
  { key: 'retry_per_credential',      label: '同凭据重试次数',              min: 0, max: 5, step: 1 },
  { key: 'tier_fallback_max',         label: '跨级回退最大候选数',          min: 1, max: 20, step: 1 },
  { key: 'slot_soft_limit_ratio',     label: '并发软上限比例',              min: 0.1, max: 5,  step: 0.1 },
  { key: 'slot_hard_limit_ratio',     label: '并发硬上限比例',              min: 0.1, max: 5,  step: 0.1 },
  { key: 'slot_wait_max_ms',          label: '槽位等待最大毫秒',            min: 0, max: 5000, step: 10 },
  { key: 'circuit_open_seconds',      label: '熔断基础冷却秒',              min: 1, max: 3600, step: 1 },
  { key: 'circuit_failure_threshold', label: '熔断触发连续失败次数',        min: 1, max: 50, step: 1 },
  { key: 'circuit_max_open_seconds',  label: '熔断最大冷却秒',              min: 1, max: 86400, step: 1 },
]

const formulaPreview = computed(() => {
  const w = weightsDraft.value
  return `composite_score = 手工序号 + (归一化价格 × ${w.price}) + (会话负载 × ${w.session_load}) + (错误次数 × ${w.failure_penalty})`
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    policy.value = await getPolicy()
    draft.value  = { ...policy.value }
    const f = await getFeatured()
    featuredArray.value = (f.featured_models || []).slice()
    const w = await getScoringWeights()
    weights.value = w
    weightsDraft.value = { ...w }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

const dirtyKeys = computed<string[]>(() => {
  if (!policy.value) return []
  const out: string[] = []
  for (const f of FIELDS) {
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
    message.value = '没有变更'
    return
  }
  saving.value = true
  error.value = ''
  message.value = ''
  try {
    const patch: Record<string, unknown> = { actor: 'admin' }
    for (const k of dirtyKeys.value) patch[k] = (draft.value as any)[k]
    const updated = await patchPolicy(patch as Partial<RoutingPolicy>)
    policy.value = updated
    draft.value  = { ...updated }
    message.value = '策略已更新'
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    saving.value = false
  }
}

async function saveWeights() {
  saving.value = true
  error.value = ''
  message.value = ''
  try {
    await updateScoringWeights(weightsDraft.value)
    weights.value = { ...weightsDraft.value }
    message.value = '综合得分系数已更新'
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    saving.value = false
  }
}

async function saveFeatured() {
  saving.value = true
  error.value = ''
  message.value = ''
  try {
    const list = featuredArray.value.map(s => s.trim()).filter(Boolean)
    await patchFeatured(list)
    message.value = `特色模型已更新（${list.length}）`
    await load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <h2>路由策略</h2>
      <button class="btn btn-ghost" @click="load" :disabled="loading">刷新</button>
    </div>
    <p style="color:var(--muted);margin-bottom:16px">
      调整路由 v2 全局策略：算法版本、并发槽位、熔断阈值、综合得分系数。改动会写入审计日志并立即生效。
    </p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="message" class="alert alert-success">{{ message }}</div>
    <div v-if="loading" class="empty">加载中…</div>

    <div v-if="!loading && policy" class="card" style="margin-bottom:16px">
      <h3 style="margin-top:0">全局策略</h3>
      <table>
        <thead>
          <tr><th style="width:40%">参数</th><th>当前值</th><th>新值</th></tr>
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
          {{ saving ? '保存中…' : '保存策略' }}
        </button>
        <span v-if="dirtyKeys.length" style="color:var(--muted);font-size:12px">
          {{ dirtyKeys.length }} 项变更：{{ dirtyKeys.join(', ') }}
        </span>
      </div>
    </div>

    <div v-if="!loading" class="card" style="margin-bottom:16px">
      <h3 style="margin-top:0">综合得分系数</h3>
      <p style="color:var(--muted);font-size:12px;margin-bottom:12px">
        综合得分公式：值越小，候选越优先。<strong>免费模型（价格=0）得分固定为 0，最高优先。</strong>
      </p>
      <div style="background:var(--bg-subtle,#161b22);border:1px solid var(--border,#30363d);padding:12px;border-radius:6px;margin-bottom:16px;font-family:monospace;font-size:13px;color:var(--text,#e6edf3)">
        {{ formulaPreview }}
      </div>
      <div class="weights-grid">
        <div class="weight-item">
          <label>价格权重 (W2)</label>
          <input type="number" v-model.number="weightsDraft.price" min="0" max="100" step="1" />
          <span class="cell-muted">归一化价格 = 实际价格 / 默认价格</span>
        </div>
        <div class="weight-item">
          <label>会话负载权重 (W3)</label>
          <input type="number" v-model.number="weightsDraft.session_load" min="0" max="100" step="1" />
          <span class="cell-muted">会话数 / 并发上限，0-1</span>
        </div>
        <div class="weight-item">
          <label>错误惩罚权重 (W4)</label>
          <input type="number" v-model.number="weightsDraft.failure_penalty" min="0" max="100" step="1" />
          <span class="cell-muted">直接使用连续错误次数</span>
        </div>
        <div class="weight-item">
          <label>国内模型默认价格 (CNY)</label>
          <input type="number" v-model.number="weightsDraft.default_price_cny" min="0.01" max="100" step="0.1" />
          <span class="cell-muted">无价格时的归一化基准</span>
        </div>
        <div class="weight-item">
          <label>国外模型默认价格 (USD)</label>
          <input type="number" v-model.number="weightsDraft.default_price_usd" min="0.01" max="100" step="0.1" />
          <span class="cell-muted">无价格时的归一化基准</span>
        </div>
      </div>
      <div style="margin-top:12px;display:flex;gap:8px;align-items:center">
        <button class="btn btn-primary" @click="saveWeights" :disabled="saving || !weightsDirty">
          {{ saving ? '保存中…' : '保存系数' }}
        </button>
        <span v-if="weightsDirty" style="color:var(--muted);font-size:12px">
          系数有变更，未保存
        </span>
      </div>
    </div>

    <div v-if="!loading" class="card">
      <h3 style="margin-top:0">特色模型 (Featured)</h3>
      <p style="color:var(--muted);font-size:12px;margin-bottom:8px">
        选择标准模型名称，将在路由总览中以 ★ 标记，并可启用「仅特色」筛选。
      </p>
      <ModelPicker
        v-model="featuredArray"
        mode="multi"
        placeholder="选择特色模型…"
        title="特色模型（多选）"
      />
      <div style="margin-top:8px">
        <button class="btn btn-primary" @click="saveFeatured" :disabled="saving">
          {{ saving ? '保存中…' : '保存特色模型' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
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
  border: 1px solid var(--border, #e5e7eb);
  border-radius: 4px;
  font-size: 14px;
}
.cell-muted {
  color: var(--muted);
  font-size: 11px;
}
</style>
