// Auto-translated draft (es-ES) · 2026-07-02 · please review
// credentialMonitor.ts — Monitor de credenciales.
export default {
  page: {
    title: 'Monitor de credenciales',
  },
  table: {
    cell: {
      modelsNotMeasured: 'Esta credencial no devolvió recuentos de modelos esta vez (sin medir): no es «0 disponibles»',
    },
  },
  drawer: {
    tab: {
      overview: 'Información general',
      models: 'Modelos',
      requests: 'Solicitudes',
    },
    operationFailedWithReason: 'No se pudo completar la operación: {reason}. Vuelva a intentarlo.',
    offlineFailedWithReason: 'No se pudo pasar a offline: {reason}. Vuelva a intentarlo.',
    onlineFailedWithReason: 'No se pudo pasar a online: {reason}. Vuelva a intentarlo.',
    degradeFailedWithReason: 'No se pudo degradar: {reason}. Vuelva a intentarlo.',
    upgradeFailedWithReason: 'No se pudo actualizar: {reason}. Vuelva a intentarlo.',
    settingFailedWithReason: 'No se pudo aplicar la configuración: {reason}. Vuelva a intentarlo.',
    manualDisabledTooltip: 'Desactivada manualmente: la sonda automática no la toca. Puede reactivarla manualmente.',
    availableTooltip: 'Actualmente disponible; puede desactivarla manualmente.',
    autoControlledTooltip: 'Controlada por la sonda automática (broken_confirmed, etc.). Puede desactivarla manualmente (forzarla a offline).',
    rowSelectsMonitor: 'Seleccione una fila para ver el monitor de la derecha.',
    selectModelRow: 'Seleccione una fila de modelo de la izquierda para ver los detalles.',
    sourceSessionTurns: 'Registro de sesión',
  },
  chart: {
    allHealthy: 'Todas sanas',
    errorsWhenHealthy: 'Distribución de errores: todas sanas',
    errorsTitle: 'Distribución de errores',
    /**
     * Estado vacío del mapa de calor de credenciales (2026-10-27 · incremento 14).
     * ★ kind = **Filtered**: esta página filtra por modelo, excluye la
     *   autoverificación, muestra solo anomalías y aplica un rango de tiempo
     *   ⇒ vacío = filtrado, no «esta página no tiene datos».
     */
    emptyFiltered: 'No hay registros de credenciales con el rango de tiempo y los filtros actuales; puede ampliar las condiciones para ver más.',
  },
  error: {
    selectFirst: 'Seleccione primero las credenciales',
    clearFailed: 'Error al borrar: ',
    batchFailed: 'Operación en lote fallida: ',
    superAdminOnlyHint: 'Solo los superadministradores pueden aplicar correcciones de estado; actualmente en modo de solo lectura.',
    superAdminOnlyError: 'Solo los superadministradores pueden aplicar correcciones de estado.',
  },
}
