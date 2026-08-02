// modelIntegrityView.ts — Page de surveillance de l'intégrité des modèles (fr-FR).
export default {
  pageTitle: "Surveillance de l'intégrité des modèles",
  pageSubtitle: "Suivez la substitution de modèle, la troncature des réponses, les réponses vides, le contenu répété et la dérive d'empreinte.",

  tabs: {
    events: "Événements d'intégrité",
    drift: "Dérive d'empreinte",
  },

  stats: {
    total: 'Total des événements',
    unresolved: 'Non résolues',
    critical: 'Critiques',
    window: 'Fenêtre de stats',
  },

  filter: {
    provider: 'Fournisseur',
    providerPlaceholder: 'Sélectionner un fournisseur…',
    model: 'Modèle',
    modelPlaceholder: 'Sélectionner un modèle…',
    anomalyType: "Type d'anomalie",
    anomalyTypePlaceholder: "Sélectionner un type d'anomalie…",
    severity: 'Sévérité',
    unresolvedOnly: 'Non résolues uniquement',
    query: 'Rechercher',
    refresh: 'Rafraîchir',
  },

  anomalyType: {
    all: "Tous les types d'anomalie",
    model_mismatch: 'Modèle non concordant',
    finish_refusal: 'Refus / Filtre de contenu',
    finish_truncation: 'Troncature de fin',
    token_arith_fail: 'Échec arithmétique des tokens',
    empty_response: 'Réponse vide',
    repeated_content: 'Contenu répété',
    fingerprint_drift: "Dérive d'empreinte",
  },

  anomalyTypeDescription: {
    model_mismatch: "L'amont a renvoyé un modèle ne correspondant pas à celui demandé (substitution silencieuse possible)",
    finish_refusal: "L'amont a renvoyé refusal ou content_filter",
    finish_truncation: 'Tronqué par longueur / max_tokens',
    token_arith_fail: "prompt + completion n'égale pas total",
    empty_response: "Le flux n'a produit ni contenu ni tokens",
    repeated_content: 'Le texte de réponse contient de grands blocs répétés (boucle de modèle possible)',
    fingerprint_drift: 'system_fingerprint a dérivé de la référence',
  },

  severity: {
    all: 'Toutes les sévérités',
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
    anomalyType: "Type d'anomalie",
    providerModel: 'Fournisseur / Modèle',
    requestId: 'Request ID',
    actual: 'Réel',
    status: 'Statut',
    actions: 'Actions',
    loading: 'Chargement...',
    noData: "Aucun événement d'intégrité trouvé",
    viewDetail: 'Détails',
  },

  drift: {
    days: 'Jours',
    query: 'Rechercher',
    noData: "Aucune dérive d'empreinte détectée dans la fenêtre sélectionnée",
  },

  pager: {
    prev: 'Précédent',
    next: 'Suivant',
    summary: 'Page {page} / {totalPages}, {total} enregistrements',
  },

  detail: {
    title: "Détails de l'événement d'intégrité",
    close: 'Fermer',
    requestId: 'Request ID',
    detectedAt: 'Détectée le',
    provider: 'Fournisseur',
    model: 'Modèle',
    outboundModel: 'Modèle sortant',
    credential: 'ID de credential',
    expected: 'Attendu',
    actual: 'Réel',
    context: 'Contexte',
    sample: 'Exemple',
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
    driftLoadFailed: "Échec du chargement de la dérive d'empreinte",
    markFailed: 'Échec du marquage',
    needSuperAdmin: 'Permission super-admin requise',
  },
}
