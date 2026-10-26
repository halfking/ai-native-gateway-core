export default {
  title: "Tableau de bord",
  refresh: "Actualiser",
  modelsSelectedCount: '{n} modèles sélectionnés',  // R50 ModelPicker compact
tabs: {
    board: 'Board',
    liveStream: 'Flux de requêtes en direct',
    sessionStats: 'Sessions et statistiques',
    selfcheck: 'Auto-vérification',
  },
v2: {
    quickApiKey: 'API Key',
    quickModels: 'Models',
    quickApiKeyTitle: 'View API key ranking',
    quickModelsTitle: 'View model statistics',
    refreshData: 'Refresh data',
    retry: 'Retry',
    reloadAria: 'Reload data',
    totalTokensShort: 'Total Tokens',
    totalCredits: 'Credits charged',
    creditsSub: 'Pricing × tokens',
    creditsDegraded: 'Vue crédits {view} non initialisée: ce chiffre n’est pas fiable',
    summaryDegraded: "Vue agrégée {view} non initialisée: les chiffres de requêtes/tokens/coûts ci-dessous sont indisponibles, pas à zéro. À ne pas utiliser comme conclusion.",
    modelCount: 'Models',
  },
  range: {
    today: "Aujourd'hui",
    last7d: "7 derniers jours",
    last30d: "30 derniers jours",
    last90d: "90 derniers jours"
  },
  tenantLabel: {
    default: "Données du site entier",
    super: "Locataire par défaut",
    tenant: "Locataire : {tenantId}"
  },
  loadError: 'Le chargement des données du tableau de bord a échoué. Veuillez réessayer.',
  board: {
    heroErrors: 'Erreurs',
    heroInput: 'Entrée',
    heroOutput: 'Sortie',
    heroCreditsEq: '≈ {n} crédits',
    drillDown: 'Forer',
    rpmLabel: 'RPM',
    rpmTitle: 'Requêtes par minute (moyenne de la période)',
    tpmLabel: 'TPM',
    tpmTitle: 'Jetons par minute (moyenne de la période)',
    providerCount: '{n} fournisseurs',
    opsRunning: 'En cours',
    opsChecks: 'Sondes (10 min) : {n}',
    opsRate24h: 'Succès 24h {n}',
    opsChecksUnknown: 'contrôles (10 min) : inconnus',
    opsRateUnknown: 'taux de réussite 24 h : inconnu',
    providerCostTitle: 'Coût et approvisionnement des fournisseurs',
    providerDetail: 'Détails',
    providerWindowCost: 'Coût de la période',
    providerCredits: 'Crédits',
    providerBalance: 'Solde',
    providerScore: 'Score {n}',
    providerPlan: 'Forfait',
    providerStatsTitle: 'Statistiques d’usage des fournisseurs',
    providerStatsHint: 'Suit la plage de temps : sélectionnez n’importe quelle ligne pour voir les détails',
    providerExport: 'Exporter Excel',
    filterRange: 'Plage de temps',
    filterGran: 'Granularité',
    granAuto: 'Auto',
    granAutoTitle: 'Granularité alignée automatiquement sur la plage (jour=5m / 7j=15m / 30j=1h) ; manuel en phase 2',
    granHour: 'Par heure',
    granMinutes: '{n} min',
    sourceLabel: 'Source',
    sourcePg: 'Base PG',
    sourceRedis: 'Delta Redis',
    sourceSse: 'SSE direct',
    sourceTitle: 'Source des données du tableau (base PostgreSQL / delta Redis / fusion SSE en direct)',
    modelDistTitle: 'Répartition des modèles',
    modelTop: 'Top {n} de la période',
    colShare: 'Part',
    distTitle: 'Analyse de répartition',
    distTop: 'TOP {n}',
    distOthers: '{n} autres',
    distDrillHint: 'Sélectionnez un type d’erreur → forage par modèle/fournisseur/client',
    /**
     * États vides (2026-10-27 · incrément 14 — « état vide en 5 points »).
     * ★ Avant, il n’y avait qu’un seul `empty: 'No data'` (une expression
     *   **anglaise** dans un pack français), partagé par
     *   **six** cartes différentes (fournisseurs / répartition / modèles /
     *   tendances / usages / les deux cartes de tâches de l’exploitation) —
     *   une phrase pour 6 sens ⇒ l’utilisateur ne pouvait plus dire si c’est
     *   « vraiment vide », « filtré » ou « mal calculé ».
     *   ⇒ désormais on sépare par **le fait de la page**, le kind dans le nom.
     *   · emptyNoResult* = aucun résultat (on a cherché, il n’y en a vraiment pas)
     *   · emptyFailed*    = échec / inconnu (le serveur n’a rien renvoyé ⇒
     *                       on ne peut pas dire « il n’y en a pas »)
     */
    emptyNoResultProviders: 'Sur la période sélectionnée, aucune donnée de fournisseur n’est disponible.',
    emptyNoResultDist: 'Sur la période sélectionnée, aucune donnée ne peut être classée.',
    emptyNoResultDrill: 'Aucune requête n’a échoué avec ce type d’erreur.',
    emptyNoResultModelDist: 'Sur la période sélectionnée, aucune donnée de modèle n’est disponible.',
    emptyNoResultModelTrend: 'Sur la période sélectionnée, aucune tendance de l’usage des modèles n’est disponible.',
    emptyNoResultTrend: 'Sur la période sélectionnée, aucune tendance de l’utilisation n’est disponible.',
    emptyNoResultDiscovery: 'Aucune exécution de découverte de modèles n’a encore été enregistrée.',
    emptyNoResultProbe: 'Aucune exécution d’inspection par sonde n’a encore été enregistrée.',
    bgTasks: 'Background tasks',
    discoveryStatus: 'Model discovery',
    selfcheckTitle: 'Self-check',
    pieClients: 'Types de client',
    pieDegraded: "Indisponible: {reason}. Un classement vide ici signifie « non calculé », pas « aucune donnée ».",
    pieDegradedGeneric: 'La requête du classement n’a pas pu aboutir. Veuillez réessayer.',
    drillFailed: "Impossible de charger ce détail: {reason}. Une liste vide ici ne signifie pas qu’il n’y a aucune erreur.",
    pieClientIp: 'IP client',
    pieIdentity: 'Empreinte d’identité',
    pieErrors: 'Types d’erreur',
    pieTenants: 'Utilisation des tenants',
    metricRequests: 'Requests',
    metricTokens: 'Tokens',
    metricToggle: 'Métrique',
    errorDrill: 'Error drill-down: {kind}',
    drillModel: 'By model',
    drillProvider: 'By provider',
    drillClient: 'By client',
    trendTitle: 'Usage trends',
    trendRequests: 'Requests',
    trendTokens: 'Tokens',
    trendCredits: 'Credits',
    trendCost: 'Cost (USD)',
    trendMore: 'Plus',
    trendMoreTitle: "Ouvrir l'analyse des tendances d'utilisation en pleine page",
    trendOthers: 'Autres',





    rangeCustom: '{start} to {end}',
  },
  stat: {
    totalRequests: "Total des requêtes",
    totalCost: "Coût total",
    successRate: "Taux de réussite",
    avgLatency: "Latence moyenne {n} ms",
    models: "Modèles",
    activeInDays: "Au cours des {days} derniers jours, {n} actifs",
    avgRequestSize: "Taille moyenne de requête",
    avgResponseSize: "Taille moyenne de réponse",
    maxLabel: "Pic",
  },
  table: {
    colModel: "Modèle",
  },
  loading: "Chargement…",
  liveStream: {
    title: "Flux des requêtes en temps réel",
    groupByQueue: "Par file de traitement",
    controlsAria: "Commandes du flux des requêtes en temps réel",
    pause: "Pause",
    resume: "Reprendre",
    legendButton: 'Légende',
    legend: {
      success: "Succès",
      inProgress: "En cours",
      cancelled: 'Cancelled',
    },
    tooltip: {




      statusValue: {
        success: 'Success',
        inProgress: 'In progress',
        cancelled: 'Cancelled by user',
        failure: 'Request failed',
      },

      tokenFormat: '{p} + {c}',
      cacheFormat: 'lecture {r} / écriture {w} (taux {pct})',
    },

    emptyWaiting: 'En attente des données du flux de requêtes en direct…',
    emptyConnecting: 'Connexion au flux en direct en cours, chargement automatique une fois connecté…',
    groupByCredential: 'Par identifiant', groupByProvider: 'Par fournisseur', groupByModel: 'Par modèle',
    modeSmall: 'Petit',
    modeLarge: 'Grand',
    modeSmallTitle: 'Mode petit : barres verticales, contient plus de requêtes (par défaut)',
    modeLargeTitle: 'Mode grand : cartes avec plus de détails sur les requêtes',
    // 2026-08-06 : filtres métier / sonde + titres (aligné avec zh-CN)
    business: 'Métier',
    probe: 'Sonde',
    businessTitle: 'Afficher uniquement les vraies requêtes métier',
    probeTitle: 'Afficher uniquement les requêtes de sonde',
    cacheWindow: 'Cache / fenêtre', connectionDetailTitle: 'Voir les détails de connexion',
    dimensionCredential: 'Identifiant', dimensionVendor: 'Fournisseur', dimensionProvider: 'Fournisseur', dimensionModel: 'Modèle',
    statusOpen: 'Connecté', statusConnecting: 'Connexion en cours', statusReconnecting: 'Reconnexion',
    statusUnsupported: 'Non pris en charge', statusClosed: 'Déconnecté',
    queueStreamConnecting: 'Connexion au flux en direct en cours: cette zone se rafraîchira automatiquement une fois connectée…',
    queueStreamClosed: 'Flux en direct déconnecté, reconnexion automatique en cours…',
    queueStreamUnsupported: 'Ce navigateur ne prend pas en charge les flux en direct (EventSource) ; cette zone ne peut pas recevoir de données en temps réel.',
    queueStreamAwaiting: 'Flux en direct connecté, réception des premières données…',
    sseDetailTitle: 'Détails de connexion SSE', sseStatusLabel: 'État de connexion', sseUrlLabel: 'URL SSE',
    editUrl: 'Modifier', editUrlPlaceholder: 'Entrer l\'URL SSE', save: 'Enregistrer', resetDefault: 'Par défaut',
    cancel: 'Annuler', testConnection: 'Tester la connexion', close: 'Fermer',
    sseTestOk: 'Connexion SSE OK !\nÉtat : connecté\nURL : {url}',
    sseTestFail: 'SSE non connecté\nÉtat : {status}\nURL : {url}',
    redisWarning: 'Redis indisponible : {error}. Les données en direct basculent vers des requêtes DB.',
    redisFallbackError: 'Échec de connexion du service de cache',

    probeDirect: 'Sonde active (directe vers l\'upstream)',

    probeGateway: 'Sonde active (chemin passerelle)',

    probeScheduled: 'Sonde planifiée (planificateur)',

    probeGeneric: 'Requête de sonde',

    probeOriginLabel: 'Origine : {origin}',

    probeAttempt: 'Tentative : n°{n}',

    originGateway: 'Chemin passerelle',

    originScheduled: 'Sonde planifiée',

    originDirect: 'Directe vers l\'upstream',

    idleHeartbeat: 'Espace réservé pour battement de cœur',

    tileIdle: 'Inactif',

    idleUnderOneMin: 'Inactif < 1 min',

    idleMinutes: 'Inactif {n} min',

    idleHours: 'Inactif {h} h',

    idleHoursMinutes: 'Inactif {h} h {m} min',

    filterStatus: 'Statut',
    filterModel: 'Modèle',
    filterGroup: 'Filtrer',
    filterProvider: 'Fournisseur',
    filterVendor: 'Vendor',
    filterAgent: 'Client',  // 2026-08-06 aligné avec zh-CN
    clearFilters: 'Effacer les filtres',
    status: {
      in_progress: 'En cours',
      success: 'Succès',
      failure: 'Échec',
      rate_limited: 'Limité',
    },
  },
  charts: {
    gradeA: "Note A",
    gradeB: "Note B",
    gradeC: "Note C",
    gradeD: "Note D",
    gradeF: "Note F",
    avgScore: "Score moyen",
    healthDistribution: "Distribution de santé",
    totalSessions: "Total {n} sessions",
    newSessions: "Nouvelles sessions",
    activeSessions: "Sessions actives",
    closedSessions: "Sessions fermées",
    costUSD: "Coût (USD)",
    sessionCount: "Nombre de sessions",
    sessionTrend: "Tendance des sessions",
    /** États vides (2026-10-27 · incrément 14) : les deux graphes utilisaient le `dashboard.noData` générique (« Aucune donnée pour cette période »). */
    emptyNoResultHealthGrade: 'Sur la période sélectionnée, aucune distribution de santé n’est disponible.',
    emptyNoResultSessionTrend: 'Sur la période sélectionnée, aucune tendance des sessions n’est disponible.',
  },



  performance: {


    avgLatency: 'Latence moyenne',

  },

  providerUsage: {
    title: 'Utilisation des fournisseurs',
    subtitle: '{period}: consommation de tous les fournisseurs (prêt pour rapprochement)',
    more: 'Plus',
    search: 'Rechercher nom, code ou ID…',
    back: 'Retour à la liste',
    exportAll: 'Exporter tout (Excel)',
    exportDetail: 'Exporter le détail (Excel)',
    colName: 'Fournisseur',
    colCode: 'Code',
    colRequests: 'Requêtes',
    colTokens: 'Jetons',
    colCost: 'Coût (USD)',
    colSuccess: 'Taux de réussite',
    colModel: 'Modèle',
    colDate: 'Date',
    periodDay: 'Quotidien',
    periodWeek: 'Hebdomadaire',
    periodMonth: 'Mensuel',
    periodLabel: 'Période : {period}',
    periodRange: '{start} à {end}',
    modelBreakdown: 'Par modèle',
    dailyBreakdown: 'Répartition quotidienne par modèle',
    /** État vide : aucun enregistrement d’usage (l’état dégradé a son propre texte : `dataLifecycle.usageCost.degraded.title`). */
    emptyNoResult: 'Sur la période sélectionnée, aucun enregistrement d’utilisation n’est disponible.',
  },
  statsRow: {
    totalSessions: "Total Sessions",
    activeSessions: "Active Sessions",
    activeHint: "Active in 24h",
    totalCost: "Total Cost",
    complianceRate: "Compliance Rate",
    avgHealthScore: "Avg Health Score",
    healthHint: "From session health score",
    avgLatency: "Avg Latency",
    totalRequests: "Total Requests",
    totalTokens: "Total Tokens",
  },
  requestTile: {
    childRequestsTitle: 'Sous-requêtes {count} (développer pour voir)',
    childRequestsEmpty: 'Sous-requêtes (aucune donnée reçue, développer pour voir)',
    actionTrailTitle: 'Journal des actions {count} entrées (développer pour voir)',
    actionTrailEmpty: 'Journal des actions (aucune donnée reçue, développer pour voir)',
  },
}
