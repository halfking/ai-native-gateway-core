// Auto-synced from en-US (ja-JP)
export default {
  title: 'Quality Correlations',
  subtitle: 'リクエストの特徴と結果（成功・レイテンシ・品質）を突き合わせます。',
  filter: {
    window: 'Window',
    bucketBy: 'Bucket by',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: '1日間', d7: '7日間', d30: '30日間', d90: '90日間' },
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

    windowDays: "{n}日",
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
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: "意味のある相関を算出するには、少なくとも 30 サンプル必要です。",

    emptyUnexpected: "インサイトがありません（異常）。サーバーログを確認してください。",
  },
}
