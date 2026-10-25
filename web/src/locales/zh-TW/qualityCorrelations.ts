// Auto-synced from en-US (zh-TW)
export default {
  title: 'Quality Correlations',
  subtitle: '把請求特徵與結果（成功、延遲、品質）交叉比對。',
  filter: {
    window: 'Window',
    bucketBy: 'Bucket by',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: '1 天', d7: '7 天', d30: '30 天', d90: '90 天' },
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
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: '至少需要 30 個樣本才能計算有意義的相關性。',

    emptyUnexpected: '無洞察（異常）。請檢查服務端日誌。',
  },
}
