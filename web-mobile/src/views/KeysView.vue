<script setup lang="ts">
// KeysView — API 密钥管理：列表 + 创建 Sheet（脏关闭守卫）+ 揭示（确认框，
// 结果不缓存不预取，13 §4）+ 停用/启用。
import { computed, ref } from 'vue'
import { ContinuousListController, useHyperPage } from '@/hyper'
import { createKey, disableKey, enableKey, getKeys, revealKey, type ApiKey } from '@/api/keys'
import { t } from '@/i18n'
import HyperList from '@/components/common/HyperList.vue'
import AppSheet from '@/components/common/AppSheet.vue'
import AppConfirm from '@/components/common/AppConfirm.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { fmtInt, fmtUsd } from '@/utils/format'

useHyperPage({ title: () => t('keys.title') })

let cache: ApiKey[] = []
const controller = new ContinuousListController<ApiKey>({
  fetchPage: async (page) => {
    if (page > 1) return { items: [], total: cache.length }
    cache = await getKeys()
    return { items: cache, total: cache.length }
  },
  stableKey: (k) => `key-${k.id}`,
  scopeKey: 'keys',
})

// ---- 创建 ----
const createOpen = ref(false)
const formName = ref('')
const formAppCode = ref('')
const formBudget = ref('')
const creating = ref(false)
const createdSecret = ref<string | null>(null)
const createError = ref<string | null>(null)
// 停用/启用的失败反馈（2026-10-06 补；原先 doDisable 无 catch ⇒ 失败静默）
const disableError = ref<string | null>(null)
const disabling = ref(false)
const copied = ref(false)

const formDirty = computed(() => formName.value.trim() !== '' || formAppCode.value.trim() !== '' || formBudget.value.trim() !== '')

async function beforeCloseCreate(): Promise<boolean> {
  if (createdSecret.value) return true // 已创建完成，直接关
  if (!formDirty.value) return true
  return window.confirm(t('common.confirmDirtyBody'))
}

async function submitCreate(): Promise<void> {
  if (creating.value) return
  creating.value = true
  createError.value = null
  try {
    const budget = formBudget.value.trim() === '' ? undefined : Number(formBudget.value)
    const resp = await createKey({
      application_code: formAppCode.value.trim() || formName.value.trim() || 'mobile',
      key_alias: formName.value.trim() || undefined,
      budget_usd: Number.isFinite(budget) ? budget : undefined,
    })
    createdSecret.value = resp.api_key
    await controller.refresh()
  } catch (err) {
    createError.value = err instanceof Error ? err.message : String(err)
  } finally {
    creating.value = false
  }
}

function closeCreate(): void {
  createOpen.value = false
  formName.value = ''
  formAppCode.value = ''
  formBudget.value = ''
  createdSecret.value = null
  createError.value = null
  copied.value = false
}

async function copySecret(): Promise<void> {
  if (!createdSecret.value) return
  try {
    await navigator.clipboard.writeText(createdSecret.value)
    copied.value = true
  } catch {
    /* 剪贴板不可用 — 用户手动长按复制 */
  }
}

// ---- 揭示 / 停用 ----
const revealTarget = ref<ApiKey | null>(null)
const revealed = ref<string | null>(null)
const disableTarget = ref<ApiKey | null>(null)

const revealOpen = computed({
  get: () => revealTarget.value != null,
  set: (v: boolean) => {
    if (!v) {
      revealTarget.value = null
      revealed.value = null // 敏感 reveal 不缓存（13 §4）
    }
  },
})

async function doReveal(): Promise<void> {
  if (!revealTarget.value) return
  try {
    const resp = await revealKey(revealTarget.value.id)
    revealed.value = resp.api_key
  } catch {
    revealed.value = null
  }
}

