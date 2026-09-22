<script setup lang="ts">
import type { WaterfallRequest } from '../api/dispatch'
import WaterfallRequestDetailContent from './detail/WaterfallRequestDetailContent.vue'

const props = defineProps<{
  selected: WaterfallRequest
}>()

const emit = defineEmits<{
  close: []
  'open-session': []
  'open-fullscreen': []
}>()
</script>

<template>
  <Teleport to="body">
    <div class="drawer-backdrop" @click="emit('close')">
      <div class="drawer-panel card drawer-panel-wide dwf-detail" @click.stop>
        <div class="drawer-header">
          <div>
            <h3 style="margin: 0">请求详情</h3>
          </div>
          <div class="actions">
            <button
              class="btn btn-sm"
              type="button"
              @click="emit('open-fullscreen')"
            >全屏详情</button>
            <button
              v-if="selected.session_id"
              class="btn btn-sm"
              type="button"
              @click="emit('open-session')"
            >打开会话</button>
            <button class="btn btn-sm" type="button" @click="emit('close')">关闭</button>
          </div>
        </div>

        <WaterfallRequestDetailContent
          :request="selected"
          show-request-summary
        />
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.actions { display: flex; gap: 8px; }
</style>
