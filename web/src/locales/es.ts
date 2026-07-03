export default {
  // General
  login: 'Iniciar sesión',
  logout: 'Cerrar sesión',
  changePassword: 'Cambiar contraseña',
  cancel: 'Cancelar',
  confirm: 'Confirmar',
  save: 'Guardar',
  delete: 'Eliminar',
  edit: 'Editar',
  search: 'Buscar',
  reset: 'Restablecer',
  submit: 'Enviar',
  back: 'Volver',
  next: 'Siguiente',
  previous: 'Anterior',
  close: 'Cerrar',
  
  // Roles de usuario
  role: {
    super_admin: 'Super Administrador',
    tenant_admin: 'Administrador de Inquilino',
  },
  
  // Navegación
  nav: {
    collapseSidebar: 'Contraer menú',
    expandSidebar: 'Expandir barra lateral',
  },
  
  // Contraseña
  password: {
    changeSuccess: 'Contraseña cambiada exitosamente',
    changeFailed: 'Error al cambiar la contraseña',
  },
  
  // Información de versión
  version: 'Versión',
  build: 'Build',

  // 2026-07-02 (visualización de adjuntos request-logs): textos relacionados
  // con adjuntos, alineados con el documento de referencia §6.
  requests: {
    list: {
      table: {
        attachmentsTitle: 'Adjuntos',
        noAttachments: 'Sin adjuntos',
      },
    },
    detail_extra: {
      attachmentsTab: 'Adjuntos',
      attachmentsLoading: 'Cargando adjuntos…',
      noAttachments: 'Sin adjuntos',
      clickToPreviewTitle: 'Clic para previsualizar',
      download: 'Descargar',
      downloadOriginal: 'Descargar original',
      closePreview: 'Cerrar',
    },
  },

  // 2026-07-03 (flujo de solicitudes en tiempo real): swim lane del panel.
  dashboard: {
    liveStream: {
      title: 'Flujo de solicitudes en tiempo real',
      connected: 'Conectado',
      connecting: 'Conectando…',
      reconnecting: 'Reconectando…',
      disconnected: 'Desconectado',
      unsupported: 'No soportado',
      pause: 'Pausar',
      resume: 'Reanudar',
      filterAll: 'Todos los estados',
      filterSuccess: 'Sólo éxito',
      filterInProgress: 'En curso',
      filterGroupFailures: 'Desglose de fallos',
      filterFailure5xx: 'Servidor / upstream (5xx)',
      filterFailure4xx: 'Cliente / auth (4xx)',
      filterFailureTimeout: 'Timeout / red',
      filterFailureNotFound: 'Routing / modelo no encontrado',
      filterFailureOther: 'Otros fallos',
      empty: 'Esperando solicitudes…',
      countTooltip: '{buffer} en búfer / {visible} visibles',
      countAria: '{buffer} solicitudes en búfer, {visible} visibles',
      legend: {
        model: 'Familia de modelos',
        status: 'Estado',
        openai: 'OpenAI',
        anthropic: 'Anthropic',
        domestic: 'Doméstico',
        oss: 'Código abierto',
        other: 'Otro',
        success: 'Éxito',
        inProgress: 'En curso',
        failure: 'Fallo',
      },
    },
  },
}