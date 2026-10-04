<script setup lang="ts">
// KeysView.vue — 密钥：卡片列表 + 创建 Sheet + 禁用/启用/揭示（确认框，
// 揭示不缓存 13 §4）+「全页查看密钥表」专注宽表（可编辑：禁用/启用）。
import { computed, onMounted, ref } from 'vue'
import {
  getKeys,
  normalizeKeysPayload,
  createKey,
  disableKey,
  enableKey,
  revealKey,
  type ApiKey,
} from '../api/keys'
import { useHyperPage } from '../composables/useHyperPage'
import { focusWorkspace, type FocusHandle } from '../runtime/focusWorkspace'
import { useTitleStore } from '../stores/titleStore'
import SkeletonList from '../components/ui/SkeletonList.vue'
import ErrorRetry from '../components/ui/ErrorRetry.vue'
import EmptyState from '../components/ui/EmptyState.vue'
import Sheet from '../components/ui/Sheet.vue'
import ConfirmDialog from '../components/ui/ConfirmDialog.vue'
import Icon from '../components/Icon.vue'
import FocusWorkspace from '../components/focus/FocusWorkspace.vue'
import { t } from '../i18n'

const titleStore = useTitleStore()
useHyperPage({ routeTitle: t('keys.title') })

const keys = ref<ApiKey[]>([])
const loading = ref(true)
const loadError = ref('')
const createOpen = ref(false)
const focusHandle = ref<FocusHandle | null>(null)

const confirmState = ref<{ kind: 'disable' | 'enable'; key: ApiKey } | null>(null)
const revealConfirm = ref<ApiKey | null>(null)
const revealed = ref<{ key: ApiKey; plaintext: string } | null>(null)
const busy = ref(false)

const form = ref({ application_code: '', key_alias: '', budget_usd: '' })

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    keys.value = normalizeKeysPayload(await getKeys())
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

function statusTone(k: ApiKey): 'success' | 'warning' | 'info' {
  if (!k.enabled || k.status === 'disabled') return 'info'
  if (k.status === 'pending') return 'warning'
  return 'success'
}

function statusLabel(k: ApiKey): string {
  if (!k.enabled || k.status === 'disabled') return t('keys.status.disabled')
  if (k.status === 'pending') return t('keys.status.pending')
  return t('keys.status.active')
}

async function submitCreate(): Promise<void> {
  if (!form.value.application_code || busy.value) return
  busy.value = true
  try {
    const res = await createKey({
      application_code: form.value.application_code,
      key_alias: form.value.key_alias || undefined,
      budget_usd: form.value.budget_usd ? Number(form.value.budget_usd) : undefined,
    })
    createOpen.value = false
    form.value = { application_code: '', key_alias: '', budget_usd: '' }
    if (res.api_key) {
      revealed.value = { key: res as unknown as ApiKey, plaintext: res.api_key }
    }
    await load()
  } finally {
    busy.value = false
  }
}

async function doConfirm(): Promise<void> {
  const c = confirmState.value
  confirmState.value = null
  if (!c) return
  busy.value = true
  try {
    if (c.kind === 'disable') await disableKey(c.key.id)
    else await enableKey(c.key.id)
    await load()
  } finally {
    busy.value = false
  }
}

async function doReveal(): Promise<void> {
  const k = revealConfirm.value
  revealConfirm.value = null
  if (!k) return
  busy.value = true
  try {
    const res = await revealKey(k.id)
    revealed.value = { key: k, plaintext: res.api_key ?? res.key ?? res.plaintext ?? '' }
  } finally {
    busy.value = false
  }
}

function openFocus(e: Event): void {
  focusHandle.value = focusWorkspace().open({
    title: t('keys.title'),
    triggerEl: (e.currentTarget as HTMLElement) ?? null,
  })
}

function fmtCost(n: number): string {
  return `$${n.toFixed(2)}`
}

function relTime(iso: string | null | undefined): string {
  if (!iso) return t('keys.never')
  const d = new Date(iso)
  const diff = Date.now() - d.getTime()
  if (diff < 3600_000) return `${Math.max(1, Math.round(diff / 60_000))}m`
  if (diff < 86_400_000) return `${Math.round(diff / 3600_000)}h`
  return `${Math.round(diff / 86_400_000)}d`
}

function copyReveal(): void {
  if (revealed.value) void navigator.clipboard?.writeText(revealed.value.plaintext)
}

const keyCount = computed(() => keys.value.length)

onMounted(() => {
  titleStore.setRegistered(t('keys.title'))
  void load()
})
</script>

