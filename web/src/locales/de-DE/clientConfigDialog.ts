// German locale for ClientConfigDialog
export default {
  title: '{tool} Konfigurationsgenerator',
  close: 'Schließen',

  step1: {
    title: "① Verfügbaren API-Schlüssel auswählen",
    refresh: 'Aktualisieren',
    refreshing: 'Wird aktualisiert…',
    loading: 'Lädt…',
    empty: {
      title: 'Keine API-Schlüssel',
      description: 'Unter dem aktuellen Mandanten sind noch keine API-Schlüssel verfügbar. Administratoren können unten einen neuen Schlüssel beantragen.',
      applyButton: 'Neuen Schlüssel beantragen',
    },
    selected: 'Ausgewählt:',
  },

  step2: {
    title: '② Betriebssystem',
    pathHint: "Konfigurationsort:",
  },

  step3: {
    title: "④ Modellumfang auswählen",
    featured: "Empfohlene Modelle (Routing des Mandanten des gewählten Schlüssels)",
    all: 'Alle verfügbaren Modelle (vom Gateway)',
    featuredPreview: {
      empty: "Für den gewählten Mandanten sind keine empfohlenen Modelle konfiguriert",
      manageButton: 'Zu "Routing-Richtlinie" gehen, um zu konfigurieren →',
      manage: 'Verwalten →',
    },
    allModels: {
      selectAll: 'Alle auswählen',
      deselectAll: 'Löschen',
      selected: 'ausgewählt',
      filterHint: '({count} Treffer)',
      searchPlaceholder: '🔍 Modellname / Anbieter / Familie suchen…',
      noMatch: 'Keine passenden Modelle',
    },
  },

  retry: "Erneut versuchen",
  otherFamily: "Andere",
  gateway: {
    title: "③ Gateway-API-URL",
    hint: "Standardmäßig wird die Adresse dieses Gateways verwendet. Generierte Dateien und Skripte enthalten den gewählten API-Schlüssel. Bewahren Sie sie sicher auf.",
  },
  provider: {
    label: "Anbietername",
    hint: "Ein Eintrag mit gleichem Namen und gleicher URL wird aktualisiert. Bei abweichender URL stoppt das Skript und fordert einen neuen Namen an.",
  },
  notes: {
    cherryStudio: "Tragen Sie den gewählten Schlüssel in den Client-Einstellungen ein. Diese Datei enthält nur die Parameter.",
    rooCode: "Konfigurieren Sie OpenAI Compatible in den Einstellungen von Roo Code.",
  },

  footer: {
    generated: '{count} Modellkonfigurationen generiert',
    generate: 'Konfiguration generieren',
    generating: 'Wird generiert…',
    regenerate: 'Neu generieren',
  },

  results: {
    tabs: {
      yaml: "YAML-Ausschnitt",
      file: 'Konfigurationsdatei',
      script: 'Konfigurationsskript',
      manual: 'Manuelle Schritte',
    },
    actions: {
      scriptWarning: "Das Skript enthält Ihren Schlüssel. Laden Sie es herunter und führen Sie es lokal aus. Der Inhalt wird hier nicht angezeigt.",
      mergeHint: "Das Skript sichert die alte Konfiguration, führt {provider} zusammen und erhält andere Anbieter",
      copyContent: 'Inhalt kopieren',
      downloadFile: 'Datei herunterladen',
      downloadScript: 'Skript herunterladen',
    },
  },

  applyDialog: {
    title: 'Neuen API-Schlüssel beantragen',
    applicationCode: 'Anwendungscode (application_code)',
    applicationCodePlaceholder: 'z.B. default-app',
    description: 'Beschreibung',
    descriptionPlaceholder: 'Optional: Grund / Zweck',
    cancel: 'Abbrechen',
    submit: 'Antrag senden',
    submitting: 'Wird gesendet…',
  },

  error: {
    invalidGatewayURL: "Geben Sie eine gültige HTTP(S)-API-URL ohne Zugangsdaten, Abfrageparameter oder Fragment ein.",
    invalidProviderName: "Der Anbietername muss aus 1–64 Buchstaben (A–Z), Ziffern, Unterstrichen oder Bindestrichen bestehen und mit einem Buchstaben oder einer Ziffer beginnen.",
    featuredLoadFailed: "Empfohlene Modelle konnten nicht geladen werden. Bitte erneut versuchen.",
    catalogLoadFailed: "Modellkatalog konnte nicht geladen werden. Bitte erneut versuchen.",
    selectModel: "Wählen Sie mindestens ein Modell. Falls keine empfohlenen Modelle vorhanden sind, konfigurieren Sie diese zuerst unter Routing-Richtlinie.",
    keyUnavailable: "Der gewählte Schlüssel konnte nicht gelesen werden. Bitte erneut auswählen.",
    generateFailed: "Die Konfiguration konnte nicht generiert werden. Bitte erneut versuchen.",
    copyFailed: "Kopieren fehlgeschlagen. Bitte die Datei herunterladen.",
    applyFailed: 'Antrag fehlgeschlagen',
  },
  // Added 2026-07-02 — Cursor fallback note (used by ClientConfigDialog when tool === 'cursor').
  cursorNotSupported: 'Cursor unterstützt keine Datei-Schreibvorgänge. Bitte manuell in der Settings-UI konfigurieren',
}
