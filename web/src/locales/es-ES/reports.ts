// reports.ts — Página de conciliación de informes (doble vista proveedores/interna, i18n añadido en R65).
export default {
  // Cambio de vista
  providerView: 'Conciliación de proveedores',
  internalView: 'Conciliación interna',
  // Acciones de la barra de herramientas
  exportExcel: 'Exportar a Excel',
  rerun: 'Recalcular día final',
  rerunDone: 'Recalculo completado',
  rerunFailed: 'Error en el recálculo',
  // Cobertura / estado vacío
  daysCovered: 'días de instantáneas',
  noSnapshots: 'No hay instantáneas de informes en este intervalo (el trabajo de agregación diaria genera los datos del día anterior de madrugada, o use «Recalcular día final» para recuperarlos)',
  // Tarjetas de resumen
  requests: 'Solicitudes',
  totalTokens: 'Total de tokens',
  in: 'Entrada',
  out: 'Salida',
  cacheRead: 'Lectura de caché',
  cacheWrite: 'Escritura de caché',
  providerCost: 'Coste de proveedores',
  cacheHit: 'Aciertos de caché',
  internalCredits: 'Créditos internos',
  internalCost: 'Importe interno',
  // Títulos de las tablas agrupadas
  byProvider: 'Por proveedor',
  byTenant: 'Por inquilino',
  byPerson: 'Por persona',
  byModel: 'Por modelo',
  byDay: 'Por día',
  // Encabezados de columna
  provider: 'Proveedor',
  tenant: 'Inquilino',
  person: 'Persona',
  model: 'Modelo',
  date: 'Fecha',
  success: 'Éxitos',
  errors: 'Fallos',
  cost: 'Coste',
  errorBreakdown: 'Desglose de errores',
  qualityScore: "Puntuación de calidad",

}
