<script setup lang="ts">
// AnnotationForm.vue — 首轮会话人工标注表单(2026-09-14 重构)。
//
// 标注语义升级:人工标注的 ground truth 是「任务类型 + 所选模型」
// (供应商不重要),由后端写入 training_human_annotations.annotation_metadata,
// 用于 auto 任务类型定位训练。是否正确/原因沿用原语义。
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { VALID_REASONS, type FirstTurnSample } from '../api/annotations'
import type { AnnotationReason } from '../api/annotations'

const { t } = useI18n()

const props = defineProps<{
  sample: FirstTurnSample | null
  defaultAnnotator?: string
  /** L1 task type options (key + display label). */
  taskTypes?: { key: string; label: string }[]
  /** Candidate model names for the "which model should have been picked" pick. */
  models?: string[]
}>()

const emit = defineEmits<{
  submit: [data: {
    task_type: string
    model: string
    human_provider: string
    is_correct: boolean
    reason: string
    annotator: string
  }]
  cancel: []
}>()

const taskType = ref<string>('')
const model = ref<string>('')
const isCorrect = ref<boolean>(true)
const reason = ref<string>('correct')
const annotator = ref<string>('')

const errors = ref<Record<string, string>>({})

const taskTypeChoices = computed<{ key: string; label: string }[]>(() =>
  props.taskTypes?.length ? props.taskTypes : [{ key: 'chat', label: 'chat' }],
)
const modelChoices = computed<string[]>(() => props.models ?? [])

// Initialize form when sample changes; prefill with the auto decision.
watch(() => props.sample, (newSample) => {
  if (newSample) {
    taskType.value = newSample.human_task_type || newSample.task_type || ''
    model.value = newSample.human_model || newSample.chosen_model || ''
    isCorrect.value = newSample.is_correct ?? true
    reason.value = newSample.reason || 'correct'
    annotator.value = newSample.annotator || props.defaultAnnotator || ''
  }
}, { immediate: true })

// 确认语义:人工选择的任务类型与模型都和 auto 决策一致 → 正确;否则视为纠正。
watch([taskType, model], ([tt, m]) => {
  if (!props.sample) return
  const matches = tt === props.sample.task_type && (!props.sample.chosen_model || m === props.sample.chosen_model)
  isCorrect.value = matches
})

const isValid = computed(() => {
  return !!taskType.value && !!model.value && !!reason.value && !!annotator.value
})

function validate(): boolean {
  errors.value = {}
  if (!taskType.value) errors.value.task_type = t('annotation.form.errors.taskTypeRequired')
  if (!model.value) errors.value.model = t('annotation.form.errors.modelRequired')
  if (!reason.value) {
    errors.value.reason = t('annotation.form.errors.reasonRequired')
  } else if (!VALID_REASONS.includes(reason.value as AnnotationReason)) {
    errors.value.reason = t('annotation.form.errors.reasonInvalid')
  }
  if (!annotator.value) errors.value.annotator = t('annotation.form.errors.annotatorRequired')
  return Object.keys(errors.value).length === 0
}

function handleSubmit() {
  if (!validate()) return
  emit('submit', {
    task_type: taskType.value,
    model: model.value,
    // 供应商不重要:model 兜底 human_label(后端同样兜底)。
    human_provider: model.value,
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
          <span class="label">{{ t('annotation.form.confidence') }}:</span>
          <span :class="(sample.confidence ?? 0) < 0.5 ? 'text-danger' : 'text-muted'">
            {{ sample.confidence != null ? (sample.confidence * 100).toFixed(1) + '%' : '-' }}
          </span>
        </span>
        <span class="info-item">
          <span class="label">{{ t('annotation.form.autoRoute') }}:</span>
          <span class="badge badge-blue">{{ sample.task_type }}</span>
          <code class="text-mono">{{ sample.chosen_model }}</code>
        </span>
      </div>
    </div>

    <div class="form-body">
      <div class="form-field">
        <label for="anno-task-type" class="form-label">
          {{ t('annotation.form.taskType') }}
          <span class="required">*</span>
        </label>
        <select
          id="anno-task-type"
          v-model="taskType"
          class="form-select"
          :class="{ 'is-invalid': errors.task_type }"
        >
          <option value="">{{ t('annotation.form.selectTaskType') }}</option>
          <option v-for="tt in taskTypeChoices" :key="tt.key" :value="tt.key">
            {{ tt.label }}
          </option>
          <option v-if="taskType && !taskTypeChoices.some(x => x.key === taskType)" :value="taskType">
            {{ taskType }}
          </option>
        </select>
        <span v-if="errors.task_type" class="error-message">{{ errors.task_type }}</span>
      </div>

      <div class="form-field">
        <label for="anno-model" class="form-label">
          {{ t('annotation.form.model') }}
          <span class="required">*</span>
        </label>
        <select
          id="anno-model"
          v-model="model"
          class="form-select"
          :class="{ 'is-invalid': errors.model }"
        >
          <option value="">{{ t('annotation.form.selectModel') }}</option>
          <option v-for="m in modelChoices" :key="m" :value="m">{{ m }}</option>
          <option v-if="model && !modelChoices.includes(model)" :value="model">{{ model }}</option>
        </select>
        <span v-if="errors.model" class="error-message">{{ errors.model }}</span>
      </div>

      <div class="form-field">
        <label class="form-label">
          {{ t('annotation.form.isCorrect') }}
          <span class="required">*</span>
        </label>
        <div class="radio-group">
          <label class="radio-label">
            <input v-model="isCorrect" type="radio" name="is-correct" :value="true" />
            <span>{{ t('annotation.form.correct') }}</span>
          </label>
          <label class="radio-label">
            <input v-model="isCorrect" type="radio" name="is-correct" :value="false" />
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
        <span v-if="errors.reason" class="error-message">{{ errors.reason }}</span>
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
        <span v-if="errors.annotator" class="error-message">{{ errors.annotator }}</span>
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

.text-mono {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  word-break: break-all;
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
  background: var(--info-bg);
  color: var(--accent);
}
</style>
