// modulesView.ts — Modulverwaltungsseite.
export default {
  pageTitle: 'Module',
  pageSubtitle: 'Zentrale Verwaltung der Enterprise-Funktionsmodule — Funktionen nach Bedarf aktivieren/deaktivieren.',
  modulesEnabled: 'Module aktiviert',
  loading: 'Wird geladen…',

  category: {
    compression: 'Anfrage-Komprimierung',
    session: 'Sitzungsverwaltung',
    security: 'Sicherheit',
    rate_limit: 'Ratenbegrenzung',
    general: 'Allgemein',
    integration: 'Integration',
  },

  status: {
    enabled: 'Aktiviert',
    disabled: 'Deaktiviert',
    processing: 'Wird verarbeitet…',
    enabledAction: 'Dieses Modul deaktivieren',
    disabledAction: 'Dieses Modul aktivieren',
  },

  dangerLevel: {
    safe: 'Sicher',
    warn: 'Vorsicht',
    danger: 'Gefährlich',
    breaking: 'Kritisch',
    unknown: 'Unbekannt',
  },

  tabs: {
    overview: 'Übersicht',
    config: 'Konfiguration',
    integration: 'Integration',
    status: 'Laufzeit',
    routing: 'Routing',
  },

  overview: {
    sectionDescription: 'Beschreibung',
    sectionCapabilities: 'Funktionen',
    sectionRequirements: 'Abhängigkeiten',
    labelKey: 'Modulschlüssel',
    labelDanger: 'Gefahrenstufe',
    labelConfigCount: 'Konfigurationselemente',
    labelStatus: 'Aktueller Status',
    viewAllSettings: 'Alle Systemeinstellungen anzeigen',
    requirementsMet: 'Alle Abhängigkeiten aktiviert',
    requirementsMissing: 'Die folgenden Abhängigkeiten sind deaktiviert — zugehörige Funktionen sind möglicherweise eingeschränkt:',
    jumpToModule: 'Konfigurieren',
    testConnection: 'Verbindung testen',
    testSuccess: 'Verbindungstest erfolgreich',
    testFailed: 'Verbindungstest fehlgeschlagen',
    testInProgress: 'Testnachricht wird gesendet…',
  },

  config: {
    noSettings: 'Dieses Modul hat keine konfigurierbaren Einstellungen.',
    sourceDefault: 'Standard',
    sourceEnv: 'Umgebungsvariable',
    sourceDb: 'Datenbank',
    switchOn: 'Ein',
    switchOff: 'Aus',
    inputPlaceholder: '{description} eingeben',
    sections: {
      connection: 'Verbindung',
      alerts: 'Alarmweiterleitung',
      approvals: 'Genehmigungsbenachrichtigungen',
      commands: 'Befehlspanel',
      security: 'Sicherheit',
      general: 'Allgemein',
    },
  },

  feishu: {
    connectionHint: 'Erstellen Sie einen benutzerdefinierten Bot auf der Feishu Open Platform und fügen Sie die Webhook-URL unten ein.',
    callbackUrlLabel: 'Callback-URL (im Feishu-Bot-Backend konfigurieren)',
    callbackUrlHelp: 'Fügen Sie diese URL in die Feishu-Bot-Callback-Konfiguration ein, um Genehmigungsaktionen zu empfangen.',
    whitelistHelp: 'OpenIDs, die Befehle ausführen dürfen (durch Kommas getrennt). Leer = commands.admin_only entscheidet.',
    quietHoursHelp: 'Während der Ruhezeiten werden nur kritische Alarme gepusht (vermeidet nächtliche Störungen). Übernacht-Zeiträume (22:00 → 08:00) werden unterstützt.',
    commandsHelp: 'Wenn aktiviert, können Administratoren über Feishu-Befehle (/status /help /stats /audit /test) mit dem System interagieren.',
    signatureHelp: 'In Produktion immer aktivieren. Aktiviert erfordert Feishu-Callbacks eine gültige HMAC-SHA256-Signatur und einen Zeitstempel im Fenster.',
  },

  integration: {
    docsLabel: 'Dokumentation: ',
    stepsTitle: 'Konfigurationsschritte',
    enabledStatus: 'Integration aktiviert',
    disabledHint: 'Integration deaktiviert — bitte zuerst das Modul aktivieren',
    feishuSteps: [
      'Erstellen Sie einen benutzerdefinierten Bot auf der Feishu Open Platform',
      'Kopieren Sie die Webhook-URL und fügen Sie sie in die unten stehende Konfiguration ein',
      '(Optional) Konfigurieren Sie Signatur-Verifizierungstoken und Verschlüsselungsschlüssel',
      'Klicken Sie nach der Konfiguration auf „Verbindung testen"',
      'Schalten Sie den Schalter „Feishu-Bot-Integration" ein',
    ],
    feishuBotIntegration: 'Feishu-Bot-Integration',
  },

    importCsv: 'CSV importieren',
    close: 'Schließen',
    csvImportResult: 'Import-Ergebnis',
    csvImportSuccess: '{imported} importiert, {skipped} übersprungen',
    csvErrorRow: 'Zeile {row}: {error}',
  empty: {
    selectModule: 'Wählen Sie ein Modul aus, um Details und Konfiguration anzuzeigen',
  },

  routing: {
    title: 'Feishu-Routing-Regeln',
    addNew: 'Neu hinzufügen',
    cancel: 'Abbrechen',
    save: 'Speichern',
    formTitle: 'Neue Feishu OpenID-Regel',
    openId: 'Feishu OpenID',
    openIdPlaceholder: 'ou_xxxxxxxx',
    displayName: 'Anzeigename',
    userRole: 'Benutzerrolle',
    priority: 'Priorität',
    note: 'Notiz',
    enabled: 'Aktiviert',
    riskLevels: 'Risikostufen',
    actions: 'Aktionen',
    enable: 'Aktivieren',
    disable: 'Deaktivieren',
    delete: 'Löschen',
    loading: 'Wird geladen…',
    empty: 'Noch keine Regeln. Klicken Sie auf „Neu hinzufügen", um die erste zu erstellen.',
  },

  error: {
    loadFailed: 'Fehler beim Laden der Modulliste',
    operationFailed: 'Vorgang fehlgeschlagen',
    saveFailed: 'Konfiguration konnte nicht gespeichert werden',
    testFailed: 'Test fehlgeschlagen',
  },
}