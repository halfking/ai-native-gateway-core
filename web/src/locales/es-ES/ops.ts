export default {

  // License Management

  // Fault Management

  // Auto Update

  // Center Operations

  // VibeCoding
  vibecoding: {
    title: 'VibeCoding',
    loadFailed: 'No se pudo cargar los datos. Vuelva a intentarlo.',
    fillRequired: 'Rellene los campos obligatorios',
    projects: 'Proyectos',
    sessions: 'Sesiones',
    codeReviews: 'Revisiones de código',
    createProject: 'Crear proyecto',
    createProjectTitle: 'Crear un proyecto nuevo',
    createProjectSuccess: 'Proyecto creado correctamente',
    createProjectFailed: 'No se pudo crear el proyecto. Vuelva a intentarlo.',
    newSession: 'Nueva sesión',
    createSessionTitle: 'Crear una sesión nueva',
    createSessionSuccess: 'Sesión creada correctamente',
    createSessionFailed: 'No se pudo crear la sesión. Vuelva a intentarlo.',
    viewSessions: 'Ver sesiones',
    viewReviews: 'Ver revisiones',
    showAll: 'Mostrar todo',
    reviewDetail: 'Detalle de la revisión',
    /**
     * Estados vacíos (2026-10-27 · incremento 14 · «empty state de 5 puntos»).
     * ★ Antes las tres tablas compartían una sola frase `hyper.list.empty`
     *   («La lista todavía no tiene contenido.»), pero son **dos** estados:
     *   · projects —— usa el array original ⇒ vacío = aún no se creó ningún
     *     proyecto (FirstUse)
     *   · sessions / reviews —— usan el array **ya filtrado** ⇒ vacío = quedó
     *     filtrado (Filtered)
     *   ⇒ una sola frase para ambos estados equivale a decir «todavía no has
     *     empezado» y «tus filtros son demasiado estrechos» como si fueran lo mismo.
     */
    emptyFirstUseProjects: 'Aún no se ha creado ningún proyecto.',
    emptyFilteredSessions: 'No hay registros de sesiones con los filtros actuales.',
    emptyFilteredReviews: 'No hay registros de revisiones de código con los filtros actuales.',
    projectName: 'Nombre del proyecto',
    projectNamePlaceholder: 'Introduzca el nombre del proyecto',
    language: 'Lenguaje',
    languagePlaceholder: 'p. ej. TypeScript',
    framework: 'Entorno de desarrollo',
    frameworkPlaceholder: 'p. ej. Vue 3',
    projectId: 'ID del proyecto',
    sessionName: 'Nombre de la sesión',
    taskType: 'Tipo de tarea',
    taskTypePlaceholder: 'p. ej. revisión de código, refactorización, explicación',
    startedAt: 'Inicio',
    endedAt: 'Fin',
    filePath: 'Ruta del archivo',
    score: 'Puntuación',
    summary: 'Resumen',
    issues: 'Problemas',
    suggestions: 'Sugerencias',
    category: 'Categoría',
    reviewedAt: 'Revisado el',
    line: 'Línea',
    severity: 'Gravedad',
    message: 'Mensaje',
  },


  overview: {




























    nodeDetailSuperAdminOnlyHint: 'Solo los superadministradores pueden mantener los nodos; se muestra en modo de solo lectura.',
    nodeDetailDefaultTenantOnlyHint: 'Solo el tenant default puede mantener los nodos; se muestra en modo de solo lectura.',

















    pingFailedWithReason: 'El ping de sesión falló: {reason}. Vuelva a intentarlo.',
    saveSettingFailedWithReason: 'No se pudo guardar la configuración: {reason}. Vuelva a intentarlo.',
    maintainFailed: 'No se pudo completar el mantenimiento. Vuelva a intentarlo.',
    modelMaintainFailed: 'No se pudo completar el mantenimiento de modelos. Vuelva a intentarlo.',
    fpSlotLoadFailed: 'No se pudieron cargar los slots de huella. Vuelva a intentarlo.',
    fpSlotProviderMissing: 'Falta provider_id, por lo que no se pudieron cargar los slots de huella. Seleccione primero un proveedor y vuelva a intentarlo.',
    accessErrorsHint: 'Llamadas fallidas + errores de enrutamiento · Abra el detalle de la solicitud original',
    recentRequestsHint: 'Última hora{source} · Ver el detalle de las solicitudes',
  }
,

}
