// formatAnomaliesView.ts — Page de surveillance des anomalies de format (fr-FR).
export default {
  pageTitle: 'Surveillance des anomalies de format',
  pageSubtitle: 'Visualisez rapidement les changements de format de réponse des fournisseurs, les échecs d\'extraction de tokens et les problèmes de compatibilité.',

  stats: {
    total: 'Total des anomalies',
    unresolved: 'Non résolues',
    critical: 'Critiques',
    window: 'Fenêtre de stats',
  },

  filter: {
    provider: 'Fournisseur',
    providerPlaceholder: 'Sélectionner un fournisseur…',
    model: 'Modèle',
    modelPlaceholder: 'Sélectionner un modèle…',
    anomalyType: 'Type d\'anomalie',
    anomalyTypePlaceholder: 'Sélectionner un type d\'anomalie…',
    unresolvedOnly: 'Non résolues uniquement',
    query: 'Rechercher',
    refresh: 'Rafraîchir',
  },

  anomalyType: {
    all: 'Tous les types d\'anomalie',
    missing_usage_block: 'Bloc usage manquant',
    zero_completion_tokens: 'Completion Tokens à 0',
    extraction_failed: 'Échec de l\'extraction',
    unexpected_structure: 'Structure inattendue',
    null_usage_values: 'Valeurs usage nulles',
    token_mismatch: 'Discordance de tokens',
    missing_provider_tokens: 'Tokens fournisseur manquants',
    missing_client_tokens: 'Tokens client manquants',
    json_parse_error: 'Erreur d\'analyse JSON',
    missing_finish_reason: 'Finish Reason manquant',
    missing_content: 'Content manquant',
  },

  anomalyTypeDescription: {
    missing_usage_block: 'Réponse amont sans bloc usage',
    zero_completion_tokens: 'Réponse avec contenu mais completion_tokens à 0',
    extraction_failed: 'Impossible d\'extraire les infos usage de la réponse',
    unexpected_structure: 'Structure retournée par l\'amont incohérente',
    null_usage_values: 'Champs usage présents mais valeurs nulles',
  },

  severity: {
    critical: 'Critique',
    high: 'Élevée',
    medium: 'Moyenne',
    low: 'Faible',
  },

  status: {
    resolved: 'Résolue',
    unresolved: 'Non résolue',
  },

  table: {
    detectedAt: 'Détectée le',
    severity: 'Sévérité',
    anomalyType: 'Type d\'anomalie',
    providerModel: 'Fournisseur / Modèle',
    requestId: 'Request ID',
    tokenInfo: 'Infos tokens',
    status: 'Statut',
    actions: 'Actions',
    loading: 'Chargement...',
    noData: 'Aucun enregistrement d\'anomalie trouvé',
    viewDetail: 'Détails',
    expectedTokens: 'Attendu : {count}',
    actualTokens: 'Réel : {count}',
  },

  token: {
    expected: 'Attendu',
    actual: 'Réel',
  },

  pager: {
    prev: 'Précédent',
    next: 'Suivant',
    summary: 'Page {page} / {totalPages}, {total} enregistrements',
  },

  detail: {
    title: 'Détails de l\'anomalie',
    close: 'Fermer',
    requestId: 'Request ID',
    detectedAt: 'Détectée le',
    provider: 'Fournisseur',
    model: 'Modèle',
    outboundModel: 'Modèle sortant',
    usageSource: 'Usage Source',
    responseStructure: 'Structure de la réponse',
    responseSample: 'Exemple de réponse',
    resolutionNotes: 'Notes de résolution',
    resolutionNotesPlaceholder: 'Enregistrer les notes de correction pour le suivi',
    markResolved: 'Marquer comme résolue',
    processing: 'Traitement...',
    resolutionInfo: 'Informations de résolution',
    noNotes: 'Aucune note de résolution',
  },

  error: {
    loadFailed: 'Échec du chargement',
    summaryLoadFailed: 'Échec du chargement des statistiques',
    markFailed: 'Échec du marquage',
    needSuperAdmin: 'Permission super-admin requise',
  },
}
