<script setup lang="ts">
// SubscriptionTiersView — 批 200（2026-10-17）
// 🟢 P5 跨仓孤儿接前端：subscriptionTiers（license-authority 独立服务）
//
// 端点：GET /api/admin/tiers（nginx 反代 license-authority 独立 service）
// 类型：SubscriptionTierInfo（api/ops.ts:145）6 字段：code / name / description
//       / price_cents / sort_order / module_keys
//
// 17 跳 evidence：subscriptionTiers 是 license-authority 独立 service 跨仓端点，
//   不是 admin 仓本地。视图层不参与授权判断，前端仅"呈现已就位数据"。
//
// 调研：/tmp/co153-wt-evidence.txt 17 跳（line 1070 / 1101 / 1123 / 1282 / 1616）
import { onMounted, ref } from 'vue'
import { surfaceError } from '../i18n/errorSurface'
import { getSubscriptionTiers, type SubscriptionTierInfo } from '../api/ops'

const loading = ref(false)
const error = ref('')
const tiers = ref<SubscriptionTierInfo[]>([])

async function load() {
  loading.value = true
  error.value = ''
  try {
    tiers.value = await getSubscriptionTiers()
    // 按 sort_order 升序
    tiers.value.sort((a, b) => a.sort_order - b.sort_order)
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function formatPrice(cents: number | undefined | null): string {
  if (cents === undefined || cents === null) return '—'
  if (cents === 0) return '免费'
  return '¥' + (cents / 100).toFixed(2)
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="subscription-tiers-view">
    <header class="st-header">
      <h1 class="st-title">订阅层级</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
      </button>
    </header>

    <p v-if="error" class="st-error">{{ error }}</p>

    <div v-if="loading && tiers.length === 0" class="st-loading">{{ $t('common.feedback.loading') }}</div>

    <div v-else-if="tiers.length === 0 && !loading" class="st-empty">
      暂无订阅层级
    </div>

    <div v-else class="st-grid">
      <article v-for="t in tiers" :key="t.code" class="st-card">
        <header class="st-card-header">
          <h2 class="st-card-name">{{ t.name }}</h2>
          <code class="st-card-code">{{ t.code }}</code>
        </header>
        <p class="st-card-price">{{ formatPrice(t.price_cents) }}</p>
        <p v-if="t.description" class="st-card-description">{{ t.description }}</p>
        <section v-if="t.module_keys.length" class="st-card-modules">
          <h3 class="st-modules-title">包含模块（{{ t.module_keys.length }}）</h3>
          <ul class="st-modules-list">
            <li v-for="m in t.module_keys" :key="m"><code>{{ m }}</code></li>
          </ul>
        </section>
        <section v-else class="st-card-modules">
          <h3 class="st-modules-title">包含模块（0）</h3>
          <p class="st-modules-empty">无</p>
        </section>
      </article>
    </div>
  </div>
</template>

<style scoped>
.subscription-tiers-view { padding: var(--space-4); }
.st-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-3); }
.st-title { font-size: var(--text-xl); font-weight: var(--font-semibold); }
.st-error { color: var(--danger); padding: var(--space-2); background: var(--danger-bg); border-radius: var(--radius); margin-bottom: var(--space-3); }
.st-loading { color: var(--text-muted); padding: var(--space-3); }
.st-empty { color: var(--text-muted); padding: var(--space-3); font-style: italic; }
.st-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: var(--space-3); }
.st-card { background: var(--bg-surface); border: 1px solid var(--border); border-radius: var(--radius); padding: var(--space-3); display: flex; flex-direction: column; }
.st-card-header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: var(--space-2); }
.st-card-name { font-size: var(--text-md); font-weight: var(--font-semibold); }
.st-card-code { color: var(--text-muted); font-size: var(--text-sm); }
.st-card-price { font-size: var(--text-2xl); font-weight: var(--font-semibold); color: var(--kx-primary); margin-bottom: var(--space-2); }
.st-card-description { color: var(--text); margin-bottom: var(--space-2); font-size: var(--text-sm); }
.st-card-modules { margin-top: auto; padding-top: var(--space-2); border-top: 1px solid var(--border-muted); }
.st-modules-title { font-size: var(--text-sm); color: var(--text-muted); margin-bottom: var(--space-1); }
.st-modules-list { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: var(--space-1); }
.st-modules-list li code { font-size: 12px; padding: 2px 4px; background: var(--bg-subtle); border: 1px solid var(--border-muted); border-radius: 4px; }
.st-modules-empty { color: var(--text-muted); font-style: italic; font-size: var(--text-sm); }
@media (max-width: 768px) { .st-grid { grid-template-columns: 1fr; } }
</style>
