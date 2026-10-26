// Auto-translated draft (es-ES) · 2026-07-02 · please review
// auditLog.ts — Vista de registro de auditoría.
export default {
  page: {
    title: 'Registro de auditoría',
    desc: 'Registra acciones de gestión de usuarios y autenticación. Visible solo para superadministradores.',
    totalChip: '{n} entradas',
    refresh: 'Actualizar',
    refreshing: 'Actualizando…',
    emptyTitle: 'No hay registros de auditoría',
    emptyHint: 'Ajuste los filtros o amplíe el rango de tiempo y vuelva a intentarlo',
    loading: 'Cargando…',
  },
  filter: {
    actorLabel: 'Actor',
    actorPlaceholder: 'Coincidencia parcial por usuario',
    actionLabel: 'Acción',
    actionPlaceholder: 'p. ej. user.* o auth.*',
    fromLabel: 'Desde',
  },
  table: {
    headers: {
      time: 'Hora',
      actor: 'Actor',
      action: 'Acción',
      target: 'Objetivo',
      details: 'Detalles',
    },
  },
  detail: {
    titleWithId: 'Detalle de auditoría #{id}',
    close: 'Cerrar',
    metaTime: 'Hora',
    metaActor: 'Actor',
    metaAction: 'Acción',
    metaTarget: 'Objetivo',
    beforeTitle: 'Antes',
    afterTitle: 'Después',
    noExtra: 'Sin detalles adicionales',
  },
  actions: {
    'user.create': 'Crear usuario',
    'user.update': 'Actualizar usuario',
    'user.delete': 'Eliminar usuario',
    'user.reset_password': 'Restablecer contraseña',
    'user.change_password': 'Cambiar contraseña',
    'auth.login': 'Inicio de sesión',
    'auth.login_failed': 'Inicio de sesión fallido',
    'auth.logout': 'Cierre de sesión',
    'auth.rate_limited': 'Limitado por tasa',
    'authentication.login': 'Inicio de sesión',
    'authentication.login_failed': 'Error de inicio de sesión',
    'authentication.logout': 'Cierre de sesión',
  },

  // 扁平键（供 Vue 组件直接使用）
  loadFailed: 'No se pudo cargar la lista de registros de auditoría. Vuelva a intentarlo.',
  dash: '—',
}
