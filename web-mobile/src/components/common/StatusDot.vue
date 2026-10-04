<script setup lang="ts">
// StatusDot — 状态点（永不只用颜色区分：配套文字由调用方给出）。
withDefaults(defineProps<{ tone: 'success' | 'warning' | 'danger' | 'info' | 'muted'; pulse?: boolean }>(), {
  pulse: false,
})
</script>

<template>
  <span class="status-dot" :class="`status-dot--${tone}`" :data-pulse="pulse ? 'on' : undefined" aria-hidden="true" />
</template>

<style scoped>
.status-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
  background: currentColor;
}

.status-dot--success {
  color: var(--app-success);
}

.status-dot--warning {
  color: var(--app-warning);
}

.status-dot--danger {
  color: var(--app-danger);
}

.status-dot--info {
  color: var(--app-info);
}

.status-dot--muted {
  color: var(--app-text-muted);
}

.status-dot[data-pulse='on'] {
  animation: status-pulse 1.6s ease infinite;
}

@keyframes status-pulse {
  50% {
    opacity: 0.45;
  }
}

@media (prefers-reduced-motion: reduce) {
  .status-dot[data-pulse='on'] {
    animation: none;
  }
}
</style>
