<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { maintainLifecycleApi } from '../../api/maintainLifecycle'

export type OperationScope = 'download' | 'activate'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    scope: OperationScope
    version: string
    subjectId?: string
  }>(),
  { version: '2026-07-17' },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
  (e: 'agreed'): void
  (e: 'cancelled'): void
}>()

const agreed = ref(false)
const dialogVisible = computed({
  get: () => props.modelValue,
  set: (val) => {
    emit('update:modelValue', val)
    if (!val && !agreed.value) emit('cancelled')
  },
})

const storageKey = computed(() => `llmgw_op_agreement_${props.scope}_${props.version}`)
const title = computed(() =>
  props.scope === 'download' ? '下载前请阅读用户协议' : '激活前请阅读用户协议',
)

const points = [
  '你有权在授权范围内使用本软件。',
  '你的业务数据与配置默认保存在你的基础设施中。',
  '请遵守适用的开源协议与商业授权条款。',
  '软件按“现状”提供，不提供任何明示或暗示的保证。',
]

function onOpen() {
  agreed.value = !!localStorage.getItem(storageKey.value)
}

async function accept() {
  if (!agreed.value) return
  localStorage.setItem(storageKey.value, new Date().toISOString())
  const agreementType = props.scope === 'download' ? 'download_terms' : 'activation_terms'
  try {
    await maintainLifecycleApi.consent({
      subject_id: props.subjectId || 'anonymous',
      agreement_type: agreementType,
      agreement_version: props.version,
      granted: true,
      source: 'gateway-web/operation_dialog',
    })
  } catch {
    // local cache still unlocks UX when consent write is unavailable
  }
  dialogVisible.value = false
  emit('agreed')
}

watch(
  () => props.modelValue,
  (val) => {
    if (val) onOpen()
  },
)
</script>

<template>
  <div v-if="dialogVisible" class="dialog-overlay" @click.self="dialogVisible = false">
    <div class="dialog-content" role="dialog" aria-modal="true" :aria-labelledby="`agreement-title-${scope}`">
      <h2 :id="`agreement-title-${scope}`">{{ title }}</h2>
      <p class="agree-intro">在执行此操作前，请确认你已了解以下要点：</p>
      <ul class="agree-list">
        <li v-for="(line, i) in points" :key="i">{{ line }}</li>
      </ul>
      <p class="agree-note">完整条款请参见 <a href="/user-agreement.html" target="_blank" rel="noopener">用户协议</a>。</p>
      <label class="agree-checkbox">
        <input v-model="agreed" type="checkbox" />
        <span>我已阅读并同意用户协议</span>
      </label>
      <div class="dialog-actions">
        <button type="button" class="btn btn-ghost" @click="dialogVisible = false">取消</button>
        <button type="button" class="btn btn-primary" :disabled="!agreed" @click="accept">同意并继续</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dialog-overlay {
  position: fixed;
  inset: 0;
  background: rgba(21, 32, 51, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  padding: 1rem;
}
.dialog-content {
  background: var(--on-primary);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 1.5rem;
  max-width: 540px;
  width: 100%;
  box-shadow: 0 24px 48px rgba(21, 32, 51, 0.16);
  color: var(--kx-text);
}
.dialog-content h2 { margin: 0 0 1rem; font-size: 1.15rem; }
.agree-intro, .agree-note { color: var(--kx-muted); font-size: 13px; line-height: 1.6; }
.agree-list { margin: 0 0 1rem; padding-left: 1.2rem; color: #344258; font-size: 13px; line-height: 1.7; }
.agree-checkbox { display: flex; gap: 8px; align-items: flex-start; margin: 14px 0; font-size: 13px; }
.agree-checkbox input { margin-top: 3px; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 8px; }
</style>
