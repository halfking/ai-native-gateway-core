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
 * Chart.js 默认颜色：与全局暗色主题（style.css :root）对齐。
 * 这些常量被各图表配置生成器引用，避免在暗色背景上出现白色边框/网格/文字。
 */
export const chartTheme = {
  text: '#e6edf3',
  muted: '#8b949e',
  grid: 'rgba(255, 255, 255, 0.06)',
  cardBorder: '#1c2128'
}

// 设置 Chart.js 全局默认值，确保所有图表的文字/网格在暗色下可读
Chart.defaults.color = chartTheme.muted
Chart.defaults.borderColor = chartTheme.grid

export interface ChartOptions {
  responsive?: boolean
  maintainAspectRatio?: boolean
  plugins?: {
    legend?: {
      display?: boolean
      position?: 'top' | 'bottom' | 'left' | 'right'
    }
    tooltip?: {
      enabled?: boolean
    }
  }
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

  const initChart = () => {
    if (!canvasRef.value) {
      error.value = 'Canvas element not found'
      return
    }

    try {
      // 销毁旧实例
      if (chartInstance.value) {
        chartInstance.value.destroy()
      }

      // 创建新图表
      chartInstance.value = new Chart(canvasRef.value, config.value)
      loading.value = false
      error.value = null
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'Failed to create chart'
      loading.value = false
    }
  }

  const updateChart = (newData?: TData[], newLabels?: TLabel[]) => {
    if (!chartInstance.value) return

    if (newData && chartInstance.value.data.datasets[0]) {
      chartInstance.value.data.datasets[0].data = newData
    }

    if (newLabels) {
      chartInstance.value.data.labels = newLabels
    }

    chartInstance.value.update()
  }

  const destroyChart = () => {
    if (chartInstance.value) {
      chartInstance.value.destroy()
      chartInstance.value = null
    }
  }

  onMounted(() => {
    initChart()
  })

  onUnmounted(() => {
    destroyChart()
  })

  return {
    chartInstance,
    loading,
    error,
    updateChart,
    destroyChart,
    initChart
  }
}

/**
 * 时间序列图表配置生成器
 */
export function createTimeSeriesConfig(
  type: 'line' | 'bar',
  labels: string[],
  datasets: Array<{
    label: string
    data: number[]
    borderColor?: string
    backgroundColor?: string
    fill?: boolean
  }>,
  options?: ChartOptions
): ChartConfiguration {
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
 * 常用颜色方案
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
