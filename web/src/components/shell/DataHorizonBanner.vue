<script setup lang="ts">
/**
 * DataHorizonBanner — 「流量数据地平线」全局告示（2026-10-15 r1033 重写）。
 *
 * 判据（admin/data_horizon.go）：网关仍在接收请求，而 session_turns——
 * 流量统计的系统记录——没有跟上 ⇒ 断流，横幅让人看见。网关空闲（没有
 * 流量）时不告示：没有新数据是正常状态，不是失效。
 *
 * 为什么是全局横幅而不是逐页面标注：平台级事实一个横幅表达，粒度诚实
 * 且全覆盖；逐端点接线会漏，而漏接的往往正是最新加的那个。
 *
 * ⚠ **取不到告示时也显示横幅**（state='failed'），且文案不同。
 * 静默降级成「健康」是这里唯一真正危险的写法：断流期间告示端点恰好挂了，
 * 页面会照常展示滞后数字而没有任何提示 —— 与断流本身是同一个失效形态
 * （没有错误信号），告示不能继承它。
 *
 * ⚠ 未知态（页面刚起来）**不显示**：闪一条「数据断流」比不闪更糟，
 * 它会让人学会忽略这条横幅。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useDataHorizon } from '../../composables/useDataHorizon'

const { t } = useI18n()
const { state, notice, shouldShowBanner, refresh } = useDataHorizon()

const failed = computed(() => state.value === 'failed')
const show = computed(() => shouldShowBanner())

// 后端给的三句是**这个实例**的实话（哪个源族、断流意味着什么、怎么排查）；
// 标题走 i18n，因为那是产品文案，会随语言变。
const effect = computed(() => notice.value?.effect ?? '')
const silence = computed(() => notice.value?.silence ?? '')
const hint = computed(() => notice.value?.hint ?? '')

// failed 态**不**展示后端 stale 态那几句：那些话描述的是「确证断流」，
// 而此时我们只知道「取不到」。
const hideBackendWording = computed(() => failed.value)

function onDismissRefresh() {
  void refresh()
}
</script>

<template>
  <div
    v-if="show"
    class="data-horizon-banner"
    :class="{ 'data-horizon-banner--failed': failed }"
    role="status"
    aria-live="polite"
  >
    <div class="data-horizon-banner__body">
      <p class="data-horizon-banner__title">
        {{ failed ? t('app.dataHorizon.titleUnavailable') : t('app.dataHorizon.title') }}
      </p>
      <template v-if="!hideBackendWording">
        <p class="data-horizon-banner__line">{{ effect }}</p>
        <p class="data-horizon-banner__line data-horizon-banner__silence">{{ silence }}</p>
        <p v-if="hint" class="data-horizon-banner__meta">
          {{ t('app.dataHorizon.hintLabel') }}: <span>{{ hint }}</span>
        </p>
      </template>
      <p v-else class="data-horizon-banner__line">
        {{ t('app.dataHorizon.failedHint') }}
      </p>
    </div>
    <button
      type="button"
      class="data-horizon-banner__action"
      :title="t('app.dataHorizon.retry')"
      @click="onDismissRefresh"
    >
      {{ t('app.dataHorizon.retry') }}
    </button>
  </div>
</template>

<style scoped>
/* 样式沿用 2026-10-03 横幅的既有约定（D21 对比度裁决）：alert 家族的
   底/边/字令牌，两主题自动适配。stale 态走 warning 族；failed 态走
   danger 族（比「确证断流」更不可信——连断没断都不知道）。 */
.data-horizon-banner {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 10px clamp(16px, 3vw, 32px);
  background: var(--warning-bg);
  border-bottom: 1px solid var(--warning-bd);
  color: var(--kx-text);
  width: 100%;
}

.data-horizon-banner--failed {
  background: var(--danger-bg);
  border-bottom-color: var(--danger-bd);
}

.data-horizon-banner__body { flex: 1 1 auto; min-width: 0; }

.data-horizon-banner__title {
  margin: 0 0 4px;
  font-weight: 600;
  font-size: 13px;
}

.data-horizon-banner__line {
  margin: 2px 0;
  font-size: 12px;
  line-height: 1.5;
}

/* 「接口没报错不代表数据是新的」——这句是这个横幅存在的主要理由，
   视觉上要能一眼扫到，所以加粗。 */
.data-horizon-banner__silence { font-weight: 600; }

.data-horizon-banner__meta {
  margin: 2px 0 0;
  font-size: 11px;
  opacity: 0.8;
}

.data-horizon-banner__action {
  flex: 0 0 auto;
  border: 1px solid currentColor;
  background: transparent;
  color: inherit;
  border-radius: 4px;
  padding: 2px 8px;
  font-size: 12px;
  cursor: pointer;
}
</style>
