<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ApprovalRule } from '../api/approval'

const { t } = useI18n()

const props = defineProps<{
  modelValue: ApprovalRule[]
}>()

const emit = defineEmits<{
  'update:modelValue': [value: ApprovalRule[]]
}>()

const showDialog = ref(false)
const editingIndex = ref<number | null>(null)
const formData = ref<ApprovalRule>({
  name: '',
  enabled: true,
  priority: 0,
  conditions: [],
  action: 'require_approval',
  risk_level: 'medium',
  description: '',
})

const draggedIndex = ref<number | null>(null)

const rules = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

const fieldKeys = ['model', 'tenant_id', 'api_key', 'prompt_tokens', 'estimated_cost', 'user_id', 'ip_address'] as const
const operatorKeys = ['equals', 'not_equals', 'contains', 'not_contains', 'greater_than', 'less_than', 'matches_regex'] as const
const actionKeys = ['require_approval', 'auto_approve', 'auto_reject'] as const
const riskKeys = ['low', 'medium', 'high', 'critical'] as const

const actionColors: Record<string, string> = {
  require_approval: '#f59e0b',
  auto_approve: '#10b981',
  auto_reject: '#ef4444',
}

const riskColors: Record<string, string> = {
  low: '#10b981',
  medium: '#f59e0b',
  high: '#f97316',
  critical: '#ef4444',
}

const fieldOptions = computed(() =>
  fieldKeys.map((value) => ({
    value,
    label: t(`approval.rulesEditor.fields.${value}`),
  })),
)

const operatorOptions = computed(() =>
  operatorKeys.map((value) => ({
    value,
    label: t(`approval.rulesEditor.operators.${value}`),
  })),
)

const actionOptions = computed(() =>
  actionKeys.map((value) => ({
    value,
    label: t(`approval.rulesEditor.actions.${value}`),
    color: actionColors[value],
  })),
)

const riskLevelOptions = computed(() =>
  riskKeys.map((value) => ({
    value,
    label: t(`approval.rulesEditor.riskLevels.${value}`),
    color: riskColors[value],
  })),
)

function openAddDialog() {
  editingIndex.value = null
  formData.value = {
    name: '',
    enabled: true,
    priority: rules.value.length,
    conditions: [{ field: 'model', operator: 'equals', value: '' }],
    action: 'require_approval',
    risk_level: 'medium',
    description: '',
  }
  showDialog.value = true
}

function openEditDialog(index: number) {
  editingIndex.value = index
  formData.value = JSON.parse(JSON.stringify(rules.value[index]))
  showDialog.value = true
}

function saveRule() {
  const list = [...rules.value]
  if (editingIndex.value !== null) {
    list[editingIndex.value] = { ...formData.value }
  } else {
    list.push({ ...formData.value, id: Date.now() })
  }
  rules.value = list
  showDialog.value = false
}

function removeRule(index: number) {
  if (confirm(t('approval.rulesEditor.confirmDelete'))) {
    const list = [...rules.value]
    list.splice(index, 1)
    list.forEach((r, i) => r.priority = i)
    rules.value = list
  }
}

function toggleEnabled(index: number) {
  const list = [...rules.value]
  list[index].enabled = !list[index].enabled
  rules.value = list
}

function addCondition() {
  formData.value.conditions.push({ field: 'model', operator: 'equals', value: '' })
}

function removeCondition(index: number) {
  formData.value.conditions.splice(index, 1)
}

function onDragStart(index: number) {
  draggedIndex.value = index
}

function onDragOver(event: DragEvent, index: number) {
  event.preventDefault()
  if (draggedIndex.value === null || draggedIndex.value === index) return
  
  const list = [...rules.value]
  const draggedItem = list[draggedIndex.value]
  list.splice(draggedIndex.value, 1)
  list.splice(index, 0, draggedItem)
  list.forEach((r, i) => r.priority = i)
  
  rules.value = list
  draggedIndex.value = index
}

function onDragEnd() {
  draggedIndex.value = null
}

function getRiskLevelColor(level: string): string {
  return riskColors[level] || '#8b949e'
}

function getActionColor(action: string): string {
  return actionColors[action] || '#8b949e'
}

function getActionLabel(action: string): string {
  return actionOptions.value.find(o => o.value === action)?.label || action
}

function getRiskLevelLabel(level: string): string {
  return riskLevelOptions.value.find(o => o.value === level)?.label || level
}
</script>

