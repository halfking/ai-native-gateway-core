// Spanish locale for ClientConfigDialog
export default {
  title: 'Generador de configuración {tool}',
  close: 'Cerrar',

  step1: {
    title: "① Seleccionar una clave API disponible",
    refresh: 'Actualizar',
    refreshing: 'Actualizando…',
    loading: 'Cargando…',
    empty: {
      title: 'Sin claves API',
      description: 'No hay claves API disponibles bajo el inquilino actual. Los administradores pueden hacer clic en el botón a continuación para solicitar una nueva clave.',
      applyButton: 'Solicitar nueva clave',
    },
    selected: 'Seleccionado:',
  },

  step2: {
    title: '② Sistema operativo',
    pathHint: "Ubicación de configuración:",
  },

  step3: {
    title: "④ Seleccionar alcance de modelos",
    featured: "Modelos destacados (enrutamiento del inquilino de la clave seleccionada)",
    all: 'Todos los modelos disponibles (desde la puerta de enlace)',
    featuredPreview: {
      empty: "No hay modelos destacados configurados para el inquilino seleccionado",
      manageButton: 'Ir a "Política de enrutamiento" para configurar →',
      manage: 'Administrar →',
    },
    allModels: {
      selectAll: 'Seleccionar todo',
      deselectAll: 'Limpiar',
      selected: 'seleccionado',
      filterHint: '({count} coincidencias de búsqueda)',
      searchPlaceholder: '🔍 Buscar nombre de modelo / proveedor / familia…',
      noMatch: 'No hay modelos coincidentes',
    },
  },

  retry: "Reintentar",
  otherFamily: "Otros",
  gateway: {
    title: "③ URL de la API de la puerta de enlace",
    hint: "Se usa la dirección de esta puerta de enlace por defecto. Los archivos y scripts generados contienen la clave API seleccionada; guárdelos de forma segura.",
  },
  provider: {
    label: "Nombre del proveedor",
    hint: "Se actualiza la entrada con el mismo nombre y URL. Si la URL es distinta, el script se detiene y solicita otro nombre.",
  },
  notes: {
    cherryStudio: "Introduzca la clave seleccionada en los ajustes del cliente. Este archivo solo enumera los parámetros.",
    rooCode: "Configure OpenAI Compatible en los ajustes de Roo Code.",
  },

  footer: {
    generated: '{count} configuraciones de modelo generadas',
    generate: 'Generar configuración',
    generating: 'Generando…',
    regenerate: 'Regenerar',
  },

  results: {
    tabs: {
      yaml: "Fragmento YAML",
      file: 'Archivo de configuración',
      script: 'Script de configuración',
      manual: 'Pasos manuales',
    },
    actions: {
      scriptWarning: "El script contiene su clave. Descárguelo y ejecútelo localmente; su contenido no se muestra aquí.",
      mergeHint: "El script guarda una copia de la configuración anterior, combina {provider} y conserva otros proveedores",
      copyContent: 'Copiar contenido',
      downloadFile: 'Descargar archivo',
      downloadScript: 'Descargar script',
    },
  },

  applyDialog: {
    title: 'Solicitar nueva clave API',
    applicationCode: 'Código de aplicación (application_code)',
    applicationCodePlaceholder: 'ej. default-app',
    description: 'Descripción',
    descriptionPlaceholder: 'Opcional: razón / propósito',
    cancel: 'Cancelar',
    submit: 'Enviar solicitud',
    submitting: 'Enviando…',
  },

  error: {
    invalidGatewayURL: "Introduzca una URL de API HTTP(S) válida sin credenciales, parámetros de consulta ni fragmentos.",
    invalidProviderName: "Use de 1 a 64 letras (A–Z), números, guiones bajos o guiones para el nombre del proveedor, comenzando con una letra o un número.",
    featuredLoadFailed: "No se pudieron cargar los modelos destacados. Reinténtelo.",
    catalogLoadFailed: "No se pudo cargar el catálogo de modelos. Reinténtelo.",
    selectModel: "Seleccione al menos un modelo. Si no hay modelos destacados, configúrelos primero en Política de enrutamiento.",
    keyUnavailable: "No se pudo leer la clave seleccionada. Selecciónela de nuevo.",
    generateFailed: "No se pudo generar la configuración. Reinténtelo.",
    copyFailed: "Error al copiar. Descargue el archivo.",
    applyFailed: 'La solicitud falló',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor no admite escritura de archivos: configúrelo manualmente en la interfaz de Settings',
}
