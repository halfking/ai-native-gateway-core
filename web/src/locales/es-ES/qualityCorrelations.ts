// Auto-synced from en-US (es-ES)
export default {
  title: 'Quality Correlations',
  subtitle: 'Contrasta las características de la petición con los resultados (éxito, latencia, calidad).',
  filter: {
    window: 'Window',
    bucketBy: 'Bucket by',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: '1 día', d7: '7 días', d30: '30 días', d90: '90 días' },
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
    empty: 'Sin datos: reduce los días o amplía el filtro.',
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
    title: '¿Qué predice la calidad? (correlación de Pearson)',
    hint: 'Cada predictor se correlaciona con avg_quality en todos los grupos.',
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: 'Se necesitan al menos 30 muestras para calcular correlaciones con sentido.',

    emptyUnexpected: 'Sin resultados (inesperado). Revisa los registros del servidor.',
  },
}
