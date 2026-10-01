<script setup lang="ts">
// EmptyState.vue — 全站共享空态组件（2026-09-04）
//
// 抽取自各 view 重复的 `<div class="empty-state">暂无数据</div>` 模板。
//
// R88-f 订正：原注释把 ProbeHealthDetailView / SelfCheckPanel / ModulesView /
// ProxyView / PromptInjectionSettingsView 都列成「抽取来源」，但实测 **ModulesView
// 与 ProxyView 至今仍各自手写 `class="empty-state"`**（ModulesView:1103、
// ProxyView:491/644/737），并未改用本组件 ⇒ 「抽取自」的说法对这 4 处不成立。
//
// **另：仓库里并存两套空态体系，不是「没有组件」**：
//   - 本组件渲染 `app-empty-state`，现有 7 个消费者；
//   - 另有 13 处自建 `.empty-state` 容器（8 个文件），用的是各 view 本地
//     定义的 `.empty-state` 类。
// 其中 3 处是 **loading 态**、2 处在 **`<td colspan=N>`**、1 处用 `!important`
// 覆盖 padding ⇒ 机械全局替换会改变客户端观感且在结构上放不进 `<td>`。
// 逐处清单与不替换的理由见 `docs/audit/playbook/domains/D15-observability-ux.md`
// 的 R88-f 回注；是否扩组件补 icon/title/desc/action 四槽已登记为待裁决第 29 条。
//
// 现存能力边界：**只有一个默认 slot**（渲染纯文字）。凡需要
// icon / title / desc / action 的场景本组件承载不了（ClientConfigDialog、
// ModulesView 即属此类），这是能力缺口而非遗漏。
withDefaults(defineProps<{
  /** 空态文案（已翻译的字符串）；也可用默认插槽放富内容 */
  text?: string
  /** 与原各 view 的 .empty-state padding 保持一致 */
  padding?: string
}>(), {
  text: '',
  padding: '40px',
})
</script>

<template>
  <div class="app-empty-state" :style="{ padding }">
    <slot>{{ text }}</slot>
  </div>
</template>

<style scoped>
.app-empty-state {
  text-align: center;
  color: var(--muted, var(--text-secondary));
  font-size: 13px;
}
</style>
