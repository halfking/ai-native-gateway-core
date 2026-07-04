// German locale for TenantModelPolicyPanel
export default {
  title: 'Modellzugriffskontrolle',
  hint: 'Hier konfigurierte Modelle werden für alle API-Schlüssel unter diesem Mandanten abgelehnt (403 model_forbidden). Leere Tabelle = keine Einschränkungen für diesen Mandanten (Standard). model="auto" Pfad unterliegt nicht der Zugriffskontrolle.',
  showDeleted: 'Gelöschte anzeigen',
  addButton: '+ Verweigerte Modell hinzufügen',
  loading: 'Lädt…',
  
  table: {
    canonicalName: 'canonical_name',
    reason: 'reason',
    createdBy: 'created_by',
    createdAt: 'created_at',
    deletedAt: 'deleted_at',
    actions: 'Aktionen',
  },
  
  actions: {
    softDelete: 'Vorläufig löschen',
    restore: 'Wiederherstellen',
  },
  
  empty: 'Keine Richtlinien (alle Modelle standardmäßig erlaubt)',
  
  audit: {
    title: 'Überwachungsprotokoll',
    recent: 'Letzte {count} Einträge',
    headers: {
      ts: 'ts',
      action: 'action',
      canonicalName: 'canonical_name',
      actor: 'actor',
      reason: 'reason',
    },
    actionLabels: {
      insert: 'Erstellen',
      update: 'Aktualisieren',
      delete: 'Vorläufig löschen',
      undelete: 'Wiederherstellen',
    },
  },
  
  dialog: {
    title: 'Verweigerte Modell hinzufügen',
    hint: 'Geben Sie canonical_name unten ein (muss mit models_canonical Tabelle übereinstimmen).',
    canonicalName: 'canonical_name',
    canonicalNamePlaceholder: 'z.B. minimax-m3',
    checkButton: 'Validieren',
    checkSuccess: '✓ In models_canonical gefunden (family={family}, modality={modality})',
    checkWarning: '⚠ Dieser canonical_name ist nicht in models_canonical (Schreiben weiterhin erlaubt, defensive Kontrolle)',
    reason: 'reason',
    reasonPlaceholder: 'Optional, z.B. Kostenkontrolle',
    cancel: 'Abbrechen',
    submit: 'Senden',
    submitting: 'Wird gesendet…',
  },
  
  confirm: {
    softDelete: 'Richtlinie {name} vorläufig löschen bestätigen? (Wiederherstellbar)',
  },
  
  error: {
    loadFailed: 'Laden fehlgeschlagen',
    canonicalNameRequired: 'canonical_name ist erforderlich',
    createFailed: 'Erstellen fehlgeschlagen',
    deleteFailed: 'Löschen fehlgeschlagen',
    restoreFailed: 'Wiederherstellen fehlgeschlagen',
  },
}
