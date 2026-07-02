<script setup lang="ts">
import { ref } from 'vue'
import type { NotificationChannel } from '../api/approval'
import { testNotificationChannel } from '../api/approval'

const props = defineProps<{
  modelValue: {
    feishu?: NotificationChannel
    wecom?: NotificationChannel
    dingtalk?: NotificationChannel
  }
}>()

const emit = defineEmits<{
  'update:modelValue': [value: typeof props.modelValue]
}>()

const testing = ref<string | null>(null)
const testResults = ref<Record<string, { success: boolean; message: string }>>({})
const formErrors = ref<Record<string, string>>({})

function updateChannel(type: 'feishu' | 'wecom' | 'dingtalk', updates: Partial<NotificationChannel>) {
  const current = props.modelValue[type] || createDefaultChannel(type)
  emit('update:modelValue', {
    ...props.modelValue,
    [type]: { ...current, ...updates }
  })
}

function createDefaultChannel(type: 'feishu' | 'wecom' | 'dingtalk'): NotificationChannel {
  return {
    type,
    enabled: false,
    config: {}
  }
}

function validateUrl(url: string): boolean {
  try {
    new URL(url)
    return true
  } catch {
    return false
  }
}

function validateChannelConfig(channel: NotificationChannel): string | null {
  if (!channel.enabled) return null
  
  if (channel.type === 'feishu') {
    if (channel.config.webhook_url && !validateUrl(channel.config.webhook_url)) {
      return 'Webhook URL 格式不正确'
    }
    if (!channel.config.app_id && !channel.config.webhook_url) {
      return '请至少填写 App ID 或 Webhook URL'
    }
  }
  
  if (channel.type === 'wecom') {
    if (!channel.config.corp_id) return '企业 ID 不能为空'
    if (!channel.config.agent_id) return 'Agent ID 不能为空'
  }
  
  if (channel.type === 'dingtalk') {
    if (!channel.config.app_key) return 'App Key 不能为空'
  }
  
  return null
}

async function testChannel(type: 'feishu' | 'wecom' | 'dingtalk') {
  const channel = props.modelValue[type]
  if (!channel) return
  
  const error = validateChannelConfig(channel)
  if (error) {
    testResults.value[type] = { success: false, message: error }
    return
  }
  
  testing.value = type
  testResults.value[type] = { success: false, message: '测试中...' }
  
  try {
    const result = await testNotificationChannel(channel)
    testResults.value[type] = {
      success: result.status === 'success',
      message: result.message || '测试成功'
    }
  } catch (e: any) {
    testResults.value[type] = {
      success: false,
      message: e.message || '测试失败'
    }
  } finally {
    testing.value = null
  }
}

const channelIcons = {
  feishu: '🦜',
  wecom: '💼',
  dingtalk: '📱'
}

const channelLabels = {
  feishu: '飞书',
  wecom: '企业微信',
  dingtalk: '钉钉'
}
</script>

