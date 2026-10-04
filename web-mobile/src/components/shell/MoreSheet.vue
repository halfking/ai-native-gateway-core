<script setup lang="ts">
// MoreSheet.vue — 「更多」抽屉（底栏第 5 席：告警/用量；账户在顶栏头像，02 §4/§5）。
import { useRouter } from 'vue-router'
import Sheet from '../ui/Sheet.vue'
import Icon from '../Icon.vue'
import { t } from '../../i18n'

const emit = defineEmits<{ (e: 'close'): void }>()

const router = useRouter()

const items = [
  { key: '/alerts', icon: 'alerts', label: t('nav.alerts') },
  { key: '/usage', icon: 'usage', label: t('nav.usage') },
]

function go(path: string): void {
  emit('close')
  router.push(path)
}
</script>

<template>
  <Sheet :title="t('nav.more')" @close="emit('close')">
    <div class="more-list">
      <button v-for="item in items" :key="item.key" type="button" class="more-item" @click="go(item.key)">
        <Icon :name="item.icon" />
        <span>{{ item.label }}</span>
        <Icon name="chevron" :size="18" />
      </button>
    </div>
  </Sheet>
</template>

<style scoped>
.more-list {
  display: grid;
  gap: var(--app-space-2);
}

.more-item {
  display: flex;
  align-items: center;
  gap: var(--app-space-3);
  min-height: 52px;
  padding: 0 var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border-subtle);
  border-radius: var(--app-radius-lg);
  color: var(--app-text);
  font-size: 0.9375rem;
}

.more-item span {
  flex: 1;
  text-align: left;
}

.more-item svg:last-child {
  color: var(--app-text-muted);
}
</style>
