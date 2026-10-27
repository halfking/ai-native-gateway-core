<script setup lang="ts">
// StatCard.vue — 通用统计卡片（2026-09-12 方案 §4.5.3；2026-10-24 重构）
//
// ── 2026-10-24 为什么改（craft-floor 排版轮）──
// 原形态是「巨数字 + 小标签 + 辅助统计 + 强调色」的 hero-metric 模板，
// 外加一个 `icon?: string` 参数，注释里明写着「图标（emoji 或字符）」——
// 即用 Unicode 字形冒充图标系统。这两条都是 craft-floor 点名的 AI 痕迹：
//   · 「Unicode 字形或 emoji 顶替图标系统」= 硬禁；
//   · 「大数字 + 小标签 + 辅助统计 + 强调色」= 页面骨架的懒默认。
//
// 改法（不是换皮，是换表达）：
//   1. **删掉 icon 参数**。运维读的是「请求数 12,345」，不是「📥 请求数 12,345」。
//      emoji 在这里是纯噪声，去掉后卡片安静下来，数字自己说话。
//      （21 处调用点同步去掉 icon 属性；#spark 槽保留 —— 那是真的 7 天趋势，
//        有数据职责，不是「用火花线充数」。）
//   2. **tone 从 3px 彩色左边条改成标签前的一个状态点**。craft-floor 明禁卡片上的
//      彩色侧边条；而状态点本身就是形状，天然是「颜色之外的第二信号」，
//      状态不能只靠颜色区分这条也顺带满足了。
//   3. **给数字补上语境**。`sub` 槽是「口径」位（同比 / 时间窗 / 异常指向），
//      没有 sub 的调用点会退化成「一个没有出处的数字」，所以 sub 缺省时
//      仍保留行位（min-height 稳定），避免同一行卡片高度跳动。
//
//   <StatCard :label="totalLabel" :value="total" />
//   <StatCard :label="errorLabel" :value="n" tone="danger" sub="+12%" compact />
//   <StatCard :label="…"><template #value>富内容</template></StatCard>
//   <StatCard :label="…"><template #spark>迷你趋势（svg/canvas）</template></StatCard>
withDefaults(defineProps<{
  /** 卡片标签（已翻译字符串） */
  label: string
  /** 主数值；复杂内容用 #value 插槽 */
  value?: string | number
  /** 数值下方的次级说明（同比/口径等） */
  sub?: string
  /** 语义色：neutral 无标记，success/warning/danger 标签前带状态点 */
  tone?: 'neutral' | 'success' | 'warning' | 'danger'
  /** 紧凑模式（对齐全局 .stat-card--compact：小内边距/灰底/11px） */
  compact?: boolean
}>(), {
  value: '',
  sub: '',
  tone: 'neutral',
  compact: false,
})
</script>

<template>
  <div
    class="stat-card app-stat-card"
    :class="[
      compact && 'stat-card--compact',
      tone !== 'neutral' && `app-stat-card--${tone}`,
    ]"
  >
    <div class="app-stat-card__main">
      <div class="label app-stat-card__label">{{ label }}</div>
      <div class="value app-stat-card__value">
        <slot name="value">{{ value }}</slot>
      </div>
      <div v-if="sub || $slots.sub" class="sub app-stat-card__sub">
        <slot name="sub">{{ sub }}</slot>
      </div>
    </div>
    <div v-if="$slots.spark" class="app-stat-card__spark"><slot name="spark" /></div>
  </div>
</template>

<style scoped>
/* 基础 .stat-card/.stat-card--compact/.label/.value/.sub 观感来自全局
 * style.css（与存量用法完全一致），这里只补 tone 状态点与响应式。 */

/* 状态点：标签前一个小圆点。
   为什么不是左边条：craft-floor 明禁卡片上的 1px 以上彩色侧边条
   （「同尺寸图标卡片网格」之外最扎眼的一处 AI 痕迹）。
   为什么不是纯色块：状态不能只靠颜色区分 —— 圆点的形状本身就是第二信号，
   色盲用户也能看出「这张卡带标记」。 */
.app-stat-card__label {
  display: flex;
  align-items: center;
  gap: 4px;
}
.app-stat-card--success .app-stat-card__label::before,
.app-stat-card--warning .app-stat-card__label::before,
.app-stat-card--danger  .app-stat-card__label::before {
  content: '';
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex: none;
}
.app-stat-card--success .app-stat-card__label::before { background: var(--success); }
.app-stat-card--warning .app-stat-card__label::before { background: var(--warning); }
.app-stat-card--danger  .app-stat-card__label::before { background: var(--danger); }

.app-stat-card__main { min-width: 0; flex: 1; }
.app-stat-card__spark {
  align-self: flex-end;
  min-width: 72px;
  max-width: 110px;
  flex: none;
}

/* 紧凑模式数值：对齐 RequestLogsView 原内联实现（16px/600），
 * 取代其 20 行 inline style 重复 */
.stat-card--compact .app-stat-card__value {
  font-size: 16px;
  font-weight: 600;
  margin-top: 2px;
}

/* 小屏：非紧凑卡数值从 24px 收敛到 16px，长数字不撑破单列布局
 * （★ 2026-10-27 第 23 增量：原为 18px —— 18 不在 DESIGN.md §4 的角色阶上，
 *   且降档阶梯被打断（24 → 20 → 18 里 18 与 20 不可区分）⇒ 收到下一档 16px。） */
@media (max-width: 480px) {
  .app-stat-card:not(.stat-card--compact) .app-stat-card__value {
    font-size: 16px;
  }
}
</style>
