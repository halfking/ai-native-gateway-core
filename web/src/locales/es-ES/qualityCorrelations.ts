// Auto-synced from en-US (es-ES)
export default {
  title: 'Quality Correlations',
  subtitle: 'Cross-references request features with outcomes (success, latency, quality).',
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
  },
  breakdown: {
    title: 'Breakdown by {by}',
    empty: 'No data — try lowering the days or expanding the filter.',
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
    title: 'What predicts quality? (Pearson correlation)',
    hint: 'Each predictor is correlated with avg_quality across buckets.',
    empty: 'Not enough samples for insights.',
    buckets: '{n} buckets',
    samples: '{n} samples',
  },
}
