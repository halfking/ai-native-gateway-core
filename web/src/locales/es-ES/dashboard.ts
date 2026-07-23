export default {
  title: "Panel",
  refresh: "Actualizar",
tabs: {
    board: 'Board',
    liveStream: 'Flujo de solicitudes en vivo',
    sessionStats: 'Sesiones y estadísticas',
    selfcheck: 'Monitoreo del sistema',
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
    today: "Hoy",
    last7d: "Últimos 7 días",
    last30d: "Últimos 30 días",
    last90d: "Últimos 90 días"
  },
  tenantLabel: {
    default: "Datos de todo el sitio",
    super: "Inquilino predeterminado",
    tenant: "Inquilino: {tenantId}"
  },
  loadError: "Error al cargar",
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
    pieVirtualIp: 'IP del cliente',
    pieIdentity: 'Identity hash',
    pieModels: 'Model usage',
    pieErrors: 'Error types',
    pieTenants: 'Tenant usage',
    pieProviders: 'Provider usage',
    metricRequests: 'Requests',
    metricTokens: 'Tokens',
    metricToggle: 'Métrica',
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
    title: "⚠ Proxy de salida inaccesible",
    detail: "La sonda del proxy configurado {proxy} falló — el gateway ha vuelto automáticamente al modo directo. Los modelos extranjeros (Anthropic / OpenAI / OpenRouter / GitHub Copilot, etc.) pueden fallar.",
    detailHealthy: "El proxy {proxy} está de vuelta en línea.",
    hint: "El sistema se reactivará automáticamente cuando el proxy se recupere."
  },
  backgroundTasks: {
    title: "Tareas en segundo plano en ejecución",
    running: "Descubrimiento de modelos ({trigger})",
    startedAt: "Iniciado {time}",
    heartbeat: "Latido {time}",
    slow: "Las páginas de administración pueden ir más lentas",
    detailsLink: "Ver detalles"
  },
  discovery: {
    latest: "Último descubrimiento de modelos: ",
    failuresTitle: "Descubrimiento de modelos · fallos en las últimas 6h",
    failuresTally: "{n} fallos · {m} modelos",
    summary: "Ver lista de fallos",
    meta: "{n} veces · {m} credenciales afectadas · última {date} · error {code}"
  },
  stat: {
    totalRequests: "Solicitudes totales",
    inLastDays: "En los últimos {days} días",
    totalTokens: "Uso total de tokens",
    prompt: "Prompt {n}",
    completion: "Completion {n}",
    totalCost: "Coste total",
    costUnit: "USD",
    successRate: "Tasa de éxito",
    avgLatency: "Latencia media {n} ms",
    apiKeys: "Claves API integradas",
    enabledActive: "Activadas {enabled} · activas {active}",
    models: "Modelos",
    activeInDays: "En los últimos {days} días, {n} activos",
    providers: "Proveedores / credenciales",
    enabledCredentials: "Activados {enabled} · credenciales {total}",
    offline: "Recursos offline",
    modelsCredentials: "Modelos {models} · credenciales {creds}"
  },
  table: {
    hotKeysTitle: "Ranking de claves API por uso",
    byModelTitle: "Por modelo",
    colKey: "Clave",
    colApplication: "Aplicación",
    colOwner: "Propietario",
    colRequests: "Solicitudes",
    colTokens: "Uso de tokens",
    colCost: "Coste (USD)",
    colLastUsed: "Último uso",
    colModel: "Modelo",
    colProvider: "Proveedor"
  },
  compression: {
    title: "🤖 Compresión de sesión",
    delta: "Delta",
    sliding: "Deslizante",
    outboundTokens: "≈ {n} tokens de salida"
  },
  empty: {
    firstUse: "🚀 Aún no hay datos de solicitudes. Tras configurar los proveedores, envíe una llamada a /v1/chat/completions para ver datos aquí."
  },
  loading: "Cargando…",
  noData: "Sin datos para este periodo",
  costSuffix: "USD",
  viewFailedRequests: "Ver solicitudes fallidas",
  liveStream: {
    title: "Flujo de solicitudes en tiempo real",
    connected: "Conectado",
    disconnected: "Reconectando...",
    pause: "Pausar",
    resume: "Reanudar",
    filterAll: "Todos los estados",
    filterSuccess: "Solo éxito",
    filterFailure: "Solo fallo",
    filterInProgress: "En curso",
    filterGroupFailures: "Desglose de fallos",
    filterFailure5xx: "Servidor / upstream (5xx)",
    filterFailure4xx: "Cliente / auth (4xx)",
    filterFailureTimeout: "Tiempo agotado / red",
    filterFailureNotFound: "Enrutamiento / modelo no encontrado",
    filterFailureOther: "Otros fallos",
    idleLabel: "Inactivo {duration}",
    countTooltip: "{buffer} en búfer / {visible} visibles",
    countAria: "{buffer} solicitudes en búfer, {visible} visibles",
    legend: {
      title: "Leyenda",
      model: "Familia de modelo",
      status: "Estado",
      openai: "OpenAI",
      anthropic: "Anthropic",
      domestic: "Local",
      oss: "Código abierto",
      other: "Otro",
      success: "Éxito",
      inProgress: "En curso",
      failure: "Fallo"
,
      cancelled: 'Cancelled',
    },
    tooltip: {
      model: "Modelo",
      provider: "Proveedor",
      status: "Estado",
      latency: "Latencia",
      tokens: "Tokens",
      cost: "Coste",
      error: "Error",
      time: "Hora"
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
    connecting: "Conectando…",
    reconnecting: "Reconectando…",
    unsupported: "El flujo en vivo no es compatible con este navegador",
    empty: "Sin solicitudes en vivo",

    emptyWaiting: 'Esperando datos del flujo de solicitudes en vivo…',
    groupByVendor: 'Por proveedor', groupByProvider: 'Por proveedor', groupByModel: 'Por modelo',
    modeSmall: 'Pequeño',
    modeLarge: 'Grande',
    modeSmallTitle: 'Modo pequeño: barras verticales, caben más solicitudes (predeterminado)',
    modeLargeTitle: 'Modo grande: tarjetas con más detalle de solicitud',
    probeAll: 'Todos', probeOnly: 'Solo sondas',
    probeAllTitle: 'Mostrar todas las solicitudes (predeterminado)', probeOnlyTitle: 'Mostrar solo solicitudes de sonda',
    cacheWindow: 'Caché / ventana', connectionDetailTitle: 'Clic para ver detalles de la conexión',
    dimensionVendor: 'Proveedor', dimensionProvider: 'Proveedor', dimensionModel: 'Modelo',
    statusOpen: 'Conectado', statusConnecting: 'Conectando', statusReconnecting: 'Reconectando',
    statusUnsupported: 'No compatible', statusClosed: 'Desconectado',
    sseDetailTitle: 'Detalles de conexión SSE', sseStatusLabel: 'Estado de conexión', sseUrlLabel: 'URL SSE',
    editUrl: 'Editar', editUrlPlaceholder: 'Ingrese URL SSE', save: 'Guardar', resetDefault: 'Predeterminado',
    cancel: 'Cancelar', testConnection: 'Probar conexión', close: 'Cerrar',
    sseTestOk: '¡Conexión SSE correcta!\nEstado: conectado\nURL: {url}',
    sseTestFail: 'SSE no conectado\nEstado: {status}\nURL: {url}',
    redisWarning: 'Redis no disponible: {error}. Los datos en vivo recurren a consultas a la base de datos.',
    redisFallbackError: 'Error de conexión del servicio de caché',

    probeDirect: 'Sonda activa (directa al upstream)',

    probeGateway: 'Sonda activa (ruta de gateway)',

    probeScheduled: 'Sonda programada (scheduler)',

    probeGeneric: 'Solicitud de sonda',

    probeOriginLabel: 'Origen: {origin}',

    probeAttempt: 'Intento: nº {n}',

    originGateway: 'Ruta del gateway',

    originScheduled: 'Sonda programada',

    originDirect: 'Directa al upstream',

    idleHeartbeat: 'Marcador de posición de latido',

    tileIdle: 'Inactivo',

    idleUnderOneMin: 'Inactivo < 1 min',

    idleMinutes: 'Inactivo {n} min',

    idleHours: 'Inactivo {h} h',

    idleHoursMinutes: 'Inactivo {h} h {m} min',

    idleReasonNoTraffic: 'Sin tráfico (5 min inactivo)',
  },
  charts: {
    gradeA: "Grado A",
    gradeB: "Grado B",
    gradeC: "Grado C",
    gradeD: "Grado D",
    gradeF: "Grado F",
    avgScore: "Puntuación media",
    healthDistribution: "Distribución de salud",
    totalSessions: "Total {n} sesiones",
    newSessions: "Nuevas sesiones",
    activeSessions: "Sesiones activas",
    closedSessions: "Sesiones cerradas",
    costUSD: "Costo (USD)",
    sessionCount: "Número de sesiones",
    sessionTrend: "Tendencia de sesiones"
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