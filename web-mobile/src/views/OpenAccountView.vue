<script setup lang="ts">
// OpenAccountView — /me /account 兼容入口。
// Capacitor WebView 对 `/?account=1` 整页导航时常丢掉 query；改为独立路由，
// 挂载后通过自定义事件通知壳打开账户 Sheet，再 replace 回总览。
import { nextTick, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'

useHyperPage({ title: () => t('nav.account') })
const router = useRouter()

onMounted(() => {
  // 必须先 replace 回总览，再开账户 Sheet。
  // Hyper runtime 在路由切换时会 silent-close 全部 overlay；若先开 Sheet
  // 再 replace，Sheet 会被导航清掉（真机 /m/me 表现为闪一下又回到总览）。
  void router.replace('/').then(() => {
    void nextTick(() => {
      window.dispatchEvent(new CustomEvent('llmgw:open-account'))
    })
  })
})
</script>

<template>
  <div class="open-account" aria-busy="true" />
</template>

<style scoped>
.open-account {
  min-height: 40vh;
}
</style>
