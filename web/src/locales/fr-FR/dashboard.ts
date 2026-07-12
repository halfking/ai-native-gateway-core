export default {
  title: "Tableau de bord",
  refresh: "Actualiser",
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
    },
    connecting: "Connexion…",
    reconnecting: "Reconnexion…",
    unsupported: "Le flux en direct n'est pas pris en charge dans ce navigateur",
    empty: "Aucune requête en temps réel"
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
  tabs: {
    liveStream: "实时请求流",
    sessionStats: "会话与统计"
  },
  moduleStats: {
    title: "模块执行统计",
    executions: "执行次数",
    successRate: "成功率",
    cacheHitRate: "缓存命中率",
    rate: "比率",
    totalModules: "总模块数",
    totalExecutions: "总执行次数",
    avgCacheHitRate: "平均缓存命中率",
    avgDuration: "平均耗时"
  },
  errors: {
    title: "错误统计",
    trendTitle: "错误趋势",
    errorCount: "错误数",
    totalCount: "总请求数",
    errorRate: "错误率",
    count: "数量",
    rate: "比率",
    totalErrors: "总错误数",
    totalRequests: "总请求数",
    avgLatency: "平均错误延迟",
    topErrors: "Top错误"
  },
  performance: {
    title: "性能指标",
    throughput: "吞吐量",
    avgLatency: "平均延迟",
    requests: "请求数",
    latency: "延迟",
    p50: "P50 延迟",
    p95: "P95 延迟",
    p99: "P99 延迟",
    latencyDist: "延迟分布",
    slowQueries: "慢查询"
  }
}
