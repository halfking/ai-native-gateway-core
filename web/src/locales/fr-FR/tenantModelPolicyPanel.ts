// French locale for TenantModelPolicyPanel
export default {
  title: 'Contrôle d\'accès aux modèles',
  hint: 'Les modèles configurés ici seront refusés pour toutes les clés API sous ce locataire (403 model_forbidden). Table vide = aucune restriction pour ce locataire (par défaut). Le chemin model="auto" n\'est pas soumis au contrôle d\'accès.',
  showDeleted: 'Afficher les supprimés',
  addButton: '+ Ajouter un modèle refusé',
  loading: 'Chargement…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: 'Actions',
  },
  
  actions: {
    softDelete: 'Supprimer logiquement',
    restore: 'Restaurer',
  },
  
  empty: 'Aucune stratégie (tous les modèles autorisés par défaut)',
  
  audit: {
    title: 'Journal d\'audit',
    recent: '{count} entrées récentes',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: 'Créer',
      update: 'Mettre à jour',
      delete: 'Supprimer logiquement',
      undelete: 'Restaurer',
    },
  },
  
  dialog: {
    title: 'Ajouter un modèle refusé',
    hint: 'Entrez canonical_name ci-dessous (doit correspondre à la table models_canonical).',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: 'par ex. minimax-m3',
    checkButton: 'Valider',
    checkSuccess: '✓ Trouvé dans models_canonical (family={family}, modality={modality})',
    checkWarning: '⚠ Ce canonical_name n\'est pas dans models_canonical (écriture toujours autorisée, contrôle défensif)',
    reason: 'reason',
    reasonPlaceholder: 'Facultatif, par ex. contrôle des coûts',
    cancel: 'Annuler',
    submit: 'Soumettre',
    submitting: 'Soumission…',
  },
  
  confirm: {
    softDelete: 'Confirmer la suppression logique de la stratégie {name}? (Récupérable)',
  },
  
  error: {
    loadFailed: 'Échec du chargement',
    canonicalNameRequired: 'canonical_name est requis',
    createFailed: 'Échec de la création',
    deleteFailed: 'Échec de la suppression',
    restoreFailed: 'Échec de la restauration',
  },
}
