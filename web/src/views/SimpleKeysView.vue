<script setup lang="ts">
// SimpleKeysView.vue — 简易模式密钥签发视图（L3）
// 只展示两个必需字段：客户端名称 + 备注
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { createKey, type KeyCreatedResponse } from '../api'
import { getCurrentTenantId, store } from '../store'
import { surfaceError } from '../i18n/errorSurface'
import { DocumentCopy, Check } from '@element-plus/icons-vue'

const { t } = useI18n()
const router = useRouter()

// 表单状态
const clientName = ref('')
const remark = ref('')
const saving = ref(false)
const error = ref('')
const success = ref(false)
const createdKey = ref<KeyCreatedResponse | null>(null)
const copied = ref(false)

// 自动填充默认值
const tenantId = getCurrentTenantId()
const defaultApp = 'default'

// 提交创建
async function submit() {
  if (!clientName.value.trim()) {
    error.value = t('simpleMode.keys.clientNameRequired')
    return
  }
  
  error.value = ''
  saving.value = true
  success.value = false

  try {
    const resp = await createKey({
      tenant_id: tenantId,
      application_code: defaultApp,
      key_alias: clientName.value.trim(),
      remark: remark.value.trim() || undefined,
    })
    
    createdKey.value = resp
    success.value = true
    
    // 清空表单
    clientName.value = ''
    remark.value = ''
  } catch (err: any) {
    error.value = surfaceError(err)
  } finally {
    saving.value = false
  }
}

