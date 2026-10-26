// Auto-synced from en-US (es-ES)
export default {
  title: 'Routing Overrides',
  subtitle: 'Fija o bloquea modelos concretos para un par (task_type, profile). Los cambios se aplican en menos de un minuto al refrescar OverrideStore.',
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
    hintPin: 'Pin: el modelo se fuerza al principio de los candidatos, gane o pierda la puntuación.',
    hintBan: 'Ban: el modelo queda excluido por completo de los candidatos.',
    errors: {
      taskTypeRequired: 'task_type is required',
      reasonRequired: 'reason is required (audit trail)',
      banRequiresModel: 'ban mode requires model_chosen',
    },
  },
  table: {
    title: 'Overrides ({n})',
    empty: 'Ninguna anulación coincide con el filtro. Pulsa "+ Nueva anulación" para crear una.',
    deleteConfirm: '¿Eliminar la anulación #{id} ({mode} {model} para {task})?',
    deleteFailed: 'Delete failed: ',
  },
}
