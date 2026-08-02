// modelIntegrityView.ts — Página de monitoreo de integridad de modelos (es-ES).
export default {
  pageTitle: 'Monitor de integridad de modelos',
  pageSubtitle: 'Realice un seguimiento de la sustitución de modelos, el truncamiento de respuestas, las respuestas vacías, el contenido repetido y la deriva de huellas.',

  tabs: {
    events: 'Eventos de integridad',
    drift: 'Deriva de huella',
  },

  stats: {
    total: 'Total de eventos',
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
    severity: 'Severidad',
    unresolvedOnly: 'Solo no resueltas',
    query: 'Buscar',
    refresh: 'Actualizar',
  },

  anomalyType: {
    all: 'Todos los tipos de anomalía',
    model_mismatch: 'Modelo no coincidente',
    finish_refusal: 'Rechazo / Filtro de contenido',
    finish_truncation: 'Truncamiento al finalizar',
    token_arith_fail: 'Fallo de aritmética de tokens',
    empty_response: 'Respuesta vacía',
    repeated_content: 'Contenido repetido',
    fingerprint_drift: 'Deriva de huella',
  },

  anomalyTypeDescription: {
    model_mismatch: 'El upstream devolvió un modelo que no coincide con el solicitado (posible sustitución silenciosa)',
    finish_refusal: 'El upstream devolvió rechazo o content_filter',
    finish_truncation: 'Truncado por longitud / max_tokens',
    token_arith_fail: 'prompt + completion no es igual a total',
    empty_response: 'El stream no produjo contenido ni tokens',
    repeated_content: 'El texto de respuesta contiene grandes bloques repetidos (posible bucle del modelo)',
    fingerprint_drift: 'system_fingerprint se desvió de la línea base',
  },

  severity: {
    all: 'Todas las severidades',
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
    actual: 'Real',
    status: 'Estado',
    actions: 'Acciones',
    loading: 'Cargando...',
    noData: 'No se encontraron eventos de integridad',
    viewDetail: 'Detalles',
  },

  drift: {
    days: 'Días',
    query: 'Buscar',
    noData: 'No se detectó deriva de huella en la ventana seleccionada',
  },

  pager: {
    prev: 'Anterior',
    next: 'Siguiente',
    summary: 'Página {page} / {totalPages}, {total} registros',
  },

  detail: {
    title: 'Detalles del evento de integridad',
    close: 'Cerrar',
    requestId: 'Request ID',
    detectedAt: 'Detectada el',
    provider: 'Proveedor',
    model: 'Modelo',
    outboundModel: 'Modelo saliente',
    credential: 'ID de credencial',
    expected: 'Esperado',
    actual: 'Real',
    context: 'Contexto',
    sample: 'Muestra',
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
    driftLoadFailed: 'Error al cargar la deriva de huella',
    markFailed: 'Error al marcar',
    needSuperAdmin: 'Se requiere permiso de super-admin',
  },
}
