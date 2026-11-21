<script setup lang="ts">
// AnalyticsTrendChart — ECharts 趋势图表 (2026-11-20)
import { ref, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import type { AnalyticsTrendPoint } from '../../api/board'
import * as echarts from 'echarts'
import type { EChartsOption } from 'echarts'

interface Props {
  trends: AnalyticsTrendPoint[]
}

const props = defineProps<Props>()

const chartRef = ref<HTMLDivElement>()
let chartInstance: echarts.ECharts | null = null

function initChart() {
  if (!chartRef.value || props.trends.length === 0) {
    return
  }

  // 测试环境检查：如果容器没有实际尺寸，跳过初始化
  const rect = chartRef.value.getBoundingClientRect()
  if (rect.width === 0 || rect.height === 0) {
    return
  }

  // 销毁旧实例
  if (chartInstance) {
    // 2026-11-21 审计轮 D-8：re-init 路径必须先 disconnect 旧 observer——
    // 旧 observer observe 的是同一个 DOM 节点，闭包引用模块级 chartInstance，
    // 不 disconnect 会累积 N 个 observer 驱动新实例重复 resize（有界泄漏）。
    const prevObserver = (chartInstance as any).__resizeObserver
    if (prevObserver) {
      prevObserver.disconnect()
    }
    chartInstance.dispose()
  }

  try {
    // 创建新实例
    chartInstance = echarts.init(chartRef.value)

    // 准备数据
    const dates = props.trends.map(d => {
      // 格式化时间戳为易读格式
      const date = new Date(d.bucket)
      return date.toLocaleString('zh-CN', { 
        month: '2-digit', 
        day: '2-digit', 
        hour: '2-digit', 
        minute: '2-digit' 
      })
    })
    
    const requests = props.trends.map(d => d.requests)
    const successRates = props.trends.map(d => 
      d.requests > 0 ? (d.success / d.requests * 100) : 0
    )
    const costs = props.trends.map(d => d.cost_usd)
    const latencies = props.trends.map(d => d.avg_latency_ms)

    const option: EChartsOption = {
      title: {
        show: false
      },
      tooltip: {
        trigger: 'axis',
        axisPointer: {
          type: 'cross'
        },
        formatter: (params: any) => {
          if (!Array.isArray(params)) return ''
          
          let result = `<strong>${params[0].axisValue}</strong><br/>`
          params.forEach((param: any) => {
            const marker = param.marker
            const name = param.seriesName
            const value = param.value
            
            let formattedValue = value
            if (name === '成功率') {
              formattedValue = value.toFixed(1) + '%'
            } else if (name === '花费') {
              formattedValue = '$' + value.toFixed(2)
            } else if (name === '延迟') {
              formattedValue = value.toFixed(0) + 'ms'
            } else {
              formattedValue = value.toLocaleString('en-US')
            }
            
            result += `${marker} ${name}: ${formattedValue}<br/>`
          })
          
          return result
        }
      },
      legend: {
        data: ['请求数', '成功率', '花费', '延迟'],
        top: 10
      },
      grid: {
        left: '3%',
        right: '4%',
        bottom: '3%',
        top: 60,
        containLabel: true
      },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: dates,
        axisLabel: {
          rotate: 45,
          fontSize: 11
        }
      },
      yAxis: [
        {
          type: 'value',
          name: '请求数',
          position: 'left',
          axisLabel: {
            formatter: '{value}'
          }
        },
        {
          type: 'value',
          name: '成功率',
          position: 'right',
          axisLabel: {
            formatter: '{value}%'
          },
          min: 0,
          max: 100,
          offset: 0
        },
        {
          type: 'value',
          name: '花费($)',
          position: 'right',
          axisLabel: {
            formatter: '${value}'
          },
          offset: 60
        },
        {
          type: 'value',
          name: '延迟(ms)',
          position: 'right',
          axisLabel: {
            formatter: '{value}ms'
          },
          offset: 120
        }
      ],
      series: [
        {
          name: '请求数',
          type: 'line',
          yAxisIndex: 0,
          data: requests,
          smooth: true,
          areaStyle: {
            opacity: 0.3
          },
          lineStyle: {
            width: 2
          }
        },
        {
          name: '成功率',
          type: 'line',
          yAxisIndex: 1,
          data: successRates,
          smooth: true,
          lineStyle: {
            width: 2
          }
        },
        {
          name: '花费',
          type: 'line',
          yAxisIndex: 2,
          data: costs,
          smooth: true,
          lineStyle: {
            width: 2
          }
        },
        {
          name: '延迟',
          type: 'line',
          yAxisIndex: 3,
          data: latencies,
          smooth: true,
          lineStyle: {
            width: 2,
            type: 'dashed'
          }
        }
      ]
    }

    chartInstance.setOption(option)

    // 响应式调整
    const resizeObserver = new ResizeObserver(() => {
      chartInstance?.resize()
    })
    resizeObserver.observe(chartRef.value)

    // 保存观察器以便清理
    ;(chartInstance as any).__resizeObserver = resizeObserver
  } catch (err) {
    // 在测试环境或其他异常情况下静默失败
    console.warn('Failed to initialize trend chart:', err)
  }
}

onMounted(async () => {
  await nextTick()
  initChart()
})

onBeforeUnmount(() => {
  if (chartInstance) {
    const resizeObserver = (chartInstance as any).__resizeObserver
    if (resizeObserver) {
      resizeObserver.disconnect()
    }
    chartInstance.dispose()
    chartInstance = null
  }
})

// 监听数据变化重新渲染
watch(() => props.trends, async () => {
  await nextTick()
  initChart()
}, { deep: true })
</script>

<template>
  <div class="trend-chart-container">
    <div v-if="trends.length === 0" class="chart-empty">
      {{ $t('analytics.trend.empty') }}
    </div>
    <div v-else ref="chartRef" class="chart-canvas"></div>
  </div>
</template>

<style scoped>
.trend-chart-container {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
  min-height: 400px;
}

.chart-canvas {
  width: 100%;
  height: 450px;
}

.chart-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 400px;
  color: var(--text-muted);
  font-style: italic;
}

@media (max-width: 768px) {
  .chart-canvas {
    height: 350px;
  }
}
</style>
