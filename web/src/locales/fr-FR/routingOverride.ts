// Auto-synced from en-US (fr-FR)
export default {
  title: 'Routing Overrides',
  subtitle: 'Épinglez ou bannissez des modèles précis pour une paire (task_type, profile). Les changements prennent effet sous une minute, au rafraîchissement d\'OverrideStore.',
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
    hintPin: 'Pin : le modèle est forcé en tête des candidats, quel que soit son score.',
    hintBan: 'Ban : le modèle est totalement exclu des candidats.',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: 'Aucune surcharge ne correspond au filtre. Utilisez « + Nouvelle surcharge » pour en créer une.',
    deleteConfirm: 'Supprimer la surcharge #{id} ({mode} {model} pour {task}) ?',
    deleteFailed: 'Delete failed: ',
  },
}
