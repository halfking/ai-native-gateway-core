// modulesView.ts — Página de gestión de módulos.
export default {
  pageTitle: 'Módulos',
  pageSubtitle: 'Gestión centralizada de módulos de funciones empresariales — active o desactive capacidades según sea necesario.',
  modulesEnabled: 'módulos activados',
  loading: 'Cargando…',

  category: {
    compression: 'Compresión de solicitudes',
    session: 'Gestión de sesiones',
    security: 'Seguridad',
    rate_limit: 'Limitación de tasa',
    general: 'General',
    integration: 'Integración',
  },

  status: {
    enabled: 'Activado',
    disabled: 'Desactivado',
    processing: 'Procesando…',
    enabledAction: 'Desactivar este módulo',
    disabledAction: 'Activar este módulo',
  },

  dangerLevel: {
    safe: 'Seguro',
    warn: 'Precaución',
    danger: 'Peligroso',
    breaking: 'Crítico',
    unknown: 'Desconocido',
  },

  tabs: {
    overview: 'Resumen',
    config: 'Configuración',
    integration: 'Integración',
    status: 'Ejecución',
  },

  overview: {
    sectionDescription: 'Descripción',
    sectionCapabilities: 'Capacidades',
    sectionRequirements: 'Dependencias',
    labelKey: 'Clave del módulo',
    labelDanger: 'Nivel de peligro',
    labelConfigCount: 'Elementos de configuración',
    labelStatus: 'Estado actual',
    viewAllSettings: 'Ver todos los ajustes del sistema',
    requirementsMet: 'Todas las dependencias activadas',
    requirementsMissing: 'Las siguientes dependencias están desactivadas — las funciones asociadas pueden estar limitadas:',
    jumpToModule: 'Configurar',
    testConnection: 'Probar conexión',
    testSuccess: 'Prueba de conexión exitosa',
    testFailed: 'Prueba de conexión fallida',
    testInProgress: 'Enviando mensaje de prueba…',
  },

  config: {
    noSettings: 'Este módulo no tiene ajustes configurables.',
    sourceDefault: 'Predeterminado',
    sourceEnv: 'Variable de entorno',
    sourceDb: 'Base de datos',
    switchOn: 'Activado',
    switchOff: 'Desactivado',
    inputPlaceholder: 'Introducir {description}',
    sections: {
      connection: 'Conexión',
      alerts: 'Reenvío de alertas',
      approvals: 'Notificaciones de aprobación',
      commands: 'Panel de comandos',
      security: 'Seguridad',
      general: 'General',
    },
  },

  feishu: {
    connectionHint: 'Cree un bot personalizado en la plataforma abierta Feishu y pegue la URL del webhook a continuación.',
    callbackUrlLabel: 'URL de callback (configurar en el backend del bot Feishu)',
    callbackUrlHelp: 'Pegue esta URL en la configuración de callback del bot personalizado de Feishu para recibir acciones de aprobación.',
    whitelistHelp: 'OpenIDs autorizados a ejecutar comandos del bot, separados por comas. Vacío = commands.admin_only decide.',
    quietHoursHelp: 'Durante las horas silenciosas solo se envían alertas críticas (evita interrupciones nocturnas). Se admiten ventanas que cruzan la medianoche (22:00 → 08:00).',
    commandsHelp: 'Cuando está activado, los administradores pueden interactuar con el sistema mediante comandos de Feishu (/status /help /stats /audit /test).',
    signatureHelp: 'Siempre active en producción. Cuando está activado, los callbacks de Feishu deben llevar una firma HMAC-SHA256 válida y una marca temporal dentro de la ventana.',
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'Master toggle and trigger mode: determine how session handoff is enabled and when it fires',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 trigger conditions run in parallel — any one of them fires handoff; the strictest wins',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'Summary engine and prompt template: how to compress the old session into a new-session-readable context',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'Cooldown, per-session cap, retry on failure, and log/Webhook notifications',
  },

  integration: {
    docsLabel: 'Documentación: ',
    stepsTitle: 'Pasos de configuración',
    enabledStatus: 'Integración activada',
    disabledHint: 'Integración desactivada — primero active el módulo',
    feishuSteps: [
      'Crear un bot personalizado en la plataforma abierta Feishu',
      'Copiar la URL del webhook y pegarla en la configuración a continuación',
      '(Opcional) Configurar el token de verificación de firma y la clave de cifrado',
      'Hacer clic en «Probar conexión» después de la configuración',
      'Activar el interruptor «Integración del bot Feishu»',
    ],
    feishuBotIntegration: 'Integración del bot Feishu',
  },

  empty: {
    selectModule: 'Seleccione un módulo para ver detalles y configuración',
  },

  error: {
    loadFailed: 'Error al cargar la lista de módulos',
    operationFailed: 'Operación fallida',
    saveFailed: 'Error al guardar la configuración',
    testFailed: 'Prueba fallida',
  },
}