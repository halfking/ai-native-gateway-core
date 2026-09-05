<script setup lang="ts">
defineProps<{
  loading: boolean
}>()

const limit = defineModel<number>('limit', { required: true })
const modelFilter = defineModel<string>('modelFilter', { required: true })
const credentialFilter = defineModel<string>('credentialFilter', { required: true })
const autoRefresh = defineModel<boolean>('autoRefresh', { required: true })

const emit = defineEmits<{
  load: []
  'poll-change': []
}>()
</script>

<template>
  <div class="dw-actions">
    <label class="field">
      <span>Limit</span>
      <select v-model.number="limit" @change="emit('load')">
        <option :value="20">20</option>
        <option :value="50">50</option>
        <option :value="100">100</option>
        <option :value="200">200</option>
      </select>
    </label>
    <label class="field">
      <span>Model</span>
      <input v-model="modelFilter" placeholder="可选筛选" @keyup.enter="emit('load')" />
    </label>
    <label class="field">
      <span>Credential</span>
      <input
        v-model="credentialFilter"
        inputmode="numeric"
        placeholder="credential_id"
        @keyup.enter="emit('load')"
      />
    </label>
    <label class="check">
      <input v-model="autoRefresh" type="checkbox" @change="emit('poll-change')" />
      自动刷新 5s
    </label>
    <button class="btn" :disabled="loading" @click="emit('load')">刷新</button>
  </div>
</template>

<style scoped>
.dw-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
}
.field {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--kx-muted);
}
.field input, .field select {
  border: 1px solid var(--kx-border);
  background: var(--kx-surface);
  color: var(--kx-text);
  border-radius: 6px;
  padding: 4px 8px;
  min-width: 100px;
}
.check {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  font-size: 12px;
  color: var(--kx-muted);
}
.btn {
  border: 1px solid var(--kx-primary);
  background: var(--kx-primary);
  color: var(--on-primary);
  border-radius: 6px;
  padding: 6px 12px;
  cursor: pointer;
  font-size: 13px;
}
.btn:disabled { opacity: 0.6; cursor: not-allowed; }
</style>
