// Auto-synced from en-US (ar-SA)
export default {
  title: 'Routing Overrides Audit',
  subtitle: 'كل تعديل في تجاوز التوجيه يُسجَّل مع المنفّذ والإجراء وحالة الصف.',
  summary: {
    total: 'Total',
    inserts: 'Inserts',
    updates: 'Updates',
    deletes: 'Deletes',
  },
  filter: {
    action: 'Action',
    all: '(all)',
    actor: 'Actor',
    actorPlaceholder: 'admin username',
    overrideId: 'Override ID',
    overrideIdPlaceholder: 'e.g. 42',
    window: 'Window',
    limit: 'Limit',
    refresh: 'Refresh',
    loading: 'Loading…',
    days: { d1: 'يوم واحد', d7: '7 أيام', d30: '30 يومًا', d90: '90 يومًا' },
    limits: { l50: '50', l200: '200', l500: '500', l1000: '1000' },
  },
  actions: {
    insert: 'Create',
  },
  table: {
    title: 'Audit entries ({n})',
    empty: 'No audit entries match the filter.',
    headers: {
      when: 'When',
      action: 'Action',
      override: 'Override',
      taskProfileMode: 'Task / Profile / Mode',
      model: 'Model',
      reason: 'Reason',
      actor: 'Actor',
    },
    details: 'Details',
  },

  expand: {
    oldExpires: 'expires_at قبل التغيير',
    newExpires: 'expires_at بعد التغيير',
    noDiff: 'لا حقول فرق لهذه العملية',
  }
}