// Auto-synced from en-US (de-DE)
export default {
  title: 'Routing Overrides Audit',
  subtitle: 'Jede Routing-Override-Änderung wird mit Akteur, Aktion und Zeilenzustand protokolliert.',
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
    days: { d1: '1 Tag', d7: '7 Tage', d30: '30 Tage', d90: '90 Tage' },
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
    oldExpires: 'expires_at vorher',
    newExpires: 'expires_at nachher',
    noDiff: 'Keine Diff-Felder für diese Aktion',
  }
}