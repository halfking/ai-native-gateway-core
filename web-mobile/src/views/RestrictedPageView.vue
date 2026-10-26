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
//
// ★ 批 151 加 "返回首页" 按钮 —— §11.187 (d) 提到"依赖浏览器 back 行为太隐晦"，
//   用 `router.push('/')` 显式导航而不是 history-back，因为深链场景下
//   history 长度可能为 1 ⇒ back() 啥也退不出。
//   "返回上一页" 不在这里加：本页"上一页"可能是 deep-link 触发的那次跳，
//   用户再点 back 仍是 /restricted，等于死循环。
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
useHyperPage({ title: () => t('common.pageDenied') })
const router = useRouter()
function goHome(): void { void router.push('/') }
</script>

<template>
  <div class="rp">
    <p data-testid="rp-denied" role="alert" class="rp__msg">{{ t('common.pageDenied') }}</p>
    <button
      type="button"
      class="rp__btn"
      data-testid="rp-back"
      @click="goHome"
    >{{ t('common.back') }}</button>
  </div>
</template>

<style scoped>
.rp { padding: 32px 16px; display: grid; gap: 12px; }
.rp__msg { font-size: 16px; color: var(--app-text); }
.rp__btn {
  justify-self: start;
  min-height: 48px; /* touch-target R1 触控门禁 */
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 16px;
  cursor: pointer;
}
.rp__btn:active { background: var(--app-surface-muted); }
</style>
