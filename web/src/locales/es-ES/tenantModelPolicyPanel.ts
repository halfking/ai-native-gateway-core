// Spanish locale for TenantModelPolicyPanel
export default {
  title: 'Control de acceso a modelos',
  hint: 'Los modelos configurados aquí serán denegados para todas las claves API bajo este inquilino (403 model_forbidden). Tabla vacía = sin restricciones para este inquilino (predeterminado). La ruta model="auto" no está sujeta al control de acceso.',
  showDeleted: 'Mostrar eliminados',
  addButton: '+ Agregar modelo denegado',
  loading: 'Cargando…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: 'Acciones',
  },
  
  actions: {
    softDelete: 'Eliminación lógica',
    restore: 'Restaurar',
  },
  
  empty: 'Sin políticas (todos los modelos permitidos por defecto)',
  
  audit: {
    title: 'Registro de auditoría',
    recent: '{count} entradas recientes',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: 'Crear',
      update: 'Actualizar',
      delete: 'Eliminación lógica',
      undelete: 'Restaurar',
    },
  },
  
  dialog: {
    title: 'Agregar modelo denegado',
    hint: 'Ingrese canonical_name a continuación (debe coincidir con la tabla models_canonical).',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: 'ej. minimax-m3',
    checkButton: 'Validar',
    checkSuccess: '✓ Encontrado en models_canonical (family={family}, modality={modality})',
    checkWarning: '⚠ Este canonical_name no está en models_canonical (escritura aún permitida, control defensivo)',
    reason: 'reason',
    reasonPlaceholder: 'Opcional, ej. control de costos',
    cancel: 'Cancelar',
    submit: 'Enviar',
    submitting: 'Enviando…',
  },
  
  confirm: {
    softDelete: '¿Confirmar eliminación lógica de la política {name}? (Recuperable)',
  },
  
  error: {
    loadFailed: 'Error al cargar',
    canonicalNameRequired: 'canonical_name es obligatorio',
    createFailed: 'Error al crear',
    deleteFailed: 'Error al eliminar',
    restoreFailed: 'Error al restaurar',
  },
}
