<script setup lang="ts">
// PageHeader.vue — 通用页头（2026-09-12，方案 §4.5.2）
//
// 抽取自 42 个视图手写的 `.page-header` 骨架（31 个视图还各自重复定义了
// 同名样式）。视觉对齐全局 style.css 的 .page-header（flex / space-between /
// margin-bottom 20px / h2 18px·600），因此各视图迁移后观感不变，仅结构
// 收敛；移动端（<768px）标题与操作区自动堆叠，操作按钮全宽换行。
//
// ★ 2026-10-26：桌面端「谁让路」的规则翻转（实测驱动的缺陷修复）。
//   原形态：.app-page-header__actions { flex-shrink: 0 } + .app-page-header__main
//   { min-width: 0 } ⇒ 操作区按内容宽度占位且不让步，标题区可缩到 0。
//   两者相加超出容器时，**被牺牲的永远是标题**：实测德语页标题宽 = 0px
//   （完全消失）、法语页 = 28px（逐字竖排）。触发器是「翻译后的按钮比中文长」
//   （德语 "Anmeldedaten in die DB schreiben" 248px vs 中文「写入 DB 凭据」）。
//   现在：标题区不让路（flex: 0 0 auto + max-width: 50%），操作区可收缩并换行
//   （flex: 0 1 auto + flex-wrap: wrap）⇒ 放不下时换行，不再吃掉标题。
//
//   <PageHeader :title="t('keys.list.title')">
//     <template #actions>
//       <button class="btn btn-primary">…</button>
//     </template>
//   </PageHeader>
//
// slot leading：标题左侧（返回链接 PageBackLink 等）；slot default：标题
// 下方整行区域（说明条/徽标行）。
defineProps<{
  title: string
  /** 标题下的次级说明；留空不渲染 */
  subtitle?: string
}>()
</script>

<template>
  <div class="app-page-header">
    <div class="app-page-header__main">
      <slot name="leading" />
      <div class="app-page-header__text">
        <h2 class="app-page-header__title">{{ title }}</h2>
        <p v-if="subtitle" class="app-page-header__subtitle">{{ subtitle }}</p>
      </div>
    </div>
    <div v-if="$slots.actions" class="app-page-header__actions">
      <slot name="actions" />
    </div>
  </div>
  <div v-if="$slots.default" class="app-page-header__below">
    <slot />
  </div>
</template>

<style scoped>
.app-page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 20px;
}
.app-page-header__main {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  /* ★ 标题区【不参与收缩】（2026-10-26）：见下方 actions 的说明。
     标题是可读性的底线 —— 操作区放不下时应该换行，而不是把标题压成 0 宽。 */
  flex: 0 0 auto;
  /* ★ 长标题最多占一半：把剩余宽度让给操作区，避免两头都挤成窄条 */
  max-width: 50%;
}
.app-page-header__text {
  min-width: 0;
}
.app-page-header__title {
  font-size: 18px;
  font-weight: 600;
  margin: 0;
  overflow-wrap: break-word;
}
.app-page-header__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--muted);
}
.app-page-header__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  /* ★ 操作区【可收缩 + 可换行】（2026-10-26）。
     原形态是 flex-shrink: 0 —— 操作区按内容宽度占位且不让步，
     而标题区是 min-width: 0（可缩到 0）⇒ 两者相加超出容器时，
     **被牺牲的永远是标题**：德语页实测标题宽 = 0px（完全消失），
     法语页 = 28px（逐字竖排）。文案变长（翻译后的德语按钮比中文长 40%+）
     只是【触发器】，根因是这条 flex 规则让「谁让路」写死成了标题。
     现在反过来：标题不让路，操作区收缩并换行。 */
  flex: 0 1 auto;
  min-width: 0;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.app-page-header__below {
  margin: -12px 0 20px; /* 收窄与标题的间距，保持与原紧凑页头一致 */
}

/* 移动端：标题与操作区堆叠，操作按钮可换行铺满（方案 §4.4） */
@media (max-width: 768px) {
  .app-page-header {
    flex-direction: column;
    align-items: stretch;
    gap: 10px;
  }
  /* ★ 堆叠后标题独占一行 ⇒ 解除「最多占一半」的限制（否则窄屏标题被压成半宽） */
  .app-page-header__main {
    max-width: none;
    flex: 0 0 auto;
  }
  .app-page-header__actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  .app-page-header__actions > :deep(*) {
    min-height: 36px;
  }
}
</style>
