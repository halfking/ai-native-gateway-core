<script setup lang="ts">
import { ref, computed } from 'vue'
import type { Approver } from '../api/approval'

const props = defineProps<{
  modelValue: Approver[]
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Approver[]]
}>()

const showDialog = ref(false)
const editingIndex = ref<number | null>(null)
const formData = ref<Approver>({
  name: '',
  email: '',
  role: '',
  priority: 0,
  enabled: true,
})

const formErrors = ref<Record<string, string>>({})

const approvers = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

function validateEmail(email: string): boolean {
  const re = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
  return re.test(email)
}

function validateForm(): boolean {
  formErrors.value = {}
  
  if (!formData.value.name.trim()) {
    formErrors.value.name = '姓名不能为空'
  }
  
  if (!formData.value.email.trim()) {
    formErrors.value.email = '邮箱不能为空'
  } else if (!validateEmail(formData.value.email)) {
    formErrors.value.email = '邮箱格式不正确'
  }
  
  if (!formData.value.role.trim()) {
    formErrors.value.role = '角色不能为空'
  }
  
  return Object.keys(formErrors.value).length === 0
}

function openAddDialog() {
  editingIndex.value = null
  formData.value = {
    name: '',
    email: '',
    role: '',
    priority: approvers.value.length,
    enabled: true,
  }
  formErrors.value = {}
  showDialog.value = true
}

function openEditDialog(index: number) {
  editingIndex.value = index
  formData.value = { ...approvers.value[index] }
  formErrors.value = {}
  showDialog.value = true
}

function saveApprover() {
  if (!validateForm()) return
  
  const list = [...approvers.value]
  if (editingIndex.value !== null) {
    list[editingIndex.value] = { ...formData.value }
  } else {
    list.push({ ...formData.value, id: Date.now() })
  }
  approvers.value = list
  showDialog.value = false
}

function removeApprover(index: number) {
  if (confirm('确认删除该审批人？')) {
    const list = [...approvers.value]
    list.splice(index, 1)
    // Reorder priorities
    list.forEach((a, i) => a.priority = i)
    approvers.value = list
  }
}

function toggleEnabled(index: number) {
  const list = [...approvers.value]
  list[index].enabled = !list[index].enabled
  approvers.value = list
}

function moveUp(index: number) {
  if (index === 0) return
  const list = [...approvers.value]
  ;[list[index - 1], list[index]] = [list[index], list[index - 1]]
  list.forEach((a, i) => a.priority = i)
  approvers.value = list
}

function moveDown(index: number) {
  if (index === approvers.value.length - 1) return
  const list = [...approvers.value]
  ;[list[index], list[index + 1]] = [list[index + 1], list[index]]
  list.forEach((a, i) => a.priority = i)
  approvers.value = list
}
</script>

<template>
  <div class="approver-manager">
    <div class="header">
      <h3>审批人列表</h3>
      <button class="btn btn-primary" @click="openAddDialog">
        <span>➕</span> 添加审批人
      </button>
    </div>

    <div v-if="!approvers.length" class="empty">
      暂无审批人，请添加
    </div>

    <div v-else class="approver-list">
      <div
        v-for="(approver, index) in approvers"
        :key="approver.id || index"
        class="approver-card"
        :class="{ disabled: !approver.enabled }"
      >
        <div class="approver-info">
          <div class="approver-header">
            <span class="approver-name">{{ approver.name }}</span>
            <span class="approver-role">{{ approver.role }}</span>
            <span class="priority-badge">优先级: {{ approver.priority + 1 }}</span>
          </div>
          <div class="approver-email">📧 {{ approver.email }}</div>
        </div>
        
        <div class="approver-actions">
          <button
            class="btn-icon"
            :class="{ active: approver.enabled }"
            @click="toggleEnabled(index)"
            :title="approver.enabled ? '禁用' : '启用'"
          >
            {{ approver.enabled ? '✓' : '✗' }}
          </button>
          <button
            class="btn-icon"
            @click="moveUp(index)"
            :disabled="index === 0"
            title="上移"
          >
            ↑
          </button>
          <button
            class="btn-icon"
            @click="moveDown(index)"
            :disabled="index === approvers.length - 1"
            title="下移"
          >
            ↓
          </button>
          <button
            class="btn-icon"
            @click="openEditDialog(index)"
            title="编辑"
          >
            ✏️
          </button>
          <button
            class="btn-icon btn-danger"
            @click="removeApprover(index)"
            title="删除"
          >
            🗑️
          </button>
        </div>
      </div>
    </div>

    <!-- Dialog -->
    <div v-if="showDialog" class="dialog-overlay" @click.self="showDialog = false">
      <div class="dialog">
        <div class="dialog-header">
          <h3>{{ editingIndex !== null ? '编辑审批人' : '添加审批人' }}</h3>
          <button class="btn-close" @click="showDialog = false">✕</button>
        </div>
        
        <div class="dialog-body">
          <div class="form-group">
            <label>姓名 <span class="required">*</span></label>
            <input
              v-model="formData.name"
              type="text"
              class="form-input"
              placeholder="请输入姓名"
              :class="{ error: formErrors.name }"
            />
            <span v-if="formErrors.name" class="error-message">{{ formErrors.name }}</span>
          </div>

          <div class="form-group">
            <label>邮箱 <span class="required">*</span></label>
            <input
              v-model="formData.email"
              type="email"
              class="form-input"
              placeholder="example@company.com"
              :class="{ error: formErrors.email }"
            />
            <span v-if="formErrors.email" class="error-message">{{ formErrors.email }}</span>
          </div>

          <div class="form-group">
            <label>角色 <span class="required">*</span></label>
            <input
              v-model="formData.role"
              type="text"
              class="form-input"
              placeholder="例如：技术主管、产品经理"
              :class="{ error: formErrors.role }"
            />
            <span v-if="formErrors.role" class="error-message">{{ formErrors.role }}</span>
          </div>

          <div class="form-group">
            <label class="checkbox-label">
              <input v-model="formData.enabled" type="checkbox" />
              <span>启用</span>
            </label>
          </div>
        </div>

        <div class="dialog-footer">
          <button class="btn btn-ghost" @click="showDialog = false">取消</button>
          <button class="btn btn-primary" @click="saveApprover">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.approver-manager {
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

.approver-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.approver-card {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 16px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  transition: all 0.2s;
}

.approver-card.disabled {
  opacity: 0.6;
}

.approver-card:hover {
  border-color: var(--accent, #6366f1);
}

.approver-info {
  flex: 1;
}

.approver-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.approver-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

.approver-role {
  font-size: 12px;
  padding: 2px 8px;
  background: rgba(99, 102, 241, 0.15);
  color: var(--accent-h, #818cf8);
  border-radius: 4px;
}

.priority-badge {
  font-size: 11px;
  padding: 2px 6px;
  background: rgba(139, 148, 158, 0.15);
  color: #8b949e;
  border-radius: 4px;
}

.approver-email {
  font-size: 12px;
  color: var(--text-secondary, #8b949e);
}

.approver-actions {
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
}

.form-group {
  margin-bottom: 16px;
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

.form-input.error {
  border-color: #f87171;
}

.error-message {
  display: block;
  margin-top: 4px;
  font-size: 12px;
  color: #f87171;
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
