// routingDefault.ts — RoutingDefaultsView copy (M2, 22 章 §22.6).
// 显式默认路由：为 (task_type, profile, tenant_id) 指定首选/兜底模型。
export default {
  title: 'Routing Defaults',
  subtitle: 'Pin a preferred or fallback model for a (task_type, profile, tenant) triple. Takes effect within ~1 minute via the DefaultRoutingStore refresh. Priority: ban > pin > this default > implicit tag > fallback.',

  scope: {
    platform: 'platform',
  },

  summary: {
    total: 'Total active',
    primary: 'Primary tier',
    tenantScoped: 'Tenant-scoped',
    expiring: 'Expiring in 7d',
  },

  filter: {
    activeOnly: 'Active only',
    taskType: 'Task type',
    taskTypePlaceholder: 'e.g. code, reasoning',
    profile: 'Profile',
    all: '(all)',
    refresh: 'Refresh',
    loading: 'Loading…',
    newDefault: '+ New default',
    cancel: 'Cancel',
    audit: 'Audit log',
  },

  create: {
    title: 'New routing default',
    hint: 'Tenant-scoped rows override platform rows. Profile-specific rows override the generic (any-profile) row at the same scope.',
    taskType: 'Task type *',
    taskTypePlaceholder: 'e.g. code, reasoning, vision',
    profile: 'Profile',
    profileAny: 'any',
    tier: 'Tier',
    model: 'Canonical model *',
    modelPlaceholder: 'e.g. claude-sonnet-4.5',
    tenantId: 'Tenant ID',
    tenantIdPlaceholder: 'empty = platform default',
    priority: 'Priority',
    reason: 'Reason',
    expiresAt: 'Expires at (optional)',
    submit: 'Create',
    submitting: 'Creating…',
    errors: {
      taskTypeRequired: 'Task type is required.',
      modelRequired: 'Canonical model is required.',
    },
  },

  table: {
    taskType: 'Task type',
    profile: 'Profile',
    tier: 'Tier',
    model: 'Model',
    scope: 'Scope',
    priority: 'Priority',
    reason: 'Reason',
    expires: 'Expires',
    expired: 'expired',
    empty: 'No routing defaults configured yet. Click "+ New default" to add one.',
    deleteConfirm: 'Delete default #{id} (model {model} for task {task})?',
    deleteFailed: 'Delete failed: ',
  },

  audit: {
    title: 'Audit log (last 500)',
    refresh: 'Refresh',
    ts: 'Time',
    action: 'Action',
    routingId: 'Routing ID',
    taskType: 'Task type',
    model: 'Model',
    actor: 'Actor',
    reason: 'Reason',
    empty: 'No audit entries yet.',
  },
}
