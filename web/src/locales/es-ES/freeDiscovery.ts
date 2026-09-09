// freeDiscovery.ts — free discovery page copy (es-ES).
export default {
  page: {
      title: "Descubrimiento de recursos gratuitos",
      desc: "Configura plantillas de proveedores → escanea listas de modelos ascendentes → revisa → importa en lote al pool de recursos gratuitos (conectado al seguimiento de cuota OmniFree)."
    },
  common: {
      refresh: "Actualizar",
      loading: "Procesando…",
      empty: "Sin datos",
      actions: "Acciones",
      enabled: "Habilitada",
      disabled: "Deshabilitada",
      hideForm: "Ocultar"
    },
  tabs: {
      templates: "Plantillas",
      tasks: "Tareas y revisión",
      history: "Historial de importación"
    },
  presets: {
      title: "Ajustes preestablecidos de proveedores",
      create: "Crear",
      exists: "Existente",
      createDone: "Plantilla creada desde el preajuste: {name}",
      scannerPending: "El escáner para este protocolo está en adaptación; el escaneo fallará por ahora",
      keyless: "keyless (sin clave API)"
    },
  form: {
      show: "+ Plantilla personalizada",
      providerCode: "Provider Code *",
      displayName: "Nombre visible",
      baseUrl: "Base URL *",
      apiType: "Protocolo API",
      apiKeyEnv: "Variable de entorno de clave API",
      apiKeyEnvHint: "La clave nunca se guarda: usa una referencia de entorno $VAR (p. ej. $GROQ_API_KEY); vacío = keyless.",
      tosVerdict: "Veredicto ToS",
      submit: "Crear plantilla",
      created: "Plantilla creada"
    },
  orbi: {
      show: "Importar JSON de plantilla Orbi",
      hint: "Pega el contenido de un archivo de plantilla Orbi pi-providers (JSON con clave providers en el nivel superior). Un archivo puede contener varios proveedores.",
      import: "Importar",
      invalidJson: "No se pudo analizar el JSON; revisa el formato",
      done: "Importación terminada: {created} creadas, {failed} fallidas"
    },
  tpl: {
      listTitle: "Plantillas ({n})",
      name: "Plantilla",
      baseUrl: "Base URL",
      apiType: "Protocolo API",
      keyEnv: "Variable de entorno de clave",
      tos: "ToS",
      enabled: "Habilitada",
      createdAt: "Creada",
      scan: "Escanear",
      delete: "Eliminar",
      deleteConfirm: "¿Eliminar la plantilla \"{name}\"? Las tareas y resultados de descubrimiento existentes no se ven afectados.",
      deleted: "Plantilla eliminada: {name}"
    },
  scan: {
      title: "Iniciar descubrimiento",
      pickTemplate: "Elige una plantilla habilitada…",
      start: "Iniciar escaneo",
      running: "Escaneando…",
      done: "Escaneo terminado: {n} modelos encontrados; revisa los resultados",
      failed: "Falló el escaneo"
    },
  task: {
      listTitle: "Tareas de descubrimiento ({n})",
      provider: "Proveedor",
      status: "Estado",
      trigger: "Disparador",
      found: "Encontrados",
      imported: "Importados",
      by: "Por",
      time: "Creada",
      error: "Error",
      review: "Revisar resultados"
    },
  res: {
      title: "Resultados · tarea {id} · {provider}",
      pending: "Pendiente de revisión",
      all: "Todos",
      model: "ID del modelo",
      displayName: "Nombre visible",
      freeType: "Tipo gratuito",
      monthly: "Cuota mensual (tokens)",
      daily: "Cuota diaria (tokens)",
      tos: "ToS",
      importStatus: "Estado de importación",
      none: "Sin resultados para este filtro",
      policy: "Política de conflicto",
      policySkip: "skip: conservar entradas existentes",
      policyOverwrite: "overwrite: sobrescribir y reactivar",
      policyMerge: "merge: completar solo campos vacíos",
      importSelected: "Importar selección ({n})",
      importAllPending: "Importar todos los pendientes",
      importedToast: "Importación terminada: {imported} importados, {skipped} omitidos, {conflicted} conflictos, {failed} fallidos"
    },
  status: {
      taskPending: "En espera",
      running: "En curso",
      success: "Éxito",
      failed: "Fallido",
      review: "Pendiente",
      imported: "Importado",
      skipped: "Omitido",
      conflict: "Conflicto"
    },
  trigger: {
      manual: "Manual",
      scheduled: "Programado",
      webhook: "Webhook"
    },
  hist: {
      title: "Historial de importación ({n})",
      desc: "Tareas que realmente importaron modelos (importe > 0). Pulsa Detalle para ver el destino final de cada resultado.",
      completed: "Completada",
      detail: "Detalle",
      none: "Aún no hay importaciones. Completa una importación por lotes en la pestaña Tareas y revisión y aparecerá aquí.",
      detailTitle: "Detalle de importación · tarea {id} · {provider}",
      importedAt: "Importado"
    },
}
