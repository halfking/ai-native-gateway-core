// Auto-synced from en-US (ja-JP)
export default {
  title: 'Routing Overrides Audit',
  subtitle: 'ルーティング上書きの変更ごとに、操作者・操作内容・行の状態が記録されます。',
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
    days: { d1: '1日間', d7: '7日間', d30: '30日間', d90: '90日間' },
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
    oldExpires: '変更前 expires_at',
    newExpires: '変更後 expires_at',
    noDiff: 'この操作に差分フィールドはありません',
  }
}