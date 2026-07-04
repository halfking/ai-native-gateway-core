// German locale for ClientConfigDialog
export default {
  title: '{tool} Konfigurationsgenerator',
  close: 'Schließen',
  
  step1: {
    title: '① API-Schlüssel auswählen (alle Schlüssel unter aktuellem Mandant)',
    refresh: 'Aktualisieren',
    refreshing: 'Wird aktualisiert…',
    loading: 'Lädt…',
    empty: {
      title: 'Keine API-Schlüssel',
      description: 'Es sind keine API-Schlüssel unter dem aktuellen Mandanten verfügbar. Administratoren können auf die Schaltfläche unten klicken, um einen neuen Schlüssel zu beantragen.',
      applyButton: 'Neuen Schlüssel beantragen',
    },
    selected: 'Ausgewählt:',
  },
  
  step2: {
    title: '② Betriebssystem',
    pathHint: 'Konfigurationsdateipfad:',
  },
  
  step3: {
    title: '③ Modellbereich auswählen',
    featured: 'Ausgewählte Modelle (Routing-Featured-Konfiguration)',
    all: 'Alle verfügbaren Modelle (vom Gateway)',
    featuredPreview: {
      loading: 'Lädt…',
      empty: 'Keine ausgewählten Modelle im Routing konfiguriert',
      manageButton: 'Zu "Routing-Richtlinie" gehen, um zu konfigurieren →',
      manage: 'Verwalten →',
    },
    allModels: {
      selectAll: 'Alle auswählen',
      deselectAll: 'Löschen',
      selected: 'ausgewählt',
      filterHint: '({count} Treffer)',
      searchPlaceholder: '🔍 Modellname / Anbieter / Familie suchen…',
      loading: 'Lädt…',
      noMatch: 'Keine passenden Modelle',
    },
  },
  
  footer: {
    generated: '{count} Modellkonfigurationen generiert',
    generate: 'Konfiguration generieren',
    generating: 'Wird generiert…',
    regenerate: 'Neu generieren',
  },
  
  results: {
    tabs: {
      file: 'Konfigurationsdatei',
      script: 'Konfigurationsskript',
      manual: 'Manuelle Schritte',
    },
    actions: {
      copyContent: 'Inhalt kopieren',
      downloadFile: 'Datei herunterladen',
      downloadScript: 'Skript herunterladen',
      scriptHint: 'Skript sichert alte Konfigurationsdateien automatisch',
    },
  },
  
  applyDialog: {
    title: 'Neuen API-Schlüssel beantragen',
    close: 'Schließen',
    applicationCode: 'Anwendungscode (application_code)',
    applicationCodePlaceholder: 'z.B. default-app',
    description: 'Beschreibung',
    descriptionPlaceholder: 'Optional: Grund / Zweck',
    cancel: 'Abbrechen',
    submit: 'Antrag senden',
    submitting: 'Wird gesendet…',
  },
  
  error: {
    applyFailed: 'Antrag fehlgeschlagen',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor unterstützt keine Datei-Schreibvorgänge — bitte manuell in der Settings-UI konfigurieren',
}
