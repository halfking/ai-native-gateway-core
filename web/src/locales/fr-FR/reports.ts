// reports.ts — Page de rapport de rapprochement (double vue fournisseurs/interne, ajout i18n R65).
export default {
  // Changement de vue
  providerView: 'Rapprochement fournisseurs',
  internalView: 'Rapprochement interne',
  // Actions de la barre d'outils
  exportExcel: 'Exporter en Excel',
  rerun: 'Recalculer le dernier jour',
  rerunDone: 'Recalcul terminé',
  rerunFailed: 'Échec du recalcul',
  // Couverture / état vide
  daysCovered: 'jours d’instantanés',
  noSnapshots: 'Aucun instantané de rapport sur cette période (la tâche d’agrégation quotidienne génère les données de la veille tôt le matin, ou utilisez « Recalculer le dernier jour » pour les recalculer)',
  // Cartes de synthèse
  requests: 'Requêtes',
  totalTokens: 'Total de tokens',
  in: 'Entrée',
  out: 'Sortie',
  cacheRead: 'Lecture cache',
  cacheWrite: 'Écriture cache',
  providerCost: 'Coût fournisseurs',
  cacheHit: 'Hits de cache',
  internalCredits: 'Crédits internes',
  internalCost: 'Montant interne',
  // Titres des sections groupées
  byProvider: 'Par fournisseur',
  byTenant: 'Par locataire',
  byPerson: 'Par personne',
  byModel: 'Par modèle',
  byDay: 'Par jour',
  // Libellés de colonnes
  provider: 'Fournisseur',
  tenant: 'Locataire',
  person: 'Personne',
  model: 'Modèle',
  date: 'Date',
  success: 'Succès',
  errors: 'Échecs',
  cost: 'Coût',
  errorBreakdown: 'Répartition des erreurs',
}
