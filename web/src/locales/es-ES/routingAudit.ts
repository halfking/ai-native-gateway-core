// Auto-synced from en-US (es-ES)
export default {
  title: 'Routing Overrides Audit',
  subtitle: 'Cada cambio en la tabla routing_overrides se registra con el actor, la acción y el estado de la fila.',
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
    days: { d1: '1 día', d7: '7 días', d30: '30 días', d90: '90 días' },
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
    oldExpires: 'expires_at anterior',
    newExpires: 'expires_at nuevo',
    noDiff: 'Sin campos de diferencia para esta acción',
  }
}