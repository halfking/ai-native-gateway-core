// taskProfile.ts — 任务档案页文案（v2 闭环 P0③，2026-09-24）。
export default {
    refresh: 'Actualizar',
    refreshing: 'Actualizando…',
    loadFailed: 'Error al cargar',
    noData: 'Sin datos de perfil',
  registry: {
    version: 'Versión del registro',
    types: 'Tipos de tarea',
    correctedTotal: 'Correcciones en la ventana',
    pendingSuggestions: 'Cambios de nivel pendientes',
  },
  table: {
    taskType: 'Tipo de tarea',
    description: 'Descripción',
    tier: 'Tier',
    fallbacks: 'Cadena de respaldo',
    minConf: 'Confianza mín.',
    total: 'Correcciones',
    rate: 'Tasa de corrección',
    suggestion: 'Tier sugerido',
  },
  action: {
    apply: 'Aplicar sugerencias',
    applyConfirm: '¿Escribir las sugerencias de tier basadas en correcciones en la configuración de tier (task_type_tier_config)?',
    applyDone: 'Aplicado: {types}',
    applyNone: 'Ninguna sugerencia alcanza actualmente el umbral',
    reload: 'Recargar overlay',
    reloadConfirm: '¿Recargar el archivo de perfil TASKPROFILE_OVERLAY? Se restablece a los valores predeterminados integrados si no está configurado.',
    reloadDone: 'Registro recargado: {version}',
    exportCsv: 'Exportar correcciones CSV',
    days: 'Ventana (días)',
  },
  status: {
    applying: 'Aplicando…',
    reloading: 'Recargando…',
    exporting: 'Exportando…',
  },
}
