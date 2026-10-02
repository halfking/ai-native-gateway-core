// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: 'Actualiser',
    refreshing: 'Actualisation…',
    loadFailed: 'Échec du chargement',
    noData: 'Aucune donnée de profil',
  registry: {
    version: 'Version du registre',
    types: 'Types de tâches',
    correctedTotal: 'Corrections sur la période',
    pendingSuggestions: 'Changements de palier en attente',
  },
  table: {
    taskType: 'Type de tâche',
    description: 'Description',
    tier: 'Tier',
    fallbacks: 'Chaîne de repli',
    minConf: 'Confiance min.',
    total: 'Corrections',
    rate: 'Taux de correction',
    suggestion: 'Tier suggéré',
  },
  action: {
    apply: 'Appliquer les suggestions',
    applyConfirm: 'Écrire les suggestions de tier pilotées par les corrections dans la config de tier (task_type_tier_config) ?',
    applyDone: 'Appliqué : {types}',
    applyNone: 'Aucune suggestion n’atteint actuellement le seuil',
    reload: 'Recharger l’overlay',
    reloadConfirm: 'Recharger le fichier de profil TASKPROFILE_OVERLAY ? Réinitialise aux valeurs par défaut intégrées si non configuré.',
    reloadDone: 'Registre rechargé : {version}',
    exportCsv: 'Exporter les corrections (CSV)',
    days: 'Fenêtre (jours)',
  },
  status: {
    applying: 'Application…',
    reloading: 'Rechargement…',
    exporting: 'Exportation…',
  },
}
