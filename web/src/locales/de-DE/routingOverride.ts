// Auto-synced from en-US (de-DE)
export default {
  title: 'Routing Overrides',
  subtitle: 'Modelle für ein Paar aus (task_type, profile) anheften oder sperren. Änderungen greifen innerhalb einer Minute nach dem OverrideStore-Refresh.',
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
    hintPin: 'Pin: Das Modell wird an die Spitze der Kandidaten gezwungen, unabhängig vom Score.',
    hintBan: 'Ban: Das Modell wird vollständig aus den Kandidaten entfernt.',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: 'Kein Override passt zum Filter. „+ Neuer Override“ wählen, um einen anzulegen.',
    deleteConfirm: 'Override #{id} ({mode} {model} für {task}) löschen?',
    deleteFailed: 'Delete failed: ',
  },
}
