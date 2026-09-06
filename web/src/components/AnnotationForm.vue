<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { PROVIDERS, VALID_REASONS, type AnnotationSample } from '../api/annotations'
import type { Provider, AnnotationReason } from '../api/annotations'

const { t } = useI18n()

const props = defineProps<{
  sample: AnnotationSample | null
  defaultAnnotator?: string
}>()

const emit = defineEmits<{
  submit: [data: {
    human_provider: string
    is_correct: boolean
    reason: string
    annotator: string
  }]
  cancel: []
}>()

const humanProvider = ref<string>('')
const isCorrect = ref<boolean>(true)
const reason = ref<string>('correct')
const annotator = ref<string>('')

const errors = ref<Record<string, string>>({})

// Initialize form when sample changes
watch(() => props.sample, (newSample) => {
  if (newSample) {
    // Pre-fill with auto provider if available
    humanProvider.value = newSample.human_provider || newSample.auto_provider || ''
    isCorrect.value = newSample.is_correct ?? true
    reason.value = newSample.reason || 'correct'
    annotator.value = newSample.annotator || props.defaultAnnotator || ''
  }
}, { immediate: true })

const isValid = computed(() => {
  return humanProvider.value && reason.value && annotator.value
})

function validate(): boolean {
  errors.value = {}
  
  if (!humanProvider.value) {
    errors.value.human_provider = t('annotation.form.errors.providerRequired')
  }
  
  if (!reason.value) {
    errors.value.reason = t('annotation.form.errors.reasonRequired')
  } else if (!VALID_REASONS.includes(reason.value as AnnotationReason)) {
    errors.value.reason = t('annotation.form.errors.reasonInvalid')
  }
  
  if (!annotator.value) {
    errors.value.annotator = t('annotation.form.errors.annotatorRequired')
  }
  
  return Object.keys(errors.value).length === 0
}

function handleSubmit() {
  if (!validate()) return
  
  emit('submit', {
    human_provider: humanProvider.value,
    is_correct: isCorrect.value,
    reason: reason.value,
    annotator: annotator.value,
  })
}

function handleCancel() {
  emit('cancel')
}
</script>

<template>
  <div class="annotation-form">
    <div v-if="sample" class="form-header">
      <h3>{{ t('annotation.form.title') }}</h3>
      <div class="sample-info">
        <span class="info-item">
          <span class="label">{{ t('annotation.form.requestId') }}:</span>
          <code>{{ sample.request_id }}</code>
        </span>
        <span class="info-item">
          <span class="label">{{ t('annotation.form.model') }}:</span>
          <span>{{ sample.model_name }}</span>
        </span>
        <span class="info-item">
          <span class="label">{{ t('annotation.form.autoProvider') }}:</span>
          <span class="badge badge-blue">{{ sample.auto_provider }}</span>
        </span>
        <span class="info-item">
          <span class="label">{{ t('annotation.form.confidence') }}:</span>
          <span :class="sample.confidence < 0.5 ? 'text-danger' : 'text-muted'">
            {{ (sample.confidence * 100).toFixed(1) }}%
          </span>
        </span>
      </div>
    </div>

    <div class="form-body">
      <div class="form-field">
        <label for="human-provider" class="form-label">
          {{ t('annotation.form.humanProvider') }}
          <span class="required">*</span>
        </label>
        <select
          id="human-provider"
          v-model="humanProvider"
          class="form-select"
          :class="{ 'is-invalid': errors.human_provider }"
        >
          <option value="">{{ t('annotation.form.selectProvider') }}</option>
          <option v-for="p in PROVIDERS" :key="p" :value="p">
            {{ p }}
          </option>
        </select>
        <span v-if="errors.human_provider" class="error-message">
          {{ errors.human_provider }}
        </span>
      </div>

      <div class="form-field">
        <label class="form-label">
          {{ t('annotation.form.isCorrect') }}
          <span class="required">*</span>
        </label>
        <div class="radio-group">
          <label class="radio-label">
            <input
              v-model="isCorrect"
              type="radio"
              name="is-correct"
              :value="true"
            />
            <span>{{ t('annotation.form.correct') }}</span>
          </label>
          <label class="radio-label">
            <input
              v-model="isCorrect"
              type="radio"
              name="is-correct"
              :value="false"
            />
            <span>{{ t('annotation.form.incorrect') }}</span>
          </label>
        </div>
      </div>

      <div class="form-field">
        <label for="reason" class="form-label">
          {{ t('annotation.form.reason') }}
          <span class="required">*</span>
        </label>
        <select
          id="reason"
          v-model="reason"
          class="form-select"
          :class="{ 'is-invalid': errors.reason }"
        >
          <option v-for="r in VALID_REASONS" :key="r" :value="r">
            {{ t(`annotation.reasons.${r}`) }}
          </option>
        </select>
        <span v-if="errors.reason" class="error-message">
          {{ errors.reason }}
        </span>
      </div>

      <div class="form-field">
        <label for="annotator" class="form-label">
          {{ t('annotation.form.annotator') }}
          <span class="required">*</span>
        </label>
        <input
          id="annotator"
          v-model="annotator"
          type="text"
          class="form-input"
          :class="{ 'is-invalid': errors.annotator }"
          :placeholder="t('annotation.form.annotatorPlaceholder')"
        />
        <span v-if="errors.annotator" class="error-message">
          {{ errors.annotator }}
        </span>
      </div>
    </div>

    <div class="form-actions">
      <button type="button" class="btn btn-ghost" @click="handleCancel">
        {{ t('annotation.form.cancel') }}
      </button>
      <button
        type="button"
        class="btn btn-primary"
        :disabled="!isValid"
        @click="handleSubmit"
      >
        {{ t('annotation.form.submit') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.annotation-form {
  display: flex;
  flex-direction: column;
  gap: 1.5rem;
}

.form-header {
  border-bottom: 1px solid var(--border);
  padding-bottom: 1rem;
}

.form-header h3 {
  margin: 0 0 1rem 0;
  font-size: 1.25rem;
  font-weight: 600;
}

.sample-info {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
  font-size: 0.875rem;
}

.info-item {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.info-item .label {
  color: var(--text-muted);
}

.info-item code {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  padding: 0.125rem 0.25rem;
  background: var(--bg-code);
  border-radius: 3px;
}

.form-body {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
}

.form-field {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.form-label {
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--text);
}

.required {
  color: var(--danger);
}

.form-select,
.form-input {
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.875rem;
  background: var(--bg);
  color: var(--text);
  transition: border-color 0.2s;
}

.form-select:focus,
.form-input:focus {
  outline: none;
  border-color: var(--primary);
}

.form-select.is-invalid,
.form-input.is-invalid {
  border-color: var(--danger);
}

.error-message {
  font-size: 0.8125rem;
  color: var(--danger);
}

.radio-group {
  display: flex;
  gap: 1.5rem;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  cursor: pointer;
  font-size: 0.875rem;
}

.radio-label input[type="radio"] {
  cursor: pointer;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
  padding-top: 1rem;
  border-top: 1px solid var(--border);
}

.text-danger {
  color: var(--danger);
}

.text-muted {
  color: var(--text-muted);
}

.badge {
  padding: 0.125rem 0.5rem;
  border-radius: 3px;
  font-size: 0.75rem;
  font-weight: 500;
}

.badge-blue {
  background: var(--primary-light);
  color: var(--primary);
}
</style>
