<script setup lang="ts">
/**
 * DeploySeqUpdateBanner — 部署序号更新提示条（docs/UI规范/18 §4 非阻塞提示条；
 * §10.3 步骤 4：桌面侧只接 deploy-seq 检查，不接 SW）。
 *
 * 仅在「序号变化且不满足自动重载三条件」时出现（fixed 底部悬浮条，不挤压
 * 内容布局）；「不可判定」不出条——那由账户面板的版本区如实展示，
 * 不用横幅打扰。reduced-motion 下不出动效（规范 12 §4）。
 */
import { useI18n } from 'vue-i18n'
import { useDeploySeqUpdate } from '../../composables/useDeploySeqUpdate'

const { t } = useI18n()
const { bannerVisible, dismissBanner, applyUpdate } = useDeploySeqUpdate()
</script>

<template>
  <Transition name="deploy-seq-bar">
    <div
      v-if="bannerVisible"
      class="deploy-seq-banner"
      role="status"
      aria-live="polite"
      data-testid="deploy-seq-update-banner"
    >
      <div class="deploy-seq-banner__body">
        <p class="deploy-seq-banner__title">{{ t('app.deploySeq.bannerTitle') }}</p>
        <p class="deploy-seq-banner__hint">{{ t('app.deploySeq.bannerHint') }}</p>
      </div>
      <div class="deploy-seq-banner__actions">
        <button type="button" class="btn btn-sm btn-primary" @click="applyUpdate">
          {{ t('app.deploySeq.updateNow') }}
        </button>
        <button type="button" class="btn btn-sm btn-ghost" @click="dismissBanner">
          {{ t('app.deploySeq.later') }}
        </button>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
.deploy-seq-banner {
  position: fixed;
  left: 50%;
  transform: translateX(-50%);
  bottom: 24px;
  z-index: 2100; /* 悬浮于内容之上、弹窗(2000+)档之下 */
  display: flex;
  align-items: center;
  gap: 16px;
  max-width: min(560px, calc(100vw - 32px));
  padding: 10px 16px;
  border-radius: 8px;
  border: 1px solid var(--info-bd);
  background: var(--info-bg);
  color: var(--kx-text);
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.16);
  backdrop-filter: blur(6px);
}

.deploy-seq-banner__body {
  flex: 1 1 auto;
  min-width: 0;
}

.deploy-seq-banner__title {
  margin: 0 0 2px;
  font-size: 13px;
  font-weight: 600;
}

.deploy-seq-banner__hint {
  margin: 0;
  font-size: 12px;
  color: var(--kx-text-muted, inherit);
}

.deploy-seq-banner__actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.deploy-seq-bar-enter-active,
.deploy-seq-bar-leave-active {
  transition: opacity 200ms ease, transform 200ms ease;
}

.deploy-seq-bar-enter-from,
.deploy-seq-bar-leave-to {
  opacity: 0;
  transform: translateX(-50%) translateY(8px);
}

@media (prefers-reduced-motion: reduce) {
  .deploy-seq-bar-enter-active,
  .deploy-seq-bar-leave-active {
    transition: none;
  }
}
</style>
