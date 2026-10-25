// Auto-synced from en-US (de-DE)
export default {
  title: 'Quality Correlations',
  subtitle: 'Stellt Request-Merkmale den Ergebnissen gegenüber (Erfolg, Latenz, Qualität).',
  filter: {
    window: 'Window',
    bucketBy: 'Bucket by',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: '1 Tag', d7: '7 Tage', d30: '30 Tage', d90: '90 Tage' },
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

    windowDays: '{n} days',
  },
  breakdown: {
    title: 'Breakdown by {by}',
    empty: 'Keine Daten. Reduzieren Sie die Tage oder erweitern Sie den Filter.',
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
    title: 'Was sagt die Qualität voraus? (Pearson-Korrelation)',
    hint: 'Jeder Prädiktor wird mit der avg_quality über die Buckets korreliert.',
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: 'Für aussagekräftige Korrelationen werden mindestens 30 Stichproben benötigt.',

    emptyUnexpected: 'Keine Erkenntnisse (unerwartet). Prüfen Sie die Server-Logs.',
  },
}
