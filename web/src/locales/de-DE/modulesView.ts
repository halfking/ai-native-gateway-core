// modulesView.ts — Modulverwaltungsseite.
export default {
  pageTitle: 'Module',
  pageSubtitle: 'Zentrale Verwaltung der Enterprise-Funktionsmodule. Funktionen nach Bedarf aktivieren/deaktivieren.',
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
  },

  overview: {
    sectionDescription: 'Beschreibung',
    sectionCapabilities: 'Funktionen',
    labelKey: 'Modulschlüssel',
    labelDanger: 'Gefahrenstufe',
    labelConfigCount: 'Konfigurationselemente',
    labelStatus: 'Aktueller Status',
    viewAllSettings: 'Alle Systemeinstellungen anzeigen',
    testConnection: 'Verbindung testen',
    testSuccess: 'Verbindungstest erfolgreich',
    testFailed: 'Verbindungstest fehlgeschlagen',
    testInProgress: 'Testnachricht wird gesendet…',
    dependenciesTitle: 'Abhängigkeiten',
    required: 'Erforderlich',
    optional: 'Optional',
    enableAllDeps: '{count} fehlende Pflichtabhängigkeiten aktivieren',
    enableAllDepsHint: 'Das Backend aktiviert zuerst alle fehlenden Pflichtabhängigkeiten in stabiler Reihenfolge und danach dieses Modul.',
    cascading: 'Abhängigkeiten werden aktiviert…',
    cascadeSuccess: '{count} Abhängigkeiten automatisch aktiviert: {names}',
    cascadeEmpty: 'Alle Abhängigkeiten sind aktiviert, das Modul ist jetzt aktiv.',
    cascadeAlreadyEnabled: 'Das Modul ist bereits aktiviert, es sind keine weiteren Abhängigkeiten nötig.',
    cascadeLogTitle: 'In diesem Durchlauf aktivierte Abhängigkeiten: ',

    sectionDependencies: 'Dependencies',

    dependencyDisabled: 'Dependency module is disabled',

    notEnabled: 'Not enabled',
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
  },

  handoff: {
    groupMaster: 'Master switch',
    groupMasterHint: 'Hauptschalter und Auslösemodus: sie legen fest, ob und wann eine Sitzung übergeben wird',
    groupTrigger: 'Trigger thresholds',
    groupTriggerHint: '4 Auslösebedingungen laufen parallel. Eine davon genügt für die Übergabe, die strengste gewinnt.',
    groupSummary: 'Summary generation',
    groupSummaryHint: 'Zusammenfassungs-Engine und Prompt-Vorlage: wie die alte Sitzung in einen für die neue Sitzung lesbaren Kontext verdichtet wird',
    groupSafety: 'Safety limits and notifications',
    groupSafetyHint: 'Abklingzeit, Obergrenze je Sitzung, Wiederholung bei Fehlern sowie Log- und Webhook-Benachrichtigungen',
  },

  integration: {
    docsLabel: 'Dokumentation: ',
    stepsTitle: 'Konfigurationsschritte',
    enabledStatus: 'Integration aktiviert',
    disabledHint: 'Integration deaktiviert: bitte zuerst das Modul aktivieren',
    feishuSteps: [
      'Erstellen Sie einen benutzerdefinierten Bot auf der Feishu Open Platform',
      'Kopieren Sie die Webhook-URL und fügen Sie sie in die unten stehende Konfiguration ein',
      '(Optional) Konfigurieren Sie Signatur-Verifizierungstoken und Verschlüsselungsschlüssel',
      'Klicken Sie nach der Konfiguration auf „Verbindung testen"',
      'Schalten Sie den Schalter „Feishu-Bot-Integration" ein',
    ],
    feishuBotIntegration: 'Feishu-Bot-Integration',
    wechatSteps: [
      'Erstellen Sie eine Eigenentwicklung im WeCom-Administrationsbereich',
      '(Optional) Konfigurieren Sie Callback-URL und Token unter "Nachrichten empfangen"',
      'Kopieren Sie CorpID, AgentID und Secret in die Konfiguration',
      '(Optional) Konfigurieren Sie die Webhook-URL des Gruppen-Bots',
      '(Optional) Konfigurieren Sie EncodingAESKey für Callback-Verschlüsselung',
      'Aktivieren Sie den "WeChat-Bot-Integration"-Schalter',
      'Stellen Sie sicher, dass alle Voraussetzungs-Module aktiviert sind',
    ],
    wechatBotIntegration: 'WeChat-Bot-Integration',
    dingtalkSteps: [
      'Fügen Sie in einer DingTalk-Gruppe einen „Benutzerdefinierten Bot“ hinzu, wählen Sie Sicherheit „Signatur“ und kopieren Sie den Webhook',
      'Fügen Sie die Webhook-URL unten ein und tragen Sie das Signatur-Secret ein',
      '(Optional) Erstellen Sie eine interne App in der Konsole und tragen Sie AppKey/AppSecret/AgentID für Arbeitsbenachrichtigungen ein',
      'Tragen Sie die Freigabe-Callback-URL (/api/webhooks/dingtalk/approval-callback) als Bot-Nachrichtenempfänger ein',
      'Aktivieren Sie den Schalter „DingTalk-Bot-Integration“',
      'Stellen Sie sicher, dass die Voraussetzungsmodule (Kompression, Injektionserkennung, Sitzungscache, Sitzungsaudit) aktiviert sind',
    ],
    dingtalkBotIntegration: 'DingTalk-Bot-Integration',
  },

  empty: {
    selectModule: 'Wählen Sie ein Modul aus, um Details und Konfiguration anzuzeigen',
  },


  error: {
    loadFailed: 'Modulliste konnte nicht geladen werden. Bitte erneut versuchen.',
    operationFailed: 'Modulvorgang konnte nicht abgeschlossen werden. Bitte erneut versuchen.',
    saveFailed: 'Modulkonfiguration konnte nicht gespeichert werden. Bitte erneut versuchen.',
    testFailed: 'Modultest konnte nicht bestanden werden. Bitte erneut versuchen.',
  },

  // 同步的扁平键

  modeBannerTitle: 'This page only does three things:',

  modeBannerBody: 'Installierte Module ein- und ausschalten und Module herunterladen, für die bereits eine Lizenz vorliegt. Preise, Bestellung und Tarifauswahl gibt es hier nicht.',
}