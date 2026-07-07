// formatAnomaliesView.ts — Página de monitoreo de anomalías de formato (es-ES).
export default {
  pageTitle: 'Monitor de anomalías de formato',
  pageSubtitle: 'Visualice rápidamente los cambios en el formato de respuesta del proveedor, fallos de extracción de tokens y problemas de compatibilidad.',

  stats: {
    total: 'Total de anomalías',
    unresolved: 'No resueltas',
    critical: 'Críticas',
    window: 'Ventana de estadísticas',
  },

  filter: {
    provider: 'Proveedor',
    providerPlaceholder: 'Seleccionar proveedor…',
    model: 'Modelo',
    modelPlaceholder: 'Seleccionar modelo…',
    anomalyType: 'Tipo de anomalía',
    anomalyTypePlaceholder: 'Seleccionar tipo de anomalía…',
    unresolvedOnly: 'Solo no resueltas',
    query: 'Buscar',
    refresh: 'Actualizar',
  },

  anomalyType: {
    all: 'Todos los tipos de anomalía',
    missing_usage_block: 'Bloque usage faltante',
    zero_completion_tokens: 'Completion Tokens en 0',
    extraction_failed: 'Extracción fallida',
    unexpected_structure: 'Estructura inesperada',
    null_usage_values: 'Valores usage nulos',
    token_mismatch: 'Tokens no coinciden',
    missing_provider_tokens: 'Tokens de proveedor faltantes',
    missing_client_tokens: 'Tokens de cliente faltantes',
    json_parse_error: 'Error de análisis JSON',
    missing_finish_reason: 'Finish Reason faltante',
    missing_content: 'Content faltante',
  },

  anomalyTypeDescription: {
    missing_usage_block: 'Respuesta upstream sin bloque usage',
    zero_completion_tokens: 'Respuesta con contenido pero completion_tokens es 0',
    extraction_failed: 'No se pudo extraer información de usage de la respuesta',
    unexpected_structure: 'Estructura devuelta por upstream inconsistente con lo esperado',
    null_usage_values: 'Campos usage presentes pero valores nulos',
  },

  severity: {
    critical: 'Crítica',
    high: 'Alta',
    medium: 'Media',
    low: 'Baja',
  },

  status: {
    resolved: 'Resuelta',
    unresolved: 'No resuelta',
  },

  table: {
    detectedAt: 'Detectada el',
    severity: 'Severidad',
    anomalyType: 'Tipo de anomalía',
    providerModel: 'Proveedor / Modelo',
    requestId: 'Request ID',
    tokenInfo: 'Info de tokens',
    status: 'Estado',
    actions: 'Acciones',
    loading: 'Cargando...',
    noData: 'No se encontraron registros de anomalías',
    viewDetail: 'Detalles',
    expectedTokens: 'Esperado: {count}',
    actualTokens: 'Real: {count}',
  },

  token: {
    expected: 'Esperado',
    actual: 'Real',
  },

  pager: {
    prev: 'Anterior',
    next: 'Siguiente',
    summary: 'Página {page} / {totalPages}, {total} registros',
  },

  detail: {
    title: 'Detalles de la anomalía',
    close: 'Cerrar',
    requestId: 'Request ID',
    detectedAt: 'Detectada el',
    provider: 'Proveedor',
    model: 'Modelo',
    outboundModel: 'Modelo saliente',
    usageSource: 'Usage Source',
    responseStructure: 'Estructura de la respuesta',
    responseSample: 'Muestra de respuesta',
    resolutionNotes: 'Notas de resolución',
    resolutionNotesPlaceholder: 'Registrar notas de corrección para seguimiento',
    markResolved: 'Marcar como resuelta',
    processing: 'Procesando...',
    resolutionInfo: 'Información de resolución',
    noNotes: 'Sin notas de resolución',
  },

  error: {
    loadFailed: 'Error al cargar',
    summaryLoadFailed: 'Error al cargar estadísticas',
    markFailed: 'Error al marcar',
    needSuperAdmin: 'Se requiere permiso de super-admin',
  },
}
