// errorPlaybook.ts — Textes du manuel de gestion des erreurs (fr-FR).
//
// ★ Les valeurs doivent différer mot à mot de en-US ; ensemble de clés et
//   signature des placeholders identiques à zh-CN.
export default {
  title: 'Manuel des erreurs',
  subtitle:
    'Les erreurs connues suivent leur plan standard prédéfini ; les erreurs inconnues sont analysées par l’agent PI puis versées au manuel',
  nav: {
    detail: 'Détail de l’entrée',
  },
  stats: {
    totalEntries: 'Entrées du manuel',
    enabledEntries: 'Entrées activées',
    autoExecuteEntries: 'Entrées à exécution auto',
    executions24h: 'Exécutions (24 h)',
    executionsTotal: 'Traces d’exécution cumulées',
    pendingUnknown: 'Erreurs inconnues en attente d’analyse',
    engineState: 'Automatisation du manuel',
    engineOn: 'connectée',
    engineOff: 'non connectée',
    llmOn: 'Canal d’analyse disponible',
    llmOff: 'Canal d’analyse indisponible',
  },
  filter: {
    query: 'Rechercher un titre ou un type d’erreur',
    source: 'Origine de l’entrée',
    enabled: 'État d’activation',
    all: 'Tout',
    enabledOnly: 'Activées seulement',
    disabledOnly: 'Désactivées seulement',
    apply: 'Appliquer les filtres',
  },
  col: {
    title: 'Titre',
    errorType: 'Type d’erreur',
    matchPattern: 'Motif de correspondance',
    planKind: 'Forme du plan',
    source: 'Origine',
    enabled: 'Activé',
    autoExecute: 'Exécution auto',
    confidence: 'Confiance',
    hits: 'Occurrences',
    successRate: 'Succès/Échecs',
    lastStatus: 'Dernier statut d’exécution',
    lastExecutedAt: 'Dernière exécution',
    duration: 'Durée',
  },
  kind: {
    script: 'Étapes de script',
    api: 'Appel API d’admin',
    prompt: 'Prompt IA',
  },
  source: {
    seed: 'Historique distillé',
    manual: 'Saisie manuelle',
    pi_analysis: 'Agent PI',
    refinement: 'Réaffinage après échec',
  },
  status: {
    succeeded: 'Réussi',
    failed: 'Échoué',
    skipped: 'Ignoré',
    awaiting_approval: 'En attente de validation',
    no_action: 'Aucune action requise',
  },
  trigger: {
    standard: 'Mode standard',
    auto_analyze: 'Analyse auto',
    manual: 'Exécution manuelle',
    refinement: 'Réaffinage auto',
  },
  action: {
    refresh: 'Rafraîchir',
    retry: 'Recharger',
    reset: 'Réinitialiser les filtres',
    create: 'Nouvelle entrée',
    save: 'Enregistrer l’entrée',
    savePlan: 'Enregistrer les changements du plan',
    cancel: 'Annuler',
    delete: 'Supprimer l’entrée',
    execute: 'Exécuter maintenant',
    revert: 'Restaurer depuis les snapshots',
    viewDetail: 'Voir le détail',
    back: 'Retour au manuel',
    confirmDelete: 'Confirmer la suppression',
  },
  form: {
    errorType: 'Type d’erreur (obligatoire)',
    errorTypeHint: 'p. ex. auth, rate_limit, context_budget_exceeded',
    matchPattern: 'Motif de correspondance (regex facultative)',
    matchPatternHint:
      'Vide pour une entrée au niveau du type ; 1024 caractères au plus',
    title: 'Titre',
    description: 'Description détaillée',
    rootCause: 'Cause racine',
    plan: 'Plan d’exécution',
    planKind: 'Forme du plan',
    prompt: 'Corps du prompt',
    promptHint:
      'L’instruction complète remise à l’agent PI ou à l’exploitation, jusqu’à 16 Ko',
    expect: 'Résultat attendu',
    expectAnalysis: 'Analyse (lecture seule)',
    expectProposal: 'Proposition (exécutable)',
    method: 'Méthode HTTP',
    path: 'Chemin d’admin',
    pathHint:
      'Doit commencer par /api/admin/ ; seules les routes enregistrées dans l’ensemble fermé peuvent s’exécuter',
    body: 'Corps de requête (JSON facultatif)',
    timeout: 'Délai d’attente (ms, ≤60000)',
    stepsHint:
      '8 étapes au plus ; chaque action doit être une primitive de la liste blanche',
    addStep: 'Ajouter une étape',
    removeStep: 'Retirer',
    stepAction: 'Opération',
    stepParams: 'Paramètres (JSON)',
    stepNote: 'Note (facultative)',
    invalidJsonStep: 'Les paramètres de l’étape {n} ne sont pas un JSON valide',
    invalidJsonBody: 'Le corps de requête n’est pas un JSON valide',
    stepsRequired: 'Un plan de script demande au moins une étape',
    entryEnabled: 'Activer l’entrée',
    entryAutoExecute: 'Autoriser l’exécution auto',
    message: 'Échantillon du message d’erreur (facultatif)',
    confirmDeleteText:
      'Après suppression, le type d’erreur {type} repasse dans le flux d’analyse des erreurs inconnues. Supprimer cette entrée ?',
  },
  exec: {
    timeline: 'Chronologie des traces d’exécution',
    hasSnapshot: 'Avec snapshot de l’ancienne valeur, restaurable',
  },
  banner: {
    validationTitle: 'Enregistrement refusé (validation métier non passée)',
    saveOkTitle: 'Modifications enregistrées',
  },
  empty: {
    firstUseEntries:
      'Le manuel ne contient encore aucune entrée. Utilisez « Nouvelle entrée » pour distiller votre première conduite standard.',
    noResultEntries:
      'Aucune entrée du manuel dans les conditions actuelles. Ajustez les filtres ou créez-en une.',
    filteredEntries:
      'Aucune entrée ne correspond aux filtres actuels. Réinitialisez les filtres pour tout voir.',
    noResultExecutions:
      'Cette entrée n’a pas encore de trace d’exécution. Un lancement manuel en produira une.',
  },
  error: {
    listFailed: 'La liste du manuel n’a pas pu être chargée. Veuillez réessayer.',
    detailFailed: 'Le détail de l’entrée n’a pas pu être chargé. Veuillez réessayer.',
    statsFailed: 'Les statistiques du manuel n’ont pas pu être chargées. Veuillez réessayer.',
    executeFailed: 'Le lancement manuel n’a pas pu aboutir. Veuillez réessayer.',
    execListFailed: 'Les traces d’exécution n’ont pas pu être chargées. Veuillez réessayer.',
    revertFailed:
      'La restauration depuis les snapshots n’a pas pu aboutir. Veuillez réessayer.',
    deleteFailed: 'L’entrée n’a pas pu être supprimée. Veuillez réessayer.',
  },
}
