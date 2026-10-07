<script setup lang="ts">
// AppUpdateBanner — 部署序号更新提示条（docs/UI规范/18 §4「非阻塞提示条」）。
// 仅在「序号变化且不满足自动重载三条件」时出现；「不可判定」不出条
// （那由账户 Sheet 的版本区如实展示，不用横幅打扰）。
import { useDeploySeqUpdate } from '@/hyper/update/useDeploySeqUpdate'
import { t } from '@/i18n'

const { bannerVisible, dismissBanner, applyUpdate } = useDeploySeqUpdate()
</script>

<template>
  <Transition name="update-bar">
    <div v-if="bannerVisible" class="update-bar" role="status" aria-live="polite">
      <div class="update-bar__body">
        <p class="update-bar__title">{{ t('update.bannerTitle') }}</p>
        <p class="update-bar__hint">{{ t('update.bannerHint') }}</p>
      </div>
      <button type="button" class="update-bar__btn" @click="applyUpdate">
        {{ t('update.updateNow') }}
      </button>
      <button type="button" class="update-bar__later" :aria-label="t('update.later')" @click="dismissBanner">
        {{ t('update.later') }}
      </button>
    </div>
  </Transition>
</template>

<style scoped>
/* fixed 定位悬浮于内容之上、底栏之下（z-index 对齐 AppSheet 之下的浮层档） */
.update-bar {
  position: fixed;
  left: calc(var(--app-space-3) + var(--app-safe-left));
  right: calc(var(--app-space-3) + var(--app-safe-right));
  /* compact 有底栏时让位；medium+ 只有安全区 */
  bottom: calc(var(--app-bottomnav-height, 0px) + var(--app-safe-bottom, 0px) + var(--app-space-2));
  z-index: 60;
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  min-height: 56px;
  padding: var(--app-space-2) var(--app-space-3);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  box-shadow: var(--app-shadow-dialog);
}

.update-bar__body {
  flex: 1;
  min-width: 0;
}

.update-bar__title {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text);
}

.update-bar__hint {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.update-bar__btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border: none;
  border-radius: var(--app-radius);
  background: var(--app-primary);
  color: var(--app-on-primary);
  font-size: 0.8125rem;
  font-weight: 600;
}

.update-bar__btn:active {
  opacity: 0.85;
}

.update-bar__later {
  min-height: 48px;
  min-width: 48px;
  padding: 0 var(--app-space-2);
  border: none;
  background: transparent;
  color: var(--app-text-secondary);
  font-size: 0.8125rem;
}

.update-bar__later:active {
  opacity: 0.7;
}

.update-bar-enter-active,
.update-bar-leave-active {
  transition: opacity 200ms ease, transform 200ms ease;
}

.update-bar-enter-from,
.update-bar-leave-to {
  opacity: 0;
  transform: translateY(8px);
}

/* reduced-motion：不出动效，功能不变（12 §4） */
@media (prefers-reduced-motion: reduce) {
  .update-bar-enter-active,
  .update-bar-leave-active {
    transition: none;
  }
}
</style>
