<script setup lang="ts">
// SimpleAccessListView.vue — 简易模式首页：接入清单（2026-11-17）
//
// 设计稿：docs/03-design/02-feature-design/simple-mode-concept-v2.html §四
//
// ★ 它是 magpie「一屏即产品」那条借鉴（B1）的落地：首页不再是一堆 KPI，
//   而是**一行一个客户端接入** —— 它是谁、用得怎么样、花了多少。
//
// ★ 数据口径**与专家面完全一致**（同一批 API、同一张表）：
//   /api/keys ⇒ api_keys 行。简易模式不新建任何表、不新造任何统计口径。
//   ★ 诚实边界：当前 /api/keys 只给**累计**请求数与累计花费，
//     没有「按密钥的当日统计」⇒ 本页的请求列是累计口径，并在页面上写明。
//     （想要当日口径需要后端新增聚合，属独立立项，见实现规划 §五）
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getKeys, createKey, type ApiKey } from '../api'
import { getKeyUsageByModel } from '../api/usage'
import PageHeader from '../components/ui/PageHeader.vue'
import { useWorkMode } from '../composables/useWorkMode'
import { fmtDateTimeShort } from '../i18n/useFormat'

const { t } = useI18n()
const router = useRouter()
const { isSimple } = useWorkMode()

const keys = ref<ApiKey[]>([])
const keyModels = ref<Map<number, string>>(new Map()) // keyId → 最近使用的模型名
const loading = ref(false)
const error = ref('')

const showCreate = ref(false)
const createAlias = ref('')
const createBudget = ref<number | ''>('')
const creating = ref(false)
const createError = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    keys.value = await getKeys()
    // L2: 异步获取每个密钥的最近模型（只读）
    await Promise.allSettled(
      keys.value.map(async (k) => {
        try {
          const models = await getKeyUsageByModel(k.id, { limit: 1 })
          if (models.length > 0) {
            keyModels.value.set(k.id, models[0].model)
          }
        } catch {
          // 单个密钥获取失败不阻塞整体
        }
      })
    )
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)

/** 一行 = 一个接入对象（网关密钥 ≈ 一个客户端接入） */
interface Row {
  key: ApiKey
  name: string
  stateKey: 'ok' | 'idle' | 'pending' | 'off'
  spend: string
  budget: string
  lastUsed: string
  currentModel: string // L2: 当前模型（只读，从 per-key 用量取最近模型名）
}

const rows = computed<Row[]>(() =>
  keys.value.map((k) => ({
    key: k,
    name: k.key_alias || k.application_code || k.key_prefix,
    stateKey: stateOf(k),
    spend: fmtUsd(k.total_cost_usd),
    budget: k.budget_usd == null ? '—' : fmtUsd(k.budget_usd),
    lastUsed: k.last_used_at ? fmtDateTimeShort(k.last_used_at) : t('simpleMode.accessList.neverUsed'),
    currentModel: keyModels.value.get(k.id) || '—',
  })),
)

function stateOf(k: ApiKey): Row['stateKey'] {
  if (k.status === 'pending') return 'pending'
  if (!k.enabled) return 'off'
  if (!k.last_used_at) return 'idle'
  return 'ok'
}

function stateLabel(state: Row['stateKey']): string {
  switch (state) {
    case 'ok':
      return t('simpleMode.accessList.healthOk')
    case 'idle':
      return t('simpleMode.accessList.healthIdle')
    case 'pending':
      return t('common.status.pending')
    default:
      return t('common.status.disabled')
  }
}

function stateClass(state: Row['stateKey']): string {
  switch (state) {
    case 'ok':
      return 'badge-green'
    case 'idle':
      return 'badge-gray'
    case 'pending':
      return 'badge-yellow'
    default:
      return 'badge-red'
  }
}

function fmtUsd(v: number | null | undefined): string {
  if (v == null) return '—'
  return `$${v.toFixed(2)}`
}

function openCreate(): void {
  createAlias.value = ''
  createBudget.value = ''
  createError.value = ''
  showCreate.value = true
}

