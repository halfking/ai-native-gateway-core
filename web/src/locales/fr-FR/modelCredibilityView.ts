// modelCredibilityView.ts — Fiabilité des modèles (page fr-FR).
export default {
  pageTitle: 'Fiabilité des modèles',
  pageSubtitle:
    'Détecte si un fournisseur fait passer un plus petit modèle pour un plus grand ' +
    '(substitution, rétrogradation, dilution). Le verdict repose sur quatre mesures indépendantes : ' +
    'le modèle que le fournisseur déclare, la comparaison des capacités avec les autres nœuds du même ' +
    'modèle, la stabilité de la signature de requête et une référence externe. Quand rien ne peut être ' +
    'concluant, on affiche « preuves insuffisantes » — jamais « pas de problème ».',

  stats: {
    total: 'Nœuds évalués',
    verified: 'Vérifiés',
    suspected: 'Suspects',
    diluted: 'Dilués',
    unknown: 'Preuves insuffisantes',
    providers: 'Fournisseurs concernés',
  },

  filter: {
    verdict: 'Conclusion',
    verdictPlaceholder: 'Tous les verdicts',
    model: 'Modèle',
    modelPlaceholder: 'Filtrer par nom de modèle',
    query: 'Rechercher',
    refresh: 'Actualiser',
    scanAll: 'Tout analyser',
    scanning: 'Analyse en cours…',
    sort: 'Tri',
    sortTime: 'Par date de mesure (le plus récent d’abord)',
    sortName: 'Par nom de modèle',
    sortScore: 'Par fiabilité (croissante)',
  },

  verdict: {
    verified: 'Vérifié',
    suspected: 'Suspect',
    diluted: 'Dilué',
    unknown: 'Preuves insuffisantes',
  },

  dimension: {
    identity: 'Identité',
    capability: 'Capacité',
    consistency: 'Stabilité de la signature',
    reference: 'Référence externe',
  },

  table: {
    provider: 'Fournisseur',
    credential: 'Identifiants',
    model: 'Modèle',
    credibility: 'Fiabilité',
    verdict: 'Conclusion',
    dimensions: 'Dimensions étayées',
    measuredAt: 'Mesuré le',
    action: 'Opérations',
  },

  status: {
    success: 'Conforme',
    partial: 'Partiel',
    failed: 'Sonde en échec',
    inconclusive: 'Non concluant',
    unknown: 'Non mesuré',
  },

  anomaly: {
    identity_model_name_mismatch: 'Le fournisseur annonce un modèle différent de celui demandé',
    identity_family_mismatch: 'Le fournisseur annonce un modèle d’un autre fournisseur',
    identity_reported_model_absent: 'Le fournisseur ne renvoie pas de nom de modèle : l’identité n’est pas prouvable',
    capability_below_peer_best: 'Capacité inférieure au meilleur nœud servant le même modèle',
    capability_severely_below_peer_best: 'Capabilité nettement inférieure au meilleur nœud servant le même modèle',
    capability_above_peer_best: 'Capacité supérieure au meilleur nœud servant le même modèle',
    consistency_prompt_token_drift: 'La signature de requête a dérivé : le backend a peut-être été remplacé',
    consistency_system_fingerprint_drift: 'L’empreinte du fournisseur a changé',
    reference_below_expected_accuracy: 'En dessous du niveau attendu par la référence externe',
    capability_no_measurement: 'Aucune mesure de capacité exploitable',
    capability_peer_sample_too_small: 'Trop peu de nœuds de comparaison pour ce modèle',
    identity_probe_failed: 'La sonde d’identité a échoué',
    identity_probe_empty_response: 'La sonde d’identité n’a reçu aucune réponse',
  },

  detail: {
    title: 'Détail du verdict',
    evidence: 'Preuves',
    anomalies: 'Anomalies détectées',
    history: 'Historique des verdicts',
    noHistory: 'Aucun historique de verdict',
    requestedModel: 'Modèle demandé',
    reportedModel: 'Modèle annoncé par le fournisseur',
    promptSignature: 'Signature de requête (nombre de tokens du prompt)',
    fingerprint: 'Empreinte du fournisseur',
    measuredDimensions: 'Dimensions mesurées ce tour',
    close: 'Fermer',
  },

  empty: {
    noData: 'Aucune donnée de fiabilité pour le moment.',
    hint: 'Les verdicts sont produits par l’analyse périodique en arrière-plan (une petite sonde par nœud et par tour) ; « Tout analyser » lance un tour immédiat.',
  },

  error: {
    loadFailed: 'Les données de fiabilité des modèles n\'ont pas pu être chargées. Veuillez réessayer.',
    scanFailed: 'L\'analyse de fiabilité n\'a pas pu être terminée. Veuillez réessayer.',
    detailFailed: 'Le détail du verdict de fiabilité n\'a pas pu être chargé. Veuillez réessayer.',
  },
}
