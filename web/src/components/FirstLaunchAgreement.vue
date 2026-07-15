<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

const AGREEMENT_VERSION = '2026-07-15'
const STORAGE_KEY = `llmgw_user_agreement_${AGREEMENT_VERSION}`

const { t, tm } = useI18n()
const visible = ref(!localStorage.getItem(STORAGE_KEY))
const agreed = ref(false)
const points = tm('public.agreement.points') as string[]

function accept() {
  if (!agreed.value) return
  localStorage.setItem(STORAGE_KEY, new Date().toISOString())
  visible.value = false
}
</script>

<template>
  <el-dialog
    v-model="visible"
    :title="t('public.agreement.title')"
    width="min(640px, 92vw)"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    :show-close="false"
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
