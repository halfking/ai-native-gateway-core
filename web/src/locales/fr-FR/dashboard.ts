export default {
  title: "Tableau de bord",
  refresh: "Actualiser",
tabs: {
    board: 'Board',
    liveStream: 'Flux de requêtes en direct',
    sessionStats: 'Sessions et statistiques',
    selfcheck: 'Auto-vérification',
    systemmonitor: 'Surveillance système',
  },
v2: {
    quickApiKey: 'API Key',
    quickModels: 'Models',
    quickApiKeyTitle: 'View API key ranking',
    quickModelsTitle: 'View model statistics',
    refreshData: 'Refresh data',
    retry: 'Retry',
    reloadAria: 'Reload data',
    degradedViewLabel: 'View',
    degradedHint: 'Data view {view} is not initialized yet. Run the aggregation migration first.',
    emptyTitle: 'No request data yet',
    emptyHint: 'After configuring providers, send a call via /v1/chat/completions to see the live stream here.',
    modelsPage: 'Models',
    totalTokensShort: 'Total Tokens',
    totalCredits: 'Credits charged',
    creditsSub: 'Pricing × tokens',
    modelCount: 'Models',
    sessionCompression: 'Session compression',
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
  loadError: "Échec du chargement",
  board: {
    empty: 'No data',
    bgTasks: 'Background tasks',
    discoveryStatus: 'Model discovery',
    probeChecks: 'Probes (10m)',
    selfcheckTitle: 'Self-check',
    selfcheckLast: 'Last status',
    selfcheckRate: '24h success rate',
    selfcheckHint: 'Click for details →',
    pieClients: 'Client types',
    pieVirtualIp: 'IP client',
    pieIdentity: 'Identity hash',
    pieModels: 'Model usage',
    pieErrors: 'Error types',
    pieTenants: 'Tenant usage',
    pieProviders: 'Provider usage',
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
    filterProvider: 'Provider ID',
    allProviders: 'All',

    rangeCustomTab: 'Custom',

    rangeFrom: 'From',

    rangeTo: 'To',

    rangePicker: 'Statistics period',

    rangeCustom: '{start} to {end}',
  },
  proxyWarning: {
    title: "⚠ Proxy sortant injoignable",
    detail: "Le proxy configuré {proxy} n'a pas pu être contacté — la passerelle est automatiquement passée en mode direct. Les modèles étrangers (Anthropic / OpenAI / OpenRouter / GitHub Copilot, etc.) peuvent échouer.",
    detailHealthy: "Le proxy {proxy} est de nouveau en ligne.",
    hint: "Le système se réactivera automatiquement une fois le proxy rétabli."
  },
  backgroundTasks: {
    title: "Tâches en arrière-plan en cours",
    running: "Découverte de modèles ({trigger})",
    startedAt: "Démarré {time}",
    heartbeat: "Battement {time}",
    slow: "Les pages d'admin peuvent ralentir",
    detailsLink: "Voir les détails"
  },
  discovery: {
    latest: "Dernière découverte de modèles : ",
    failuresTitle: "Découverte de modèles · échecs au cours des 6 dernières heures",
    failuresTally: "{n} échecs · {m} modèles",
    summary: "Voir la liste des échecs",
    meta: "{n} fois · {m} identifiants concernés · dernier {date} · erreur {code}"
  },
  stat: {
    totalRequests: "Total des requêtes",
    inLastDays: "Au cours des {days} derniers jours",
    totalTokens: "Utilisation totale de tokens",
    prompt: "Prompt {n}",
    completion: "Complétion {n}",
    totalCost: "Coût total",
    costUnit: "USD",
    successRate: "Taux de réussite",
    avgLatency: "Latence moyenne {n} ms",
    apiKeys: "Clés API intégrées",
    enabledActive: "Activées {enabled} · actives {active}",
    models: "Modèles",
    activeInDays: "Au cours des {days} derniers jours, {n} actifs",
    providers: "Fournisseurs / identifiants",
    enabledCredentials: "Activés {enabled} · identifiants {total}",
    offline: "Ressources hors ligne",
    modelsCredentials: "Modèles {models} · identifiants {creds}",
    // 2026-08-06 : Taille moyenne de requête / Taille moyenne de réponse / Pic (aligné avec zh-CN)
    avgRequestSize: "Taille moyenne de requête",
    avgResponseSize: "Taille moyenne de réponse",
    maxLabel: "Pic",
  },
  table: {
    hotKeysTitle: "Classement des clés API à forte utilisation",
    byModelTitle: "Par modèle",
    colKey: "Clé",
    colApplication: "Application",
    colOwner: "Propriétaire",
    colRequests: "Requêtes",
    colTokens: "Utilisation de tokens",
    colCost: "Coût (USD)",
    colLastUsed: "Dernière utilisation",
    colModel: "Modèle",
    colProvider: "Fournisseur"
  },
  compression: {
    title: "🤖 Compression de session",
    delta: "Delta",
    sliding: "Glissante",
    outboundTokens: "≈ {n} tokens sortants"
  },
  empty: {
    firstUse: "🚀 Aucune donnée de requête pour l'instant. Après avoir configuré les fournisseurs, envoyez un appel via /v1/chat/completions pour voir les données ici."
  },
  loading: "Chargement…",
  noData: "Aucune donnée pour cette période",
  costSuffix: "USD",
  viewFailedRequests: "Voir les requêtes échouées",
  liveStream: {
    title: "Flux des requêtes en temps réel",
    groupByQueue: "Par file de traitement",
    controlsAria: "Commandes du flux des requêtes en temps réel",
    connected: "Connecté",
    disconnected: "Reconnexion...",
    pause: "Pause",
    resume: "Reprendre",
    filterAll: "Tous les statuts",
    filterSuccess: "Succès seulement",
    filterFailure: "Échecs seulement",
    filterInProgress: "En cours",
    filterGroupFailures: "Détail des échecs",
    filterFailure5xx: "Serveur / amont (5xx)",
    filterFailure4xx: "Client / auth (4xx)",
    filterFailureTimeout: "Délai / réseau",
    filterFailureNotFound: "Routage / modèle introuvable",
    filterFailureOther: "Autres échecs",
    idleLabel: "Inactif {duration}",
    countTooltip: "{buffer} en mémoire / {visible} visibles",
    countAria: "{buffer} requêtes en mémoire, {visible} visibles",
    legendButton: 'Légende',
    legend: {
      title: "Légende",
      model: "Famille de modèle",
      status: "Statut",
      openai: "OpenAI",
      anthropic: "Anthropic",
      domestic: "Local",
      oss: "Open source",
      other: "Autre",
      success: "Succès",
      inProgress: "En cours",
      failure: "Échec"
,
      cancelled: 'Cancelled',
    },
    tooltip: {
      model: "Modèle",
      provider: "Fournisseur",
      status: "Statut",
      latency: "Latence",
      tokens: "Tokens",
      cost: "Coût",
      error: "Erreur",
      time: "Heure"
,
      vendor: 'Vendor',

      requestId: 'ID',

      errorKind: 'Error type',

      errorRawCode: 'Raw code',

      statusValue: {
        success: 'Success',
        inProgress: 'In progress',
        cancelled: 'Cancelled by user',
        idle: 'Idle (heartbeat placeholder)',
        failure: 'Request failed',
      },

      tokenFormat: '{p} + {c}',
    },
    connecting: "Connexion…",
    reconnecting: "Reconnexion…",
    unsupported: "Le flux en direct n'est pas pris en charge dans ce navigateur",
    empty: "Aucune requête en temps réel",

    emptyWaiting: 'En attente des données du flux de requêtes en direct…',
    groupByCredential: 'Par identifiant', groupByVendor: 'Par fournisseur', groupByProvider: 'Par fournisseur', groupByModel: 'Par modèle',
    modeSmall: 'Petit',
    modeLarge: 'Grand',
    modeSmallTitle: 'Mode petit : barres verticales, contient plus de requêtes (par défaut)',
    modeLargeTitle: 'Mode grand : cartes avec plus de détails sur les requêtes',
    probeAll: 'Tous', probeOnly: 'Sondes uniquement',
    probeAllTitle: 'Afficher toutes les requêtes (par défaut)', probeOnlyTitle: 'Afficher uniquement les requêtes de sonde',
    // 2026-08-06 : filtres métier / sonde + titres (aligné avec zh-CN)
    business: 'Métier',
    probe: 'Sonde',
    businessTitle: 'Afficher uniquement les vraies requêtes métier',
    probeTitle: 'Afficher uniquement les requêtes de sonde',
    cacheWindow: 'Cache / fenêtre', connectionDetailTitle: 'Cliquer pour les détails de connexion',
    dimensionCredential: 'Identifiant', dimensionVendor: 'Fournisseur', dimensionProvider: 'Fournisseur', dimensionModel: 'Modèle',
    statusOpen: 'Connecté', statusConnecting: 'Connexion en cours', statusReconnecting: 'Reconnexion',
    statusUnsupported: 'Non pris en charge', statusClosed: 'Déconnecté',
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

    idleReasonNoTraffic: 'Aucun trafic (5 min d\'inactivité)',
    // 2026-07-24: Multi-dimension filters
    filterStatus: 'Statut',
    filterAllOptions: 'Tous',
    filterEmpty: 'Aucune option disponible',
    filterModel: 'Modèle',
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
    vendor: {
      openai: 'OpenAI',
      anthropic: 'Anthropic',
      domestic: 'Local',
      oss: 'Open source',
      other: 'Autre',
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
    sessionTrend: "Tendance des sessions"
  },

  moduleStats: {
    executions: 'Exécutions',
    cacheHitRate: 'Taux de succès du cache',
    rate: 'Taux',
    totalModules: 'Total des modules',
    totalExecutions: 'Total des exécutions',
    avgCacheHitRate: 'Taux de succès moyen du cache',
    avgDuration: 'Durée moyenne',

    title: 'Statistiques d\'exécution des modules',

    successRate: 'Taux de réussite',
  },

  errors: {
    trendTitle: 'Tendance des erreurs',
    errorCount: 'Nombre d\'erreurs',
    totalCount: 'Total des requêtes',
    errorRate: 'Taux d\'erreur',
    count: 'Nombre',
    rate: 'Taux',
    totalErrors: 'Total des erreurs',
    topErrors: 'Principales erreurs',

    title: 'Statistiques d\'erreurs',

    totalRequests: 'Total des requêtes',

    avgLatency: 'Latence moyenne des erreurs',
  },

  performance: {
    throughput: 'Débit',
    requests: 'Requêtes',
    p50: 'Latence P50',
    p95: 'Latence P95',
    p99: 'Latence P99',
    latencyDist: 'Distribution de latence',
    slowQueries: 'Requêtes lentes',

    title: 'Métriques de performance',

    avgLatency: 'Latence moyenne',

    latency: 'Latence',
  },

  providerUsage: {
    title: 'Utilisation des fournisseurs',
    subtitle: '{period} — consommation de tous les fournisseurs (prêt pour rapprochement)',
    periodHint: 'Période actuelle : {period}',
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
}
