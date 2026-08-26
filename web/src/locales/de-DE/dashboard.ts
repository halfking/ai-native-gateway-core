export default {
  title: "Dashboard",
  refresh: "Aktualisieren",
tabs: {
    board: 'Board',
    liveStream: 'Live-Anfragestream',
    sessionStats: 'Sitzungen & Statistik',
    selfcheck: 'Selbstprüfung',
    systemmonitor: 'Systemüberwachung',
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
    today: "Heute",
    last7d: "Letzte 7 Tage",
    last30d: "Letzte 30 Tage",
    last90d: "Letzte 90 Tage"
  },
  tenantLabel: {
    default: "Gesamte Seite",
    super: "Standardmandant",
    tenant: "Mandant: {tenantId}"
  },
  loadError: "Laden fehlgeschlagen",
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
    pieVirtualIp: 'Client-IP',
    pieIdentity: 'Identity hash',
    pieModels: 'Model usage',
    pieErrors: 'Error types',
    pieTenants: 'Tenant usage',
    pieProviders: 'Provider usage',
    metricRequests: 'Requests',
    metricTokens: 'Tokens',
    metricToggle: 'Metrik',
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
    title: "⚠ Ausgangs-Proxy nicht erreichbar",
    detail: "Der konfigurierte Proxy {proxy} konnte nicht erreicht werden — das Gateway ist automatisch in den Direktmodus gewechselt. Ausländische Modelle (Anthropic / OpenAI / OpenRouter / GitHub Copilot usw.) können fehlschlagen.",
    detailHealthy: "Proxy {proxy} ist wieder online.",
    hint: "Das System wird automatisch wieder aktiviert, sobald der Proxy wiederhergestellt ist."
  },
  backgroundTasks: {
    title: "Hintergrundaufgaben laufen",
    running: "Modellerkennung ({trigger})",
    startedAt: "Gestartet {time}",
    heartbeat: "Heartbeat {time}",
    slow: "Die Admin-Seiten können langsamer werden",
    detailsLink: "Details anzeigen"
  },
  discovery: {
    latest: "Letzte Modellerkennung: ",
    failuresTitle: "Modellerkennung · Fehler in den letzten 6 Std.",
    failuresTally: "{n} Fehler · {m} Modelle",
    summary: "Fehlerliste anzeigen",
    meta: "{n} mal · {m} Anmeldedaten betroffen · letzte {date} · Fehler {code}"
  },
  stat: {
    totalRequests: "Gesamtanfragen",
    inLastDays: "In den letzten {days} Tagen",
    totalTokens: "Gesamte Token-Nutzung",
    prompt: "Prompt {n}",
    completion: "Completion {n}",
    totalCost: "Gesamtkosten",
    costUnit: "USD",
    successRate: "Erfolgsrate",
    avgLatency: "Durchschn. Latenz {n} ms",
    apiKeys: "Eingebundene API-Schlüssel",
    enabledActive: "Aktiviert {enabled} · aktiv {active}",
    models: "Modelle",
    activeInDays: "In den letzten {days} Tagen {n} aktiv",
    providers: "Anbieter / Anmeldedaten",
    enabledCredentials: "Aktiviert {enabled} · Anmeldedaten {total}",
    offline: "Offline-Ressourcen",
    modelsCredentials: "Modelle {models} · Anmeldedaten {creds}",
    // 2026-08-06: Durchschn. Anfragegröße / Durchschn. Antwortgröße / Spitze (Abgleich mit zh-CN)
    avgRequestSize: "Durchschn. Anfragegröße",
    avgResponseSize: "Durchschn. Antwortgröße",
    maxLabel: "Spitze",
  },
  table: {
    hotKeysTitle: "Rangliste der API-Schlüssel mit hoher Nutzung",
    byModelTitle: "Nach Modell",
    colKey: "Schlüssel",
    colApplication: "Anwendung",
    colOwner: "Besitzer",
    colRequests: "Anfragen",
    colTokens: "Token-Nutzung",
    colCost: "Kosten (USD)",
    colLastUsed: "Zuletzt verwendet",
    colModel: "Modell",
    colProvider: "Anbieter"
  },
  compression: {
    title: "🤖 Sitzungskomprimierung",
    delta: "Delta",
    sliding: "Gleitend",
    outboundTokens: "≈ {n} ausgehende Tokens"
  },
  empty: {
    firstUse: "🚀 Noch keine Anfragedaten. Nach der Anbieterkonfiguration senden Sie einen Aufruf über /v1/chat/completions, um hier Daten zu sehen."
  },
  loading: "Wird geladen…",
  noData: "Keine Daten für diesen Zeitraum",
  costSuffix: "USD",
  viewFailedRequests: "Fehlgeschlagene Anfragen anzeigen",
  liveStream: {
    title: "Echtzeit-Anfrage-Stream",
    groupByQueue: "Nach Verarbeitungswarteschlange",
    controlsAria: "Steuerung des Echtzeit-Anfrage-Streams",
    connected: "Verbunden",
    disconnected: "Verbindet erneut...",
    pause: "Pause",
    resume: "Fortsetzen",
    filterAll: "Alle Status",
    filterSuccess: "Nur Erfolg",
    filterFailure: "Nur Fehler",
    filterInProgress: "In Bearbeitung",
    filterGroupFailures: "Fehleraufschlüsselung",
    filterFailure5xx: "Server / Upstream (5xx)",
    filterFailure4xx: "Client / Auth (4xx)",
    filterFailureTimeout: "Timeout / Netzwerk",
    filterFailureNotFound: "Routing / Modell nicht gefunden",
    filterFailureOther: "Andere Fehler",
    idleLabel: "Leerlauf {duration}",
    countTooltip: "{buffer} im Puffer / {visible} sichtbar",
    countAria: "{buffer} Anfragen im Puffer, {visible} sichtbar",
    legendButton: 'Legende',
    legend: {
      title: "Legende",
      model: "Modellfamilie",
      status: "Status",
      openai: "OpenAI",
      anthropic: "Anthropic",
      domestic: "Inland",
      oss: "Open Source",
      other: "Andere",
      success: "Erfolg",
      inProgress: "In Bearbeitung",
      failure: "Fehler"
,
      cancelled: 'Cancelled',
    },
    tooltip: {
      model: "Modell",
      provider: "Anbieter",
      status: "Status",
      latency: "Latenz",
      tokens: "Tokens",
      cost: "Kosten",
      error: "Fehler",
      time: "Zeit"
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
    connecting: "Verbindung wird hergestellt…",
    reconnecting: "Verbindung wird wiederhergestellt…",
    unsupported: "Live-Stream wird in diesem Browser nicht unterstützt",
    empty: "Keine Live-Anfragen",

    emptyWaiting: 'Warten auf Live-Anfragestream-Daten…',
    groupByCredential: 'Nach Zugangsdaten', groupByVendor: 'Nach Anbieter', groupByProvider: 'Nach Provider', groupByModel: 'Nach Modell',
    modeSmall: 'Klein',
    modeLarge: 'Groß',
    modeSmallTitle: 'Kleiner Modus: vertikale Balken, fasst mehr Anfragen (Standard)',
    modeLargeTitle: 'Großer Modus: Karten mit mehr Anfragedetails',
    probeAll: 'Alle', probeOnly: 'Nur Probes',
    probeAllTitle: 'Alle Anfragen anzeigen (Standard)', probeOnlyTitle: 'Nur Probe-Anfragen anzeigen',
    // 2026-08-06: Geschäfts- / Probe-Filter + Titel (Abgleich mit zh-CN)
    business: 'Geschäft',
    probe: 'Probe',
    businessTitle: 'Nur echte Geschäftsanfragen anzeigen',
    probeTitle: 'Nur Probe-Anfragen anzeigen',
    cacheWindow: 'Cache / Fenster', connectionDetailTitle: 'Klicken für Verbindungsdetails',
    dimensionCredential: 'Zugangsdaten', dimensionVendor: 'Anbieter', dimensionProvider: 'Provider', dimensionModel: 'Modell',
    statusOpen: 'Verbunden', statusConnecting: 'Verbinden', statusReconnecting: 'Wiederverbinden',
    statusUnsupported: 'Nicht unterstützt', statusClosed: 'Nicht verbunden',
    sseDetailTitle: 'SSE-Verbindungsdetails', sseStatusLabel: 'Verbindungsstatus', sseUrlLabel: 'SSE-URL',
    editUrl: 'Bearbeiten', editUrlPlaceholder: 'SSE-URL eingeben', save: 'Speichern', resetDefault: 'Standard',
    cancel: 'Abbrechen', testConnection: 'Verbindung testen', close: 'Schließen',
    sseTestOk: 'SSE-Verbindung OK!\nStatus: Verbunden\nURL: {url}',
    sseTestFail: 'SSE nicht verbunden\nStatus: {status}\nURL: {url}',
    redisWarning: 'Redis nicht verfügbar: {error}. Live-Daten fallen auf DB-Abfragen zurück.',
    redisFallbackError: 'Cache-Service-Verbindung fehlgeschlagen',

    probeDirect: 'Aktive Sonde (direkt zum Upstream)',

    probeGateway: 'Aktive Sonde (Gateway-Pfad)',

    probeScheduled: 'Geplante Sonde (Scheduler)',

    probeGeneric: 'Probe-Anfrage',

    probeOriginLabel: 'Quelle: {origin}',

    probeAttempt: 'Versuch: Nr. {n}',

    originGateway: 'Gateway-Pfad',

    originScheduled: 'Geplante Sonde',

    originDirect: 'Direkt zum Upstream',

    idleHeartbeat: 'Heartbeat-Platzhalter',

    tileIdle: 'Leer',

    idleUnderOneMin: 'Leer < 1 Min',

    idleMinutes: 'Leer {n} Min',

    idleHours: 'Leer {h} Std',

    idleHoursMinutes: 'Leer {h} Std {m} Min',

    idleReasonNoTraffic: 'Kein Traffic (5 Min Leerlauf)',
    // 2026-07-24: Multi-dimension filters
    filterStatus: 'Status',
    filterAllOptions: 'Alle',
    filterEmpty: 'Keine Optionen verfügbar',
    filterModel: 'Modell',
    filterProvider: 'Anbieter',
    filterVendor: 'Vendor',
    filterAgent: 'Client',  // 2026-08-06 Abgleich mit zh-CN
    clearFilters: 'Filter löschen',
    status: {
      in_progress: 'In Bearbeitung',
      success: 'Erfolg',
      failure: 'Fehler',
      rate_limited: 'Begrenzt',
    },
    vendor: {
      openai: 'OpenAI',
      anthropic: 'Anthropic',
      domestic: 'Inland',
      oss: 'Open Source',
      other: 'Andere',
    },
  },
  charts: {
    gradeA: "Note A",
    gradeB: "Note B",
    gradeC: "Note C",
    gradeD: "Note D",
    gradeF: "Note F",
    avgScore: "Durchschnittspunktzahl",
    healthDistribution: "Gesundheitsverteilung",
    totalSessions: "Gesamt {n} Sitzungen",
    newSessions: "Neue Sitzungen",
    activeSessions: "Aktive Sitzungen",
    closedSessions: "Geschlossene Sitzungen",
    costUSD: "Kosten (USD)",
    sessionCount: "Sitzungsanzahl",
    sessionTrend: "Sitzungstrend"
  },

  moduleStats: {
    executions: 'Ausführungen',
    cacheHitRate: 'Cache-Trefferrate',
    rate: 'Rate',
    totalModules: 'Gesamtzahl Module',
    totalExecutions: 'Gesamtausführungen',
    avgCacheHitRate: 'Durchschn. Cache-Trefferrate',
    avgDuration: 'Durchschn. Dauer',

    title: 'Modul-Ausführungsstatistik',

    successRate: 'Erfolgsrate',
  },

  errors: {
    trendTitle: 'Fehlertrend',
    errorCount: 'Fehleranzahl',
    totalCount: 'Gesamtanfragen',
    errorRate: 'Fehlerrate',
    count: 'Anzahl',
    rate: 'Rate',
    totalErrors: 'Gesamtfehler',
    topErrors: 'Top-Fehler',

    title: 'Fehlerstatistik',

    totalRequests: 'Gesamtanfragen',

    avgLatency: 'Durchschn. Fehlerlatenz',
  },

  performance: {
    throughput: 'Durchsatz',
    requests: 'Anfragen',
    p50: 'P50 Latenz',
    p95: 'P95 Latenz',
    p99: 'P99 Latenz',
    latencyDist: 'Latenzverteilung',
    slowQueries: 'Langsame Abfragen',

    title: 'Leistungsmetriken',

    avgLatency: 'Durchschn. Latenz',

    latency: 'Latenz',
  },

  providerUsage: {
    title: 'Anbieternutzung',
    subtitle: '{period} — Gesamtverbrauch aller Anbieter (abgleichsbereit)',
    periodHint: 'Aktueller Zeitraum: {period}',
    more: 'Mehr',
    search: 'Name, Code oder ID suchen…',
    back: 'Zurück zur Liste',
    exportAll: 'Alle exportieren (Excel)',
    exportDetail: 'Details exportieren (Excel)',
    colName: 'Anbieter',
    colCode: 'Code',
    colRequests: 'Anfragen',
    colTokens: 'Tokens',
    colCost: 'Kosten (USD)',
    colSuccess: 'Erfolgsrate',
    colModel: 'Modell',
    colDate: 'Datum',
    periodDay: 'Täglich',
    periodWeek: 'Wöchentlich',
    periodMonth: 'Monatlich',
    periodLabel: 'Zeitraum: {period}',
    periodRange: '{start} bis {end}',
    modelBreakdown: 'Nach Modell',
    dailyBreakdown: 'Tägliche Modellaufschlüsselung',
  },
}
