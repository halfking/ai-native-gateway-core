export default {
  title: "Dashboard",
  refresh: "Aktualisieren",
  modelsSelectedCount: '{n} Modelle ausgewählt',  // R50 ModelPicker compact
tabs: {
    board: 'Board',
    liveStream: 'Live-Anfragestream',
    sessionStats: 'Sitzungen & Statistik',
    selfcheck: 'Selbstprüfung',
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
    creditsDegraded: 'Credit-Tabelle {view} ist nicht initialisiert. Dieser Wert ist nicht belastbar',
    summaryDegraded: "Aggregatansicht {view} ist nicht initialisiert. Die Zahlen zu Anfragen/Token/Kosten sind nicht verfügbar, nicht null. Bitte nicht als Ergebnis verwenden.",
    modelCount: 'Models',
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
  loadError: 'Dashboard-Daten konnten nicht geladen werden. Bitte erneut versuchen.',
  board: {
    heroErrors: 'Fehler',
    heroInput: 'Eingabe',
    heroOutput: 'Ausgabe',
    heroCreditsEq: '≈ {n} Credits',
    drillDown: 'Drilldown',
    rpmLabel: 'RPM',
    rpmTitle: 'Anfragen pro Minute (Fensterdurchschnitt)',
    tpmLabel: 'TPM',
    tpmTitle: 'Tokens pro Minute (Fensterdurchschnitt)',
    providerCount: '{n} Anbieter',
    opsRunning: 'Läuft',
    opsChecks: 'Prüfläufe (10 Min.): {n}',
    opsRate24h: '24h-Erfolg {n}',
    opsChecksUnknown: 'Prüfungen (10 Min.) unbekannt',
    opsRateUnknown: '24-Std-Erfolgsquote unbekannt',
    providerCostTitle: 'Anbieterkosten & Beschaffung',
    providerDetail: 'Details',
    providerWindowCost: 'Fensterkosten',
    providerCredits: 'Credits',
    providerBalance: 'Guthaben',
    providerScore: 'Score {n}',
    providerPlan: 'Tarif',
    providerStatsTitle: 'Anbieter-Nutzungsstatistik',
    providerStatsHint: 'Folgt dem Zeitraum: eine beliebige Zeile wählen, um Details zu sehen.',
    providerExport: 'Excel exportieren',
    filterRange: 'Zeitraum',
    filterGran: 'Granularität',
    granAuto: 'Auto',
    granAutoTitle: 'Granularität richtet sich automatisch nach dem Zeitraum (heute=5 Min / 7T=15 Min / 30T=1h); manuelle Auswahl in Phase 2',
    granHour: 'Stündlich',
    granMinutes: '{n} Min.',
    sourceLabel: 'Quelle',
    sourcePg: 'PG-Baseline',
    sourceRedis: 'Redis-Delta',
    sourceSse: 'SSE live',
    sourceTitle: 'Datenquelle des Boards (PostgreSQL-Baseline / Redis-Delta / SSE-Live-Merge)',
    modelDistTitle: 'Modellverteilung',
    modelTop: 'Top {n} im Fenster',
    colShare: 'Anteil',
    distTitle: 'Verteilungsanalyse',
    distTop: 'TOP {n}',
    distOthers: '{n} weitere',
    distDrillHint: 'Fehlertyp anklicken → Drilldown nach Modell/Anbieter/Client',
    /**
     * Leerzustände (2026-10-27, Inkrement 14 · „Leerzustand 5 Punkte“).
     * ★ Vorher stand hier ein einziges `empty: 'No data'`, das von 6 **verschiedenen**
     *   Karten geteilt wurde (Anbieter / Verteilungsrangliste / Modellverteilung /
     *   Trenddiagramme / Nutzungstabelle / die zwei Aufgabenkarten der Betriebsübersicht)
     *   — ein Satz für 6 Bedeutungen ⇒ „keine Daten“ ließ offen, ob wirklich nichts da ist,
     *   ob herausgefiltert wurde oder ob schlicht falsch gerechnet wurde.
     *   ⇒ Jetzt nach **Seitenfakt** getrennt, die Art steht im Schlüsselnamen.
     *   · emptyNoResult* = kein Ergebnis (geprüft, es gibt wirklich nichts)
     *   · emptyFailed*    = fehlgeschlagen/unbekannt (der Server lieferte nichts)
     */
    emptyNoResultProviders: 'Im ausgewählten Zeitraum liegen keine Anbieterdaten vor.',
    emptyNoResultDist: 'Im ausgewählten Zeitraum gibt es keine Daten für die Rangliste.',
    emptyNoResultDrill: 'Für diesen Fehlertyp gibt es keine fehlgeschlagenen Anfragen.',
    emptyNoResultModelDist: 'Im ausgewählten Zeitraum liegen keine Modelldaten vor.',
    emptyNoResultModelTrend: 'Im ausgewählten Zeitraum liegen keine Daten zum Modelltrend vor.',
    emptyNoResultTrend: 'Im ausgewählten Zeitraum liegen keine Daten zum Nutzungstrend vor.',
    emptyNoResultDiscovery: 'Die Modellentdeckung wurde noch nicht ausgeführt.',
    emptyNoResultProbe: 'Der Probe-Prüflauf wurde noch nicht ausgeführt.',
    bgTasks: 'Background tasks',
    discoveryStatus: 'Model discovery',
    selfcheckTitle: 'Self-check',
    pieClients: 'Client-Typen',
    pieDegraded: "Nicht verfügbar: {reason}. Eine leere Rangliste bedeutet hier, dass nicht berechnet wurde, nicht dass nichts vorhanden war.",
    pieDegradedGeneric: 'Die Ranglisten-Abfrage konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
    drillFailed: "Diese Aufschlüsselung konnte nicht geladen werden. {reason}. Eine leere Liste bedeutet nicht, dass keine Fehler auftraten.",
    pieClientIp: 'Client-IP',
    pieIdentity: 'Identitäts-Hash',
    pieErrors: 'Fehlertypen',
    pieTenants: 'Mandanten-Nutzung',
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
    trendMore: 'Mehr',
    trendMoreTitle: 'Ganzseitige Nutzungstrend-Analyse öffnen',
    trendOthers: 'Weitere',





    rangeCustom: '{start} to {end}',
  },
  stat: {
    totalRequests: "Gesamtanfragen",
    totalCost: "Gesamtkosten",
    successRate: "Erfolgsrate",
    avgLatency: "Durchschn. Latenz {n} ms",
    models: "Modelle",
    activeInDays: "In den letzten {days} Tagen {n} aktiv",
    avgRequestSize: "Durchschn. Anfragegröße",
    avgResponseSize: "Durchschn. Antwortgröße",
    maxLabel: "Spitze",
  },
  table: {
    colModel: "Modell",
  },
  loading: "Wird geladen…",
  liveStream: {
    title: "Echtzeit-Anfrage-Stream",
    groupByQueue: "Nach Verarbeitungswarteschlange",
    controlsAria: "Steuerung des Echtzeit-Anfrage-Streams",
    pause: "Pause",
    resume: "Fortsetzen",
    legendButton: 'Legende',
    legend: {
      success: "Erfolg",
      inProgress: "In Bearbeitung",
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
      cacheFormat: 'Lesen {r} / Schreiben {w} (Treffer {pct})',
    },

    emptyWaiting: 'Warten auf Live-Anfragestream-Daten…',
    emptyConnecting: 'Verbindung zum Live-Stream wird aufgebaut, nach dem Verbinden wird automatisch geladen…',
    groupByCredential: 'Nach Zugangsdaten', groupByProvider: 'Nach Provider', groupByModel: 'Nach Modell',
    modeSmall: 'Klein',
    modeLarge: 'Groß',
    modeSmallTitle: 'Kleiner Modus: vertikale Balken, fasst mehr Anfragen (Standard)',
    modeLargeTitle: 'Großer Modus: Karten mit mehr Anfragedetails',
    // 2026-08-06: Geschäfts- / Probe-Filter + Titel (Abgleich mit zh-CN)
    business: 'Geschäft',
    probe: 'Probe',
    businessTitle: 'Nur echte Geschäftsanfragen anzeigen',
    probeTitle: 'Nur Probe-Anfragen anzeigen',
    cacheWindow: 'Cache / Fenster', connectionDetailTitle: 'Verbindungsdetails anzeigen',
    dimensionCredential: 'Zugangsdaten', dimensionVendor: 'Anbieter', dimensionProvider: 'Provider', dimensionModel: 'Modell',
    statusOpen: 'Verbunden', statusConnecting: 'Verbinden', statusReconnecting: 'Wiederverbinden',
    statusUnsupported: 'Nicht unterstützt', statusClosed: 'Nicht verbunden',
    queueStreamConnecting: 'Verbindung zum Live-Stream wird aufgebaut. Dieser Bereich aktualisiert sich nach dem Verbinden automatisch…',
    queueStreamClosed: 'Live-Stream getrennt, automatische Neuverbindung läuft…',
    queueStreamUnsupported: 'Dieser Browser unterstützt keine Live-Streams (EventSource); dieser Bereich kann keine Echtzeitdaten empfangen.',
    queueStreamAwaiting: 'Live-Stream verbunden, erste Daten werden empfangen…',
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

    filterStatus: 'Status',
    filterModel: 'Modell',
    filterGroup: 'Filter',
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
    sessionTrend: "Sitzungstrend",
    /** Leerzustände (2026-10-27, Inkrement 14): vorher nutzten beide Stellen `dashboard.noData` („keine Daten für diesen Zeitraum“). */
    emptyNoResultHealthGrade: 'Im ausgewählten Zeitraum liegen keine Daten zur Gesundheitsverteilung vor.',
    emptyNoResultSessionTrend: 'Im ausgewählten Zeitraum liegen keine Daten zum Sitzungstrend vor.',
  },



  performance: {


    avgLatency: 'Durchschn. Latenz',

  },

  providerUsage: {
    title: 'Anbieternutzung',
    subtitle: '{period}: Gesamtverbrauch aller Anbieter (abgleichsbereit)',
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
    /** Leerzustand: keine Nutzungsdatensätze (der herabgestufte Zustand hat mit `dataLifecycle.usageCost.degraded.title` einen eigenen Wortlaut). */
    emptyNoResult: 'Im ausgewählten Zeitraum liegen keine Nutzungsdaten vor.',
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
    childRequestsTitle: 'Unteranfragen {count} (zum Aufklappen)',
    childRequestsEmpty: 'Unteranfragen (noch keine Übertragung, zum Aufklappen)',
    actionTrailTitle: 'Aktionsverlauf {count} Einträge (zum Aufklappen)',
    actionTrailEmpty: 'Aktionsverlauf (noch keine Übertragung, zum Aufklappen)',
  },
}
