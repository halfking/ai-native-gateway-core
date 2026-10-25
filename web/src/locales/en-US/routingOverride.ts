// routingOverride.ts — RoutingOverrideView copy.
export default {
  title: 'Routing Overrides',
  subtitle: 'Pin or ban specific models for a (task_type, profile) pair. Changes take effect within 1 minute via the OverrideStore refresh.',
  correlationsLink: 'Correlations dashboard',
  summary: {
    total: 'Total active',
    bans: 'Bans',
    pins: 'Pins',
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
    newOverride: '+ New override',
    cancel: 'Cancel',
  },
  create: {
    title: 'New routing override',
    hintPin: 'Pin: model is forced to top of candidates (wins regardless of score).',
    hintBan: 'Ban: model is excluded from candidates entirely.',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: 'No overrides match the filter. Click "+ New override" to create one.',
    deleteConfirm: 'Delete override #{id} ({mode} {model} for {task})?',
    deleteFailed: 'Delete failed: ',
  },
}