<template>
  <div class="approval-rules">
    <div class="header">
      <h3>{{ t('approval.rulesEditor.title') }}</h3>
      <button class="btn btn-primary" @click="openAddDialog">
        <span>➕</span> {{ t('approval.rulesEditor.add') }}
      </button>
    </div>

    <div v-if="!rules.length" class="empty">
      {{ t('approval.rulesEditor.empty') }}
    </div>

    <div v-else class="rules-list">
      <div
        v-for="(rule, index) in rules"
        :key="rule.id || index"
        class="rule-card"
        :class="{ disabled: !rule.enabled, dragging: draggedIndex === index }"
        draggable="true"
        @dragstart="onDragStart(index)"
        @dragover="onDragOver($event, index)"
        @dragend="onDragEnd"
      >
        <div class="rule-header">
          <div class="rule-drag-handle">☰</div>
          <div class="rule-info">
            <div class="rule-name">{{ rule.name }}</div>
            <div class="rule-meta">
              <span class="priority-badge">{{ t('approval.rulesEditor.priority', { n: rule.priority + 1 }) }}</span>
              <span class="risk-badge" :style="{ background: getRiskLevelColor(rule.risk_level) + '22', color: getRiskLevelColor(rule.risk_level) }">
                {{ getRiskLevelLabel(rule.risk_level) }}
              </span>
              <span class="action-badge" :style="{ background: getActionColor(rule.action) + '22', color: getActionColor(rule.action) }">
                {{ getActionLabel(rule.action) }}
              </span>
            </div>
          </div>
          <div class="rule-actions">
            <button
              class="btn-icon"
              :class="{ active: rule.enabled }"
              @click="toggleEnabled(index)"
              :title="rule.enabled ? t('approval.rulesEditor.disable') : t('approval.rulesEditor.enable')"
            >
              {{ rule.enabled ? '✓' : '✗' }}
            </button>
            <button class="btn-icon" @click="openEditDialog(index)" :title="t('approval.rulesEditor.edit')">
              ✏️
            </button>
            <button class="btn-icon btn-danger" @click="removeRule(index)" :title="t('approval.rulesEditor.delete')">
              🗑️
            </button>
          </div>
        </div>
        
        <div v-if="rule.description" class="rule-description">
          {{ rule.description }}
        </div>
        
        <div class="rule-conditions">
          <div class="conditions-label">{{ t('approval.rulesEditor.conditions') }}</div>
          <div class="conditions-list">
            <div v-for="(cond, ci) in rule.conditions" :key="ci" class="condition-item">
              <code>{{ fieldOptions.find(f => f.value === cond.field)?.label || cond.field }}</code>
              <span class="operator">{{ operatorOptions.find(o => o.value === cond.operator)?.label || cond.operator }}</span>
              <code class="value">{{ cond.value }}</code>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Dialog -->
    <div v-if="showDialog" class="dialog-overlay" @click.self="showDialog = false">
      <div class="dialog dialog-large">
        <div class="dialog-header">
          <h3>{{ editingIndex !== null ? t('approval.rulesEditor.editTitle') : t('approval.rulesEditor.addTitle') }}</h3>
          <button class="btn-close" @click="showDialog = false">✕</button>
        </div>
        
        <div class="dialog-body">
          <div class="form-group">
            <label>{{ t('approval.rulesEditor.form.name') }} <span class="required">*</span></label>
            <input
              v-model="formData.name"
              type="text"
              class="form-input"
              :placeholder="t('approval.rulesEditor.form.namePlaceholder')"
            />
          </div>

          <div class="form-group">
            <label>{{ t('approval.rulesEditor.form.description') }}</label>
            <textarea
              v-model="formData.description"
              class="form-textarea"
              rows="2"
              :placeholder="t('approval.rulesEditor.form.descriptionPlaceholder')"
            />
          </div>

          <div class="form-row">
            <div class="form-group">
              <label>{{ t('approval.rulesEditor.form.action') }} <span class="required">*</span></label>
              <select v-model="formData.action" class="form-select">
                <option v-for="opt in actionOptions" :key="opt.value" :value="opt.value">
                  {{ opt.label }}
                </option>
              </select>
            </div>

            <div class="form-group">
              <label>{{ t('approval.rulesEditor.form.riskLevel') }} <span class="required">*</span></label>
              <select v-model="formData.risk_level" class="form-select">
                <option v-for="opt in riskLevelOptions" :key="opt.value" :value="opt.value">
                  {{ opt.label }}
                </option>
              </select>
            </div>
          </div>

          <div class="form-group">
            <label>{{ t('approval.rulesEditor.form.conditions') }} <span class="required">*</span></label>
            <div class="conditions-editor">
              <div
                v-for="(cond, index) in formData.conditions"
                :key="index"
                class="condition-row"
              >
                <select v-model="cond.field" class="form-select form-select-sm">
                  <option v-for="opt in fieldOptions" :key="opt.value" :value="opt.value">
                    {{ opt.label }}
                  </option>
                </select>
                
                <select v-model="cond.operator" class="form-select form-select-sm">
                  <option v-for="opt in operatorOptions" :key="opt.value" :value="opt.value">
                    {{ opt.label }}
                  </option>
                </select>
                
                <input
                  v-model="cond.value"
                  type="text"
                  class="form-input form-input-sm"
                  :placeholder="t('approval.rulesEditor.form.value')"
                />
                
                <button
                  class="btn-icon"
                  @click="removeCondition(index)"
                  :disabled="formData.conditions.length === 1"
                  :title="t('approval.rulesEditor.form.removeCondition')"
                >
                  ✕
                </button>
              </div>
              
              <button class="btn btn-ghost btn-sm" @click="addCondition">
                ➕ {{ t('approval.rulesEditor.form.addCondition') }}
              </button>
            </div>
          </div>

          <div class="form-group">
            <label class="checkbox-label">
              <input v-model="formData.enabled" type="checkbox" />
              <span>{{ t('approval.rulesEditor.form.enableRule') }}</span>
            </label>
          </div>
        </div>

        <div class="dialog-footer">
          <button class="btn btn-ghost" @click="showDialog = false">{{ t('approval.rulesEditor.cancel') }}</button>
          <button class="btn btn-primary" @click="saveRule">{{ t('approval.rulesEditor.save') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.approval-rules {
  padding: 16px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 16px;
}

.header h3 {
  margin: 0;
  font-size: 16px;
  color: var(--text-primary, #e6edf3);
}

.empty {
  text-align: center;
  padding: 32px;
  color: var(--text-secondary, #8b949e);
  font-size: 14px;
}

.rules-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.rule-card {
  padding: 12px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  cursor: move;
  transition: all 0.2s;
}

.rule-card.disabled {
  opacity: 0.6;
}

.rule-card:hover {
  border-color: var(--accent, #6366f1);
}

.rule-card.dragging {
  opacity: 0.5;
}

.rule-header {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.rule-drag-handle {
  font-size: 16px;
  color: var(--text-secondary, #8b949e);
  cursor: move;
  padding-top: 2px;
}

.rule-info {
  flex: 1;
}

.rule-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
  margin-bottom: 4px;
}

.rule-meta {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.priority-badge,
.risk-badge,
.action-badge {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
}

.priority-badge {
  background: rgba(139, 148, 158, 0.15);
  color: #8b949e;
}

.rule-description {
  margin-top: 8px;
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
  padding-left: 28px;
}

.rule-conditions {
  margin-top: 8px;
  padding-left: 28px;
  font-size: 12px;
}

.conditions-label {
  color: var(--text-secondary, #8b949e);
  margin-bottom: 4px;
}

.conditions-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.condition-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.condition-item code {
  padding: 2px 6px;
  background: rgba(99, 102, 241, 0.1);
  border-radius: 3px;
  font-size: 11px;
  font-family: ui-monospace, monospace;
  color: var(--accent-h, #818cf8);
}

.condition-item .operator {
  color: var(--text-secondary, #8b949e);
  font-size: 11px;
}

.condition-item .value {
  background: rgba(52, 211, 153, 0.1);
  color: #34d399;
}

.rule-actions {
  display: flex;
  gap: 4px;
}

.btn-icon {
  padding: 6px 10px;
  border: 1px solid var(--border, #30363d);
  background: transparent;
  color: var(--text-secondary, #8b949e);
  border-radius: 4px;
  cursor: pointer;
  font-size: 14px;
  transition: all 0.2s;
}

.btn-icon:hover:not(:disabled) {
  background: var(--bg-hover, #21262d);
  color: var(--text-primary, #e6edf3);
}

.btn-icon.active {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
  border-color: #34d399;
}

.btn-icon:disabled {
  opacity: 0.3;
  cursor: not-allowed;
}

.btn-icon.btn-danger:hover:not(:disabled) {
  background: rgba(248, 113, 113, 0.1);
  color: #f87171;
  border-color: rgba(248, 113, 113, 0.3);
}

/* Dialog styles */
.dialog-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.7);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.dialog {
  width: 90%;
  max-width: 500px;
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  max-height: 90vh;
  display: flex;
  flex-direction: column;
}

.dialog-large {
  max-width: 700px;
}

.dialog-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border, #30363d);
}

.dialog-header h3 {
  margin: 0;
  font-size: 16px;
  color: var(--text-primary, #e6edf3);
}

.btn-close {
  padding: 4px 8px;
  border: none;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  cursor: pointer;
  font-size: 20px;
}

.btn-close:hover {
  color: var(--text-primary, #e6edf3);
}

.dialog-body {
  padding: 20px;
  overflow-y: auto;
}

.form-group {
  margin-bottom: 16px;
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
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

.form-input,
.form-select,
.form-textarea {
  width: 100%;
  padding: 8px 12px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 14px;
}

.form-input-sm,
.form-select-sm {
  padding: 6px 10px;
  font-size: 13px;
}

.form-input:focus,
.form-select:focus,
.form-textarea:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}

.form-textarea {
  resize: vertical;
  font-family: inherit;
}

.conditions-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.condition-row {
  display: grid;
  grid-template-columns: 1.5fr 1fr 2fr auto;
  gap: 8px;
  align-items: center;
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.checkbox-label input[type="checkbox"] {
  width: 16px;
  height: 16px;
  cursor: pointer;
}

.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 16px 20px;
  border-top: 1px solid var(--border, #30363d);
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

.btn-primary:hover {
  background: #5558e3;
}

.btn-ghost {
  background: transparent;
  color: var(--text-primary, #e6edf3);
  border: 1px solid var(--border, #30363d);
}

.btn-ghost:hover {
  background: var(--bg-hover, #21262d);
}
</style>
