// Auto-synced from en-US (fr-FR)
export default {
  title: 'Routing Overrides Audit',
  subtitle: 'Chaque modification de la table routing_overrides est enregistrée avec l\'auteur, l\'action et l\'état de la ligne.',
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
    days: { d1: '1 jour', d7: '7 jours', d30: '30 jours', d90: '90 jours' },
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
    oldExpires: 'expires_at avant',
    newExpires: 'expires_at après',
    noDiff: 'Aucun champ de diff pour cette action',
  }
}