<template>
  <div class="notification-channels">
    <h3 class="section-title">通知渠道配置</h3>
    
    <!-- 飞书 -->
    <div class="channel-card">
      <div class="channel-header">
        <div class="channel-title">
          <span class="channel-icon">{{ channelIcons.feishu }}</span>
          <span>{{ channelLabels.feishu }}</span>
        </div>
        <label class="switch-label">
          <input
            type="checkbox"
            :checked="modelValue.feishu?.enabled || false"
            @change="updateChannel('feishu', { enabled: ($event.target as HTMLInputElement).checked })"
            class="switch-input"
          />
          <span class="switch-track"></span>
        </label>
      </div>
      
      <div v-if="modelValue.feishu?.enabled" class="channel-body">
        <div class="form-group">
          <label>App ID</label>
          <input
            type="text"
            class="form-input"
            placeholder="cli_xxxxxxxxxxxxxxxx"
            :value="modelValue.feishu?.config.app_id || ''"
            @input="updateChannel('feishu', { config: { ...modelValue.feishu?.config, app_id: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="form-group">
          <label>App Secret</label>
          <input
            type="password"
            class="form-input"
            placeholder="••••••••••••••••"
            :value="modelValue.feishu?.config.app_secret || ''"
            @input="updateChannel('feishu', { config: { ...modelValue.feishu?.config, app_secret: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="form-group">
          <label>Webhook URL <span class="optional">(可选)</span></label>
          <input
            type="text"
            class="form-input"
            placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..."
            :value="modelValue.feishu?.config.webhook_url || ''"
            @input="updateChannel('feishu', { config: { ...modelValue.feishu?.config, webhook_url: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="channel-actions">
          <button
            class="btn btn-primary btn-sm"
            @click="testChannel('feishu')"
            :disabled="testing === 'feishu'"
          >
            {{ testing === 'feishu' ? '测试中...' : '测试连接' }}
          </button>
          <div v-if="testResults.feishu" class="test-result" :class="{ success: testResults.feishu.success, error: !testResults.feishu.success }">
            {{ testResults.feishu.message }}
          </div>
        </div>
      </div>
    </div>
    
    <!-- 企业微信 -->
    <div class="channel-card">
      <div class="channel-header">
        <div class="channel-title">
          <span class="channel-icon">{{ channelIcons.wecom }}</span>
          <span>{{ channelLabels.wecom }}</span>
        </div>
        <label class="switch-label">
          <input
            type="checkbox"
            :checked="modelValue.wecom?.enabled || false"
            @change="updateChannel('wecom', { enabled: ($event.target as HTMLInputElement).checked })"
            class="switch-input"
          />
          <span class="switch-track"></span>
        </label>
      </div>
      
      <div v-if="modelValue.wecom?.enabled" class="channel-body">
        <div class="form-group">
          <label>Corp ID <span class="required">*</span></label>
          <input
            type="text"
            class="form-input"
            placeholder="ww1234567890abcdef"
            :value="modelValue.wecom?.config.corp_id || ''"
            @input="updateChannel('wecom', { config: { ...modelValue.wecom?.config, corp_id: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="form-group">
          <label>Corp Secret</label>
          <input
            type="password"
            class="form-input"
            placeholder="••••••••••••••••"
            :value="modelValue.wecom?.config.corp_secret || ''"
            @input="updateChannel('wecom', { config: { ...modelValue.wecom?.config, corp_secret: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="form-group">
          <label>Agent ID <span class="required">*</span></label>
          <input
            type="text"
            class="form-input"
            placeholder="1000002"
            :value="modelValue.wecom?.config.agent_id || ''"
            @input="updateChannel('wecom', { config: { ...modelValue.wecom?.config, agent_id: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="channel-actions">
          <button
            class="btn btn-primary btn-sm"
            @click="testChannel('wecom')"
            :disabled="testing === 'wecom'"
          >
            {{ testing === 'wecom' ? '测试中...' : '测试连接' }}
          </button>
          <div v-if="testResults.wecom" class="test-result" :class="{ success: testResults.wecom.success, error: !testResults.wecom.success }">
            {{ testResults.wecom.message }}
          </div>
        </div>
      </div>
    </div>
    
    <!-- 钉钉 -->
    <div class="channel-card">
      <div class="channel-header">
        <div class="channel-title">
          <span class="channel-icon">{{ channelIcons.dingtalk }}</span>
          <span>{{ channelLabels.dingtalk }}</span>
        </div>
        <label class="switch-label">
          <input
            type="checkbox"
            :checked="modelValue.dingtalk?.enabled || false"
            @change="updateChannel('dingtalk', { enabled: ($event.target as HTMLInputElement).checked })"
            class="switch-input"
          />
          <span class="switch-track"></span>
        </label>
      </div>
      
      <div v-if="modelValue.dingtalk?.enabled" class="channel-body">
        <div class="form-group">
          <label>App Key <span class="required">*</span></label>
          <input
            type="text"
            class="form-input"
            placeholder="dingxxxxxxxxxxxxxxx"
            :value="modelValue.dingtalk?.config.app_key || ''"
            @input="updateChannel('dingtalk', { config: { ...modelValue.dingtalk?.config, app_key: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="form-group">
          <label>App Secret</label>
          <input
            type="password"
            class="form-input"
            placeholder="••••••••••••••••"
            :value="modelValue.dingtalk?.config.app_secret || ''"
            @input="updateChannel('dingtalk', { config: { ...modelValue.dingtalk?.config, app_secret: ($event.target as HTMLInputElement).value } })"
          />
        </div>
        
        <div class="channel-actions">
          <button
            class="btn btn-primary btn-sm"
            @click="testChannel('dingtalk')"
            :disabled="testing === 'dingtalk'"
          >
            {{ testing === 'dingtalk' ? '测试中...' : '测试连接' }}
          </button>
          <div v-if="testResults.dingtalk" class="test-result" :class="{ success: testResults.dingtalk.success, error: !testResults.dingtalk.success }">
            {{ testResults.dingtalk.message }}
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.notification-channels {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.section-title {
  margin: 0 0 8px;
  font-size: 16px;
  color: var(--text-primary, #e6edf3);
}

.channel-card {
  padding: 16px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
}

.channel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.channel-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

.channel-icon {
  font-size: 20px;
}

.switch-label {
  display: flex;
  align-items: center;
  cursor: pointer;
  user-select: none;
}

.switch-input {
  position: absolute;
  opacity: 0;
  pointer-events: none;
}

.switch-track {
  position: relative;
  width: 44px;
  height: 24px;
  background: var(--border, #30363d);
  border-radius: 12px;
  transition: background 0.2s;
}

.switch-track::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 20px;
  height: 20px;
  background: white;
  border-radius: 50%;
  transition: transform 0.2s;
}

.switch-input:checked + .switch-track {
  background: var(--accent, #6366f1);
}

.switch-input:checked + .switch-track::after {
  transform: translateX(20px);
}

.channel-body {
  padding-top: 12px;
  border-top: 1px solid var(--border, #30363d);
}

.form-group {
  margin-bottom: 12px;
}

.form-group:last-child {
  margin-bottom: 0;
}

.form-group label {
  display: block;
  margin-bottom: 6px;
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
}

.required {
  color: #f87171;
}

.optional {
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
}

.form-input {
  width: 100%;
  padding: 8px 12px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 14px;
}

.form-input:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}

.channel-actions {
  margin-top: 12px;
  display: flex;
  align-items: center;
  gap: 12px;
}

.btn {
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 13px;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.2s;
}

.btn-sm {
  padding: 6px 12px;
  font-size: 12px;
}

.btn-primary {
  background: var(--accent, #6366f1);
  color: #fff;
}

.btn-primary:hover:not(:disabled) {
  background: #5558e3;
}

.btn-primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.test-result {
  font-size: 12px;
  padding: 4px 8px;
  border-radius: 4px;
}

.test-result.success {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}

.test-result.error {
  background: rgba(248, 113, 113, 0.1);
  color: #f87171;
}
</style>
