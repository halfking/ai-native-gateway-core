// modulesView.ts — Página de gestión de módulos.
export default {
  pageTitle: 'Módulos',
  pageSubtitle: 'Gestión centralizada de módulos de funciones empresariales. Active o desactive capacidades según sea necesario.',
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
    routing: 'Enrutamiento',
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
    requirementsMissing: 'Las siguientes dependencias están desactivadas. Las funciones asociadas pueden estar limitadas:',
    jumpToModule: 'Configurar',
    testConnection: 'Probar conexión',
    testSuccess: 'Prueba de conexión exitosa',
    testFailed: 'Prueba de conexión fallida',
    testInProgress: 'Enviando mensaje de prueba…',
    dependenciesTitle: 'Dependencias',
    required: 'Obligatorias',
    optional: 'Opcionales',
    enableAllDeps: 'Activar {count} dependencias obligatorias que faltan',
    enableAllDepsHint: 'El backend activará en orden estable todas las dependencias obligatorias que falten y después activará este módulo',
    cascading: 'Activando dependencias…',
    cascadeSuccess: 'Activadas automáticamente {count} dependencias: {names}',
    cascadeEmpty: 'Todas las dependencias ya estaban activadas; el módulo ya funciona',
    cascadeAlreadyEnabled: 'El módulo ya está activado, no hace falta la cascada',
    cascadeLogTitle: 'Dependencias activadas por cascada en esta ejecución:',

    sectionDependencies: 'Dependencies',

    dependencyDisabled: 'Dependency module is disabled',

    notEnabled: 'Not enabled',
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
    groupMasterHint: 'Interruptor general y modo de activación: definen si el relevo de sesión se activa y cuándo se dispara',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 condiciones de activación se evalúan en paralelo: basta con una para que se dispare el relevo, y gana la más estricta.',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'Motor de resumen y plantilla de prompt: cómo comprimir la sesión antigua en un contexto legible para la nueva sesión',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'Tiempo de espera, límite por sesión, reintento ante fallo y notificaciones por log o webhook',
  },

  integration: {
    docsLabel: 'Documentación: ',
    stepsTitle: 'Pasos de configuración',
    enabledStatus: 'Integración activada',
    disabledHint: 'Integración desactivada: primero active el módulo',
    feishuSteps: [
      'Crear un bot personalizado en la plataforma abierta Feishu',
      'Copiar la URL del webhook y pegarla en la configuración a continuación',
      '(Opcional) Configurar el token de verificación de firma y la clave de cifrado',
      'Hacer clic en «Probar conexión» después de la configuración',
      'Activar el interruptor «Integración del bot Feishu»',
    ],
    feishuBotIntegration: 'Integración del bot Feishu',
    wechatSteps: [
      'Cree una aplicación en el panel de administración de WeCom',
      '(Opcional) Configure la URL de callback y el Token en "Recibir mensajes"',
      'Copie CorpID, AgentID y Secret a la configuración',
      '(Opcional) Configure la URL del webhook del bot de grupo',
      '(Opcional) Configure EncodingAESKey para el cifrado de callback',
      'Active el interruptor "Integración del bot WeChat"',
      'Asegúrese de que todos los módulos de requisitos previos estén habilitados',
    ],
    wechatBotIntegration: 'Integración del bot WeChat',
    dingtalkSteps: [
      'Añada un «Bot personalizado» en un grupo DingTalk, elija seguridad «Firma» y copie el Webhook',
      'Pegue la URL del Webhook abajo e introduzca el Secret de firma',
      '(Opcional) Cree una app interna en la consola e introduzca AppKey/AppSecret/AgentID para notificaciones de trabajo',
      'Configure la URL de callback de aprobación (/api/webhooks/dingtalk/approval-callback) como receptor del bot',
      'Active el interruptor «Integración del bot DingTalk»',
      'Asegúrese de que los módulos requeridos (compresión, detección de inyección, caché de sesión, auditoría de sesión) estén activados',
    ],
    dingtalkBotIntegration: 'Integración del bot DingTalk',
  },

  empty: {
    selectModule: 'Seleccione un módulo para ver detalles y configuración',
  },

  routing: {
    title: 'Reglas de enrutamiento de Feishu',
    addNew: 'Añadir',
    cancel: 'Cancelar',
    save: 'Guardar',
    formTitle: 'Nueva regla de Feishu OpenID',
    openId: 'Feishu OpenID',
    openIdPlaceholder: 'ou_xxxxxxxx',
    displayName: 'Nombre mostrado',
    userRole: 'Rol de usuario',
    priority: 'Prioridad',
    note: 'Nota',
    enabled: 'Habilitado',
    riskLevels: 'Niveles de riesgo',
    actions: 'Acciones',
    enable: 'Habilitar',
    disable: 'Deshabilitar',
    delete: 'Eliminar',
    loading: 'Cargando…',
    importCsv: 'Importación masiva de CSV',
    close: 'Cerrar',
    csvImportResult: 'Resultado de la importación masiva',
    csvImportSuccess: '{imported} importadas, {skipped} omitidas',
    csvErrorRow: 'Fila {row}: {error}',
  },

  error: {
    loadFailed: 'Error al cargar la lista de módulos',
    operationFailed: 'Operación fallida',
    saveFailed: 'Error al guardar la configuración',
    testFailed: 'Prueba fallida',
  },

  // 同步的扁平键

  modeBannerTitle: 'This page only does three things:',

  modeBannerBody: 'Activa o desactiva los módulos instalados y descarga los que ya tienen licencia pero no están activados. Aquí no hay precios, pedidos ni planes.',
}