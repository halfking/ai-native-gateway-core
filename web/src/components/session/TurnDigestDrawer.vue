<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ApiError } from '../../api/_core'
import {
  getAttachmentSignedUrl,
  getSessionTurn,
  type TurnAttachment,
  type TurnDetail,
} from '../../api/sessions_v2'
import TurnDigestCard from './TurnDigestCard.vue'

const props = defineProps<{
  modelValue: boolean
  sessionId: string
  turnNo: number | null
  /** Fallbacks from the session/turn list when the detail has no digest. */
  title?: string
  summary?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  close: []
}>()

const { t } = useI18n()
const detail = ref<TurnDetail | null>(null)
const loading = ref(false)
const error = ref('')
const activeTab = ref<'summary' | 'request' | 'response' | 'compression' | 'meta' | 'governance' | 'attachments'>('summary')
const openingAttachment = ref<string | null>(null)
let requestSeq = 0
let controller: AbortController | null = null

const headerTitle = computed(() => {
  if (props.turnNo == null) return t('turnDigest.title')
  return `${t('turnDigest.turn')} #${props.turnNo}`
})

const fallbackTitle = computed(() => detail.value?.title?.trim() || props.title?.trim() || '')
const fallbackSummary = computed(() => detail.value?.summary?.trim() || props.summary?.trim() || '')

function stringify(value: unknown): string {
  if (value == null) return ''
  try { return JSON.stringify(value, null, 2) } catch { return String(value) }
}

function attachmentKey(attachment: TurnAttachment): string {
  return attachment.att_id || attachment.object || attachment.name
}

async function openAttachment(attachment: TurnAttachment) {
  if (props.turnNo == null || !attachment.att_id) return
  const key = attachmentKey(attachment)
  openingAttachment.value = key
  try {
    const result = await getAttachmentSignedUrl(props.sessionId, props.turnNo, attachment.att_id)
    window.open(result.url, '_blank', 'noopener,noreferrer')
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    if (openingAttachment.value === key) openingAttachment.value = null
  }
}

function close() {
  emit('update:modelValue', false)
  emit('close')
}

function reload() {
  // Re-trigger the watch by toggling modelValue through the existing path.
  // The watcher resets state and refetches when the props tuple changes.
  error.value = ''
  if (props.modelValue && props.sessionId && props.turnNo != null) {
    // Force re-execution by resetting detail + bumping the seq, then
    // delegating to the same async fetch path used by the watch.
    requestSeq++
    const seq = requestSeq
    controller?.abort()
    controller = new AbortController()
    loading.value = true
    void getSessionTurn(props.sessionId, props.turnNo, { signal: controller.signal })
      .then((value) => {
        if (seq === requestSeq) detail.value = value
      })
      .catch((cause) => {
        if (seq !== requestSeq || (cause instanceof DOMException && cause.name === 'AbortError')) return
        error.value = cause instanceof ApiError ? cause.detail : cause instanceof Error ? cause.message : String(cause)
      })
      .finally(() => {
        if (seq === requestSeq) loading.value = false
      })
  }
}

watch(
  () => [props.modelValue, props.sessionId, props.turnNo] as const,
  ([open, sessionId, turnNo]) => {
    requestSeq++
    controller?.abort()
    controller = null
    detail.value = null
    error.value = ''
    openingAttachment.value = null
    activeTab.value = 'summary'
    if (!open || !sessionId || turnNo == null) {
      loading.value = false
      return
    }
    const seq = requestSeq
    controller = new AbortController()
    loading.value = true
    void getSessionTurn(sessionId, turnNo, { signal: controller.signal })
      .then((value) => {
        if (seq === requestSeq) detail.value = value
      })
      .catch((cause) => {
        if (seq !== requestSeq || (cause instanceof DOMException && cause.name === 'AbortError')) return
        error.value = cause instanceof ApiError ? cause.detail : cause instanceof Error ? cause.message : String(cause)
      })
      .finally(() => {
        if (seq === requestSeq) loading.value = false
      })
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  requestSeq++
  controller?.abort()
})
</script>