<template>
  <section class="m-page">
    <div class="page-actions">
      <button type="button" class="m-btn m-btn--ghost act" @click="openFocus">
        <Icon name="focus" />
        {{ t('keys.viewAll') }}
      </button>
      <button type="button" class="m-btn act" @click="createOpen = true">
        <Icon name="more" />
        {{ t('keys.create') }}
      </button>
    </div>

    <SkeletonList v-if="loading" :lines="5" />
    <ErrorRetry v-else-if="loadError" :message="loadError" @retry="load" />
    <EmptyState v-else-if="keyCount === 0" />

    <template v-else>
      <div v-for="k in keys" :key="k.id" class="m-card" :data-row-id="`key-${k.id}`">
        <div class="m-card__row">
          <span class="key-name">{{ k.key_alias || k.key_prefix }}</span>
          <span class="m-chip" :class="`m-chip--${statusTone(k)}`">{{ statusLabel(k) }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('keys.app') }}</span>
          <span class="m-kv__v">{{ k.application_code }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('keys.requests') }}</span>
          <span class="m-kv__v num">{{ k.total_requests }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('keys.cost') }}</span>
          <span class="m-kv__v num">{{ fmtCost(k.total_cost_usd) }}</span>
        </div>
        <div class="m-kv">
          <span class="m-kv__k">{{ t('keys.lastUsed') }}</span>
          <span class="m-kv__v">{{ relTime(k.last_used_at ?? k.last_request_at) }}</span>
        </div>
        <div class="key-actions">
          <button type="button" class="mini-btn" @click="revealConfirm = k">{{ t('keys.reveal') }}</button>
          <button
            v-if="k.enabled"
            type="button"
            class="mini-btn mini-btn--danger"
            @click="confirmState = { kind: 'disable', key: k }"
          >
            {{ t('keys.disable') }}
          </button>
          <button v-else type="button" class="mini-btn" @click="confirmState = { kind: 'enable', key: k }">
            {{ t('keys.enable') }}
          </button>
        </div>
      </div>
    </template>

    <!-- 创建 Sheet（脏表单守卫：有输入未提交时返回需确认） -->
    <Sheet
      v-if="createOpen"
      :title="t('keys.create')"
      :before-close="() => !form.application_code && !form.key_alias"
      @close="createOpen = false"
    >
      <label class="m-field">
        <span class="m-field__label">{{ t('keys.create.app') }}</span>
        <input v-model="form.application_code" class="m-input" type="text" autocapitalize="none" />
      </label>
      <label class="m-field">
        <span class="m-field__label">{{ t('keys.create.alias') }}</span>
        <input v-model="form.key_alias" class="m-input" type="text" />
      </label>
      <label class="m-field">
        <span class="m-field__label">{{ t('keys.create.budget') }}</span>
        <input v-model="form.budget_usd" class="m-input" type="text" inputmode="decimal" />
      </label>
      <button type="button" class="m-btn" style="width: 100%" :disabled="busy || !form.application_code" @click="submitCreate">
        {{ t('common.confirm') }}
      </button>
    </Sheet>

    <ConfirmDialog
      v-if="confirmState"
      :title="confirmState.kind === 'disable' ? t('keys.disableConfirm') : t('keys.enableConfirm')"
      :danger="confirmState.kind === 'disable'"
      @confirm="doConfirm"
      @cancel="confirmState = null"
    />

    <ConfirmDialog
      v-if="revealConfirm"
      :title="t('keys.reveal.confirmTitle')"
      :body="t('keys.reveal.confirmBody')"
      @confirm="doReveal"
      @cancel="revealConfirm = null"
    />

    <!-- 揭示结果：仅本次展示，关闭即弃（不缓存） -->
    <Sheet v-if="revealed" :title="t('keys.reveal')" @close="revealed = null">
      <p class="reveal-note">{{ t('keys.reveal.confirmBody') }}</p>
      <div class="reveal-box">
        <code class="reveal-code">{{ revealed.plaintext }}</code>
      </div>
      <button
        type="button"
        class="m-btn m-btn--ghost"
        style="width: 100%; margin-top: 0.75rem"
        @click="copyReveal"
      >
        {{ t('common.copy') }}
      </button>
    </Sheet>

    <!-- 专注宽表：密钥全页查看与编辑 -->
    <FocusWorkspace v-if="focusHandle" :handle="focusHandle" @exit="focusHandle = null">
      <div class="m-table-wrap">
        <table class="m-table">
          <thead>
            <tr>
              <th>Key</th>
              <th>App</th>
              <th>Status</th>
              <th class="num">Requests</th>
              <th class="num">Cost</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in keys" :key="`focus-${k.id}`">
              <td>{{ k.key_alias || k.key_prefix }}</td>
              <td>{{ k.application_code }}</td>
              <td :class="k.enabled ? 'cell-ok' : 'cell-mute'">{{ statusLabel(k) }}</td>
              <td class="num">{{ k.total_requests }}</td>
              <td class="num">{{ fmtCost(k.total_cost_usd) }}</td>
              <td>
                <button
                  v-if="k.enabled"
                  type="button"
                  class="mini-btn mini-btn--danger"
                  @click="confirmState = { kind: 'disable', key: k }"
                >
                  {{ t('keys.disable') }}
                </button>
                <button v-else type="button" class="mini-btn" @click="confirmState = { kind: 'enable', key: k }">
                  {{ t('keys.enable') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </FocusWorkspace>
  </section>
</template>

<style scoped>
.page-actions {
  display: flex;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}

.act {
  flex: 1;
  gap: var(--app-space-2);
}

.key-name {
  font-weight: 600;
  font-size: 0.9375rem;
  word-break: break-all;
}

.key-actions {
  display: flex;
  gap: var(--app-space-2);
  margin-top: var(--app-space-2);
}

.mini-btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 0.875rem;
}

.mini-btn--danger {
  border-color: var(--app-danger);
  color: var(--app-danger);
}

.reveal-note {
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  margin-bottom: var(--app-space-2);
}

.reveal-box {
  background: var(--app-surface-muted);
  border-radius: var(--app-radius);
  padding: var(--app-space-3);
}

.reveal-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8125rem;
  word-break: break-all;
  color: var(--app-text);
}

.cell-ok {
  color: var(--app-success);
}

.cell-mute {
  color: var(--app-text-muted);
}
</style>
