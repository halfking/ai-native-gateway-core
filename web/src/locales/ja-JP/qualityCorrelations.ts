// Auto-synced from en-US (ja-JP)
export default {
  title: 'Quality Correlations',
  subtitle: 'リクエストの特徴と結果（成功・レイテンシ・品質）を突き合わせます。',
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
    empty: 'データがありません。日数を減らすか、フィルタ条件を緩めてください。',
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
    title: '何要因が品質を予測するか（ピアソン相関）',
    hint: '各予測要因と、各バケットの平均品質の相関を取ります。',
    empty: 'Not enough samples for insights.',
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: '至少需要 30 个样本才能计算有意义的相关性。',

    emptyUnexpected: '无洞察（异常）。请检查服务端日志。',
  },
}
