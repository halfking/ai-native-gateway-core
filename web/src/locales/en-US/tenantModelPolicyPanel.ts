// English locale for TenantModelPolicyPanel
export default {
  title: 'Model Access Control',
  hint: 'Models configured here will be denied for all API keys under this tenant (403 model_forbidden). Empty table = no restrictions for this tenant (default). model="auto" path is not subject to access control.',
  showDeleted: 'Show Deleted',
  addButton: '+ Add Denied Model',
  loading: 'Loading…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: 'Actions',
  },
  
  actions: {
    softDelete: 'Soft Delete',
    restore: 'Restore',
  },
  
  empty: 'No policies (all models allowed by default)',
  
  audit: {
    title: 'Audit Log',
    recent: 'Recent {count} entries',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: 'Create',
      update: 'Update',
      delete: 'Soft Delete',
      undelete: 'Restore',
    },
  },
  
  dialog: {
    title: 'Add Denied Model',
    hint: 'Enter canonical_name below (must match models_canonical table).',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: 'e.g. minimax-m3',
    checkButton: 'Validate',
    checkSuccess: '✓ Found in models_canonical (family={family}, modality={modality})',
    checkWarning: '⚠ This canonical_name is not in models_canonical (write still allowed, defensive control)',
    reason: 'reason',
    reasonPlaceholder: 'Optional, e.g. cost control',
    cancel: 'Cancel',
    submit: 'Submit',
    submitting: 'Submitting…',
  },
  
  confirm: {
    softDelete: 'Confirm soft delete policy {name}? (Recoverable)',
  },
  
  error: {
    loadFailed: 'Failed to load',
    canonicalNameRequired: 'canonical_name is required',
    createFailed: 'Failed to create',
    deleteFailed: 'Failed to delete',
    restoreFailed: 'Failed to restore',
  },
}
