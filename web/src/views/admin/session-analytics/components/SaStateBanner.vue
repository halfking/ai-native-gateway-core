<script setup lang="ts">
// SaStateBanner.vue — 会话分析页面的状态横幅（2026-11-20）
//
// 三种语义，各自对应用户该做什么：
//   · error  —— 加载失败，文案自带恢复动作（「请重试」）
//   · stale  —— 聚合视图超出新鲜度预算，服务端主动拒绝返回过期数字
//                （**不是**故障；再等一轮刷新即可）
//   · info   —— 纯说明

const props = withDefaults(
  defineProps<{
    kind: 'error' | 'stale' | 'info'
    title: string
    reason?: string
    /** 操作按钮文案（已翻译）；给了才渲染按钮 */
    actionLabel?: string
  }>(),
  { reason: '', actionLabel: '' },
)

const emit = defineEmits<{ (e: 'action'): void }>()
</script>

<template>
  <div class="sa-banner" :class="`sa-banner--${props.kind}`" role="status">
    <div class="sa-banner__body">
      <strong class="sa-banner__title">{{ title }}</strong>
      <span v-if="reason" class="sa-banner__reason">{{ reason }}</span>
    </div>
    <button v-if="actionLabel" class="sa-banner__action" type="button" @click="emit('action')">
      {{ actionLabel }}
    </button>
  </div>
</template>

<style scoped>
.sa-banner {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface-soft, var(--surface));
  margin-bottom: 16px;
}

.sa-banner--error {
  border-color: var(--danger);
  background: var(--danger-soft, transparent);
}

.sa-banner--stale {
  border-color: var(--warning);
  background: var(--warning-soft, transparent);
}

.sa-banner--info {
  border-color: var(--border);
}

.sa-banner__body {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sa-banner__title {
  color: var(--text);
  font-size: 13px;
}

.sa-banner__reason {
  color: var(--muted);
  font-size: 12px;
  line-height: 1.5;
}

.sa-banner__action {
  flex: none;
  padding: 4px 12px;
  border-radius: var(--radius-card, 8px);
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  font-size: 12px;
  cursor: pointer;
}

.sa-banner__action:hover {
  background: var(--bg-hover, var(--surface-soft));
}

@media (max-width: 768px) {
  .sa-banner {
    flex-direction: column;
  }
}
</style>