/**
 * ★ 2026-10-06 修正：原实现**既无 catch 也无错误提示位** ——
 * `try { ... } finally { disableTarget.value = null }`。
 * 停用/启用失败时（如 409 已被别处停用、403 权限不足、500）用户点完确认框，
 * 界面**毫无反馈**，误以为成功了。而同文件的 submitCreate 有完整 catch + 错误位
 * （:48/:59/:193）⇒ 同页两套标准，这是漏写不是有意设计。
 *
 * 修复：加 catch + 错误位；403 单独说人话（权限档位问题，不是网络故障）。
 */
async function doDisable(): Promise<void> {
  if (!disableTarget.value) return
  const target = disableTarget.value
  const enabling = target.status === 'disabled'
  disabling.value = true
  disableError.value = null
  try {
    if (enabling) {
      await enableKey(target.id)
    } else {
      await disableKey(target.id)
    }
    await controller.refresh()
    disableTarget.value = null
  } catch (err) {
    const status = (err as { status?: number })?.status
    disableError.value =
      status === 403
        ? t('keys.errForbidden')
        : status === 409
          ? t('keys.errConflict')
          : err instanceof Error
            ? err.message
            : String(err)
  } finally {
    disabling.value = false
  }
}

function statusBadge(k: ApiKey): { cls: string; label: string } {
  if (k.status === 'disabled' || !k.enabled) return { cls: 'badge--muted', label: t('keys.disabled') }
  if (k.status === 'pending') return { cls: 'badge--warning', label: k.status }
  return { cls: 'badge--success', label: t('keys.active') }
}

function keyName(k: ApiKey): string {
  return k.key_alias || k.application_code || k.key_prefix
}
</script>

