// Auto-synced from en-US (zh-TW)
export default {
  title: 'Quality Correlations',
  subtitle: '把請求特徵與結果（成功、延遲、品質）交叉比對。',
  filter: {
    window: 'Window',
    bucketBy: 'Bucket by',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: '1 day', d7: '7 days', d30: '30 days', d90: '90 days' },
    by: {
      prompt_length: 'Prompt length',
      tools: 'Tool count',
      images: 'Has image',
      code_block: 'Has code block',
    },
  },
  meta: {
    generated: 'Generated',
    window: 'Window',
    totalSamples: 'Total samples',
    needSamples: 'Need ≥ 30 samples for insights',

    windowDays: '{n} 天',
  },
  breakdown: {
    title: 'Breakdown by {by}',
    empty: '沒有資料 — 試著減少天數或放寬篩選條件。',
    headers: {
      bucket: 'Bucket',
      samples: 'Samples',
      success: 'Success',
      latency: 'Latency',
      quality: 'Quality',
      cost: 'Cost',
    },
  },
  insights: {
    title: '什麼在預測品質？（皮爾森相關）',
    hint: '每個預測因子都與各分桶的平均品質做相關性分析。',
    empty: 'Not enough samples for insights.',
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: '至少需要 30 个样本才能计算有意义的相关性。',

    emptyUnexpected: '无洞察（异常）。请检查服务端日志。',
  },
}
