export default {
  title: "Panel",
  refresh: "Actualizar",
  modelsSelectedCount: '{n} modelos seleccionados',  // R50 ModelPicker compact
tabs: {
    board: 'Board',
    liveStream: 'Flujo de solicitudes en vivo',
    sessionStats: 'Sesiones y estadísticas',
    selfcheck: 'Autoverificación',
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
    creditsDegraded: 'La vista de créditos {view} no está inicializada: la cifra no es fiable',
    summaryDegraded: "La vista agregada {view} no está inicializada: las cifras de solicitudes/tokens/coste son «no disponibles», no cero. No las uses como conclusión.",
    modelCount: 'Models',
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
  loadError: 'No se pudo cargar los datos del panel. Vuelva a intentarlo.',
  board: {
    heroErrors: 'Errores',
    heroInput: 'Entrada',
    heroOutput: 'Salida',
    heroCreditsEq: '≈ {n} créditos',
    drillDown: 'Profundizar',
    rpmLabel: 'RPM',
    rpmTitle: 'Peticiones por minuto (media del período)',
    tpmLabel: 'TPM',
    tpmTitle: 'Tokens por minuto (media del período)',
    providerCount: '{n} proveedores',
    opsRunning: 'En ejecución',
    opsChecks: 'Sondeos (10 min): {n}',
    opsRate24h: 'Éxito 24h {n}',
    opsChecksUnknown: 'comprobaciones (10 min): desconocidas',
    opsRateUnknown: 'tasa de éxito 24 h: desconocida',
    providerCostTitle: 'Costo y adquisición de proveedores',
    providerDetail: 'Detalles',
    providerWindowCost: 'Costo del período',
    providerCredits: 'Créditos',
    providerBalance: 'Saldo',
    providerScore: 'Puntuación {n}',
    providerPlan: 'Plan',
    providerStatsTitle: 'Estadísticas de uso de proveedores',
    providerStatsHint: 'Sigue el rango de tiempo: selecciona cualquier fila para ver los detalles',
    providerExport: 'Exportar Excel',
    filterRange: 'Rango de tiempo',
    filterGran: 'Granularidad',
    granAuto: 'Auto',
    granAutoTitle: 'La granularidad se alinea con el rango (hoy=5m / 7d=15m / 30d=1h); el modo manual llegará en la fase 2',
    granHour: 'Por hora',
    granMinutes: '{n} min',
    sourceLabel: 'Fuente',
    sourcePg: 'Línea base PG',
    sourceRedis: 'Delta Redis',
    sourceSse: 'SSE en vivo',
    sourceTitle: 'Fuente de datos del panel (línea base PostgreSQL / delta Redis / fusión SSE en vivo)',
    modelDistTitle: 'Distribución de modelos',
    modelTop: 'Top {n} del período',
    colShare: 'Cuota',
    distTitle: 'Análisis de distribución',
    distTop: 'TOP {n}',
    distOthers: '{n} más',
    distDrillHint: 'Selecciona un tipo de error → desglosa por modelo/proveedor/cliente',
    /**
     * Estados vacíos (2026-10-27 · incremento 14 · «empty state de 5 puntos»).
     * ★ Antes había un único `empty: 'No data'` compartido por **seis** tarjetas
     *   distintas (proveedores / ranking de distribución / distribución de modelos
     *   / tendencias de uso / tabla de uso / las dos tarjetas de tareas operativas)
     *   ⇒ una sola frase cubría seis significados ⇒ el usuario no podía saber si
     *   «no hay datos» era «no hay», «está filtrado» o «se calculó mal».
     *   ⇒ Ahora se separan por el **hecho real de cada página**, con el kind en la clave.
     *   · emptyNoResult* = sin resultado (se buscó y realmente no hay)
     *   · emptyFailed*    = fallo/desconocido (el servidor no devolvió nada ⇒ no
     *     se puede decir «no hay»)
     */
    emptyNoResultProviders: 'En el periodo seleccionado no hay datos de proveedores.',
    emptyNoResultDist: 'En el periodo seleccionado no hay datos que se puedan ordenar en un ranking.',
    emptyNoResultDrill: 'No hay solicitudes fallidas para este tipo de error.',
    emptyNoResultModelDist: 'En el periodo seleccionado no hay datos de modelos.',
    emptyNoResultModelTrend: 'En el periodo seleccionado no hay datos de tendencias de modelos.',
    emptyNoResultTrend: 'En el periodo seleccionado no hay datos de tendencias de uso.',
    emptyNoResultDiscovery: 'Las tareas de descubrimiento de modelos aún no tienen registros de ejecución.',
    emptyNoResultProbe: 'Las comprobaciones de sondeo aún no tienen registros de ejecución.',
    bgTasks: 'Background tasks',
    discoveryStatus: 'Model discovery',
    selfcheckTitle: 'Self-check',
    pieClients: 'Tipos de cliente',
    pieDegraded: "No disponible: {reason}. Una clasificación vacía aquí significa «no se calculó», no «no hay datos».",
    pieDegradedGeneric: 'No se pudo completar la consulta de clasificación. Vuelva a intentarlo.',
    drillFailed: "No se pudo cargar este desglose: {reason}. Una lista vacía aquí no significa que no hubiera errores.",
    pieClientIp: 'IP del cliente',
    pieIdentity: 'Hash de identidad',
    pieErrors: 'Tipos de error',
    pieTenants: 'Uso por tenant',
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
    trendMore: 'Más',
    trendMoreTitle: 'Abrir el análisis de tendencias de uso a página completa',
    trendOthers: 'Otros',





    rangeCustom: '{start} to {end}',
  },
  stat: {
    totalRequests: "Solicitudes totales",
    totalCost: "Coste total",
    successRate: "Tasa de éxito",
    avgLatency: "Latencia media {n} ms",
    models: "Modelos",
    activeInDays: "En los últimos {days} días, {n} activos",
    avgRequestSize: "Tamaño medio de petición",
    avgResponseSize: "Tamaño medio de respuesta",
    maxLabel: "Pico",
  },
  table: {
    colModel: "Modelo",
  },
  loading: "Cargando…",
  liveStream: {
    title: "Flujo de solicitudes en tiempo real",
    groupByQueue: "Por cola de procesamiento",
    controlsAria: "Controles del flujo de solicitudes en tiempo real",
    pause: "Pausar",
    resume: "Reanudar",
    legendButton: 'Leyenda',
    legend: {
      success: "Éxito",
      inProgress: "En curso",
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
      cacheFormat: 'lectura {r} / escritura {w} (aciertos {pct})',
    },

    emptyWaiting: 'Esperando datos del flujo de solicitudes en vivo…',
    emptyConnecting: 'Conectando al flujo en vivo, se cargará automáticamente al conectarse…',
    groupByCredential: 'Por credencial', groupByProvider: 'Por proveedor', groupByModel: 'Por modelo',
    modeSmall: 'Pequeño',
    modeLarge: 'Grande',
    modeSmallTitle: 'Modo pequeño: barras verticales, caben más solicitudes (predeterminado)',
    modeLargeTitle: 'Modo grande: tarjetas con más detalle de solicitud',
    // 2026-08-06: filtros de negocio / sonda + títulos (alineado con zh-CN)
    business: 'Negocio',
    probe: 'Sonda',
    businessTitle: 'Mostrar solo solicitudes de negocio reales',
    probeTitle: 'Mostrar solo solicitudes de sonda',
    cacheWindow: 'Caché / ventana', connectionDetailTitle: 'Clic para ver detalles de la conexión',
    dimensionCredential: 'Credencial', dimensionVendor: 'Proveedor', dimensionProvider: 'Proveedor', dimensionModel: 'Modelo',
    statusOpen: 'Conectado', statusConnecting: 'Conectando', statusReconnecting: 'Reconectando',
    statusUnsupported: 'No compatible', statusClosed: 'Desconectado',
    queueStreamConnecting: 'Conectando al flujo en vivo; este panel se actualizará automáticamente al conectarse…',
    queueStreamClosed: 'Flujo en vivo desconectado, reconectando automáticamente…',
    queueStreamUnsupported: 'Este navegador no admite flujos en vivo (EventSource); este panel no puede recibir datos en tiempo real.',
    queueStreamAwaiting: 'Flujo en vivo conectado, recibiendo los primeros datos…',
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

    filterStatus: 'Estado',
    filterModel: 'Modelo',
    filterGroup: 'Filtrar',
    filterProvider: 'Proveedor',
    filterVendor: 'Vendor',
    filterAgent: 'Cliente',  // 2026-08-06 alineado con zh-CN
    clearFilters: 'Borrar filtros',
    status: {
      in_progress: 'En curso',
      success: 'Éxito',
      failure: 'Fallo',
      rate_limited: 'Limitado',
    },
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
    sessionTrend: "Tendencia de sesiones",
    /** Estados vacíos (2026-10-27 · incremento 14): antes ambos usaban `dashboard.noData` («Sin datos para este periodo»). */
    emptyNoResultHealthGrade: 'En el periodo seleccionado no hay datos de distribución de salud.',
    emptyNoResultSessionTrend: 'En el periodo seleccionado no hay datos de tendencias de sesiones.',
  },



  performance: {


    avgLatency: 'Latencia promedio',

  },

  providerUsage: {
    title: 'Uso del proveedor',
    subtitle: '{period}: consumo de todos los proveedores (listo para conciliación)',
    more: 'Más',
    search: 'Buscar nombre, código o ID…',
    back: 'Volver a la lista',
    exportAll: 'Exportar todo (Excel)',
    exportDetail: 'Exportar detalle (Excel)',
    colName: 'Proveedor',
    colCode: 'Código',
    colRequests: 'Solicitudes',
    colTokens: 'Tokens',
    colCost: 'Costo (USD)',
    colSuccess: 'Tasa de éxito',
    colModel: 'Modelo',
    colDate: 'Fecha',
    periodDay: 'Diario',
    periodWeek: 'Semanal',
    periodMonth: 'Mensual',
    periodLabel: 'Período: {period}',
    periodRange: '{start} a {end}',
    modelBreakdown: 'Por modelo',
    dailyBreakdown: 'Desglose diario por modelo',
    /** Estado vacío: sin registros de uso (el estado degradado tiene su propio texto en `dataLifecycle.usageCost.degraded.title`). */
    emptyNoResult: 'En el periodo seleccionado no hay registros de uso.',
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
    childRequestsTitle: 'Sub-solicitudes {count} (expanda para ver)',
    childRequestsEmpty: 'Sub-solicitudes (aún sin datos recibidos, expanda para ver)',
    actionTrailTitle: 'Traza de acciones {count} (expanda para ver)',
    actionTrailEmpty: 'Traza de acciones (aún sin datos recibidos, expanda para ver)',
  },
}
