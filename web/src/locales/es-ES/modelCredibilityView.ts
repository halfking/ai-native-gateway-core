// modelCredibilityView.ts — Fiabilidad de los modelos (página es-ES).
export default {
  pageTitle: 'Fiabilidad de los modelos',
  pageSubtitle:
    'Detecta si un proveedor está vendiendo un modelo más pequeño como si fuera uno mayor ' +
    '(sustitución, degradación, dilución). El veredicto se apoya en cuatro mediciones independientes: ' +
    'el modelo que declara el proveedor, la comparación de capacidades con otros nodos del mismo modelo, ' +
    'la estabilidad de la firma de la petición y una referencia externa. Cuando no puede concluirse nada, ' +
    'se muestra «evidencia insuficiente» — nunca «sin problemas».',

  stats: {
    total: 'Nodos evaluados',
    verified: 'Verificados',
    suspected: 'Sospechosos',
    diluted: 'Diluidos',
    unknown: 'Evidencia insuficiente',
    providers: 'Proveedores implicados',
  },

  filter: {
    verdict: 'Veredicto',
    verdictPlaceholder: 'Todos los veredictos',
    model: 'Modelo',
    modelPlaceholder: 'Filtrar por nombre de modelo',
    query: 'Consultar',
    refresh: 'Actualizar',
    scanAll: 'Analizar todo',
    scanning: 'Analizando…',
    sort: 'Orden',
    sortTime: 'Por fecha de medición (más reciente primero)',
    sortName: 'Por nombre de modelo',
    sortScore: 'Por fiabilidad (ascendente)',
  },

  verdict: {
    verified: 'Verificado',
    suspected: 'Sospechoso',
    diluted: 'Diluido',
    unknown: 'Evidencia insuficiente',
  },

  dimension: {
    identity: 'Identidad',
    capability: 'Capacidad',
    consistency: 'Estabilidad de la firma',
    reference: 'Referencia externa',
  },

  table: {
    provider: 'Proveedor',
    credential: 'Credenciales',
    model: 'Modelo',
    credibility: 'Fiabilidad',
    verdict: 'Veredicto',
    dimensions: 'Dimensiones con evidencia',
    measuredAt: 'Medido el',
    action: 'Acciones',
  },

  status: {
    success: 'Correcto',
    partial: 'Parcial',
    failed: 'Sonda fallida',
    inconclusive: 'No concluyente',
    unknown: 'Sin medir',
  },

  anomaly: {
    identity_model_name_mismatch: 'El proveedor declara un modelo distinto del solicitado',
    identity_family_mismatch: 'El proveedor declara un modelo de otro fabricante',
    identity_reported_model_absent: 'El proveedor no devuelve nombre de modelo: la identidad no es demostrable',
    capability_below_peer_best: 'Capacidad por debajo del mejor nodo que sirve el mismo modelo',
    capability_severely_below_peer_best: 'Capacidad muy por debajo del mejor nodo que sirve el mismo modelo',
    capability_above_peer_best: 'Capacidad por encima del mejor nodo que sirve el mismo modelo',
    consistency_prompt_token_drift: 'La firma de la petición se ha desplazado: el backend pudo cambiar',
    consistency_system_fingerprint_drift: 'La huella del proveedor ha cambiado',
    reference_below_expected_accuracy: 'Por debajo del nivel que espera la referencia externa',
    capability_no_measurement: 'No hay medición de capacidad utilizable',
    capability_peer_sample_too_small: 'Muy pocos nodos de comparación para este modelo',
    identity_probe_failed: 'La sonda de identidad falló',
    identity_probe_empty_response: 'La sonda de identidad no recibió respuesta',
  },

  detail: {
    title: 'Detalle del veredicto',
    evidence: 'Evidencia',
    anomalies: 'Anomalías',
    history: 'Historial de veredictos',
    noHistory: 'Todavía no hay historial de veredictos',
    requestedModel: 'Modelo solicitado',
    reportedModel: 'Modelo declarado por el proveedor',
    promptSignature: 'Firma de la petición (tokens del prompt)',
    fingerprint: 'Huella del proveedor',
    measuredDimensions: 'Dimensiones con datos en esta ronda',
    close: 'Cerrar',
  },

  empty: {
    noData: 'Todavía no hay datos de fiabilidad.',
    hint: 'Los veredictos los genera el análisis periódico en segundo plano (una sonda pequeña por nodo y ronda); «Analizar todo» lanza una ronda ahora.',
  },

  error: {
    loadFailed: 'No se pudieron cargar los datos de fiabilidad de los modelos. Vuelve a intentarlo.',
    scanFailed: 'No se pudo completar el análisis de fiabilidad. Vuelve a intentarlo.',
    detailFailed: 'No se pudo cargar el detalle del veredicto de fiabilidad. Vuelve a intentarlo.',
  },
}
