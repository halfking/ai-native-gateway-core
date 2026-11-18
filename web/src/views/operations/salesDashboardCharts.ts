// salesDashboardCharts.ts — 销售看板图表配置
// 2026-11-19 优化轮：为销售工作台看板添加 ECharts 可视化
import type { EChartsOption } from 'echarts'
import type { SalesDashboard } from '../../api/sales'

/**
 * 生成销售漏斗图配置
 */
export function createFunnelChartOption(
  dashboard: SalesDashboard,
  t: (key: string) => string,
): EChartsOption {
  const funnelOrder = ['draft', 'pending_approval', 'approved', 'sent', 'won', 'lost', 'expired'] as const
  
  const data = funnelOrder.map(k => ({
    name: t(`sales.funnel_${k}`),
    value: dashboard.funnel[k] ?? 0,
  }))

  return {
    tooltip: {
      trigger: 'item',
      formatter: '{b}: {c}',
    },
    series: [
      {
        type: 'funnel',
        left: '10%',
        top: 20,
        bottom: 20,
        width: '80%',
        min: 0,
        max: Math.max(...data.map(d => d.value), 1),
        minSize: '0%',
        maxSize: '100%',
        sort: 'none', // 保持原始顺序
        gap: 2,
        label: {
          show: true,
          position: 'inside',
          formatter: '{b}: {c}',
          fontSize: 12,
        },
        labelLine: {
          length: 10,
          lineStyle: {
            width: 1,
            type: 'solid',
          },
        },
        itemStyle: {
          borderColor: '#fff',
          borderWidth: 1,
        },
        emphasis: {
          label: {
            fontSize: 14,
            fontWeight: 'bold',
          },
        },
        data,
      },
    ],
  }
}

/**
 * 生成毛利分布饼图配置
 */
export function createMarginPieOption(
  dashboard: SalesDashboard,
  t: (key: string) => string,
): EChartsOption {
  const data = Object.entries(dashboard.margin_buckets || {}).map(([bucket, count]) => ({
    name: `${bucket}%`,
    value: count,
  }))

  return {
    tooltip: {
      trigger: 'item',
      formatter: '{b}: {c} ({d}%)',
    },
    legend: {
      orient: 'vertical',
      right: 10,
      top: 'center',
      textStyle: {
        fontSize: 12,
      },
    },
    series: [
      {
        name: t('sales.dashboardMarginBuckets'),
        type: 'pie',
        radius: ['40%', '70%'],
        center: ['40%', '50%'],
        avoidLabelOverlap: true,
        itemStyle: {
          borderRadius: 8,
          borderColor: '#fff',
          borderWidth: 2,
        },
        label: {
          show: false,
          position: 'center',
        },
        emphasis: {
          label: {
            show: true,
            fontSize: 16,
            fontWeight: 'bold',
            formatter: '{b}\n{c}',
          },
        },
        labelLine: {
          show: false,
        },
        data: data.length > 0 ? data : [{ name: t('common.noData'), value: 1 }],
      },
    ],
  }
}

/**
 * 生成丢单原因柱状图配置
 */
export function createLostReasonsBarOption(
  dashboard: SalesDashboard,
  t: (key: string) => string,
): EChartsOption {
  const reasons = dashboard.lost_reasons || []
  
  const data = reasons.map(r => ({
    name: r.reason,
    value: r.count,
  }))

  return {
    tooltip: {
      trigger: 'axis',
      axisPointer: {
        type: 'shadow',
      },
    },
    grid: {
      left: '3%',
      right: '4%',
      bottom: '3%',
      containLabel: true,
    },
    xAxis: {
      type: 'value',
      boundaryGap: [0, 0.01],
    },
    yAxis: {
      type: 'category',
      data: data.map(d => d.name),
      axisLabel: {
        fontSize: 12,
        overflow: 'truncate',
        width: 100,
      },
    },
    series: [
      {
        name: t('sales.dashboardLostReasons'),
        type: 'bar',
        data: data.map(d => d.value),
        itemStyle: {
          borderRadius: [0, 4, 4, 0],
        },
        label: {
          show: true,
          position: 'right',
          formatter: '{c}',
          fontSize: 11,
        },
      },
    ],
  }
}

/**
 * 生成触发维度横向条形图配置
 */
export function createTriggerDimensionsBarOption(
  dashboard: SalesDashboard,
  t: (key: string) => string,
): EChartsOption {
  const dims = dashboard.triggers_by_dimension || {}
  
  const data = Object.entries(dims).map(([dim, count]) => ({
    name: dim,
    value: count,
  }))

  return {
    tooltip: {
      trigger: 'axis',
      axisPointer: {
        type: 'shadow',
      },
    },
    grid: {
      left: '3%',
      right: '4%',
      bottom: '3%',
      containLabel: true,
    },
    xAxis: {
      type: 'value',
    },
    yAxis: {
      type: 'category',
      data: data.map(d => d.name),
      axisLabel: {
        fontSize: 12,
      },
    },
    series: [
      {
        name: t('sales.dashboardTriggerRate'),
        type: 'bar',
        data: data.map(d => d.value),
        itemStyle: {
          borderRadius: [0, 4, 4, 0],
        },
        label: {
          show: true,
          position: 'right',
          formatter: '{c}',
          fontSize: 11,
        },
      },
    ],
  }
}
