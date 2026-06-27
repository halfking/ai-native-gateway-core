<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { changeMyPassword } from '../api'
import { checkPasswordPolicy } from '../utils/passwordPolicy'

const props = defineProps<{
  modelValue: boolean
  forced?: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: boolean]
  success: []
}>()

const oldPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const error = ref('')
const passwordPolicy = computed(() => checkPasswordPolicy(newPassword.value))
const confirmMatches = computed(() => !confirmPassword.value || newPassword.value === confirmPassword.value)
const canSubmit = computed(() => {
  return !!oldPassword.value && !!newPassword.value && !!confirmPassword.value && passwordPolicy.value.valid && confirmMatches.value && !loading.value
})

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      oldPassword.value = ''
      newPassword.value = ''
      confirmPassword.value = ''
      error.value = ''
    }
  },
)

function close() {
  if (props.forced) return
  emit('update:modelValue', false)
}

async function submit() {
  error.value = ''
  if (!oldPassword.value || !newPassword.value || !confirmPassword.value) {
    error.value = '请完整填写当前密码、新密码和确认密码'
    return
  }
  if (!passwordPolicy.value.valid) {
    error.value = '新密码不符合复杂度要求'
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    error.value = '两次输入的新密码不一致'
    return
  }

  loading.value = true
  try {
    await changeMyPassword(oldPassword.value, newPassword.value)
    emit('success')
    close()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '修改密码失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="modelValue"
      class="modal-overlay"
      role="presentation"
      @click.self="close"
    >
      <div
        class="password-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="change-password-title"
        @click.stop
      >
        <div class="password-modal__header">
          <div>
            <h2 id="change-password-title">修改密码</h2>
            <p class="password-modal__subtitle">
              {{ forced ? '首次登录需要先修改密码后才能继续使用' : '修改当前登录账号的密码' }}
            </p>
          </div>
          <button v-if="!forced" type="button" class="btn btn-ghost btn-sm password-modal__close" aria-label="关闭" @click="close">
            ✕
          </button>
        </div>

        <div v-if="error" class="alert alert-danger">{{ error }}</div>

        <form @submit.prevent="submit">
          <div class="form-group">
            <label for="current-password">当前密码</label>
            <input
              id="current-password"
              v-model="oldPassword"
              type="password"
              autocomplete="current-password"
              placeholder="请输入当前密码"
            />
          </div>
          <div class="form-group">
            <label for="new-password">新密码</label>
            <input
              id="new-password"
              v-model="newPassword"
              type="password"
              autocomplete="new-password"
              placeholder="至少8位，包含大小写字母和数字"
            />
            <div class="password-policy">
              <div
                v-for="item in passwordPolicy.requirements"
                :key="item.key"
                class="password-policy__item"
                :class="item.passed ? 'is-pass' : 'is-pending'"
              >
                {{ item.passed ? '✓' : '○' }} {{ item.label }}
              </div>
            </div>
          </div>
          <div class="form-group">
            <label for="confirm-password">确认新密码</label>
            <input
              id="confirm-password"
              v-model="confirmPassword"
              type="password"
              autocomplete="new-password"
              placeholder="再次输入新密码"
            />
            <div v-if="confirmPassword" class="password-confirm" :class="confirmMatches ? 'is-pass' : 'is-error'">
              {{ confirmMatches ? '✓ 两次输入一致' : '✕ 两次输入的新密码不一致' }}
            </div>
          </div>

          <div class="password-modal__hint">
            密码规则：至少 8 位，且包含大写字母、小写字母和数字。
          </div>

          <div class="password-modal__actions">
            <button v-if="!forced" type="button" class="btn btn-ghost" @click="close">取消</button>
            <button class="btn btn-primary" type="submit" :disabled="!canSubmit">
              {{ loading ? '提交中…' : '确认修改' }}
            </button>
          </div>
        </form>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.password-modal {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 24px;
  width: min(420px, calc(100vw - 32px));
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.45);
}

.password-modal__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 20px;
}

.password-modal__header h2 {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
}

.password-modal__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--muted);
}

.password-modal__close {
  flex-shrink: 0;
  min-width: 32px;
  padding: 4px 8px;
}

.password-modal__hint {
  margin-top: 4px;
  color: var(--muted);
  font-size: 12px;
  line-height: 1.5;
}

.password-policy {
  display: grid;
  gap: 6px;
  margin-top: 10px;
}

.password-policy__item,
.password-confirm {
  font-size: 12px;
  line-height: 1.4;
}

.is-pass {
  color: #4ade80;
}

.is-pending {
  color: var(--muted);
}

.is-error {
  color: #f87171;
}

.password-confirm {
  margin-top: 8px;
}

.password-modal__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>
