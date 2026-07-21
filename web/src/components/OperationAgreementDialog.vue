<script setup lang="ts">
// OperationAgreementDialog.vue — 操作前协议确认弹窗
//
// 用于「下载离线包」与「激活」两个关键操作的门控：
// - 用户首次点对应操作时弹出，未勾选「我已阅读并同意」则按钮 disabled
// - 同意后写入 localStorage（key 包含协议版本号），后续同 scope 操作不再弹
// - 升级版本号（version prop）后所有用户需重新同意
//
// 2026-07-17: 取代原先散落在各页面 footer / 折叠面板 / 全局弹窗的"用户须知"常驻展示。
// 2026-07-22: 按钮换用全局 .btn 样式（细边框 / 稍大尺寸 / 箭头 / hover 反显 / disabled 显灰）。

import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'

export type OperationScope = 'download' | 'activate'

const props = withDefaults(
  defineProps<{
    modelValue: boolean
    scope: OperationScope
    /** 协议版本号；升级此值会清空所有用户的同意记录。 */
    version?: string
  }>(),
  { version: '2026-07-15' },
)

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  /** 用户点击「同意并继续」时触发，父组件据此执行后续操作。 */
  agreed: []
  /**
   * 用户取消 / 关闭弹窗（点 X、Esc、点击遮罩、点取消按钮）时触发。
   * 父组件应据此丢弃挂起的操作闭包，避免下次再点同类按钮时执行旧 action。
   */
  cancelled: []
}>()

const { t, tm } = useI18n()
const agreed = ref(false)
const points = tm('public.agreement.points') as string[]

const dialogVisible = computed({
  get: () => props.modelValue,
  set: (v) => {
    emit('update:modelValue', v)
    if (!v && !agreed.value) {
      emit('cancelled')
    }
  },
})

const title = computed(() => {
  const baseTitle = t('public.agreement.title')
  const key =
    props.scope === 'download'
      ? 'public.opAgreement.downloadTitle'
      : 'public.opAgreement.activateTitle'
  return t(key, { title: baseTitle })
})

function onOpen() {
  agreed.value = false
}

function accept() {
  if (!agreed.value) return
  try {
    localStorage.setItem(
      `llmgw_op_agreement_${props.scope}_${props.version}`,
      new Date().toISOString(),
    )
  } catch {
    /* localStorage 不可用 */
  }
  emit('agreed')
  emit('update:modelValue', false)
}

function cancelDialog() {
  emit('update:modelValue', false)
  emit('cancelled')
}
</script>

<template>
  <el-dialog
    v-model="dialogVisible"
    :title="title"
    width="min(640px, 92vw)"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    :show-close="true"
    @open="onOpen"
  >
    <p class="agree-intro">{{ t('public.agreement.intro') }}</p>
    <ul class="agree-list">
      <li v-for="(line, i) in points" :key="i">{{ line }}</li>
    </ul>
    <p class="agree-note">{{ t('public.agreement.disclaimer') }}</p>
    <el-checkbox v-model="agreed">
      {{ t('public.agreement.checkbox') }}
      <a href="/user-agreement.html" target="_blank" rel="noopener">{{ t('public.agreement.fullLink') }}</a>
    </el-checkbox>
    <template #footer>
      <div class="dialog-actions">
        <button type="button" class="btn btn-ghost btn-no-arrow" @click="cancelDialog">
          取消
        </button>
        <button type="button" class="btn btn-primary" :disabled="!agreed" @click="accept">
          {{ t('public.agreement.accept') }}
        </button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.agree-intro { margin: 0 0 12px; line-height: 1.6; color: var(--text); }
.agree-list { margin: 0 0 12px; padding-left: 1.25rem; color: var(--text); line-height: 1.65; }
.agree-note { margin: 0 0 16px; font-size: 13px; color: var(--muted); line-height: 1.6; }
.dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  flex-wrap: wrap;
}
</style>