// 复制密钥
async function copyKey() {
  if (!createdKey.value?.api_key) return
  
  try {
    await navigator.clipboard.writeText(createdKey.value.api_key)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch (err) {
    console.error('Failed to copy:', err)
  }
}

// 关闭成功提示
function closeSuccess() {
  success.value = false
  createdKey.value = null
}

// 返回总览页
// ★ 2026-11-17 修：原来推的是 '/simple/access-list'，**这条路由从来不存在**
//   （路由表里只有 /simple/keys）⇒ 点「返回」落到空路由。
//   简易模式的首页就是 /dashboard（HomeView 按模式分流）⇒ 回到它。
function backToAccessList() {
  router.push('/dashboard')
}
</script>

<template>
  <div class="simple-keys-view">
    <div class="header">
      <h1>{{ t('simpleMode.accessList.signKey') }}</h1>
      <p class="subtitle">{{ t('simpleMode.keys.subtitle') }}</p>
    </div>

    <!-- 成功提示（显示新密钥） -->
    <div v-if="success && createdKey" class="success-card">
      <div class="success-header">
        <Check class="icon-success" />
        <h3>{{ t('simpleMode.keys.created') }}</h3>
      </div>
      <p class="hint">{{ t('simpleMode.keys.copyHint') }}</p>
      
      <div class="key-display">
        <code class="api-key">{{ createdKey.api_key }}</code>
        <button 
          class="btn-copy" 
          @click="copyKey"
          :title="t('common.copy')"
        >
          <Check v-if="copied" class="icon-check" />
          <DocumentCopy v-else class="icon-copy" />
        </button>
      </div>
      
      <div class="key-info">
        <div class="info-row">
          <span class="label">{{ t('simpleMode.keys.client') }}:</span>
          <span class="value">{{ createdKey.key_alias }}</span>
        </div>
        <div class="info-row" v-if="createdKey.remark">
          <span class="label">{{ t('simpleMode.keys.remark') }}:</span>
          <span class="value">{{ createdKey.remark }}</span>
        </div>
      </div>

      <div class="actions">
        <button class="btn btn-ghost" @click="closeSuccess">
          {{ t('simpleMode.keys.signAnother') }}
        </button>
        <button class="btn btn-primary" @click="backToAccessList">
          {{ t('simpleMode.keys.backToList') }}
        </button>
      </div>
    </div>

    <!-- 签发表单 -->
    <div v-else class="form-card">
      <div v-if="error" class="alert alert-danger">{{ error }}</div>

      <div class="form-group">
        <label for="client-name">
          {{ t('simpleMode.keys.clientName') }} *
        </label>
        <input
          id="client-name"
          v-model="clientName"
          type="text"
          :placeholder="t('simpleMode.keys.clientNamePlaceholder')"
          :disabled="saving"
          @keyup.enter="submit"
          autofocus
        />
        <span class="hint">{{ t('simpleMode.keys.clientNameHint') }}</span>
      </div>

      <div class="form-group">
        <label for="remark">
          {{ t('simpleMode.keys.remark') }}
        </label>
        <textarea
          id="remark"
          v-model="remark"
          :placeholder="t('simpleMode.keys.remarkPlaceholder')"
          :disabled="saving"
          rows="3"
        />
      </div>

      <div class="form-actions">
        <button class="btn btn-ghost" @click="backToAccessList" :disabled="saving">
          {{ t('common.cancel') }}
        </button>
        <button 
          class="btn btn-primary" 
          @click="submit"
          :disabled="saving || !clientName.trim()"
        >
          {{ saving ? t('simpleMode.keys.signing') : t('simpleMode.keys.sign') }}
        </button>
      </div>
    </div>

    <!-- 自动填充说明 -->
    <div class="auto-fill-info">
      <p class="hint">
        {{ t('simpleMode.keys.autoFillHint') }}
      </p>
      <ul class="auto-fill-list">
        <li>
          <span class="field-name">{{ t('simpleMode.keys.tenant') }}:</span>
          <code>{{ tenantId }}</code>
        </li>
        <li>
          <span class="field-name">{{ t('simpleMode.keys.application') }}:</span>
          <code>{{ defaultApp }}</code>
        </li>
      </ul>
      <p class="hint">
        {{ t('simpleMode.keys.advancedHint') }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.simple-keys-view {
  max-width: 100%;
  padding: 24px 16px;
}

.header {
  margin-bottom: 24px;
}

.header h1 {
  font-size: 24px;
  font-weight: 600;
  margin: 0 0 8px;
  color: var(--text);
}

.subtitle {
  font-size: 14px;
  color: var(--muted);
  margin: 0;
}

/* 成功卡片 */
.success-card {
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 24px;
  margin-bottom: 24px;
}

.success-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
}

.icon-success {
  width: 32px;
  height: 32px;
  color: var(--success);
}

.success-header h3 {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
  color: var(--text);
}

.key-display {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 16px 0;
  padding: 12px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
}

.api-key {
  flex: 1;
  font-family: 'SF Mono', Consolas, monospace;
  font-size: 13px;
  color: var(--text);
  word-break: break-all;
}

.btn-copy {
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.15s;
}

.btn-copy:hover {
  background: var(--bg-hover);
}

.icon-copy,
.icon-check {
  width: 16px;
  height: 16px;
  color: var(--muted);
}

.icon-check {
  color: var(--success);
}

.key-info {
  margin: 16px 0;
}

.info-row {
  display: flex;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px solid var(--border);
}

.info-row:last-child {
  border-bottom: none;
}

.info-row .label {
  font-size: 13px;
  color: var(--muted);
  min-width: 60px;
}

.info-row .value {
  font-size: 13px;
  color: var(--text);
  flex: 1;
}

/* 表单卡片 */
.form-card {
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 24px;
  margin-bottom: 24px;
}

.form-group {
  margin-bottom: 20px;
}

.form-group:last-of-type {
  margin-bottom: 24px;
}

.form-group label {
  display: block;
  font-size: 14px;
  font-weight: 500;
  color: var(--text);
  margin-bottom: 8px;
}

.form-group input,
.form-group textarea {
  width: 100%;
  padding: 8px 12px;
  font-size: 14px;
  color: var(--text);
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  transition: border-color 0.15s;
}

.form-group input:focus,
.form-group textarea:focus {
  outline: none;
  border-color: var(--primary);
}

.form-group input:disabled,
.form-group textarea:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.form-group textarea {
  resize: vertical;
  font-family: inherit;
}

.hint {
  display: block;
  font-size: 12px;
  color: var(--muted);
  margin-top: 6px;
}

.form-actions,
.actions {
  display: flex;
  gap: 12px;
  justify-content: flex-end;
}

/* 自动填充说明 */
.auto-fill-info {
  padding: 16px;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 8px;
}

.auto-fill-list {
  list-style: none;
  padding: 0;
  margin: 12px 0;
}

.auto-fill-list li {
  display: flex;
  gap: 8px;
  padding: 6px 0;
  font-size: 13px;
}

.field-name {
  color: var(--muted);
  min-width: 80px;
}

.auto-fill-list code {
  font-family: 'SF Mono', Consolas, monospace;
  font-size: 12px;
  color: var(--text);
  background: var(--bg);
  padding: 2px 6px;
  border-radius: 3px;
}

/* 按钮 */
.btn {
  padding: 8px 16px;
  font-size: 14px;
  font-weight: 500;
  border-radius: 4px;
  border: none;
  cursor: pointer;
  transition: all 0.15s;
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--primary);
  color: white;
}

.btn-primary:hover:not(:disabled) {
  opacity: 0.9;
}

.btn-ghost {
  background: transparent;
  color: var(--muted);
  border: 1px solid var(--border);
}

.btn-ghost:hover:not(:disabled) {
  background: var(--bg-hover);
}

.alert {
  padding: 12px 16px;
  border-radius: 6px;
  margin-bottom: 16px;
  font-size: 14px;
}

.alert-danger {
  background: var(--danger-bg, #fee);
  color: var(--danger, #c00);
  border: 1px solid var(--danger, #c00);
}
</style>
