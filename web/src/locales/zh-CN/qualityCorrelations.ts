// qualityCorrelations.ts — QualityCorrelationsView 文案。
export default {
  title: '质量关联分析',
  subtitle: '交叉分析请求特征（提示词长度、工具数、图片、代码块）与结果（成功率、延迟、质量），快速发现导致失败的因素。',
  filter: {
    window: '时间窗口',
    bucketBy: '分桶维度',
    refresh: '刷新',
    loading: '加载中…',
    days: { d1: '1 天', d7: '7 天', d30: '30 天', d90: '90 天' },
    by: {
      prompt_length: '提示词长度',
      tools: '工具数量',
      images: '含图片',
      code_block: '含代码块',
    },
  },
  meta: {
    generated: '生成时间',
    window: '窗口',
    windowDays: '{n} 天',
    totalSamples: '总样本',
    needSamples: '洞察需要 ≥ 30 个样本',
  },
  breakdown: {
    title: '按 {by} 分解',
    empty: '暂无数据 — 尝试缩短时间窗口或放宽筛选。',
    headers: {
      bucket: '分桶',
      samples: '样本',
      success: '成功率',
      latency: '延迟',
      quality: '质量',
      cost: '成本',
    },
  },
  insights: {
    title: '什么预测质量？（Pearson 相关）',
    hint: '每个预测因子与各分桶的平均质量相关。|r| 越大，该特征对质量的预测力越强。',
    empty: '样本不足，无法生成洞察。',
    emptyInsufficient: '至少需要 30 个样本才能计算有意义的相关性。',
    emptyUnexpected: '无洞察（异常）。请检查服务端日志。',
    buckets: '{n} 个分桶',
    samples: '{n} 个样本',
  },
}
