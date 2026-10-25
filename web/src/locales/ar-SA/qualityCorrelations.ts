// Auto-synced from en-US (ar-SA)
export default {
  title: 'Quality Correlations',
  subtitle: 'يقارن خصائص الطلب بالنتائج (النجاح، زمن الاستجابة، الجودة).',
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

    windowDays: '{n} days',
  },
  breakdown: {
    title: 'Breakdown by {by}',
    empty: 'لا توجد بيانات. قلّل عدد الأيام أو وسّع نطاق التصفية.',
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
    title: 'ما الذي يتنبأ بالجودة؟ (ارتباط بيرسون)',
    hint: 'كل عامل تنبؤ يُقارن مع avg_quality عبر المجموعات.',
    empty: 'Not enough samples for insights.',
    buckets: '{n} buckets',
    samples: '{n} samples',

    emptyInsufficient: 'يلزم 30 عينة على الأقل لحساب ارتباطات ذات معنى.',

    emptyUnexpected: 'لا توجد نتائج (غير متوقع). راجع سجلات الخادم.',
  },
}
