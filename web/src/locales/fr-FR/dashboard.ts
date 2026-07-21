export default {
  title: "Tableau de bord",
  refresh: "Actualiser",
tabs: {
    board: 'Board',
    liveStream: 'Flux de requêtes en direct',
    sessionStats: 'Sessions et statistiques',
    selfcheck: 'Surveillance système',
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
    modelsCredentials: "Modèles {models} · identifiants {creds}"
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

    emptyWaiting: 'Waiting for live request stream data…',
    groupByVendor: 'By vendor', groupByProvider: 'By provider', groupByModel: 'By model',
    probeAll: 'All', probeOnly: 'Probes only',
    probeAllTitle: 'Show all requests (default)', probeOnlyTitle: 'Show probe requests only',
    cacheWindow: 'Cache / window', connectionDetailTitle: 'Click for connection details',
    dimensionVendor: 'Vendor', dimensionProvider: 'Provider', dimensionModel: 'Model',
    statusOpen: 'Connected', statusConnecting: 'Connecting', statusReconnecting: 'Reconnecting',
    statusUnsupported: 'Unsupported', statusClosed: 'Disconnected',
    sseDetailTitle: 'SSE connection details', sseStatusLabel: 'Status', sseUrlLabel: 'SSE URL',
    editUrl: 'Edit', editUrlPlaceholder: 'Enter SSE URL', save: 'Save', resetDefault: 'Default',
    cancel: 'Cancel', testConnection: 'Test connection', close: 'Close',
    sseTestOk: 'SSE connection OK!\nStatus: connected\nURL: {url}',
    sseTestFail: 'SSE not connected\nStatus: {status}\nURL: {url}',
    redisWarning: 'Redis unavailable: {error}. Live data falls back to DB queries.',
    redisFallbackError: 'Cache service connection failed',

    probeDirect: 'Active probe (direct upstream)',

    probeGateway: 'Active probe (gateway path)',

    probeScheduled: 'Scheduled probe (scheduler)',

    probeGeneric: 'Probe request',

    probeOriginLabel: 'Origin: {origin}',

    probeAttempt: 'Attempt: #{n}',

    originGateway: 'Gateway path',

    originScheduled: 'Scheduled probe',

    originDirect: 'Direct upstream',

    idleHeartbeat: 'Heartbeat placeholder',

    tileIdle: 'Idle',

    idleUnderOneMin: 'Idle < 1 min',

    idleMinutes: 'Idle {n} min',

    idleHours: 'Idle {h} h',

    idleHoursMinutes: 'Idle {h} h {m} min',

    idleReasonNoTraffic: 'No traffic (5 minutes idle)',
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
    executions: 'Executions',
    cacheHitRate: 'Cache Hit Rate',
    rate: 'Rate',
    totalModules: 'Total Modules',
    totalExecutions: 'Total Executions',
    avgCacheHitRate: 'Avg Cache Hit Rate',
    avgDuration: 'Avg Duration',

    title: 'Module Execution Statistics',

    successRate: 'Success Rate',
  },

  errors: {
    trendTitle: 'Error Trend',
    errorCount: 'Error Count',
    totalCount: 'Total Requests',
    errorRate: 'Error Rate',
    count: 'Count',
    rate: 'Rate',
    totalErrors: 'Total Errors',
    topErrors: 'Top Errors',

    title: 'Error Statistics',

    totalRequests: 'Total Requests',

    avgLatency: 'Avg Error Latency',
  },

  performance: {
    throughput: 'Throughput',
    requests: 'Requests',
    p50: 'P50 Latency',
    p95: 'P95 Latency',
    p99: 'P99 Latency',
    latencyDist: 'Latency Distribution',
    slowQueries: 'Slow Queries',

    title: 'Performance Metrics',

    avgLatency: 'Avg Latency',

    latency: 'Latency',
  },

  providerUsage: {
    title: 'Provider usage',
    subtitle: '{period} — all-provider consumption (reconciliation-ready)',
    periodHint: 'Current period: {period}',
    more: 'More',
    search: 'Search name, code, or ID…',
    back: 'Back to list',
    exportAll: 'Export all (Excel)',
    exportDetail: 'Export detail (Excel)',
    colName: 'Provider',
    colCode: 'Code',
    colRequests: 'Requests',
    colTokens: 'Tokens',
    colCost: 'Cost (USD)',
    colSuccess: 'Success rate',
    colModel: 'Model',
    colDate: 'Date',
    periodDay: 'Daily',
    periodWeek: 'Weekly',
    periodMonth: 'Monthly',
    periodLabel: 'Period: {period}',
    periodRange: '{start} to {end}',
    modelBreakdown: 'By model',
    dailyBreakdown: 'Daily model breakdown',
  },
}