<template>
  <el-drawer
    :model-value="modelValue"
    direction="rtl"
    size="70%"
    :destroy-on-close="false"
    @update:model-value="(value: boolean) => value ? emit('update:modelValue', value) : close()"
    @close="close"
  >
    <template #header>
      <div class="tdd-header">
        <strong>{{ headerTitle }}</strong>
        <span v-if="detail?.model" class="tdd-sub">{{ detail.model }}<template v-if="detail.cost_usd != null"> · ${{ detail.cost_usd.toFixed(4) }}</template></span>
      </div>
    </template>

    <div v-if="loading" class="tdd-state">{{ t('turnDigest.loading') }}</div>
    <div v-else-if="error" class="tdd-error" role="alert" data-testid="tdd-error">
      <el-empty :description="error" />
      <el-button size="small" @click="reload">{{ t('turnDigest.close') }}</el-button>
    </div>
    <el-tabs v-else v-model="activeTab" class="tdd-tabs">
      <el-tab-pane :label="t('turnDigest.tabs.summary')" name="summary">
        <TurnDigestCard
          :digest="detail?.digest"
          :summary="fallbackSummary"
          :title="fallbackTitle"
          :turn-index="turnNo ?? undefined"
        />
      </el-tab-pane>
      <el-tab-pane :label="t('turnDigest.tabs.request')" name="request"><pre>{{ stringify(detail?.request) }}</pre></el-tab-pane>
      <el-tab-pane :label="t('turnDigest.tabs.response')" name="response"><pre>{{ stringify(detail?.response) }}</pre></el-tab-pane>
      <el-tab-pane :label="t('turnDigest.tabs.compression')" name="compression"><pre>{{ stringify(detail?.compression) }}</pre></el-tab-pane>
      <el-tab-pane :label="t('turnDigest.tabs.meta')" name="meta"><pre>{{ stringify(detail?.meta) }}</pre></el-tab-pane>
      <el-tab-pane :label="t('turnDigest.tabs.governance')" name="governance"><pre>{{ stringify(detail?.governance) }}</pre></el-tab-pane>
      <el-tab-pane :label="t('turnDigest.tabs.attachments')" name="attachments">
        <el-empty v-if="!detail?.attachments?.length" :description="t('turnDigest.noAttachments')" />
        <ul v-else class="tdd-attachments">
          <li v-for="attachment in detail.attachments" :key="attachmentKey(attachment)">
            <span class="tdd-attachment-name">{{ attachment.name || attachment.att_id }}</span>
            <span class="tdd-attachment-meta">{{ attachment.mime || 'file' }} · {{ attachment.size }} B</span>
            <el-button size="small" :disabled="openingAttachment === attachmentKey(attachment)" @click="openAttachment(attachment)">
              {{ openingAttachment === attachmentKey(attachment) ? t('turnDigest.openingAttachment') : t('turnDigest.openAttachment') }}
            </el-button>
          </li>
        </ul>
      </el-tab-pane>
    </el-tabs>

    <template #footer><el-button size="small" @click="close">{{ t('turnDigest.close') }}</el-button></template>
  </el-drawer>
</template>

<style scoped>
.tdd-header { display: flex; flex-direction: column; gap: 3px; }
.tdd-sub { color: var(--kx-muted); font-size: 12px; }
.tdd-state { padding: 24px; text-align: center; color: var(--kx-muted); }
.tdd-error { padding: 10px 12px; color: var(--kx-danger); background: var(--kx-danger-soft); border: 1px solid var(--kx-danger); border-radius: 6px; }
.tdd-tabs :deep(pre) { margin: 0; max-height: 70vh; overflow: auto; padding: 12px; background: var(--kx-bg-accent); color: var(--kx-text); border-radius: 6px; font-size: 12px; }
.tdd-attachments { list-style: none; margin: 0; padding: 0; display: grid; gap: 8px; }
.tdd-attachments li { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--kx-border); border-radius: 6px; }
.tdd-attachment-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tdd-attachment-meta { color: var(--kx-muted); font-size: 12px; white-space: nowrap; }
</style>
