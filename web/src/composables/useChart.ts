import {
  Chart,
  ChartConfiguration,
  ChartType,
  DefaultDataPoint,
  registerables
} from 'chart.js'
import { onMounted, onUnmounted, ref, Ref } from 'vue'

// 注册 Chart.js 所有组件
Chart.register(...registerables)

/**
 * 读取 CSS 变量的当前值（用于让 Chart.js 的固定颜色与暗色主题保持一致）。
 * 在 SSR 或变量缺失时回退到给定的默认值。
 */
function getCssVar(name: string, fallback = ''): string {
  if (typeof window === 'undefined' || !window.getComputedStyle) return fallback
  const value = window.getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return value || fallback
}

/**
 * Chart.js 默认颜色（按当前皮肤读取）。
 *
 * ⚠️ CANVAS 铁律：Chart.js 走 canvas fillStyle，**不能**解析 CSS `var()` /
 * `color-mix()`。必须用字面量 hex/rgba——所以我们不在源里写死某一套皮肤的 hex，
 * 而是在 `getChartTheme()` 里通过 `getComputedStyle(document.documentElement)`
 * 读当前主题下的 `--kx-text` / `--kx-muted` / `--border`，把 token 解析为字面量。
 * `color-token-fix.mjs` 已 SKIP 本文件，禁止把 var() 字面量塞进 chart option。
 * 多处调用方还会做 `chartColors.blue + '80'` 拼 alpha，也要求纯 6 位 hex。
 *
 * 2026-09-29：把常量改成函数，原因是亮色皮肤下硬编码暗色 hex (`#e6edf3` 文字、
 * `#8b949e` 副字、`#30363d` 轴线、`#1c2128` 边框）会让 chart 在亮色页面上
 * 几乎看不见。改用 token 后两套皮肤都能读，并新增 `applyThemeToChart()` 让
 * 主题切换时所有活动 chart 实例立即重算颜色。
 */
export interface ChartThemeSnapshot {
  text: string    // 主文字色（亮色 #152033，暗色 #e8eef7）
  muted: string   // 副文字色（亮色 #5a6473，暗色 #8b949e）
  grid: string    // 网格线（亮色 rgba(21,32,51,.08)，暗色 rgba(255,255,255,.06)）
  cardBorder: string  // 卡片描边（亮色 #d8dde3，暗色 #1c2128）
}

function toHexRgba(value: string, fallbackAlpha = 1): string {
  // 把 #rgb / #rrggbb / rgb() / rgba() 规范化成 Chart.js 能解析的形式。
  // Chart.js 接受 #rrggbb / rgba(r,g,b,a)；如果 token 返回的不是字符串，给个兜底。
  if (!value) return '#000000'
  if (value.startsWith('#') || value.startsWith('rgb')) return value
  return '#000000'
}

export function getChartTheme(): ChartThemeSnapshot {
  // 网格线：明亮皮肤下用主文字色 8% alpha，暗色下用白 6% alpha。
  const text = toHexRgba(getCssVar('--kx-text', '#e8eef7'))
  const muted = toHexRgba(getCssVar('--kx-muted', '#8b949e'))
  const border = toHexRgba(getCssVar('--border', '#1c2128'))
  // 网格 alpha：写死的字面量（Chart.js 不会读 color-mix），仅按当前主题选一组。
  const isDark = (typeof document !== 'undefined') &&
    document.documentElement.getAttribute('data-theme') === 'dark'
  const grid = isDark ? 'rgba(255, 255, 255, 0.06)' : 'rgba(21, 32, 51, 0.08)'
  return { text, muted, grid, cardBorder: border }
}

// 设置 Chart.js 全局默认值：模块加载时执行一次（用暗色兜底）。
// 用户切主题后必须由 useChart 内的 applyTheme 重新写入当前值。
const _initial = getChartTheme()
Chart.defaults.color = _initial.muted
Chart.defaults.borderColor = _initial.grid

// 保留旧名作为**函数快照**的别名，**向后兼容**已有导入：
// `import { chartTheme } from './useChart'` —— 但类型从对象变成 getter。
// 旧访问 `chartTheme.text` 在 SSR / 测试无 document 时会拿到暗色兜底。
// 新写法请用 `getChartTheme()`。
export const chartTheme: ChartThemeSnapshot = new Proxy({} as ChartThemeSnapshot, {
  get(_t, key: keyof ChartThemeSnapshot) {
    return getChartTheme()[key]
  }
})

