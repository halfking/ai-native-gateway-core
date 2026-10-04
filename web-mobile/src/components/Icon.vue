<script setup lang="ts">
// Icon.vue — 矢量 SVG 图标（UI 规范 06 §7：禁字形 ⋯/☰，全部 SVG path）。
// 24×24 viewBox，stroke 风格（2px），触控目标由外层按钮保证 ≥48px。
const PATHS: Record<string, string> = {
  overview: 'M3 12l9-8 9 8M5 10v10h5v-6h4v6h5V10',
  nodes: 'M5 4h14v6H5zM5 14h6v6H5zM15 14h4v6h-4zM8 10v4M11 17h4',
  models: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3zM12 12l8-4.5M12 12v9M12 12L4 7.5',
  keys: 'M14 7a4 4 0 1 0-3.9 5L4 18v2h3v-2h2v-2h2l1.1-1.1A4 4 0 0 0 14 7z',
  alerts: 'M12 3l9 16H3l9-16zM12 10v4M12 17.5v.5',
  usage: 'M4 20V6M4 20h16M8 16v-5M12 16V8M16 16v-3M20 16V5',
  more: 'M5 12h14M12 5v14',
  back: 'M15 5l-7 7 7 7',
  close: 'M6 6l12 12M18 6L6 18',
  account: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21c1.5-4 4.5-6 8-6s6.5 2 8 6',
  search: 'M10.5 17a6.5 6.5 0 1 0 0-13 6.5 6.5 0 0 0 0 13zM20 20l-4.5-4.5',
  refresh: 'M20 12a8 8 0 1 1-2.3-5.7M20 3v4h-4',
  focus: 'M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5M9 12h6',
  copy: 'M9 9h10v12H9zM5 15V3h10',
  check: 'M4 12l5 5L20 6',
  logout: 'M14 4H6v16h8M10 12h11M18 9l3 3-3 3',
  desktop: 'M3 5h18v11H3zM9 20h6M12 16v4',
  chevron: 'M9 6l6 6-6 6',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19 12l2-1.5-1-3.5-2.5.3-2-2 .3-2.5L12.5 2 11 4h-3L6.5 2 4 3l.3 2.5-2 2L0 7',
}

const props = defineProps<{ name: keyof typeof PATHS | string; size?: number }>()
</script>

<template>
  <svg
    :width="props.size ?? 22"
    :height="props.size ?? 22"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path :d="PATHS[props.name] ?? PATHS['overview']" />
  </svg>
</template>
