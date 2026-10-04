<script setup lang="ts">
/**
 * V1DataFrozenBanner — 「v1 流量数据已停更」全局告示（审计 §9.73.7，③-4 裁决）。
 *
 * 为什么是全局横幅而不是逐页面标注：
 * silently_frozen 那一档的 21 个读点分散在 11 个 UI 可见的 API 与 10 个后台
 * worker/bench 里。逐读点接线的覆盖面是假的 —— 漏接的那一个没有任何门会报，
 * 而漏接的往往正是最新加的那个。平台级事实用一个横幅表达，粒度诚实且全覆盖。
 *
 * ⚠ **取不到告示时也显示横幅**（state='failed'），且文案不同。
 * 静默降级成「未停更」是这里唯一真正危险的写法：停写期间告示端点恰好挂了，
 * 页面会照常展示停写前的数字而没有任何提示 —— 这与 silently_frozen
 * 本身是同一个失效形态（没有错误信号），告示不能继承它。
 *
 * ⚠ 未知态（页面刚起来）**不显示**：闪一条「数据已停更」比不闪更糟，
 * 它会让人学会忽略这条横幅。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useV1DataHorizon } from '../../composables/useV1DataHorizon'

const { t } = useI18n()
const { state, notice, shouldShowBanner, refresh } = useV1DataHorizon()

const failed = computed(() => state.value === 'failed')
// ⚠ `unconfirmed`（§9.79.8）与 `failed` **必须分开**：
// `failed` = 请求挂了/取不到（我们对停写状态一无所知）；
// `unconfirmed` = 请求成功，但后端读到的是**回落值**（后端知道，
//   而它明确告诉我们「这个值不是有人配的」）。
//
// 合并成一个的代价不是少一个分支，而是：unknown 判据变成冗余 ——
// 删掉它，composable 的 12 个用例仍然全绿（变异 F4 实录）。
const unconfirmed = computed(() => state.value === 'unconfirmed')
// 两者都**不展示**后端 frozen 态那两句：它们描述的是「已停更」，
// 而这两种状态下我们都还不知道是不是停更。
const hideBackendWording = computed(() => failed.value || unconfirmed.value)
const show = computed(() => shouldShowBanner())

// 后端给的两句是**这个实例**的实话（哪个源族、哪个键）；
// 标题与解释走 i18n，因为那是产品文案，会随语言变。
const effect = computed(() => notice.value?.effect ?? '')
const silence = computed(() => notice.value?.silence ?? '')
const gateKey = computed(() => notice.value?.gate_key ?? '')
const affects = computed(() => (notice.value?.affects ?? []).join(' / '))

function onDismissRefresh() {
  void refresh()
}
</script>

<template>
  <div
    v-if="show"
    class="v1-frozen-banner"
    :class="{ 'v1-frozen-banner--failed': failed || unconfirmed }"
    role="status"
    aria-live="polite"
  >
    <div class="v1-frozen-banner__body">
      <p class="v1-frozen-banner__title">
        {{ unconfirmed
          ? t('app.v1DataFrozen.titleUnconfirmed')
          : failed
            ? t('app.v1DataFrozen.titleUnavailable')
            : t('app.v1DataFrozen.title') }}
      </p>
      <!-- 取失败/回落未知时**不**展示后端 frozen 态那两句：那些话描述的是
           「已停更」，而此时我们并不知道是不是停更。 -->
      <template v-if="!hideBackendWording">
        <p class="v1-frozen-banner__line">{{ effect }}</p>
        <p class="v1-frozen-banner__line v1-frozen-banner__silence">{{ silence }}</p>
        <p v-if="affects" class="v1-frozen-banner__meta">
          {{ t('app.v1DataFrozen.affects') }}: <code>{{ affects }}</code>
        </p>
        <p v-if="gateKey" class="v1-frozen-banner__meta">
          {{ t('app.v1DataFrozen.gateKey') }}: <code>{{ gateKey }}</code>
        </p>
      </template>
      <p v-else class="v1-frozen-banner__line">
        {{ t('app.v1DataFrozen.failedHint') }}
      </p>
    </div>
    <button
      type="button"
      class="v1-frozen-banner__action"
      :title="t('app.v1DataFrozen.retry')"
      @click="onDismissRefresh"
    >
      {{ t('app.v1DataFrozen.retry') }}
    </button>
  </div>
</template>

<style scoped>
.v1-frozen-banner {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 10px clamp(16px, 3vw, 32px);
  /* D21：此处原为 var(--kx-warning-surface, #fff8e1) / (--kx-warning-border, #f0c36d)。
     这两个令牌全仓从未定义过 ⇒ 兜底字面值在**两个主题下都生效**，于是暗色主题拿到
     浅奶油底 #fff8e1 去配浅字 --kx-text #e8eef7，实测对比度 1.10:1
     （失败态 #fdecea 更低，1.02:1；WCAG AA 普通文本要 4.5:1）——横幅等于隐形，
     而它承载的正是「这些数字不可信」这句话。

     改用仓内既有的 alert 约定（底 --*-bg / 边 --*-bd / 字 --kx-text，
     见 TenantDetailView .alert-danger、TaskProfileView），两主题自动适配，
     也不再需要字面兜底：dark 升到 13.09:1，light 由 15.37 微降到 14.29，均为 AA。 */
  background: var(--warning-bg);
  border-bottom: 1px solid var(--warning-bd);
  color: var(--kx-text);
  width: 100%;
}

/* 取失败态用更强的色：它比「已停更」更不可信 ——
   连「是不是停更」都不知道时，页面上的每个数字都该被怀疑。 */
.v1-frozen-banner--failed {
  background: var(--danger-bg);
  border-bottom-color: var(--danger-bd);
}

.v1-frozen-banner__body { flex: 1 1 auto; min-width: 0; }

.v1-frozen-banner__title {
  margin: 0 0 4px;
  font-weight: 600;
  font-size: 13px;
}

.v1-frozen-banner__line {
  margin: 2px 0;
  font-size: 12px;
  line-height: 1.5;
}

/* 「接口没报错不代表数据是新的」——这句是这个横幅存在的主要理由，
   视觉上要能一眼扫到，所以加粗。 */
.v1-frozen-banner__silence { font-weight: 600; }

.v1-frozen-banner__meta {
  margin: 2px 0 0;
  font-size: 11px;
  opacity: 0.8;
}

.v1-frozen-banner__action {
  flex: 0 0 auto;
  border: 1px solid currentColor;
  background: transparent;
  color: inherit;
  border-radius: 4px;
  padding: 2px 10px;
  font-size: 12px;
  cursor: pointer;
}
</style>
