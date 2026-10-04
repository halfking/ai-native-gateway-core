<script setup lang="ts">
// AppIcon — 手写矢量图标集（UI规范 06 §7：禁用字形 ⋯/☰ 充当图标；
// 24px viewBox stroke 风格，feather 系线条）。新增图标在 PATHS 登记路径。
import { computed } from 'vue'

const props = withDefaults(defineProps<{ name: IconName; size?: number }>(), { size: 22 })

export type IconName =
  | 'home'
  | 'server'
  | 'cube'
  | 'key'
  | 'menu'
  | 'alert'
  | 'chart'
  | 'user'
  | 'logout'
  | 'back'
  | 'close'
  | 'more'
  | 'search'
  | 'refresh'
  | 'expand'
  | 'sun'
  | 'moon'
  | 'globe'
  | 'check'
  | 'plus'
  | 'chevron'
  | 'copy'
  | 'clock'
  | 'cpu'

const PATHS: Record<IconName, string> = {
  home: 'M3 10.5 12 3l9 7.5M5 9.5V21h5v-6h4v6h5V9.5',
  server: 'M4 4h16v6H4zM4 14h16v6H4zM7.5 7h.01M7.5 17h.01',
  cube: 'M12 2.5 21 7v10l-9 4.5L3 17V7l9-4.5zM3 7l9 4.5L21 7M12 11.5V21.5',
  key: 'M14.5 3a6.5 6.5 0 1 0-4.9 10.8L3 20.4V22h3.5l1-1v-2h2v-2h2l1.6-1.6A6.5 6.5 0 0 0 14.5 3zM17 7h.01',
  menu: 'M4 6h16M4 12h16M4 18h16',
  alert: 'M12 3 2.5 20h19L12 3zM12 9.5v5M12 17.5h.01',
  chart: 'M4 20V4M4 20h16M8 16v-5M12 16V8M16 16v-3',
  user: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4.5 20.5a7.5 7.5 0 0 1 15 0',
  logout: 'M9 4H5v16h4M14 8l4 4-4 4M8.5 12H18',
  back: 'M19 12H5M11 6l-6 6 6 6',
  close: 'M5 5l14 14M19 5 5 19',
  more: 'M12 5.5h.01M12 12h.01M12 18.5h.01',
  search: 'M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13zM20 20l-4.8-4.8',
  refresh: 'M20 5v5h-5M4 19v-5h5M5.5 9a7 7 0 0 1 12-2.5L20 10M18.5 15a7 7 0 0 1-12 2.5L4 14',
  expand: 'M4 9V4h5M20 15v5h-5M15 4h5v5M9 20H4v-5',
  sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1.5v3M12 19.5v3M4.6 4.6l2 2M17.4 17.4l2 2M1.5 12h3M19.5 12h3M4.6 19.4l2-2M17.4 6.6l2-2',
  moon: 'M20 14.5A8.5 8.5 0 0 1 9.5 4 8.5 8.5 0 1 0 20 14.5z',
  globe: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.5 3.8 5.6 3.8 9S14.5 18.5 12 21c-2.5-2.5-3.8-5.6-3.8-9S9.5 5.5 12 3z',
  check: 'M4.5 12.5 10 18 19.5 6.5',
  plus: 'M12 5v14M5 12h14',
  chevron: 'M9 5l7 7-7 7',
  copy: 'M9 9h11v11H9zM15 9V4H4v11h5',
  clock: 'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3.5 2',
  cpu: 'M7 7h10v10H7zM10 3v4M14 3v4M10 17v4M14 17v4M3 10h4M3 14h4M17 10h4M17 14h4',
}

const d = computed(() => PATHS[props.name] ?? '')
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.8"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    class="app-icon"
  >
    <path :d="d" />
  </svg>
</template>

<style scoped>
.app-icon {
  display: block;
  flex-shrink: 0;
}
</style>
