// Spanish locale for ClientConfigDialog
export default {
  title: 'Generador de configuración {tool}',
  close: 'Cerrar',
  
  step1: {
    title: '① Seleccionar clave API (todas las claves bajo el inquilino actual)',
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
    pathHint: 'Ruta del archivo de configuración:',
  },
  
  step3: {
    title: '③ Seleccionar alcance del modelo',
    featured: 'Modelos destacados (configuración de enrutamiento featured)',
    all: 'Todos los modelos disponibles (desde la puerta de enlace)',
    featuredPreview: {
      loading: 'Cargando…',
      empty: 'No hay modelos destacados configurados en el enrutamiento',
      manageButton: 'Ir a "Política de enrutamiento" para configurar →',
      manage: 'Administrar →',
    },
    allModels: {
      selectAll: 'Seleccionar todo',
      deselectAll: 'Limpiar',
      selected: 'seleccionado',
      filterHint: '({count} coincidencias de búsqueda)',
      searchPlaceholder: '🔍 Buscar nombre de modelo / proveedor / familia…',
      loading: 'Cargando…',
      noMatch: 'No hay modelos coincidentes',
    },
  },
  
  footer: {
    generated: '{count} configuraciones de modelo generadas',
    generate: 'Generar configuración',
    generating: 'Generando…',
    regenerate: 'Regenerar',
  },
  
  results: {
    tabs: {
      file: 'Archivo de configuración',
      script: 'Script de configuración',
      manual: 'Pasos manuales',
    },
    actions: {
      copyContent: 'Copiar contenido',
      downloadFile: 'Descargar archivo',
      downloadScript: 'Descargar script',
      scriptHint: 'El script hace copia de seguridad automática de archivos de configuración antiguos',
    },
  },
  
  applyDialog: {
    title: 'Solicitar nueva clave API',
    close: 'Cerrar',
    applicationCode: 'Código de aplicación (application_code)',
    applicationCodePlaceholder: 'ej. default-app',
    description: 'Descripción',
    descriptionPlaceholder: 'Opcional: razón / propósito',
    cancel: 'Cancelar',
    submit: 'Enviar solicitud',
    submitting: 'Enviando…',
  },
  
  error: {
    applyFailed: 'La solicitud falló',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor no admite escritura de archivos — configúrelo manualmente en la interfaz de Settings',
}
