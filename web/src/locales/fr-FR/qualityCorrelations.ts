// Auto-synced from en-US (fr-FR)
export default {
  title: 'Quality Correlations',
  subtitle: 'Croise les caractéristiques des appels avec les résultats (succès, latence, qualité).',
  filter: {
    window: 'Window',
    bucketBy: 'Bucket by',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: '1 jour', d7: '7 jours', d30: '30 jours', d90: '90 jours' },
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
    empty: 'Aucune donnée. Réduisez le nombre de jours ou élargissez le filtre.',
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
    title: 'Quoi prédit la qualité ? (corrélation de Pearson)',
    hint: 'Chaque prédicteur est corrélé avec avg_quality sur les seaux.',
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: 'Il faut au moins 30 échantillons pour calculer des corrélations significatives.',

    emptyUnexpected: 'Aucun résultat (inattendu). Consultez les journaux du serveur.',
  },
}