<template>
  <div class="view-root">
  <HyperList
    :controller="controller"
    :item-key="(k: ApiKey) => `key-${k.id}`"
    :empty-hint="t('keys.emptyHint')"
  >
    <template #header>
      <button type="button" class="btn btn--primary keys__create" @click="createOpen = true">
        <AppIcon name="plus" :size="18" />
        {{ t('keys.create') }}
      </button>
    </template>

    <template #item="{ item: k }">
      <div class="data-card key-card">
        <div class="card-row">
          <span class="key-card__name">{{ keyName(k) }}</span>
          <span class="badge" :class="statusBadge(k).cls">{{ statusBadge(k).label }}</span>
        </div>
        <code class="key-card__prefix">{{ k.key_prefix }}…</code>
        <div class="key-card__stats">
          <span>{{ t('keys.requests') }} <b class="num">{{ fmtInt(k.total_requests) }}</b></span>
          <span>{{ t('keys.tokens') }} <b class="num">{{ fmtInt(k.total_prompt_tokens + k.total_completion_tokens) }}</b></span>
        </div>
        <div class="key-card__stats">
          <span>{{ t('keys.used') }} <b class="num">{{ fmtUsd(k.total_cost_usd) }}</b></span>
          <span v-if="k.budget_usd != null">{{ t('keys.budget') }} <b class="num">{{ fmtUsd(k.budget_usd) }}</b></span>
        </div>
        <div class="key-card__actions">
          <button type="button" class="btn btn--sm" @click="revealTarget = k">
            {{ t('keys.reveal') }}
          </button>
          <button type="button" class="btn btn--sm btn--danger" @click="disableTarget = k">
            {{ k.status === 'disabled' ? t('keys.enable') : t('keys.disable') }}
          </button>
        </div>
      </div>
    </template>
  </HyperList>

  <!-- 创建 -->
  <AppSheet v-model="createOpen" presentation="sheet" :title="t('keys.create')" :before-close="beforeCloseCreate" @update:model-value="(v: boolean) => { if (!v) closeCreate() }">
    <template v-if="!createdSecret">
      <p class="keys__hint">{{ t('keys.createHint') }}</p>
      <form class="keys__form" @submit.prevent="submitCreate">
        <label class="keys__field">
          <span>{{ t('keys.form.name') }}</span>
          <input v-model="formName" type="text" :placeholder="t('keys.form.namePlaceholder')" />
        </label>
        <label class="keys__field">
          <span>{{ t('keys.form.appName') }}</span>
          <input v-model="formAppCode" type="text" autocapitalize="none" />
        </label>
        <label class="keys__field">
          <span>{{ t('keys.budget') }}</span>
          <input v-model="formBudget" type="number" min="0" step="0.01" inputmode="decimal" />
        </label>
        <p v-if="createError" class="keys__error" role="alert">{{ createError }}</p>
        <button type="submit" class="btn btn--primary btn--block" :disabled="creating || !formName.trim()">
          {{ creating ? t('common.loading') : t('keys.form.submit') }}
        </button>
      </form>
    </template>
    <template v-else>
      <p class="keys__hint">{{ t('keys.revealed') }}</p>
      <code class="keys__secret">{{ createdSecret }}</code>
      <div class="keys__secret-actions">
        <button type="button" class="btn" @click="copySecret">
          {{ copied ? t('common.copied') : t('common.copy') }}
        </button>
        <button type="button" class="btn btn--primary" @click="closeCreate">{{ t('common.confirm') }}</button>
      </div>
    </template>
  </AppSheet>

  <!-- 揭示 -->
  <AppSheet v-model="revealOpen" presentation="sheet" :title="t('keys.reveal')">
    <template v-if="revealTarget">
      <template v-if="!revealed">
        <p class="keys__hint">{{ t('keys.revealConfirm') }}</p>
        <button type="button" class="btn btn--primary btn--block" @click="doReveal">{{ t('common.confirm') }}</button>
      </template>
      <template v-else>
        <code class="keys__secret">{{ revealed }}</code>
      </template>
    </template>
  </AppSheet>

  <!-- 停用/启用确认 -->
  <!-- 停用/启用失败反馈（2026-10-06 补）。放在页面级而非确认框内：
       AppConfirm 失败后会关闭（onConfirm 立即置 false），框内的提示没人看得到。 -->
  <p v-if="disableError" class="keys__error" role="alert">{{ disableError }}</p>

  <AppConfirm
    :model-value="disableTarget != null"
    :title="disableTarget && disableTarget.status === 'disabled' ? t('keys.enable') : t('keys.disable')"
    :body="disableTarget ? keyName(disableTarget) : ''"
    @update:model-value="(v: boolean) => { if (!v) disableTarget = null }"
    @confirm="doDisable"
  />
  </div>
</template>

<style scoped>
.keys__create {
  width: 100%;
  margin-bottom: var(--app-space-3);
}

.key-card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.key-card__name {
  font-weight: 600;
  font-size: 0.9375rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.key-card__prefix {
  font-size: 0.75rem;
  color: var(--app-text-muted);
  background: var(--app-surface-muted);
  border-radius: var(--app-radius-sm);
  padding: 2px 8px;
  align-self: flex-start;
}

.key-card__stats {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1) var(--app-space-3);
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.key-card__actions {
  display: flex;
  gap: var(--app-space-2);
  margin-top: var(--app-space-1);
}

.keys__hint {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  margin-bottom: var(--app-space-3);
}

.keys__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-3);
}

.keys__field {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.keys__field input {
  height: 48px;
  padding: 0 var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: var(--app-font-input);
}

.keys__error {
  color: var(--app-danger);
  font-size: 0.8125rem;
  margin: 0;
}

.keys__secret {
  display: block;
  padding: var(--app-space-3);
  background: var(--app-surface-muted);
  border: 1px dashed var(--app-border);
  border-radius: var(--app-radius);
  font-size: 0.8125rem;
  word-break: break-all;
  user-select: all;
}

.keys__secret-actions {
  display: flex;
  gap: var(--app-space-2);
  margin-top: var(--app-space-3);
}

.keys__secret-actions .btn {
  flex: 1;
}
</style>