export interface ChartDataset {
  label: string
  data: number[]
  borderColor?: string
  backgroundColor?: string
  fill?: boolean
  yAxisID?: string
  borderWidth?: number
  tension?: number
}

export interface ChartOptions {
  responsive?: boolean
  maintainAspectRatio?: boolean
  // chart.js additional options (kept loose to avoid type churn with
  // chart.js minor version bumps). Properties include onClick,
  // scales, plugins.legend, plugins.tooltip (with callbacks),
  // plugins.annotation.annotations, interaction.{mode,intersect}.
  [key: string]: unknown
}

/**
 * Chart.js 封装 composable
 * 统一管理图表生命周期和配置
 */
export function useChart<
  TType extends ChartType = ChartType,
  TData = DefaultDataPoint<TType>,
  TLabel = unknown
>(
  canvasRef: Ref<HTMLCanvasElement | null>,
  config: Ref<ChartConfiguration<TType, TData, TLabel>>
) {
  const chartInstance = ref<Chart<TType, TData, TLabel> | null>(null)
  const loading = ref(true)
  const error = ref<string | null>(null)
  let disposed = false
  let themeObserver: MutationObserver | null = null

  const destroyChart = () => {
    const chart = chartInstance.value
    chartInstance.value = null
    if (!chart) return
    try {
      chart.stop()
    } catch {
      /* animator may already be stopped */
    }
    try {
      chart.destroy()
    } catch {
      /* ignore destroy races when canvas is detached */
    }
  }

  const initChart = () => {
    if (disposed) return
    const canvas = canvasRef.value
    if (!canvas || !canvas.isConnected) {
      error.value = 'Canvas element not found'
      return
    }

    try {
      const existing = Chart.getChart(canvas)
      if (existing) {
        try {
          existing.stop()
          existing.destroy()
        } catch {
          /* best-effort cleanup of orphaned instance on same canvas */
        }
      }
      destroyChart()
      chartInstance.value = new Chart(canvas, config.value)
      loading.value = false
      error.value = null
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create chart'
      loading.value = false
    }
  }

  const updateChart = (newData?: TData[], newLabels?: TLabel[]) => {
    if (!chartInstance.value || disposed) return
    const canvas = canvasRef.value
    if (!canvas?.isConnected) {
      destroyChart()
      return
    }

    if (newData && chartInstance.value.data.datasets[0]) {
      ;(chartInstance.value.data.datasets[0] as { data: unknown }).data = newData as unknown
    }

    if (newLabels) {
      ;(chartInstance.value.data as { labels: unknown }).labels = newLabels as unknown
    }

    try {
      chartInstance.value.update('none')
    } catch {
      destroyChart()
    }
  }

  onMounted(() => {
    initChart()
    // 主题切换时自动重算 chart 默认色，避免亮色皮肤下还在用暗色 hex。
    // 2026-09-29：applyThemeFromUrl → applyTheme 会改 documentElement.data-theme，
    // 这里监听 data-theme 变化并刷新图表（chart.update('none')）。
    themeObserver = new MutationObserver(() => {
      if (!chartInstance.value || disposed) return
      const t = getChartTheme()
      // Chart.js options 是深层 partial，编译器看不到 color/scales 直接挂在根；
      // 这里只在主题切换时写入，运行时强转即可（失败就 destroy 后重画）。
      const opts = chartInstance.value.options as unknown as {
        color?: string
        scales?: Record<string, { grid?: { color?: string }; ticks?: { color?: string } }>
      }
      opts.color = t.muted
      const scales = opts.scales
      if (scales) {
        for (const k of Object.keys(scales)) {
          const s = scales[k]
          if (s?.grid) s.grid.color = t.grid
          if (s?.ticks) s.ticks.color = t.muted
        }
      }
      Chart.defaults.color = t.muted
      Chart.defaults.borderColor = t.grid
      try {
        chartInstance.value.update('none')
      } catch {
        destroyChart()
      }
    })
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme']
    })
  })

  onUnmounted(() => {
    disposed = true
    themeObserver?.disconnect()
    destroyChart()
  })

  return {
    chartInstance,
    loading,
    error,
    updateChart,
    destroyChart,
    initChart,
    isDisposed: () => disposed,
  }
}

/**
 * 时间序列图表配置生成器
 */
