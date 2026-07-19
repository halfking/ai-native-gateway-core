// routingDefault.ts — smart routing config (default routing) copy
export default {
  title: 'Smart routing',
  subtitle: 'Configure primary / secondary / fallback models per task type. Changes apply within ~1 minute.',
  scope: {
    platform: 'Platform',
  },
  rail: {
    all: 'All',
    allHint: 'Show all task types',
  },
  tiers: {
    primary: 'Primary',
    secondary: 'Secondary',
    fallback: 'Fallback',
  },
  profiles: {
    any: 'Any',
    smart: 'Smart',
    speed_first: 'Speed',
    cost_first: 'Cost',
  },
  actions: {
    addModel: '+ Add model',
    detail: 'Details',
    delete: 'Delete',
    save: 'Save',
    saving: 'Saving…',
    cancel: 'Cancel',
    refresh: 'Refresh',
    loading: 'Loading…',
  },
  fields: {
    model: 'Model',
    profile: 'Preference',
    priority: 'Priority',
    platform: 'Platform / tenant',
    reason: 'Reason',
    expires: 'Expires',
    tier: 'Tier',
    taskType: 'Task type',
  },
  empty: {
    group: 'No models in this tier. Click “Add model” to configure.',
    needTask: 'Select a task type on the left before adding models.',
    none: 'No default routes configured yet.',
  },
  create: {
    title: 'Add model to “{tier}”',
    modelRequired: 'Model is required',
    taskRequired: 'Select a task type first',
    submit: 'Add',
    submitting: 'Adding…',
  },
  detail: {
    title: 'Edit default route #{id}',
    clearExpires: 'Clear expiry (never expires)',
  },
  table: {
    deleteConfirm: 'Delete default #{id} (model {model}, task {task})?',
    deleteFailed: 'Delete failed: ',
    saveFailed: 'Save failed: ',
  },
  filter: {
    activeOnly: 'Active only',
  },
}
