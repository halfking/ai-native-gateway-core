<script setup lang="ts">
// PageHeader.vue — 通用页头（2026-09-12，方案 §4.5.2）
//
// 抽取自 42 个视图手写的 `.page-header` 骨架（31 个视图还各自重复定义了
// 同名样式）。视觉对齐全局 style.css 的 .page-header（flex / space-between /
// margin-bottom 20px / h2 18px·600），因此各视图迁移后观感不变，仅结构
// 收敛；移动端（<768px）标题与操作区自动堆叠，操作按钮全宽换行。
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
  flex-shrink: 0;
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
  .app-page-header__actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  .app-page-header__actions > :deep(*) {
    min-height: 36px;
  }
}
</style>