export function createTimeSeriesConfig(
  type: 'line' | 'bar',
  labels: string[],
  datasets: ChartDataset[],
  options?: ChartOptions
): ChartConfiguration {
  const { scales: scaleOverrides, ...restOptions } = options ?? {}
  const defaultScales = {
    x: {
      grid: {
        display: false
      }
    },
    y: {
      beginAtZero: true,
      grid: {
        color: 'rgba(255, 255, 255, 0.06)'
      }
    }
  }
  return {
    type,
    data: {
      labels,
      datasets: datasets.map(ds => ({
        ...ds,
        tension: type === 'line' ? 0.4 : undefined,
        borderWidth: 2
      }))
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: false,
      interaction: {
        mode: 'index',
        intersect: false
      },
      plugins: {
        legend: {
          display: true,
          position: 'top'
        },
        tooltip: {
          enabled: true
        }
      },
      scales: {
        ...defaultScales,
        ...(scaleOverrides as Record<string, unknown> | undefined),
      },
      ...restOptions
    }
  }
}

/**
 * 环形图配置生成器
 */
export function createDoughnutConfig(
  labels: string[],
  data: number[],
  colors: string[],
  options?: ChartOptions
): ChartConfiguration<'doughnut'> {
  return {
    type: 'doughnut',
    data: {
      labels,
      datasets: [
        {
          data,
          backgroundColor: colors,
          borderWidth: 2,
          borderColor: getCssVar('--card')
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: false,
      plugins: {
        legend: {
          display: true,
          position: 'right'
        },
        tooltip: {
          enabled: true,
          callbacks: {
            label: function (context) {
              const label = context.label || ''
              const value = context.parsed
              const total = (context.dataset.data as number[]).reduce(
                (a, b) => a + b,
                0
              )
              const percentage = ((value / total) * 100).toFixed(1)
              return `${label}: ${value} (${percentage}%)`
            }
          }
        }
      },
      ...options
    }
  }
}

/**
 * 堆叠面积图配置生成器
 */
export function createStackedAreaConfig(
  labels: string[],
  datasets: Array<{
    label: string
    data: number[]
    backgroundColor: string
    borderColor: string
  }>,
  options?: ChartOptions
): ChartConfiguration<'line'> {
  return {
    type: 'line',
    data: {
      labels,
      datasets: datasets.map(ds => ({
        ...ds,
        fill: true,
        tension: 0.4,
        borderWidth: 2
      }))
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: false,
      interaction: {
        mode: 'index',
        intersect: false
      },
      plugins: {
        legend: {
          display: true,
          position: 'top'
        },
        tooltip: {
          enabled: true
        }
      },
      scales: {
        x: {
          stacked: true,
          grid: {
            display: false
          }
        },
        y: {
          stacked: true,
          beginAtZero: true,
          grid: {
            color: 'rgba(255, 255, 255, 0.06)'
          }
        }
      },
      ...options
    }
  }
}

/**
 * 直方图配置生成器
 */
export function createHistogramConfig(
  labels: string[],
  data: number[],
  color: string,
  options?: ChartOptions
): ChartConfiguration<'bar'> {
  return {
    type: 'bar',
    data: {
      labels,
      datasets: [
        {
          data,
          backgroundColor: color,
          borderColor: color,
          borderWidth: 1
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          display: false
        },
        tooltip: {
          enabled: true
        }
      },
      scales: {
        x: {
          grid: {
            display: false
          }
        },
        y: {
          beginAtZero: true,
          grid: {
            color: 'rgba(255, 255, 255, 0.06)'
          }
        }
      },
      ...options
    }
  }
}

/**
 * 常用颜色方案（字面量 hex — canvas 不能解析 var()；调用方会拼 +'80' alpha）
 */
export const chartColors = {
  primary: '#409EFF',
  success: '#67C23A',
  warning: '#E6A23C',
  danger: '#F56C6C',
  info: '#909399',
  blue: '#409EFF',
  green: '#67C23A',
  orange: '#E6A23C',
  red: '#F56C6C',
  purple: '#9b59b6',
  cyan: '#3498db',
  pink: '#e91e63',
  gray: '#95a5a6'
}

/**
 * 生成颜色数组
 */
export function generateColors(count: number): string[] {
  const baseColors = [
    chartColors.blue,
    chartColors.green,
    chartColors.orange,
    chartColors.red,
    chartColors.purple,
    chartColors.cyan,
    chartColors.pink,
    chartColors.gray
  ]

  const colors: string[] = []
  for (let i = 0; i < count; i++) {
    colors.push(baseColors[i % baseColors.length])
  }
  return colors
}
