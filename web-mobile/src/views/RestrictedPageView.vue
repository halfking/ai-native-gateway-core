<script setup lang="ts">
// RestrictedPageView — 批 150 壳层缺口的"终态页"：路由守卫把不匹配 role 的人
// 跳到这里；这里只显示 `common.pageDenied` 一句话，**不挂载任何 API 模块**。
//
// ★ 重要：此页本身不消费 `authStore.role` —— 路由层已经判过一遍了，重复判会
//   让"路径是 /restricted 但角色已变更"的用户看到不一致的中间态。
//   这里只负责"告诉用户来错地方了"。
//
// ★★ meta.titleKey 复用 `common.pageDenied`（`router/index.ts` 的 route meta），
//   ⇒ document.title 也是"无权访问此页"，不会泄露 `from` 查询里的真页面名。
//   这是 RestrictedPageTitle spec 8-3 钉住的纪律。
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
useHyperPage({ title: () => t('common.pageDenied') })
</script>

<template>
  <div class="rp">
    <p data-testid="rp-denied" role="alert" class="rp__msg">{{ t('common.pageDenied') }}</p>
  </div>
</template>

<style scoped>
.rp { padding: 32px 16px; display: grid; gap: 12px; }
.rp__msg { font-size: 16px; color: var(--app-text); }
</style>
