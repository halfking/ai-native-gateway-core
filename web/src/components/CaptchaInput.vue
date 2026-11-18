<script setup lang="ts">
import { watch } from 'vue'
import type { CaptchaResponse } from '../api/auth'
import { useI18n } from 'vue-i18n'
import { Refresh } from '@element-plus/icons-vue'

const { t } = useI18n()

// ★ 2026-11-17：验证码的**唯一持有者是父组件（LoginModal）**。
//
//   此前本组件自己 getCaptcha()，父组件也自己 getCaptcha() ⇒ 两个请求、
//   两个 captcha_id ⇒ 谁后到谁覆盖 ⇒ **图是 A 的、id 是 B 的** ⇒
//   服务端按 B 校验 ⇒ captcha_invalid（且一次性作废）⇒ 用户反复失败 ⇒ 锁死。
//
//   现在：父组件取一次（id 与图同源），本组件只渲染图、只发「要刷新」的意图。
//   ★ 这样 captcha_id 与图**永远来自同一次响应** ⇒ 该形态被从结构上消除。
const props = defineProps<{
  modelValue: string
  captcha: CaptchaResponse | null
  loading?: boolean
  error?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'refresh': []
}>()

// 换了一张验证码 ⇒ 上一次作答作废（否则用户会把旧答案提交到新图上）。
watch(
  () => props.captcha?.captcha_id,
  () => {
    emit('update:modelValue', '')
  },
)
</script>

<template>
  <div class="captcha-input">
    <label for="captcha-code">{{ t('login.captcha.label') }}</label>
    
    <div v-if="error" class="captcha-error">
      {{ error }}
      <button type="button" class="btn btn-sm btn-ghost" @click="emit('refresh')">
        {{ t('common.button.retry') }}
      </button>
    </div>

    <div v-else class="captcha-container">
      <div class="captcha-image-wrapper">
        <img
          v-if="captcha"
          :src="captcha.image"
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
          @click="emit('refresh')"
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

    <p v-if="captcha" class="captcha-hint">
      {{ t('login.captcha.hint') }}
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
