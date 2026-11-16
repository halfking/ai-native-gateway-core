<script setup lang="ts">
// ProvidersModeView.vue — 供应商路由的模式包装（2026-11-17）
//
// ★ 两套前端体系并存：**同一路径**，按工作模式渲染**不同页面**。
//   expert → 现有关联的 ProvidersView（专家面，一行未改）
//   simple → 简易模式的 SimpleProvidersView（两字段接入 + 关停折叠 + 遮蔽）
//
// ★ 为什么用包装而不是两条路由：
//   两条路由会分裂收藏链接与深链（/providers 与 /providers-simple 各一份），
//   也会让「模式切换后地址变了」变成用户的困惑点。
//   包装组件让**路径不变、数据不变**，只有页面形态变 ⇒ 切模式零心智负担。
import { computed, defineAsyncComponent } from 'vue'
import { useWorkMode } from '../composables/useWorkMode'

const ProvidersView = defineAsyncComponent(() => import('./ProvidersView.vue'))
const SimpleProvidersView = defineAsyncComponent(() => import('./SimpleProvidersView.vue'))

const { isSimple } = useWorkMode()
const active = computed(() => (isSimple.value ? SimpleProvidersView : ProvidersView))
</script>

<template>
  <component :is="active" />
</template>
