<script setup lang="ts">
import { ref, watch } from 'vue'
import { getCaptcha } from '../api/auth'
import { useI18n } from 'vue-i18n'
import { Refresh } from '@element-plus/icons-vue'

const { t } = useI18n()

interface CaptchaData {
  captcha_id: string
  image: string
  expires_at: string
  required: boolean
  ttl_seconds: number
}

const props = defineProps<{
  modelValue: string
  captchaId: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:captchaId': [value: string]
}>()

const captchaData = ref<CaptchaData | null>(null)
const loading = ref(false)
const error = ref('')

const loadCaptcha = async () => {
  loading.value = true
  error.value = ''
  
  try {
    const data = await getCaptcha()
    captchaData.value = data
    emit('update:captchaId', data.captcha_id)
    emit('update:modelValue', '')
  } catch (err: any) {
    error.value = err.message || t('login.captcha.loadFailed')
  } finally {
    loading.value = false
  }
}

// 初始加载
loadCaptcha()

// 监听外部请求刷新（例如登录失败后）
defineExpose({
  refresh: loadCaptcha
})
</script>

<template>
  <div class="captcha-input">
    <label for="captcha-code">{{ t('login.captcha.label') }}</label>
    
    <div v-if="error" class="captcha-error">
      {{ error }}
      <button type="button" class="btn btn-sm btn-ghost" @click="loadCaptcha">
        {{ t('common.button.retry') }}
      </button>
    </div>
    
    <div v-else class="captcha-container">
      <div class="captcha-image-wrapper">
        <img 
          v-if="captchaData" 
          :src="captchaData.image" 
          :alt="t('login.captcha.imageAlt')"
          class="captcha-image"
        />
        <div v-else class="captcha-loading">
          {{ t('common.loading') }}
        </div>
        
        <button 
          type="button" 
          class="btn btn-sm btn-ghost captcha-refresh"
          :disabled="loading"
          @click="loadCaptcha"
          :title="t('login.captcha.refresh')"
        >
          <Refresh />
        </button>
      </div>
      
      <input
        id="captcha-code"
        :value="modelValue"
        type="text"
        :placeholder="t('login.captcha.placeholder')"
        autocomplete="off"
        maxlength="6"
        @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
      />
    </div>
    
    <p v-if="captchaData" class="captcha-hint">
      {{ t('login.captcha.hint', { seconds: captchaData.ttl_seconds }) }}
    </p>
  </div>
</template>

<style scoped>
.captcha-input {
  margin-top: 12px;
}

.captcha-input label {
  display: block;
  margin-bottom: 4px;
  font-size: 14px;
  font-weight: 500;
}

.captcha-error {
  padding: 8px 12px;
  background: var(--danger-bg, #fee);
  border: 1px solid var(--danger, #d32f2f);
  border-radius: 4px;
  color: var(--danger, #d32f2f);
  font-size: 13px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.captcha-container {
  display: flex;
  gap: 8px;
  align-items: flex-start;
}

.captcha-image-wrapper {
  position: relative;
  flex-shrink: 0;
  width: 120px;
  height: 40px;
  border: 1px solid var(--border, #ddd);
  border-radius: 4px;
  overflow: hidden;
  background: var(--bg-secondary, #f5f5f5);
}

.captcha-image {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.captcha-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  font-size: 12px;
  color: var(--muted, #666);
}

.captcha-refresh {
  position: absolute;
  top: 2px;
  right: 2px;
  min-width: 24px;
  padding: 2px 4px;
  background: rgba(255, 255, 255, 0.9);
  backdrop-filter: blur(4px);
}

.captcha-refresh:hover {
  background: rgba(255, 255, 255, 1);
}

.captcha-container input {
  flex: 1;
  min-width: 0;
}

.captcha-hint {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--muted, #666);
}
</style>
