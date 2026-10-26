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
    loading: 'Chargement...',
    noData: 'Aucun enregistrement d\'anomalie trouvé',
    viewDetail: 'Détails',
    expectedTokens: 'Attendu : {count}',
    actualTokens: 'Réel : {count}',
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
    loadFailed: 'Le chargement des anomalies de format a échoué. Veuillez réessayer.',
    summaryLoadFailed: 'Le chargement du résumé des anomalies de format a échoué. Veuillez réessayer.',
    markFailed: 'Le marquage des anomalies de format n\'a pas pu aboutir. Veuillez réessayer.',
    needSuperAdmin: 'Permission super-admin requise',
  },

  // 2026-09-21: 请求错误 tab（reqprobe）
  tabs: {
      format: 'Format de réponse',
      request: 'Erreurs de requête',
  },
  requestTab: {
      stats: {
          unresolved: 'Non résolus (page)',
          autoRecovered: 'Auto-récupérés',
          total: 'Total',
      },
      filter: {
          day: 'Date',
          trigger: 'Type',
          triggerPlaceholder: 'Choisir un type…',
      },
      trigger: {
          all: 'Tous les types',
          param_rejected: 'Paramètre rejeté',
          mode_mismatch: 'Mode incompatible',
          upstream_error: 'Erreur amont',
      },
      triggerDesc: {
          param_rejected: 'L\'amont a rejeté un paramètre (ex. reasoning_effort) ; la passerelle l\'a retiré et réessayé',
          mode_mismatch: 'La forme d\'API (responses / chat) ne correspond pas au fournisseur',
          upstream_error: '4xx côté requête non classé, à trier',
      },
      batch: {
          selected: '{n} sélectionnés',
          resolveSelected: 'Résoudre la sélection',
          resolveFiltered: 'Résoudre tout non résolu',
      },
      table: {
          day: 'Date',
          trigger: 'Type',
          param: 'Paramètre / suggestion',
          status: 'Statut',
          occurrences: 'Occurrences',
          recoveredCount: 'Auto-résolu ×{n}',
      },
      detail: {
          title: 'Détail erreur de requête',
          clientModel: 'Modèle client',
          protocol: 'Protocole sortant',
          suggestMode: 'Mode suggéré',
          firstSeen: 'Première occurrence',
          lastSeen: 'Dernière occurrence',
          errorSample: 'Exemple d\'erreur amont',
          occurrences: 'Statistiques',
          recoveredHint: '{n} récupérés automatiquement via retrait de paramètre / bascule de mode',
      },
  },
}
