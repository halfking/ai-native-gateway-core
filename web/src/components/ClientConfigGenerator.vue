<script setup lang="ts">
import { ref } from 'vue'
import { TOOLS, type ToolId } from '../composables/useClientConfig'
import ClientConfigDialog from './ClientConfigDialog.vue'

const emit = defineEmits<{ (e: 'openDialog', tool: ToolId): void }>()

const dialogTool = ref<ToolId | null>(null)
const dialogOpen = ref(false)

function openDialog(tool: ToolId) {
  dialogTool.value = tool
  dialogOpen.value = true
}
</script>

<template>
  <div class="tool-grid">
    <div v-for="tool in TOOLS" :key="tool.id" class="tool-card">
      <div class="tool-card-header">
        <span class="tool-icon">{{ tool.icon }}</span>
        <div class="tool-title">
          <span class="tool-name">{{ tool.name }}</span>
          <span class="tool-desc">{{ tool.description }}</span>
        </div>
      </div>
      <div class="tool-card-actions">
        <button class="btn btn-primary btn-sm" @click="openDialog(tool.id)">
          配置
        </button>
      </div>
    </div>
  </div>

  <ClientConfigDialog
    v-if="dialogOpen && dialogTool"
    :tool="dialogTool"
    :open="dialogOpen"
    @close="dialogOpen = false"
  />
</template>

<style scoped>
.tool-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  gap: 12px;
  margin-bottom: 24px;
}

.tool-card {
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  transition: border-color 0.15s;
}

.tool-card:hover {
  border-color: rgba(99, 102, 241, 0.4);
}

.tool-card-header {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}

.tool-icon {
  font-size: 22px;
  line-height: 1;
  flex-shrink: 0;
  margin-top: 2px;
}

.tool-title {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.tool-name {
  font-weight: 600;
  font-size: 14px;
  color: var(--text);
}

.tool-desc {
  font-size: 12px;
  color: var(--muted);
  line-height: 1.5;
}

.tool-card-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
