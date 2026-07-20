<script setup lang="ts">
import { ref } from 'vue'
import { maintainLifecycleApi } from '../../api/maintainLifecycle'
import { SITE_TITLE } from '../../config/brand'

const AGREEMENT_VERSION = '2026-07-17'
const storageKey = `llmgw_product_terms_${AGREEMENT_VERSION}`
const hasLocalConsent = ref(!!localStorage.getItem(storageKey))
const agreed = ref(hasLocalConsent.value)
const saving = ref(false)
const message = ref('')
const error = ref('')

async function accept() {
  if (!agreed.value) return
  saving.value = true
  error.value = ''
  message.value = ''
  try {
    localStorage.setItem(storageKey, new Date().toISOString())
    hasLocalConsent.value = true
    await maintainLifecycleApi.consent({
      subject_id: localStorage.getItem('llmgw_instance_id') || 'anonymous',
      instance_id: localStorage.getItem('llmgw_instance_id') || undefined,
      agreement_type: 'product_terms',
      agreement_version: AGREEMENT_VERSION,
      granted: true,
      source: 'gateway-web/agreement_page',
    })
    message.value = '已记录你对本版本用户协议的同意。'
  } catch (e) {
    // Still keep local acceptance for offline-friendly UX.
    message.value = '已在本机记录同意；服务端同步失败时可稍后重试。'
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="lifecycle-page">
    <header class="lifecycle-page__header">
      <div>
        <p class="eyebrow">USER AGREEMENT</p>
        <h1>用户许可协议</h1>
        <p>使用 {{ SITE_TITLE }} 前，请阅读并确认当前版本协议。激活与下载相关操作也会再次确认。</p>
      </div>
      <a class="btn btn-ghost" href="/user-agreement.html" target="_blank" rel="noopener">打开完整协议 ↗</a>
    </header>

    <el-card shadow="never">
      <p class="version">当前版本：v{{ AGREEMENT_VERSION }}</p>
      <ul class="points">
        <li>你可以在授权范围内部署和使用本软件。</li>
        <li>业务数据默认保存在你控制的基础设施中。</li>
        <li>请遵守开源组件协议与商业授权条款。</li>
        <li>软件按“现状”提供；关键生产环境请完成激活与运维加固。</li>
      </ul>

      <el-alert v-if="message" :type="error ? 'warning' : 'success'" :title="message" show-icon class="mb" :closable="false" />
      <el-alert v-if="error" type="info" :title="error" show-icon class="mb" :closable="false" />

      <el-checkbox v-model="agreed">
        我已阅读并同意用户许可协议 v{{ AGREEMENT_VERSION }}
      </el-checkbox>
      <div class="actions">
        <el-button type="primary" :disabled="!agreed" :loading="saving" @click="accept">
          {{ hasLocalConsent ? '已同意（可重新同步）' : '确认同意' }}
        </el-button>
        <RouterLink class="btn btn-ghost" to="/customer/activate">继续去激活 →</RouterLink>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.lifecycle-page { padding: 24px clamp(16px, 3vw, 32px) 40px; max-width: 820px; }
.lifecycle-page__header {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}
.eyebrow {
  margin: 0 0 6px;
  color: #1e4fd6;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.12em;
}
.lifecycle-page__header h1 { margin: 0 0 8px; font-size: 24px; }
.lifecycle-page__header p { margin: 0; color: #5b6b82; font-size: 14px; line-height: 1.6; }
.version { margin: 0 0 12px; color: #5b6b82; font-size: 13px; }
.points { margin: 0 0 18px; padding-left: 1.2rem; color: #344258; line-height: 1.75; }
.actions { display: flex; flex-wrap: wrap; gap: 12px; align-items: center; margin-top: 16px; }
.mb { margin: 12px 0; }
</style>