/**
 * 两字段签发：① 应用 / 别名 ② 预算上限。
 * ★ 后端 POST /api/keys 要求 application_code 与 key_alias 都非空
 *   （admin/keys.go:1113 与冲突检查），所以这一个可见字段**同时**填这两项
 *   （key_alias = application_code）—— 少一个输入点，但不绕过任何后端校验。
 */
async function submitCreate(): Promise<void> {
  const alias = createAlias.value.trim()
  if (!alias) {
    createError.value = t('common.feedback.operationFailed')
    return
  }
  creating.value = true
  createError.value = ''
  try {
    await createKey({
      application_code: alias,
      key_alias: alias,
      budget_usd: createBudget.value === '' ? undefined : Number(createBudget.value),
    })
    showCreate.value = false
    await load()
  } catch (e) {
    createError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

function openDetail(k: ApiKey): void {
  void router.push(`/keys/${k.id}`)
}
</script>

<template>
  <div>
    <PageHeader :title="t('simpleMode.accessList.title')">
      <template #actions>
        <span class="chip">{{ t('simpleMode.mode.label') }} · {{ isSimple ? t('simpleMode.mode.simple') : t('simpleMode.mode.expert') }}</span>
        <button class="btn btn-ghost" @click="load">{{ t('common.button.refresh') }}</button>
        <button class="btn btn-primary" @click="openCreate">{{ t('simpleMode.accessList.signKey') }}</button>
      </template>
    </PageHeader>

    <p class="section-hint">{{ t('simpleMode.accessList.subtitle') }}</p>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-if="loading" class="empty">{{ t('common.feedback.loading') }}</div>

    <div v-else-if="rows.length === 0" class="card">
      <div class="empty">
        <p>{{ t('simpleMode.accessList.empty') }}</p>
        <p class="section-hint">{{ t('simpleMode.accessList.emptyHint') }}</p>
      </div>
    </div>

    <div v-else class="card">
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{{ t('simpleMode.accessList.client') }}</th>
              <th style="width:120px">{{ t('common.table.status') }}</th>
              <th style="width:160px">{{ t('simpleMode.accessList.currentModel') }}</th>
              <th style="width:120px">{{ t('simpleMode.accessList.requests') }}</th>
              <th style="width:180px">{{ t('simpleMode.accessList.spend') }}</th>
              <th style="width:180px">{{ t('simpleMode.accessList.lastUsed') }}</th>
              <th style="width:100px"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.key.id">
              <td>
                <strong>{{ r.name }}</strong>
                <span class="section-hint">{{ r.key.key_prefix }}</span>
              </td>
              <td><span class="badge" :class="stateClass(r.stateKey)">{{ stateLabel(r.stateKey) }}</span></td>
              <td><span class="mono">{{ r.currentModel }}</span></td>
              <td>{{ r.key.total_requests }}</td>
              <td>{{ r.spend }} / {{ r.budget }}</td>
              <td>{{ r.lastUsed }}</td>
              <td>
                <button class="btn btn-ghost btn-sm" @click="openDetail(r.key)">
                  {{ t('common.button.detail') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div class="card-toolbar">
        <span class="section-hint">{{ t('simpleMode.accessList.usageHint') }}</span>
      </div>
    </div>

    <!-- 两字段签发 -->
    <div v-if="showCreate" class="modal-overlay" @click.self="showCreate = false">
      <div class="modal-panel">
        <h3>{{ t('simpleMode.accessList.signKey') }}</h3>
        <div v-if="createError" class="alert alert-danger">{{ createError }}</div>
        <div class="form-group">
          <label>{{ t('simpleMode.accessList.client') }}</label>
          <input v-model="createAlias" class="cf-input" type="text" autocomplete="off" />
        </div>
        <div class="form-group">
          <label>{{ t('simpleMode.accessList.spend') }}</label>
          <input v-model="createBudget" class="cf-input" type="number" min="0" step="1" />
        </div>
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="showCreate = false">{{ t('common.button.cancel') }}</button>
          <button class="btn btn-primary" :disabled="creating" @click="submitCreate">
            {{ creating ? t('common.feedback.loading') : t('common.button.confirm') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
