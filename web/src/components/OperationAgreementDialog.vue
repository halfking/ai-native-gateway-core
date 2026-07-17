<script setup lang="ts">
// OperationAgreementDialog.vue — 操作前协议确认弹窗
//
// 用于「下载离线包」与「激活」两个关键操作的门控：
// - 用户首次点对应操作时弹出，未勾选「我已阅读并同意」则按钮 disabled
// - 同意后写入 localStorage（key 包含协议版本号），后续同 scope 操作不再弹
// - 升级版本号（AGREEMENT_VERSION 常量）后所有用户需重新同意
//
// 2026-07-17: 取代原先散落在各页面 footer / 折叠面板 / 全局弹窗的"用户须知"常驻展示。
// 设计原则：agreement 文字只在用户即将执行需要承担责任的「操作」时短暂出现，
// 而非被动地堆叠在每个页面底部 / 折叠面板中。

import { ref, watch, computed } from 'vue'
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
}>()

const { t, tm } = useI18n()
const agreed = ref(false)
const visible = ref(props.modelValue)
const points = tm('public.agreement.points') as string[]

const title = computed(() => {
  // 同一份协议文本，仅标题按 scope 区分下载 / 激活场景
  if (props.scope === 'download') {
    return t('public.opAgreement.downloadTitle', t('public.agreement.title'))
  }
  return t('public.opAgreement.activateTitle', t('public.agreement.title'))
})

watch(
  () => props.modelValue,
  (v) => {
    visible.value = v
    if (v) agreed.value = false
  },
)

watch(visible, (v) => emit('update:modelValue', v))

function close() {
  visible.value = false
  // 取消 / 关闭弹窗 = 用户拒绝当前操作；父组件应在 agreed 未触发时不继续
}

function accept() {
  if (!agreed.value) return
  try {
    localStorage.setItem(
      `llmgw_op_agreement_${props.scope}_${props.version}`,
      new Date().toISOString(),
    )
  } catch {
    /* localStorage 不可用（隐私模式 / quota）— 不影响本次操作 */
  }
  visible.value = false
  emit('agreed')
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="title"
    width="min(640px, 92vw)"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    :show-close="true"
    @close="close"
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
      <el-button type="primary" :disabled="!agreed" @click="accept">
        {{ t('public.agreement.accept') }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.agree-intro { margin: 0 0 12px; line-height: 1.6; color: #475569; }
.agree-list { margin: 0 0 12px; padding-left: 1.25rem; color: #64748b; line-height: 1.65; }
.agree-note { margin: 0 0 16px; font-size: 13px; color: #94a3b8; line-height: 1.6; }
</style>