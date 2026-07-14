// Auto-synced from en-US (zh-TW)
export default {
  title: 'Routing Overrides Audit',
  subtitle: 'Every change to the routing_overrides table is recorded with actor, action, and row state.',
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
    days: { d1: '1 day', d7: '7 days', d30: '30 days', d90: '90 days' },
    limits: { l50: '50', l200: '200', l500: '500', l1000: '1000' },
  },
  actions: {
    insert: 'Create',
    update: 'Update',
    delete: 'Delete',
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
    before: 'Before',
    after: 'After',
  },

  expand: {
    oldExpires: '變更前 expires_at',
    newExpires: '變更後 expires_at',
    noDiff: '此操作無差異欄位',
  }
}